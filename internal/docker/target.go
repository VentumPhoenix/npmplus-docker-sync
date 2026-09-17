// Package docker turns Docker container metadata into desired NPM resource
// state and streams container lifecycle events.
package docker

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Network is one network endpoint of a container.
type Network struct {
	Name    string
	IPv4    string
	IPv6    string
	Aliases []string
}

// PortBinding is one port of a container: the port inside the container and,
// when published, the port on the host.
type PortBinding struct {
	// Private is the container-internal port (what EXPOSE declares).
	Private int
	// Public is the host port, 0 when the port is not published.
	Public int
	// Type is "tcp" or "udp".
	Type string
}

// Container is the subset of Docker container data the parser needs.
type Container struct {
	ID       string
	Name     string
	Labels   map[string]string
	Networks []Network
	// Ports are the container's exposed and published ports, used to guess
	// the upstream port when no label names one.
	Ports []PortBinding
	// State is the Docker state ("running", "exited", ...).
	State string
}

// Running reports whether the container is up.
//
// "restarting" and "paused" count as running on purpose: a crash loop or a
// paused container is a temporary condition, and treating it as stopped would
// disable a host every few seconds.
func (c Container) Running() bool {
	switch strings.ToLower(c.State) {
	case "running", "restarting", "paused", "":
		// An empty state means the source did not report one (unit tests,
		// older API versions); assume the container is up.
		return true
	default:
		return false
	}
}

// ParseOptions controls how labels are turned into targets.
type ParseOptions struct {
	// Prefix is the label namespace, e.g. "npm".
	Prefix string
	// ResolveIP makes the parser default the upstream host to the container's
	// IP address instead of its name. Avoids 502s when NPM cannot resolve
	// Docker's internal DNS.
	ResolveIP bool
	// PreferNetworks lists network names to try first when resolving the IP,
	// typically the network NPM itself is attached to.
	PreferNetworks []string
	// StrictNetworks restricts IP resolution to PreferNetworks. Without it a
	// container that is not attached to any of them falls back to an address
	// from some other network - which NPM usually cannot reach, producing a
	// proxy host that resolves to a dead upstream. Set whenever the operator
	// named the network explicitly (NPM_NETWORK).
	StrictNetworks bool
	// Defaults are the effective field defaults (built-in, overridden by the
	// NPM_<KIND>_<FIELD> and NPM_DEFAULT_<FIELD> environment variables).
	Defaults *fields.Defaults
	// ExposedByDefault manages every container that carries at least one
	// label of the namespace, without requiring npm.enable=true
	// (NPM_EXPOSED_BY_DEFAULT, default true).
	ExposedByDefault bool
	// StrictLabels skips a resource whose label set contains an unknown
	// field instead of only warning about it.
	StrictLabels bool
	// PortPreference is the order in which an exposed port is picked when a
	// container exposes several (NPM_PORT_PREFERENCE).
	PortPreference []int
	// SelfID is this process's own container: it is never managed.
	SelfID string
	// Offline parses labels without a Docker daemon behind them, as the
	// `validate` subcommand does for a compose file: the container's address
	// and exposed ports are simply not knowable, and their absence is not an
	// error.
	Offline bool
}

// OnStop is the policy for a container that is stopped but still exists.
type OnStop string

// The stop policies.
const (
	// OnStopDisable disables the resources of a stopped container (default).
	OnStopDisable OnStop = "disable"
	// OnStopKeep leaves them untouched and serving.
	OnStopKeep OnStop = "keep"
	// OnStopDelete removes them, as if the container had been destroyed.
	OnStopDelete OnStop = "delete"
)

// ParseOnStop resolves the NPM_ON_STOP setting.
func ParseOnStop(raw string) (OnStop, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "disable", "disabled", "off":
		return OnStopDisable, nil
	case "keep", "ignore", "none":
		return OnStopKeep, nil
	case "delete", "remove":
		return OnStopDelete, nil
	default:
		return OnStopDisable, fmt.Errorf("unknown value %q (want keep, disable or delete)", raw)
	}
}

// Snapshot is the desired state of one reconcile run, plus what the parser
// learned about the containers it read.
type Snapshot struct {
	// Targets are the resources the labels ask for.
	Targets []*Target
	// Protected lists containers whose labels could not be parsed, by name
	// and by id. Their resources must not be deleted in this run: a typo in
	// one label must never take a production host down.
	Protected map[string]struct{}
	// Summary counts how the containers were classified.
	Summary Summary
	// Containers is how many containers Docker reported at all. Zero is
	// suspicious - a filtered socket proxy answering with an empty list looks
	// exactly like "every container is gone".
	Containers int
}

// IsProtected reports whether a container is shielded from orphan deletion.
func (s Snapshot) IsProtected(name, id string) bool {
	if s.Protected == nil {
		return false
	}
	if name != "" {
		if _, ok := s.Protected[name]; ok {
			return true
		}
	}
	if id != "" {
		if _, ok := s.Protected[id]; ok {
			return true
		}
	}
	return false
}

// Target is one desired NPM resource derived from a container. A single
// container can produce many targets through indexed labels
// (npm.proxy.domains, npm.proxy.1.domains, npm.1.stream.incoming_port, ...).
type Target struct {
	Kind          npm.Kind
	Index         int
	ContainerID   string
	ContainerName string
	// Running mirrors the container state. A resource whose container is
	// stopped is never reconfigured, only enabled or disabled (NPM_ON_STOP).
	Running bool
	// ExplicitPlus lists the NPMplus-only fields the labels actually set, so
	// a warning against upstream NPM only appears when the user asked for
	// something that flavour cannot do.
	ExplicitPlus []string

	// Shared across proxy, redirect and 404 hosts.
	DomainNames []string
	// Certificate is the unresolved certificate wish; the reconcile loop
	// turns it into an id once it knows which certificates exist.
	Certificate certs.Spec
	// SSLForced is nil for "auto": forced as soon as a certificate is
	// attached.
	SSLForced      *bool
	HTTP2Support   bool
	HSTSEnabled    bool
	HSTSSubdomains bool
	HTTP3Support   bool
	BlockExploits  bool
	AdvancedConfig string
	Enabled        bool

	// Proxy hosts.
	ForwardScheme       string
	ForwardHost         string
	ForwardPort         int
	Websockets          bool
	Caching             bool
	TrustForwardedProto bool
	AccessListIDs       []int
	AccessListNames     []string
	AccessListType      string
	Locations           []npm.Location

	// Proxy hosts, NPMplus only.
	NoIndex             bool
	CrowdsecAppsec      bool
	RequestBuffering    bool
	ResponseBuffering   bool
	UpstreamCompression bool
	FancyIndex          bool
	XFrameOptions       string
	AuthRequest         string
	AuthRequestUpstream string
	LocationConfig      string

	// Redirection hosts.
	ForwardDomainName string
	ForwardHTTPCode   int
	PreservePath      bool

	// Streams.
	IncomingPort   int
	ForwardingHost string
	ForwardingPort int
	TCPForwarding  bool
	UDPForwarding  bool
	ProxyProtocol  int
	ProxyTLS       bool
	Description    string

	// Let's Encrypt (all kinds that carry a certificate).
	LetsEncryptEmail   string
	LetsEncryptAgree   bool
	DNSChallenge       bool
	DNSProvider        string
	DNSCredentials     string
	PropagationSeconds int
}

// Key is the identity of the target within its kind: the alphabetically first
// domain name, or the incoming port for streams.
func (t *Target) Key() string {
	if t.Kind == npm.KindStream {
		if t.IncomingPort == 0 {
			return ""
		}
		return fmt.Sprintf("%d", t.IncomingPort)
	}
	domains := npm.NormalizeDomains(t.DomainNames)
	if len(domains) == 0 {
		return ""
	}
	return domains[0]
}

// Describe returns a log friendly identifier such as
// "web#0 proxy app.example.com".
func (t *Target) Describe() string {
	return fmt.Sprintf("%s#%d %s %s", t.ContainerName, t.Index, t.Kind, t.Key())
}

// Complete reports whether the target carries everything a create or update
// needs. It is false only for a stopped container, whose address and ports
// Docker no longer reports: such a resource is left exactly as it is and only
// enabled or disabled.
func (t *Target) Complete() bool {
	switch t.Kind {
	case npm.KindProxy:
		return t.ForwardHost != "" && t.ForwardPort > 0
	case npm.KindStream:
		return t.ForwardingHost != "" && t.IncomingPort > 0 && t.ForwardingPort > 0
	default:
		return len(t.DomainNames) > 0
	}
}

// Domains returns the domain names this target serves; streams have none.
func (t *Target) Domains() []string { return npm.NormalizeDomains(t.DomainNames) }

// ResolveHost returns the upstream host for the container: the container's IP
// address when resolution is enabled and an address is available, otherwise
// the container name (Docker's embedded DNS resolves it inside a shared
// user-defined network).
//
// Only IPv4 addresses are used: NPM writes the value straight into
// `proxy_pass`, where a bare IPv6 address would be invalid.
func (c Container) ResolveHost(opts ParseOptions) string {
	if !opts.ResolveIP {
		if opts.StrictNetworks && !c.OnAnyNetwork(opts.PreferNetworks) {
			return ""
		}
		return c.Name
	}
	if ip := c.IPAddress(opts.PreferNetworks, opts.StrictNetworks); ip != "" {
		return ip
	}
	if opts.StrictNetworks {
		// Falling back to the container name would be just as unreachable for
		// NPM as an address from a network it does not share.
		return ""
	}
	return c.Name
}

// IPAddress returns the container's IPv4 address, preferring the given
// networks (in order). Without strict mode the remaining networks are used as
// a fallback, in a stable alphabetical order; with strict mode an empty string
// is returned instead, because an address NPM cannot route to is worse than no
// proxy host at all.
func (c Container) IPAddress(preferred []string, strict bool) string {
	byName := make(map[string]Network, len(c.Networks))
	for _, n := range c.Networks {
		byName[n.Name] = n
	}
	for _, name := range preferred {
		if n, ok := byName[name]; ok && isIPv4(n.IPv4) {
			return n.IPv4
		}
	}
	if strict {
		return ""
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if isIPv4(byName[name].IPv4) {
			return byName[name].IPv4
		}
	}
	return ""
}

// OnAnyNetwork reports whether the container is attached to one of the given
// networks.
func (c Container) OnAnyNetwork(names []string) bool {
	if len(names) == 0 {
		return true
	}
	for _, n := range c.Networks {
		for _, want := range names {
			if n.Name == want {
				return true
			}
		}
	}
	return false
}

// DefaultPortPreference is the order in which an exposed port is picked when a
// container exposes several of them.
var DefaultPortPreference = fields.DefaultPortPreference

// GuessPort returns the container port to forward to when no label names one.
//
// A container that exposes exactly one TCP port needs no configuration at all;
// with several ports the preference list decides, and when none of them is
// listed the caller is told to be explicit rather than being handed a random
// port.
func (c Container) GuessPort(preference []int) (int, bool) {
	tcp := make([]int, 0, len(c.Ports))
	seen := make(map[int]struct{}, len(c.Ports))
	for _, p := range c.Ports {
		if p.Private <= 0 || (p.Type != "" && p.Type != "tcp") {
			continue
		}
		if _, dup := seen[p.Private]; dup {
			continue
		}
		seen[p.Private] = struct{}{}
		tcp = append(tcp, p.Private)
	}
	if len(tcp) == 0 {
		return 0, false
	}
	if len(tcp) == 1 {
		return tcp[0], true
	}
	if len(preference) == 0 {
		preference = DefaultPortPreference
	}
	for _, want := range preference {
		if _, ok := seen[want]; ok {
			return want, true
		}
	}
	return 0, false
}

// PublishedPort returns the host port a container port is published on.
func (c Container) PublishedPort(private int) (int, bool) {
	for _, p := range c.Ports {
		if p.Private == private && p.Public > 0 {
			return p.Public, true
		}
	}
	return 0, false
}

// ExposedPorts returns the container's TCP ports, sorted, for error messages.
func (c Container) ExposedPorts() []int {
	out := make([]int, 0, len(c.Ports))
	seen := make(map[int]struct{}, len(c.Ports))
	for _, p := range c.Ports {
		if p.Private <= 0 {
			continue
		}
		if _, dup := seen[p.Private]; dup {
			continue
		}
		seen[p.Private] = struct{}{}
		out = append(out, p.Private)
	}
	sort.Ints(out)
	return out
}

// DedupeNetworks removes duplicates while keeping the first occurrence, so a
// preference list assembled from several sources stays readable in logs.
func DedupeNetworks(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// NetworkNames returns the names of all networks the container is attached to.
func (c Container) NetworkNames() []string {
	names := make([]string, 0, len(c.Networks))
	for _, n := range c.Networks {
		names = append(names, n.Name)
	}
	sort.Strings(names)
	return names
}

func isIPv4(raw string) bool {
	if raw == "" {
		return false
	}
	ip := net.ParseIP(raw)
	return ip != nil && ip.To4() != nil
}
