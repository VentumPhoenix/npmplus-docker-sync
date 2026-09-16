package npm

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind enumerates the NPM resource types this tool manages.
type Kind string

// The four routing resources of Nginx Proxy Manager / NPMplus.
const (
	KindProxy    Kind = "proxy"
	KindRedirect Kind = "redirect"
	KindStream   Kind = "stream"
	KindDead     Kind = "dead"
)

// Kinds is the canonical processing order. Proxy hosts come first because
// they are by far the most common resource.
var Kinds = []Kind{KindProxy, KindRedirect, KindStream, KindDead}

// Path returns the API collection path of the kind.
func (k Kind) Path() string {
	switch k {
	case KindProxy:
		return "/api/nginx/proxy-hosts"
	case KindRedirect:
		return "/api/nginx/redirection-hosts"
	case KindStream:
		return "/api/nginx/streams"
	case KindDead:
		return "/api/nginx/dead-hosts"
	default:
		return ""
	}
}

// String implements fmt.Stringer.
func (k Kind) String() string { return string(k) }

// Label returns a human readable name used in logs and errors.
func (k Kind) Label() string {
	switch k {
	case KindProxy:
		return "proxy host"
	case KindRedirect:
		return "redirection host"
	case KindStream:
		return "stream"
	case KindDead:
		return "404 host"
	default:
		return string(k)
	}
}

// Valid reports whether the kind is one of the supported resources.
func (k Kind) Valid() bool { return k.Path() != "" }

// Resource is the behaviour every managed NPM resource shares. It lets the
// reconcile worker treat proxy hosts, redirections, streams and 404 hosts
// uniformly while each type keeps its own API payload.
type Resource interface {
	// Kind reports which collection the resource belongs to.
	Kind() Kind
	// ResourceID is the NPM id (0 for a resource that does not exist yet).
	ResourceID() int
	// SetResourceID stores the id assigned by NPM.
	SetResourceID(id int)
	// ResourceKey is the stable identity within its kind: the first domain
	// name, or the incoming port for streams.
	ResourceKey() string
	// ResourceMeta exposes the meta object carrying the ownership marker.
	ResourceMeta() Meta
	// Fingerprint hashes the configuration-relevant fields.
	Fingerprint() string
	// AdoptServerState copies server-assigned values (such as an issued
	// certificate id) from the live resource into the desired payload, so a
	// converged resource does not produce an endless update loop.
	AdoptServerState(current Resource)
	// Describe returns a short, log friendly description.
	Describe() string
}

// ---------------------------------------------------------------------------
// Proxy hosts
// ---------------------------------------------------------------------------

// ProxyHost mirrors `/api/nginx/proxy-hosts`.
type ProxyHost struct {
	ID                    int           `json:"id,omitempty"`
	CreatedOn             string        `json:"created_on,omitempty"`
	ModifiedOn            string        `json:"modified_on,omitempty"`
	OwnerUserID           int           `json:"owner_user_id,omitempty"`
	DomainNames           []string      `json:"domain_names"`
	ForwardHost           string        `json:"forward_host"`
	ForwardPort           int           `json:"forward_port"`
	ForwardScheme         string        `json:"forward_scheme"`
	CertificateID         CertificateID `json:"certificate_id"`
	SSLForced             bool          `json:"ssl_forced"`
	HSTSEnabled           bool          `json:"hsts_enabled"`
	HSTSSubdomains        bool          `json:"hsts_subdomains"`
	HTTP2Support          bool          `json:"http2_support"`
	BlockExploits         bool          `json:"block_exploits"`
	CachingEnabled        bool          `json:"caching_enabled"`
	AllowWebsocketUpgrade bool          `json:"allow_websocket_upgrade"`
	AccessListID          int           `json:"access_list_id"`
	AdvancedConfig        string        `json:"advanced_config"`
	Enabled               Flag          `json:"enabled,omitempty"`
	Locations             []Location    `json:"locations"`
	Meta                  Meta          `json:"meta"`
}

// Kind implements Resource.
func (h *ProxyHost) Kind() Kind { return KindProxy }

// ResourceID implements Resource.
func (h *ProxyHost) ResourceID() int { return h.ID }

// SetResourceID implements Resource.
func (h *ProxyHost) SetResourceID(id int) { h.ID = id }

// ResourceKey implements Resource.
func (h *ProxyHost) ResourceKey() string { return primaryDomain(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *ProxyHost) ResourceMeta() Meta { return h.Meta }

// Describe implements Resource.
func (h *ProxyHost) Describe() string {
	return fmt.Sprintf("%s → %s://%s:%d",
		strings.Join(NormalizeDomains(h.DomainNames), ","),
		h.ForwardScheme, h.ForwardHost, h.ForwardPort)
}

// AdoptServerState implements Resource.
func (h *ProxyHost) AdoptServerState(current Resource) {
	live, ok := current.(*ProxyHost)
	if !ok {
		return
	}
	adoptCertificate(&h.CertificateID, live.CertificateID)
	if len(h.Locations) == 0 {
		h.Locations = []Location{}
	}
}

// Fingerprint implements Resource.
func (h *ProxyHost) Fingerprint() string {
	managedBy, container, index := ownership(h.Meta)
	return fingerprint(struct {
		Kind          string     `json:"kind"`
		Domains       []string   `json:"domains"`
		Scheme        string     `json:"scheme"`
		Host          string     `json:"host"`
		Port          int        `json:"port"`
		Certificate   string     `json:"certificate"`
		SSLForced     bool       `json:"ssl_forced"`
		HSTS          bool       `json:"hsts"`
		HSTSSub       bool       `json:"hsts_subdomains"`
		HTTP2         bool       `json:"http2"`
		BlockExploits bool       `json:"block_exploits"`
		Caching       bool       `json:"caching"`
		Websockets    bool       `json:"websockets"`
		AccessList    int        `json:"access_list"`
		Advanced      string     `json:"advanced"`
		Enabled       bool       `json:"enabled"`
		Locations     []Location `json:"locations"`
		ManagedBy     string     `json:"managed_by"`
		Container     string     `json:"container"`
		Index         int        `json:"index"`
	}{
		Kind:          string(KindProxy),
		Domains:       NormalizeDomains(h.DomainNames),
		Scheme:        strings.ToLower(h.ForwardScheme),
		Host:          strings.ToLower(h.ForwardHost),
		Port:          h.ForwardPort,
		Certificate:   h.CertificateID.String(),
		SSLForced:     h.SSLForced,
		HSTS:          h.HSTSEnabled,
		HSTSSub:       h.HSTSSubdomains,
		HTTP2:         h.HTTP2Support,
		BlockExploits: h.BlockExploits,
		Caching:       h.CachingEnabled,
		Websockets:    h.AllowWebsocketUpgrade,
		AccessList:    h.AccessListID,
		Advanced:      strings.TrimSpace(h.AdvancedConfig),
		Enabled:       bool(h.Enabled),
		Locations:     h.Locations,
		ManagedBy:     managedBy,
		Container:     container,
		Index:         index,
	})
}

// ---------------------------------------------------------------------------
// Redirection hosts
// ---------------------------------------------------------------------------

// RedirectionHost mirrors `/api/nginx/redirection-hosts`.
type RedirectionHost struct {
	ID                int           `json:"id,omitempty"`
	CreatedOn         string        `json:"created_on,omitempty"`
	ModifiedOn        string        `json:"modified_on,omitempty"`
	OwnerUserID       int           `json:"owner_user_id,omitempty"`
	DomainNames       []string      `json:"domain_names"`
	ForwardScheme     string        `json:"forward_scheme"`
	ForwardDomainName string        `json:"forward_domain_name"`
	ForwardHTTPCode   int           `json:"forward_http_code"`
	PreservePath      bool          `json:"preserve_path"`
	CertificateID     CertificateID `json:"certificate_id"`
	SSLForced         bool          `json:"ssl_forced"`
	HSTSEnabled       bool          `json:"hsts_enabled"`
	HSTSSubdomains    bool          `json:"hsts_subdomains"`
	HTTP2Support      bool          `json:"http2_support"`
	BlockExploits     bool          `json:"block_exploits"`
	AdvancedConfig    string        `json:"advanced_config"`
	Enabled           Flag          `json:"enabled,omitempty"`
	Meta              Meta          `json:"meta"`
}

// Kind implements Resource.
func (h *RedirectionHost) Kind() Kind { return KindRedirect }

// ResourceID implements Resource.
func (h *RedirectionHost) ResourceID() int { return h.ID }

// SetResourceID implements Resource.
func (h *RedirectionHost) SetResourceID(id int) { h.ID = id }

// ResourceKey implements Resource.
func (h *RedirectionHost) ResourceKey() string { return primaryDomain(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *RedirectionHost) ResourceMeta() Meta { return h.Meta }

// Describe implements Resource.
func (h *RedirectionHost) Describe() string {
	return fmt.Sprintf("%s ⇒ %s://%s (%d)",
		strings.Join(NormalizeDomains(h.DomainNames), ","),
		h.ForwardScheme, h.ForwardDomainName, h.ForwardHTTPCode)
}

// AdoptServerState implements Resource.
func (h *RedirectionHost) AdoptServerState(current Resource) {
	live, ok := current.(*RedirectionHost)
	if !ok {
		return
	}
	adoptCertificate(&h.CertificateID, live.CertificateID)
}

// Fingerprint implements Resource.
func (h *RedirectionHost) Fingerprint() string {
	managedBy, container, index := ownership(h.Meta)
	return fingerprint(struct {
		Kind          string   `json:"kind"`
		Domains       []string `json:"domains"`
		Scheme        string   `json:"scheme"`
		ForwardDomain string   `json:"forward_domain"`
		HTTPCode      int      `json:"http_code"`
		PreservePath  bool     `json:"preserve_path"`
		Certificate   string   `json:"certificate"`
		SSLForced     bool     `json:"ssl_forced"`
		HSTS          bool     `json:"hsts"`
		HSTSSub       bool     `json:"hsts_subdomains"`
		HTTP2         bool     `json:"http2"`
		BlockExploits bool     `json:"block_exploits"`
		Advanced      string   `json:"advanced"`
		Enabled       bool     `json:"enabled"`
		ManagedBy     string   `json:"managed_by"`
		Container     string   `json:"container"`
		Index         int      `json:"index"`
	}{
		Kind:          string(KindRedirect),
		Domains:       NormalizeDomains(h.DomainNames),
		Scheme:        strings.ToLower(h.ForwardScheme),
		ForwardDomain: strings.ToLower(strings.TrimSpace(h.ForwardDomainName)),
		HTTPCode:      h.ForwardHTTPCode,
		PreservePath:  h.PreservePath,
		Certificate:   h.CertificateID.String(),
		SSLForced:     h.SSLForced,
		HSTS:          h.HSTSEnabled,
		HSTSSub:       h.HSTSSubdomains,
		HTTP2:         h.HTTP2Support,
		BlockExploits: h.BlockExploits,
		Advanced:      strings.TrimSpace(h.AdvancedConfig),
		Enabled:       bool(h.Enabled),
		ManagedBy:     managedBy,
		Container:     container,
		Index:         index,
	})
}

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// Stream mirrors `/api/nginx/streams`. Streams have no domain names: their
// identity is the incoming port.
type Stream struct {
	ID             int           `json:"id,omitempty"`
	CreatedOn      string        `json:"created_on,omitempty"`
	ModifiedOn     string        `json:"modified_on,omitempty"`
	OwnerUserID    int           `json:"owner_user_id,omitempty"`
	IncomingPort   int           `json:"incoming_port"`
	ForwardingHost string        `json:"forwarding_host"`
	ForwardingPort int           `json:"forwarding_port"`
	TCPForwarding  bool          `json:"tcp_forwarding"`
	UDPForwarding  bool          `json:"udp_forwarding"`
	CertificateID  CertificateID `json:"certificate_id"`
	Enabled        Flag          `json:"enabled,omitempty"`
	Meta           Meta          `json:"meta"`
}

// Kind implements Resource.
func (s *Stream) Kind() Kind { return KindStream }

// ResourceID implements Resource.
func (s *Stream) ResourceID() int { return s.ID }

// SetResourceID implements Resource.
func (s *Stream) SetResourceID(id int) { s.ID = id }

// ResourceKey implements Resource: NPM allows exactly one stream per
// incoming port, which makes the port the natural identity.
func (s *Stream) ResourceKey() string { return strconv.Itoa(s.IncomingPort) }

// ResourceMeta implements Resource.
func (s *Stream) ResourceMeta() Meta { return s.Meta }

// Describe implements Resource.
func (s *Stream) Describe() string {
	protocols := make([]string, 0, 2)
	if s.TCPForwarding {
		protocols = append(protocols, "tcp")
	}
	if s.UDPForwarding {
		protocols = append(protocols, "udp")
	}
	return fmt.Sprintf(":%d → %s:%d (%s)",
		s.IncomingPort, s.ForwardingHost, s.ForwardingPort, strings.Join(protocols, "+"))
}

// AdoptServerState implements Resource.
func (s *Stream) AdoptServerState(current Resource) {
	live, ok := current.(*Stream)
	if !ok {
		return
	}
	adoptCertificate(&s.CertificateID, live.CertificateID)
}

// Fingerprint implements Resource.
func (s *Stream) Fingerprint() string {
	managedBy, container, index := ownership(s.Meta)
	return fingerprint(struct {
		Kind         string `json:"kind"`
		IncomingPort int    `json:"incoming_port"`
		Host         string `json:"host"`
		Port         int    `json:"port"`
		TCP          bool   `json:"tcp"`
		UDP          bool   `json:"udp"`
		Certificate  string `json:"certificate"`
		Enabled      bool   `json:"enabled"`
		ManagedBy    string `json:"managed_by"`
		Container    string `json:"container"`
		Index        int    `json:"index"`
	}{
		Kind:         string(KindStream),
		IncomingPort: s.IncomingPort,
		Host:         strings.ToLower(s.ForwardingHost),
		Port:         s.ForwardingPort,
		TCP:          s.TCPForwarding,
		UDP:          s.UDPForwarding,
		Certificate:  s.CertificateID.String(),
		Enabled:      bool(s.Enabled),
		ManagedBy:    managedBy,
		Container:    container,
		Index:        index,
	})
}

// ---------------------------------------------------------------------------
// Dead (404) hosts
// ---------------------------------------------------------------------------

// DeadHost mirrors `/api/nginx/dead-hosts`, the "404 host" in the NPM UI.
type DeadHost struct {
	ID             int           `json:"id,omitempty"`
	CreatedOn      string        `json:"created_on,omitempty"`
	ModifiedOn     string        `json:"modified_on,omitempty"`
	OwnerUserID    int           `json:"owner_user_id,omitempty"`
	DomainNames    []string      `json:"domain_names"`
	CertificateID  CertificateID `json:"certificate_id"`
	SSLForced      bool          `json:"ssl_forced"`
	HSTSEnabled    bool          `json:"hsts_enabled"`
	HSTSSubdomains bool          `json:"hsts_subdomains"`
	HTTP2Support   bool          `json:"http2_support"`
	AdvancedConfig string        `json:"advanced_config"`
	Enabled        Flag          `json:"enabled,omitempty"`
	Meta           Meta          `json:"meta"`
}

// Kind implements Resource.
func (h *DeadHost) Kind() Kind { return KindDead }

// ResourceID implements Resource.
func (h *DeadHost) ResourceID() int { return h.ID }

// SetResourceID implements Resource.
func (h *DeadHost) SetResourceID(id int) { h.ID = id }

// ResourceKey implements Resource.
func (h *DeadHost) ResourceKey() string { return primaryDomain(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *DeadHost) ResourceMeta() Meta { return h.Meta }

// Describe implements Resource.
func (h *DeadHost) Describe() string {
	return fmt.Sprintf("%s → 404", strings.Join(NormalizeDomains(h.DomainNames), ","))
}

// AdoptServerState implements Resource.
func (h *DeadHost) AdoptServerState(current Resource) {
	live, ok := current.(*DeadHost)
	if !ok {
		return
	}
	adoptCertificate(&h.CertificateID, live.CertificateID)
}

// Fingerprint implements Resource.
func (h *DeadHost) Fingerprint() string {
	managedBy, container, index := ownership(h.Meta)
	return fingerprint(struct {
		Kind        string   `json:"kind"`
		Domains     []string `json:"domains"`
		Certificate string   `json:"certificate"`
		SSLForced   bool     `json:"ssl_forced"`
		HSTS        bool     `json:"hsts"`
		HSTSSub     bool     `json:"hsts_subdomains"`
		HTTP2       bool     `json:"http2"`
		Advanced    string   `json:"advanced"`
		Enabled     bool     `json:"enabled"`
		ManagedBy   string   `json:"managed_by"`
		Container   string   `json:"container"`
		Index       int      `json:"index"`
	}{
		Kind:        string(KindDead),
		Domains:     NormalizeDomains(h.DomainNames),
		Certificate: h.CertificateID.String(),
		SSLForced:   h.SSLForced,
		HSTS:        h.HSTSEnabled,
		HSTSSub:     h.HSTSSubdomains,
		HTTP2:       h.HTTP2Support,
		Advanced:    strings.TrimSpace(h.AdvancedConfig),
		Enabled:     bool(h.Enabled),
		ManagedBy:   managedBy,
		Container:   container,
		Index:       index,
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// primaryDomain returns the alphabetically first domain name, which is the
// identity of every domain based resource.
func primaryDomain(domains []string) string {
	normalized := NormalizeDomains(domains)
	if len(normalized) == 0 {
		return ""
	}
	return normalized[0]
}

// adoptCertificate keeps a certificate NPM issued for a "new" request, so the
// desired state converges instead of requesting a certificate on every run.
func adoptCertificate(desired *CertificateID, live CertificateID) {
	if desired.New && live.ID > 0 {
		*desired = live
	}
}

// Compile-time proof that every resource satisfies the interface.
var (
	_ Resource = (*ProxyHost)(nil)
	_ Resource = (*RedirectionHost)(nil)
	_ Resource = (*Stream)(nil)
	_ Resource = (*DeadHost)(nil)
)
