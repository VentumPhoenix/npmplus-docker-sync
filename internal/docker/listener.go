package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// APIClient is the slice of the Docker SDK this package needs. Keeping it
// narrow makes the listener trivially mockable in tests.
type APIClient interface {
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error)
	Ping(ctx context.Context) (types.Ping, error)
	Close() error
}

// reconnectBackoff controls how fast the event stream is re-established.
const (
	reconnectMin = 1 * time.Second
	reconnectMax = 30 * time.Second
)

// Listener reads container state and lifecycle events from the Docker API.
type Listener struct {
	api  APIClient
	log  *slog.Logger
	opts ParseOptions
}

// NewClient builds a Docker SDK client for the given host. Both
// "unix:///var/run/docker.sock" and "tcp://docker-socket-proxy:2375" work;
// the TCP form is the recommended, least-privilege setup.
func NewClient(host string) (*client.Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if strings.TrimSpace(host) != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	// TLS material for remote daemons is picked up from DOCKER_CERT_PATH.
	opts = append(opts, client.WithTLSClientConfigFromEnv())

	c, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker: create client for %q: %w", host, err)
	}
	return c, nil
}

// NewListener wraps an APIClient. The ParseOptions carry the label prefix and
// the upstream-host resolution policy.
func NewListener(api APIClient, opts ParseOptions, log *slog.Logger) *Listener {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	if opts.Prefix == "" {
		opts.Prefix = DefaultPrefix
	}
	return &Listener{api: api, log: log, opts: opts}
}

// Options returns the parse options in use.
func (l *Listener) Options() ParseOptions { return l.opts }

// Ping verifies that the Docker endpoint is reachable.
func (l *Listener) Ping(ctx context.Context) error {
	if _, err := l.api.Ping(ctx); err != nil {
		return fmt.Errorf("docker: ping: %w", err)
	}
	return nil
}

// Containers returns all running containers as parser input, including their
// network endpoints so the upstream IP can be resolved.
func (l *Listener) Containers(ctx context.Context) ([]Container, error) {
	summaries, err := l.api.ContainerList(ctx, container.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: list containers: %w", err)
	}
	out := make([]Container, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, Container{
			ID:       s.ID,
			Name:     containerName(s),
			Labels:   s.Labels,
			Networks: networksOf(s),
		})
	}
	return out, nil
}

// Targets returns the desired NPM resources of all running containers.
// Invalid definitions are logged and skipped so one broken label set cannot
// stall the whole sync.
func (l *Listener) Targets(ctx context.Context) ([]*Target, error) {
	containers, err := l.Containers(ctx)
	if err != nil {
		return nil, err
	}
	targets, errs := ParseAll(containers, l.opts)
	for _, parseErr := range errs {
		l.log.Warn("ignoring invalid label definition", slog.String("error", parseErr.Error()))
	}
	return targets, nil
}

// DiscoverOwnNetworks returns the networks this process's own container is
// attached to. They are preferred when resolving upstream IPs, because a
// container reachable from here is reachable from NPM when both sit on the
// same network. Failures are not fatal: the caller falls back to the
// configured network or the first available address.
func (l *Listener) DiscoverOwnNetworks(ctx context.Context) []string {
	hostname, err := os.Hostname()
	if err != nil || len(hostname) < 8 {
		return nil
	}
	containers, err := l.Containers(ctx)
	if err != nil {
		l.log.Debug("could not inspect own container networks", slog.String("error", err.Error()))
		return nil
	}
	for _, c := range containers {
		// Inside a container the hostname is the short container id.
		if !strings.HasPrefix(c.ID, hostname) && c.Name != hostname {
			continue
		}
		names := c.NetworkNames()
		if len(names) > 0 {
			l.log.Debug("resolved own container networks",
				slog.String("container", c.Name),
				slog.String("networks", strings.Join(names, ",")))
		}
		return names
	}
	return nil
}

// Watch streams relevant container events into trigger. It reconnects with
// exponential backoff until ctx is cancelled, then returns nil.
//
// The filters are applied server-side so the daemon (or the socket proxy)
// only ever sends what this tool is allowed to see.
func (l *Listener) Watch(ctx context.Context, trigger chan<- Event) error {
	backoff := reconnectMin
	for {
		if err := ctx.Err(); err != nil {
			return nil //nolint:nilerr // cancellation is a clean shutdown
		}

		msgs, errs := l.api.Events(ctx, events.ListOptions{Filters: EventFilters()})
		l.log.Debug("subscribed to docker events")

	stream:
		for {
			select {
			case <-ctx.Done():
				return nil
			case msg, ok := <-msgs:
				if !ok {
					break stream
				}
				backoff = reconnectMin
				ev := Event{
					ContainerID: msg.Actor.ID,
					Name:        strings.TrimPrefix(msg.Actor.Attributes["name"], "/"),
					Action:      string(msg.Action),
				}
				l.log.Debug("docker event",
					slog.String("action", ev.Action),
					slog.String("container", ev.Name))
				select {
				case trigger <- ev:
				case <-ctx.Done():
					return nil
				}
			case err, ok := <-errs:
				if !ok {
					break stream
				}
				if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
					break stream
				}
				l.log.Warn("docker event stream failed, reconnecting",
					slog.String("error", err.Error()),
					slog.Duration("retry_in", backoff))
				break stream
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > reconnectMax {
			backoff = reconnectMax
		}
	}
}

// Close releases the underlying Docker connection.
func (l *Listener) Close() error { return l.api.Close() }

// Event is a normalised container lifecycle event.
type Event struct {
	ContainerID string
	Name        string
	Action      string
}

// EventFilters returns the server-side filter set: only container lifecycle
// events that can change the desired configuration.
func EventFilters() filters.Args {
	f := filters.NewArgs()
	f.Add("type", string(events.ContainerEventType))
	for _, action := range []string{"start", "die", "stop", "destroy", "rename", "update", "health_status"} {
		f.Add("event", action)
	}
	return f
}

// networksOf converts the network summary of a container.
func networksOf(s container.Summary) []Network {
	if s.NetworkSettings == nil {
		return nil
	}
	out := make([]Network, 0, len(s.NetworkSettings.Networks))
	for name, endpoint := range s.NetworkSettings.Networks {
		if endpoint == nil {
			continue
		}
		out = append(out, Network{
			Name:    name,
			IPv4:    endpoint.IPAddress,
			IPv6:    endpoint.GlobalIPv6Address,
			Aliases: endpoint.Aliases,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// containerName returns the primary name of a container without the leading
// slash, falling back to the short ID.
func containerName(s container.Summary) string {
	for _, name := range s.Names {
		if trimmed := strings.TrimPrefix(name, "/"); trimmed != "" {
			return trimmed
		}
	}
	if len(s.ID) > 12 {
		return s.ID[:12]
	}
	return s.ID
}
