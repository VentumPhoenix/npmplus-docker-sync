// Package docker turns Docker container metadata into desired NPM resource
// state and streams container lifecycle events.
package docker

import (
	"fmt"
	"net"
	"sort"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Network is one network endpoint of a container.
type Network struct {
	Name    string
	IPv4    string
	IPv6    string
	Aliases []string
}

// Container is the subset of Docker container data the parser needs.
type Container struct {
	ID       string
	Name     string
	Labels   map[string]string
	Networks []Network
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
}

// Target is one desired NPM resource derived from a container. A single
// container can produce many targets through indexed labels
// (npm.0.proxy.host, npm.1.stream.incoming_port, ...).
type Target struct {
	Kind          npm.Kind
	Index         int
	ContainerID   string
	ContainerName string

	// Shared across proxy, redirect and 404 hosts.
	DomainNames    []string
	CertificateID  npm.CertificateID
	SSLForced      bool
	HTTP2Support   bool
	HSTSEnabled    bool
	HSTSSubdomains bool
	BlockExploits  bool
	AdvancedConfig string
	Enabled        bool

	// Proxy hosts.
	ForwardScheme string
	ForwardHost   string
	ForwardPort   int
	Websockets    bool
	Caching       bool
	AccessListID  int
	Locations     []npm.Location

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
// "web/0 proxy app.example.com".
func (t *Target) Describe() string {
	return fmt.Sprintf("%s#%d %s %s", t.ContainerName, t.Index, t.Kind, t.Key())
}

// ResolveHost returns the upstream host for the container: the container's IP
// address when resolution is enabled and an address is available, otherwise
// the container name (Docker's embedded DNS resolves it inside a shared
// user-defined network).
//
// Only IPv4 addresses are used: NPM writes the value straight into
// `proxy_pass`, where a bare IPv6 address would be invalid.
func (c Container) ResolveHost(opts ParseOptions) string {
	if !opts.ResolveIP {
		return c.Name
	}
	if ip := c.IPAddress(opts.PreferNetworks); ip != "" {
		return ip
	}
	return c.Name
}

// IPAddress returns the container's IPv4 address, preferring the given
// networks (in order) over the remaining ones, which are considered in a
// stable alphabetical order.
func (c Container) IPAddress(preferred []string) string {
	byName := make(map[string]Network, len(c.Networks))
	for _, n := range c.Networks {
		byName[n.Name] = n
	}
	for _, name := range preferred {
		if n, ok := byName[name]; ok && isIPv4(n.IPv4) {
			return n.IPv4
		}
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
