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
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/system"
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

// InfoClient is the optional part of the Docker API used to identify the
// daemon. A socket proxy may well refuse /info, which is why it is separate:
// the sync instance id then falls back to the host name.
type InfoClient interface {
	Info(ctx context.Context) (system.Info, error)
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
	// selfID is this process's own container id, learned by
	// DiscoverOwnNetworks. Its events are dropped: nothing this container does
	// to itself can change what any other container wants from NPM, and its
	// own health flapping would otherwise trigger a reconcile every time.
	selfID string

	// streamMu guards the event stream health, which the readiness probe
	// reports: a silently disconnected stream means the tool only reacts on
	// the periodic resync, and that must not look healthy.
	streamMu    sync.RWMutex
	streamUp    bool
	streamSince time.Time
	streamErr   string
}

// StreamStatus describes the health of the Docker event subscription.
type StreamStatus struct {
	// Connected reports whether the event stream is currently subscribed.
	Connected bool
	// Since is when the current state began.
	Since time.Time
	// LastError is why the stream dropped, if it did.
	LastError string
}

// Down reports how long the stream has been disconnected, 0 while it is up.
func (s StreamStatus) Down(now time.Time) time.Duration {
	if s.Connected || s.Since.IsZero() {
		return 0
	}
	return now.Sub(s.Since)
}

// StreamStatus returns the health of the Docker event subscription.
func (l *Listener) StreamStatus() StreamStatus {
	l.streamMu.RLock()
	defer l.streamMu.RUnlock()
	return StreamStatus{Connected: l.streamUp, Since: l.streamSince, LastError: l.streamErr}
}

// setStreamStatus records a state change of the event stream.
func (l *Listener) setStreamStatus(up bool, reason string) {
	l.streamMu.Lock()
	defer l.streamMu.Unlock()
	if l.streamUp != up || l.streamSince.IsZero() {
		l.streamSince = time.Now()
	}
	l.streamUp = up
	if !up {
		l.streamErr = reason
	} else {
		l.streamErr = ""
	}
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

// SelfID returns the container id this process runs in, if it is known.
func (l *Listener) SelfID() string { return l.selfID }

// SetSelfID records the own container id so its events can be ignored. It is
// set automatically by DiscoverOwnNetworks.
func (l *Listener) SetSelfID(id string) { l.selfID = id }

// Options returns the parse options in use.
func (l *Listener) Options() ParseOptions { return l.opts }

// Ping verifies that the Docker endpoint is reachable.
func (l *Listener) Ping(ctx context.Context) error {
	if _, err := l.api.Ping(ctx); err != nil {
		return fmt.Errorf("docker: ping: %w", err)
	}
	return nil
}

// Containers returns all containers as parser input, including their network
// endpoints so the upstream IP can be resolved.
//
// Stopped containers are included on purpose: "the container is gone" and
// "the container is stopped" are very different situations, and only the
// first one may ever remove a host (NPM_ON_STOP decides what the second one
// does).
func (l *Listener) Containers(ctx context.Context) ([]Container, error) {
	summaries, err := l.api.ContainerList(ctx, container.ListOptions{All: true})
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
			Ports:    portsOf(s),
			State:    s.State,
		})
	}
	return out, nil
}

// Snapshot returns the desired NPM resources of all containers, plus the
// containers whose labels could not be parsed. Invalid definitions are logged
// and skipped so one broken label set cannot stall the whole sync - and their
// containers are marked protected so the broken definition cannot delete
// anything either.
func (l *Listener) Snapshot(ctx context.Context) (Snapshot, error) {
	containers, err := l.Containers(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	opts := l.opts
	opts.SelfID = l.selfID

	snapshot, res := Scan(containers, opts)
	for _, parseErr := range res.Errors {
		l.log.Warn("ignoring invalid label definition (its hosts are protected from deletion)",
			slog.String("error", parseErr.Error()))
	}
	for _, warning := range res.Warnings {
		l.log.Warn(warning)
	}
	return snapshot, nil
}

// Targets returns only the desired resources. It is the convenience form used
// by tests and the validate subcommand.
func (l *Listener) Targets(ctx context.Context) ([]*Target, error) {
	snapshot, err := l.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return snapshot.Targets, nil
}

// Summarize classifies the containers for the start-up overview.
func (l *Listener) Summarize(ctx context.Context) (Summary, error) {
	containers, err := l.Containers(ctx)
	if err != nil {
		return Summary{}, err
	}
	opts := l.opts
	opts.SelfID = l.selfID
	return Classify(containers, opts), nil
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
		l.selfID = c.ID
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

// NetworksOfContainer returns the networks a named container is attached to.
// It is how NPM_CONTAINER_NAME turns into an upstream network preference
// without the operator having to name the network as well.
func (l *Listener) NetworksOfContainer(ctx context.Context, name string) []string {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	containers, err := l.Containers(ctx)
	if err != nil {
		l.log.Debug("could not inspect the npm container", slog.String("error", err.Error()))
		return nil
	}
	for _, c := range containers {
		if c.Name != name && !strings.HasPrefix(c.ID, name) {
			continue
		}
		return c.NetworkNames()
	}
	l.log.Warn("npm container not found, falling back to the own networks",
		slog.String("container", name))
	return nil
}

// DaemonID returns the id of the Docker daemon, which is the natural default
// for the sync instance id: it is stable across restarts of this container and
// different for every Docker host.
//
// It falls back to the host name when the endpoint does not expose /info - a
// filtered socket proxy usually does not.
func (l *Listener) DaemonID(ctx context.Context) string {
	if client, ok := l.api.(InfoClient); ok {
		if info, err := client.Info(ctx); err == nil && info.ID != "" {
			return info.ID
		} else if err != nil {
			l.log.Debug("could not read the docker daemon id", slog.String("error", err.Error()))
		}
	}
	if name, err := os.Hostname(); err == nil && name != "" {
		return name
	}
	return ""
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
		l.setStreamStatus(true, "")
		l.log.Debug("subscribed to docker events")

	stream:
		for {
			select {
			case <-ctx.Done():
				return nil
			case msg, ok := <-msgs:
				if !ok {
					l.setStreamStatus(false, "event stream closed")
					break stream
				}
				backoff = reconnectMin
				if l.selfID != "" && msg.Actor.ID == l.selfID {
					continue
				}
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
					l.setStreamStatus(false, "event stream closed")
					break stream
				}
				if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
					l.setStreamStatus(false, "event stream ended")
					break stream
				}
				l.setStreamStatus(false, err.Error())
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
	// health_status is deliberately absent: a container's health does not
	// change its labels or its IP address, so reacting to it only produces
	// reconcile churn (and, for this container itself, a feedback loop).
	for _, action := range []string{"start", "die", "stop", "destroy", "rename", "update"} {
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

// portsOf converts the port list of a container. Docker reports exposed ports
// with a private port only and published ones with both, which is exactly the
// distinction the upstream guess needs.
func portsOf(s container.Summary) []PortBinding {
	out := make([]PortBinding, 0, len(s.Ports))
	for _, p := range s.Ports {
		out = append(out, PortBinding{
			Private: int(p.PrivatePort),
			Public:  int(p.PublicPort),
			Type:    p.Type,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Private < out[j].Private })
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
