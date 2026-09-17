package npm

import (
	"fmt"
	"sort"
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
	// ResourceCertificate reports the attached certificate, which the
	// automatic selection uses to stay on a certificate that still fits.
	ResourceCertificate() CertificateID
	// Fingerprint hashes the configuration-relevant fields. It deliberately
	// excludes the enabled flag, which is not part of the write payload and
	// is reconciled through the /enable and /disable endpoints instead.
	Fingerprint() string
	// Payload returns the create/update request body for the given API
	// flavour. It fails when the desired configuration uses a feature the
	// flavour does not have.
	Payload(flavour Flavour) (any, error)
	// IsEnabled reports the desired (or, for a resource read back from the
	// API, the live) enabled state.
	IsEnabled() bool
	// SetEnabled records the enabled state.
	SetEnabled(enabled bool)
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

// ProxyHost mirrors `/api/nginx/proxy-hosts` as the API *returns* it. Request
// bodies are built by Payload; see payloads.go.
type ProxyHost struct {
	ID          int    `json:"id,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	ModifiedOn  string `json:"modified_on,omitempty"`
	OwnerUserID int    `json:"owner_user_id,omitempty"`

	DomainNames           []string      `json:"domain_names"`
	ForwardHost           string        `json:"forward_host"`
	ForwardPort           int           `json:"forward_port"`
	ForwardScheme         string        `json:"forward_scheme"`
	CertificateID         CertificateID `json:"certificate_id"`
	SSLForced             bool          `json:"ssl_forced"`
	HSTSEnabled           bool          `json:"hsts_enabled"`
	HSTSSubdomains        bool          `json:"hsts_subdomains"`
	TrustForwardedProto   bool          `json:"trust_forwarded_proto"`
	HTTP2Support          bool          `json:"http2_support"`
	BlockExploits         bool          `json:"block_exploits"`
	CachingEnabled        bool          `json:"caching_enabled"`
	AllowWebsocketUpgrade bool          `json:"allow_websocket_upgrade"`
	AdvancedConfig        string        `json:"advanced_config"`
	Enabled               Flag          `json:"enabled"`
	Locations             []Location    `json:"locations"`
	Meta                  Meta          `json:"meta"`

	// HTTP3Support is an NPMplus extension (`npmplus_http3_support`).
	HTTP3Support bool `json:"npmplus_http3_support"`

	// NPMplus extensions. The three Disable* switches are inverted in the
	// API (`npmplus_crowdsec_appsec: true` turns the AppSec component off),
	// which is why the model spells them out that way.
	NoIndex                  bool   `json:"npmplus_noindex"`
	DisableCrowdsecAppsec    bool   `json:"npmplus_crowdsec_appsec"`
	DisableRequestBuffering  bool   `json:"npmplus_proxy_request_buffering"`
	DisableResponseBuffering bool   `json:"npmplus_proxy_response_buffering"`
	UpstreamCompression      bool   `json:"npmplus_upstream_compression"`
	FancyIndex               bool   `json:"npmplus_fancyindex"`
	XFrameOptions            string `json:"npmplus_x_frame_options"`
	AuthRequest              string `json:"npmplus_auth_request"`
	AuthRequestUpstream      string `json:"npmplus_auth_request_upstream"`
	LocationConfig           string `json:"npmplus_location_config"`

	// Access lists: upstream NPM has a single `access_list_id`, NPMplus a
	// list plus an explicit type. Both spellings are decoded so a host can be
	// compared no matter which server returned it; accessLists() normalises.
	AccessListID   int    `json:"access_list_id"`
	AccessListIDs  []int  `json:"npmplus_access_list_ids"`
	AccessListType string `json:"npmplus_access_list_type"`
	// AccessListNames are names that still have to be resolved into ids.
	// They never reach the API.
	AccessListNames []string `json:"-"`
}

// Kind implements Resource.
func (h *ProxyHost) Kind() Kind { return KindProxy }

// ResourceID implements Resource.
func (h *ProxyHost) ResourceID() int { return h.ID }

// SetResourceID implements Resource.
func (h *ProxyHost) SetResourceID(id int) { h.ID = id }

// ResourceKey implements Resource.
func (h *ProxyHost) ResourceKey() string { return primaryDomain(h.DomainNames) }

// Domains returns the normalised domain names of the host.
func (h *ProxyHost) Domains() []string { return NormalizeDomains(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *ProxyHost) ResourceMeta() Meta { return h.Meta }

// ResourceCertificate implements Resource.
func (h *ProxyHost) ResourceCertificate() CertificateID { return h.CertificateID }

// IsEnabled implements Resource.
func (h *ProxyHost) IsEnabled() bool { return bool(h.Enabled) }

// SetEnabled implements Resource.
func (h *ProxyHost) SetEnabled(enabled bool) { h.Enabled = Flag(enabled) }

// accessLists normalises the two spellings into the NPMplus shape: a sorted,
// de-duplicated list of ids and the matching type.
func (h *ProxyHost) accessLists() ([]int, string) {
	ids := normalizeIDs(h.AccessListIDs)
	if len(ids) == 0 && h.AccessListID > 0 {
		ids = []int{h.AccessListID}
	}
	listType := h.AccessListType
	switch {
	case listType == "" && len(ids) > 0:
		listType = AccessListCustom
	case listType == "":
		listType = AccessListPublic
	case listType == AccessListPublic:
		// "public" means "no access list", whatever ids may linger.
		ids = nil
	}
	return ids, listType
}

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
	h.Meta = AdoptMeta(h.Meta, live.Meta)
	// An unset x-frame-options keeps whatever NPMplus defaults to, so the
	// live value is adopted instead of being reported as a drift forever.
	if h.XFrameOptions == "" {
		h.XFrameOptions = live.XFrameOptions
	}
	if h.AuthRequest == "" {
		h.AuthRequest = live.AuthRequest
	}
	if len(h.Locations) == 0 {
		h.Locations = []Location{}
	}
}

// Fingerprint implements Resource.
func (h *ProxyHost) Fingerprint() string {
	managedBy, container, index, instance := ownership(h.Meta)
	ids, listType := h.accessLists()
	return fingerprint(struct {
		Kind           string     `json:"kind"`
		Domains        []string   `json:"domains"`
		Scheme         string     `json:"scheme"`
		Host           string     `json:"host"`
		Port           int        `json:"port"`
		Certificate    string     `json:"certificate"`
		SSLForced      bool       `json:"ssl_forced"`
		HSTS           bool       `json:"hsts"`
		HSTSSub        bool       `json:"hsts_subdomains"`
		TrustProto     bool       `json:"trust_forwarded_proto"`
		HTTP2          bool       `json:"http2"`
		HTTP3          bool       `json:"http3"`
		BlockExploits  bool       `json:"block_exploits"`
		Caching        bool       `json:"caching"`
		Websockets     bool       `json:"websockets"`
		AccessLists    []int      `json:"access_lists"`
		AccessListType string     `json:"access_list_type"`
		Advanced       string     `json:"advanced"`
		Locations      []Location `json:"locations"`

		NoIndex             bool   `json:"noindex"`
		CrowdsecOff         bool   `json:"crowdsec_appsec_off"`
		RequestBufferingOff bool   `json:"request_buffering_off"`
		ResponseBufferOff   bool   `json:"response_buffering_off"`
		UpstreamCompression bool   `json:"upstream_compression"`
		FancyIndex          bool   `json:"fancyindex"`
		XFrameOptions       string `json:"x_frame_options"`
		AuthRequest         string `json:"auth_request"`
		AuthRequestUpstream string `json:"auth_request_upstream"`
		LocationConfig      string `json:"location_config"`

		ManagedBy string `json:"managed_by"`
		Container string `json:"container"`
		Index     int    `json:"index"`
		Instance  string `json:"instance"`
	}{
		Kind:           string(KindProxy),
		Domains:        NormalizeDomains(h.DomainNames),
		Scheme:         strings.ToLower(h.ForwardScheme),
		Host:           strings.ToLower(h.ForwardHost),
		Port:           h.ForwardPort,
		Certificate:    h.CertificateID.String(),
		SSLForced:      h.SSLForced,
		HSTS:           h.HSTSEnabled,
		HSTSSub:        h.HSTSSubdomains,
		TrustProto:     h.TrustForwardedProto,
		HTTP2:          h.HTTP2Support,
		HTTP3:          h.HTTP3Support,
		BlockExploits:  h.BlockExploits,
		Caching:        h.CachingEnabled,
		Websockets:     h.AllowWebsocketUpgrade,
		AccessLists:    idList(ids),
		AccessListType: listType,
		Advanced:       strings.TrimSpace(h.AdvancedConfig),
		Locations:      canonicalLocations(h.Locations),

		NoIndex:             h.NoIndex,
		CrowdsecOff:         h.DisableCrowdsecAppsec,
		RequestBufferingOff: h.DisableRequestBuffering,
		ResponseBufferOff:   h.DisableResponseBuffering,
		UpstreamCompression: h.UpstreamCompression,
		FancyIndex:          h.FancyIndex,
		XFrameOptions:       strings.TrimSpace(h.XFrameOptions),
		AuthRequest:         strings.TrimSpace(h.AuthRequest),
		AuthRequestUpstream: strings.TrimSpace(h.AuthRequestUpstream),
		LocationConfig:      strings.TrimSpace(h.LocationConfig),

		ManagedBy: managedBy,
		Container: container,
		Index:     index,
		Instance:  instance,
	})
}

// ---------------------------------------------------------------------------
// Redirection hosts
// ---------------------------------------------------------------------------

// RedirectionHost mirrors `/api/nginx/redirection-hosts`.
type RedirectionHost struct {
	ID          int    `json:"id,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	ModifiedOn  string `json:"modified_on,omitempty"`
	OwnerUserID int    `json:"owner_user_id,omitempty"`

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
	HTTP3Support      bool          `json:"npmplus_http3_support"`
	BlockExploits     bool          `json:"block_exploits"`
	AdvancedConfig    string        `json:"advanced_config"`
	Enabled           Flag          `json:"enabled"`
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

// Domains returns the normalised domain names of the host.
func (h *RedirectionHost) Domains() []string { return NormalizeDomains(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *RedirectionHost) ResourceMeta() Meta { return h.Meta }

// ResourceCertificate implements Resource.
func (h *RedirectionHost) ResourceCertificate() CertificateID { return h.CertificateID }

// IsEnabled implements Resource.
func (h *RedirectionHost) IsEnabled() bool { return bool(h.Enabled) }

// SetEnabled implements Resource.
func (h *RedirectionHost) SetEnabled(enabled bool) { h.Enabled = Flag(enabled) }

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
	h.Meta = AdoptMeta(h.Meta, live.Meta)
}

// Fingerprint implements Resource.
func (h *RedirectionHost) Fingerprint() string {
	managedBy, container, index, instance := ownership(h.Meta)
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
		HTTP3         bool     `json:"http3"`
		BlockExploits bool     `json:"block_exploits"`
		Advanced      string   `json:"advanced"`
		ManagedBy     string   `json:"managed_by"`
		Container     string   `json:"container"`
		Index         int      `json:"index"`
		Instance      string   `json:"instance"`
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
		HTTP3:         h.HTTP3Support,
		BlockExploits: h.BlockExploits,
		Advanced:      strings.TrimSpace(h.AdvancedConfig),
		ManagedBy:     managedBy,
		Container:     container,
		Index:         index,
		Instance:      instance,
	})
}

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// Stream mirrors `/api/nginx/streams`. Streams have no domain names: their
// identity is the incoming port.
type Stream struct {
	ID          int    `json:"id,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	ModifiedOn  string `json:"modified_on,omitempty"`
	OwnerUserID int    `json:"owner_user_id,omitempty"`

	IncomingPort   Port          `json:"incoming_port"`
	ForwardingHost string        `json:"forwarding_host"`
	ForwardingPort Port          `json:"forwarding_port"`
	TCPForwarding  bool          `json:"tcp_forwarding"`
	UDPForwarding  bool          `json:"udp_forwarding"`
	CertificateID  CertificateID `json:"certificate_id"`
	Enabled        Flag          `json:"enabled"`
	Meta           Meta          `json:"meta"`

	// NPMplus extensions.
	ProxyProtocol  int    `json:"npmplus_proxy_protocol_forwarding"`
	ProxyTLS       bool   `json:"npmplus_proxy_tls"`
	AdvancedConfig string `json:"npmplus_advanced_config"`
	Description    string `json:"npmplus_description"`
}

// Kind implements Resource.
func (s *Stream) Kind() Kind { return KindStream }

// ResourceID implements Resource.
func (s *Stream) ResourceID() int { return s.ID }

// SetResourceID implements Resource.
func (s *Stream) SetResourceID(id int) { s.ID = id }

// ResourceKey implements Resource: NPM allows exactly one stream per
// incoming port, which makes the port the natural identity.
func (s *Stream) ResourceKey() string { return s.IncomingPort.String() }

// ResourceMeta implements Resource.
func (s *Stream) ResourceMeta() Meta { return s.Meta }

// ResourceCertificate implements Resource.
func (s *Stream) ResourceCertificate() CertificateID { return s.CertificateID }

// IsEnabled implements Resource.
func (s *Stream) IsEnabled() bool { return bool(s.Enabled) }

// SetEnabled implements Resource.
func (s *Stream) SetEnabled(enabled bool) { s.Enabled = Flag(enabled) }

// Describe implements Resource.
func (s *Stream) Describe() string {
	protocols := make([]string, 0, 2)
	if s.TCPForwarding {
		protocols = append(protocols, "tcp")
	}
	if s.UDPForwarding {
		protocols = append(protocols, "udp")
	}
	return fmt.Sprintf(":%s → %s:%s (%s)",
		s.IncomingPort, s.ForwardingHost, s.ForwardingPort, strings.Join(protocols, "+"))
}

// AdoptServerState implements Resource.
func (s *Stream) AdoptServerState(current Resource) {
	live, ok := current.(*Stream)
	if !ok {
		return
	}
	adoptCertificate(&s.CertificateID, live.CertificateID)
	s.Meta = AdoptMeta(s.Meta, live.Meta)
}

// Fingerprint implements Resource.
func (s *Stream) Fingerprint() string {
	managedBy, container, index, instance := ownership(s.Meta)
	return fingerprint(struct {
		Kind          string `json:"kind"`
		IncomingPort  string `json:"incoming_port"`
		Host          string `json:"host"`
		Port          string `json:"port"`
		TCP           bool   `json:"tcp"`
		UDP           bool   `json:"udp"`
		Certificate   string `json:"certificate"`
		ProxyProtocol int    `json:"proxy_protocol"`
		ProxyTLS      bool   `json:"proxy_tls"`
		Advanced      string `json:"advanced"`
		Description   string `json:"description"`
		ManagedBy     string `json:"managed_by"`
		Container     string `json:"container"`
		Index         int    `json:"index"`
		Instance      string `json:"instance"`
	}{
		Kind:          string(KindStream),
		IncomingPort:  s.IncomingPort.String(),
		Host:          strings.ToLower(s.ForwardingHost),
		Port:          s.ForwardingPort.String(),
		TCP:           s.TCPForwarding,
		UDP:           s.UDPForwarding,
		Certificate:   s.CertificateID.String(),
		ProxyProtocol: s.ProxyProtocol,
		ProxyTLS:      s.ProxyTLS,
		Advanced:      strings.TrimSpace(s.AdvancedConfig),
		Description:   strings.TrimSpace(s.Description),
		ManagedBy:     managedBy,
		Container:     container,
		Index:         index,
		Instance:      instance,
	})
}

// ---------------------------------------------------------------------------
// Dead (404) hosts
// ---------------------------------------------------------------------------

// DeadHost mirrors `/api/nginx/dead-hosts`, the "404 host" in the NPM UI.
type DeadHost struct {
	ID          int    `json:"id,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	ModifiedOn  string `json:"modified_on,omitempty"`
	OwnerUserID int    `json:"owner_user_id,omitempty"`

	DomainNames    []string      `json:"domain_names"`
	CertificateID  CertificateID `json:"certificate_id"`
	SSLForced      bool          `json:"ssl_forced"`
	HSTSEnabled    bool          `json:"hsts_enabled"`
	HSTSSubdomains bool          `json:"hsts_subdomains"`
	HTTP2Support   bool          `json:"http2_support"`
	HTTP3Support   bool          `json:"npmplus_http3_support"`
	AdvancedConfig string        `json:"advanced_config"`
	Enabled        Flag          `json:"enabled"`
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

// Domains returns the normalised domain names of the host.
func (h *DeadHost) Domains() []string { return NormalizeDomains(h.DomainNames) }

// ResourceMeta implements Resource.
func (h *DeadHost) ResourceMeta() Meta { return h.Meta }

// ResourceCertificate implements Resource.
func (h *DeadHost) ResourceCertificate() CertificateID { return h.CertificateID }

// IsEnabled implements Resource.
func (h *DeadHost) IsEnabled() bool { return bool(h.Enabled) }

// SetEnabled implements Resource.
func (h *DeadHost) SetEnabled(enabled bool) { h.Enabled = Flag(enabled) }

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
	h.Meta = AdoptMeta(h.Meta, live.Meta)
}

// Fingerprint implements Resource.
func (h *DeadHost) Fingerprint() string {
	managedBy, container, index, instance := ownership(h.Meta)
	return fingerprint(struct {
		Kind        string   `json:"kind"`
		Domains     []string `json:"domains"`
		Certificate string   `json:"certificate"`
		SSLForced   bool     `json:"ssl_forced"`
		HSTS        bool     `json:"hsts"`
		HSTSSub     bool     `json:"hsts_subdomains"`
		HTTP2       bool     `json:"http2"`
		HTTP3       bool     `json:"http3"`
		Advanced    string   `json:"advanced"`
		ManagedBy   string   `json:"managed_by"`
		Container   string   `json:"container"`
		Index       int      `json:"index"`
		Instance    string   `json:"instance"`
	}{
		Kind:        string(KindDead),
		Domains:     NormalizeDomains(h.DomainNames),
		Certificate: h.CertificateID.String(),
		SSLForced:   h.SSLForced,
		HSTS:        h.HSTSEnabled,
		HSTSSub:     h.HSTSSubdomains,
		HTTP2:       h.HTTP2Support,
		HTTP3:       h.HTTP3Support,
		Advanced:    strings.TrimSpace(h.AdvancedConfig),
		ManagedBy:   managedBy,
		Container:   container,
		Index:       index,
		Instance:    instance,
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

// normalizeIDs sorts and de-duplicates access list ids and drops the
// placeholder 0, so two orderings of the same set hash identically.
func normalizeIDs(ids []int) []int {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Ints(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// canonicalLocations normalises location blocks for the fingerprint so the hash
// does not depend on how the server filled in the optional fields. NPMplus
// drops the id list of any location that is not "custom"
// (internalProxyHostAccessList.cleanAccessListTypes), so this does too -
// otherwise a location carrying both would never converge.
func canonicalLocations(locations []Location) []Location {
	out := make([]Location, 0, len(locations))
	for _, l := range locations {
		listType := l.accessListType()
		var ids []int
		if listType == AccessListCustom {
			ids = normalizeIDs(l.AccessListIDs)
		}
		enabled := l.IsEnabled()
		out = append(out, Location{
			Path:                     strings.TrimSpace(l.Path),
			LocationType:             l.LocationType,
			AdvancedConfig:           strings.TrimSpace(l.AdvancedConfig),
			LocationConfig:           strings.TrimSpace(l.LocationConfig),
			ForwardScheme:            strings.ToLower(l.ForwardScheme),
			ForwardHost:              strings.ToLower(l.ForwardHost),
			ForwardPort:              l.ForwardPort,
			AccessListIDs:            ids,
			AccessListType:           listType,
			Enabled:                  &enabled,
			NoIndex:                  l.NoIndex,
			DisableCrowdsecAppsec:    l.DisableCrowdsecAppsec,
			DisableRequestBuffering:  l.DisableRequestBuffering,
			DisableResponseBuffering: l.DisableResponseBuffering,
			UpstreamCompression:      l.UpstreamCompression,
			FancyIndex:               l.FancyIndex,
			XFrameOptions:            strings.TrimSpace(l.XFrameOptions),
			AuthRequest:              strings.TrimSpace(l.AuthRequest),
			AuthRequestUpstream:      strings.TrimSpace(l.AuthRequestUpstream),
		})
	}
	return out
}

// Compile-time proof that every resource satisfies the interface.
var (
	_ Resource = (*ProxyHost)(nil)
	_ Resource = (*RedirectionHost)(nil)
	_ Resource = (*Stream)(nil)
	_ Resource = (*DeadHost)(nil)
)
