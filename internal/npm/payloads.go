package npm

import (
	"fmt"
	"strings"
)

// Request bodies are deliberately separate from the response models.
//
// Both NPM and NPMplus validate every POST/PUT against a JSON schema with
// `additionalProperties: false`, so a body may only carry fields that flavour
// actually accepts. Marshalling the response model straight back — which is
// what releases up to v1.0.0-beta.1 did — sends read-only fields (`id`,
// `created_on`, ...), the `enabled` flag (write-protected: it is toggled
// through the /enable and /disable endpoints) and, on NPMplus, the removed
// `access_list_id`. The result is a flat
// `400 data must NOT have additional properties` for every write.
//
// Each payload struct below mirrors exactly one request schema, so adding a
// field to a model can no longer leak into a request by accident.
//
// Fields that only exist on NPMplus are dropped when talking to upstream NPM
// rather than failing the write: they are a better default, not a
// requirement. UnsupportedByNPM lists them so the reconcile loop can say once
// what it had to leave out.
//
// Fields a server has retired or not yet introduced - `meta` and the legacy
// proxy switches on NPMplus after its meta-to-columns migration,
// `trust_forwarded_proto` before NPM 2.12 - are decided per field through
// Dialect.Accepts, which reads the server's own schema when it serves one.
//
// Every advanced configuration starts with the ownership marker (see
// marker.go), and the meta object carries the same ownership wherever the
// server still accepts one.

// ---------------------------------------------------------------------------
// Proxy hosts
// ---------------------------------------------------------------------------

type locationPayloadNPMplus struct {
	Path           string `json:"path"`
	LocationType   string `json:"location_type"`
	ForwardScheme  string `json:"forward_scheme"`
	ForwardHost    string `json:"forward_host"`
	ForwardPort    int    `json:"forward_port"`
	AdvancedConfig string `json:"advanced_config"`
	// LocationConfig moved to the host when NPMplus retired the meta object.
	LocationConfig *string `json:"npmplus_location_config,omitempty"`
	AccessListIDs  []int   `json:"npmplus_access_list_ids"`
	AccessListType string  `json:"npmplus_access_list_type"`

	Enabled                  Flag   `json:"npmplus_enabled"`
	NoIndex                  Flag   `json:"npmplus_noindex"`
	DisableCrowdsecAppsec    Flag   `json:"npmplus_crowdsec_appsec"`
	DisableRequestBuffering  Flag   `json:"npmplus_proxy_request_buffering"`
	DisableResponseBuffering Flag   `json:"npmplus_proxy_response_buffering"`
	UpstreamCompression      Flag   `json:"npmplus_upstream_compression"`
	FancyIndex               Flag   `json:"npmplus_fancyindex"`
	XFrameOptions            string `json:"npmplus_x_frame_options,omitempty"`
	AuthRequest              string `json:"npmplus_auth_request,omitempty"`
	AuthRequestUpstream      string `json:"npmplus_auth_request_upstream"`
}

type locationPayloadNPM struct {
	Path           string `json:"path"`
	ForwardScheme  string `json:"forward_scheme"`
	ForwardHost    string `json:"forward_host"`
	ForwardPort    int    `json:"forward_port"`
	AdvancedConfig string `json:"advanced_config"`
}

type proxyPayloadNPMplus struct {
	DomainNames           []string                 `json:"domain_names"`
	ForwardScheme         string                   `json:"forward_scheme"`
	ForwardHost           string                   `json:"forward_host"`
	ForwardPort           int                      `json:"forward_port"`
	CertificateID         CertificateID            `json:"certificate_id"`
	SSLForced             Flag                     `json:"ssl_forced"`
	HSTSEnabled           Flag                     `json:"hsts_enabled"`
	HSTSSubdomains        Flag                     `json:"hsts_subdomains"`
	TrustForwardedProto   *Flag                    `json:"trust_forwarded_proto,omitempty"`
	HTTP2Support          *Flag                    `json:"http2_support,omitempty"`
	HTTP3Support          Flag                     `json:"npmplus_http3_support"`
	BlockExploits         *Flag                    `json:"block_exploits,omitempty"`
	CachingEnabled        *Flag                    `json:"caching_enabled,omitempty"`
	AllowWebsocketUpgrade *Flag                    `json:"allow_websocket_upgrade,omitempty"`
	AccessListIDs         []int                    `json:"npmplus_access_list_ids"`
	AccessListType        string                   `json:"npmplus_access_list_type"`
	AdvancedConfig        string                   `json:"advanced_config"`
	LocationConfig        string                   `json:"npmplus_location_config"`
	Locations             []locationPayloadNPMplus `json:"locations"`

	NoIndex                  Flag   `json:"npmplus_noindex"`
	DisableCrowdsecAppsec    Flag   `json:"npmplus_crowdsec_appsec"`
	DisableRequestBuffering  Flag   `json:"npmplus_proxy_request_buffering"`
	DisableResponseBuffering Flag   `json:"npmplus_proxy_response_buffering"`
	UpstreamCompression      Flag   `json:"npmplus_upstream_compression"`
	FancyIndex               Flag   `json:"npmplus_fancyindex"`
	XFrameOptions            string `json:"npmplus_x_frame_options,omitempty"`
	AuthRequest              string `json:"npmplus_auth_request,omitempty"`
	AuthRequestUpstream      string `json:"npmplus_auth_request_upstream"`

	*npmplusColumns
	Meta Meta `json:"meta,omitempty"`
}

type proxyPayloadNPM struct {
	DomainNames    []string      `json:"domain_names"`
	ForwardScheme  string        `json:"forward_scheme"`
	ForwardHost    string        `json:"forward_host"`
	ForwardPort    int           `json:"forward_port"`
	CertificateID  CertificateID `json:"certificate_id"`
	SSLForced      Flag          `json:"ssl_forced"`
	HSTSEnabled    Flag          `json:"hsts_enabled"`
	HSTSSubdomains Flag          `json:"hsts_subdomains"`
	// A pointer so it can be left out entirely: NPM added the field in 2.12
	// and 2.11 rejects the whole body over it. Sending `false` is not the
	// same as omitting it - on an update, an omitted field keeps its stored
	// value - so this is nil only where the server has no such field at all.
	TrustForwardedProto   *bool                `json:"trust_forwarded_proto,omitempty"`
	HTTP2Support          Flag                 `json:"http2_support"`
	BlockExploits         Flag                 `json:"block_exploits"`
	CachingEnabled        Flag                 `json:"caching_enabled"`
	AllowWebsocketUpgrade Flag                 `json:"allow_websocket_upgrade"`
	AccessListID          int                  `json:"access_list_id"`
	AdvancedConfig        string               `json:"advanced_config"`
	Locations             []locationPayloadNPM `json:"locations"`
	Meta                  Meta                 `json:"meta,omitempty"`
}

// npmplusColumns are the properties NPMplus introduced when it moved the meta
// object into columns: the UI group and the DNS challenge of a certificate
// request. Embedded as a pointer, so a nil value leaves all of them out.
type npmplusColumns struct {
	Directory          *string `json:"npmplus_directory,omitempty"`
	DNSChallenge       *bool   `json:"npmplus_dns_challenge,omitempty"`
	DNSProvider        string  `json:"npmplus_dns_provider,omitempty"`
	DNSCredentials     string  `json:"npmplus_dns_provider_credentials,omitempty"`
	PropagationSeconds *int    `json:"npmplus_propagation_seconds,omitempty"`
}

// Payload implements Resource.
func (h *ProxyHost) Payload(dialect Dialect) (any, error) {
	flavour := dialect.Flavour
	if err := checkScheme(flavour, h.ForwardScheme); err != nil {
		return nil, err
	}
	if err := h.checkAccessListNames(); err != nil {
		return nil, err
	}
	if err := checkLetsEncrypt(flavour, h.CertificateID, h.Meta); err != nil {
		return nil, err
	}
	ids, listType := h.accessLists()

	if flavour == FlavourNPM {
		if len(ids) > 1 {
			return nil, fmt.Errorf("upstream nginx-proxy-manager accepts a single access list, got %d", len(ids))
		}
		locations := make([]locationPayloadNPM, 0, len(h.Locations))
		for _, l := range h.Locations {
			locations = append(locations, locationPayloadNPM{
				Path:           l.Path,
				ForwardScheme:  l.ForwardScheme,
				ForwardHost:    l.ForwardHost,
				ForwardPort:    l.ForwardPort,
				AdvancedConfig: l.AdvancedConfig,
			})
		}
		var accessListID int
		if len(ids) == 1 {
			accessListID = ids[0]
		}
		var trustProto *bool
		if dialect.SupportsTrustForwardedProto() {
			value := bool(h.TrustForwardedProto)
			trustProto = &value
		}
		return &proxyPayloadNPM{
			DomainNames:           NormalizeDomains(h.DomainNames),
			ForwardScheme:         h.ForwardScheme,
			ForwardHost:           h.ForwardHost,
			ForwardPort:           h.ForwardPort,
			CertificateID:         h.CertificateID,
			SSLForced:             h.SSLForced,
			HSTSEnabled:           h.HSTSEnabled,
			HSTSSubdomains:        h.HSTSSubdomains,
			TrustForwardedProto:   trustProto,
			HTTP2Support:          h.HTTP2Support,
			BlockExploits:         h.BlockExploits,
			CachingEnabled:        h.CachingEnabled,
			AllowWebsocketUpgrade: h.AllowWebsocketUpgrade,
			AccessListID:          accessListID,
			AdvancedConfig:        advancedFor(dialect, KindProxy, h.Meta, h.ResourceKey(), h.AdvancedConfig),
			Locations:             locations,
			Meta:                  metaForWrite(dialect, KindProxy, h.Meta),
		}, nil
	}

	locations := make([]locationPayloadNPMplus, 0, len(h.Locations))
	for _, l := range h.Locations {
		// The server keeps the ids only for a "custom" location; sending them
		// alongside "global" or "public" would come straight back cleared and
		// look like a permanent drift.
		listType := l.accessListType()
		var locationIDs []int
		if listType == AccessListCustom {
			locationIDs = l.AccessListIDs
		}
		locations = append(locations, locationPayloadNPMplus{
			Path:                     l.Path,
			LocationType:             l.LocationType,
			ForwardScheme:            l.ForwardScheme,
			ForwardHost:              l.ForwardHost,
			ForwardPort:              l.ForwardPort,
			AdvancedConfig:           l.AdvancedConfig,
			LocationConfig:           textIf(dialect, KindProxy, "locations/npmplus_location_config", l.LocationConfig),
			AccessListIDs:            idList(locationIDs),
			AccessListType:           listType,
			Enabled:                  Flag(l.IsEnabled()),
			NoIndex:                  l.NoIndex,
			DisableCrowdsecAppsec:    l.DisableCrowdsecAppsec,
			DisableRequestBuffering:  l.DisableRequestBuffering,
			DisableResponseBuffering: l.DisableResponseBuffering,
			UpstreamCompression:      l.UpstreamCompression,
			FancyIndex:               l.FancyIndex,
			XFrameOptions:            l.XFrameOptions,
			AuthRequest:              l.AuthRequest,
			AuthRequestUpstream:      l.AuthRequestUpstream,
		})
	}
	return &proxyPayloadNPMplus{
		DomainNames:           NormalizeDomains(h.DomainNames),
		ForwardScheme:         h.ForwardScheme,
		ForwardHost:           h.ForwardHost,
		ForwardPort:           h.ForwardPort,
		CertificateID:         h.CertificateID,
		SSLForced:             h.SSLForced,
		HSTSEnabled:           h.HSTSEnabled,
		HSTSSubdomains:        h.HSTSSubdomains,
		HTTP3Support:          h.HTTP3Support,
		TrustForwardedProto:   flagIf(dialect, KindProxy, "trust_forwarded_proto", h.TrustForwardedProto),
		HTTP2Support:          flagIf(dialect, KindProxy, "http2_support", h.HTTP2Support),
		BlockExploits:         flagIf(dialect, KindProxy, "block_exploits", h.BlockExploits),
		CachingEnabled:        flagIf(dialect, KindProxy, "caching_enabled", h.CachingEnabled),
		AllowWebsocketUpgrade: flagIf(dialect, KindProxy, "allow_websocket_upgrade", h.AllowWebsocketUpgrade),
		AccessListIDs:         idList(ids),
		AccessListType:        listType,
		AdvancedConfig:        advancedFor(dialect, KindProxy, h.Meta, h.ResourceKey(), h.AdvancedConfig),
		LocationConfig:        h.LocationConfig,
		Locations:             locations,

		NoIndex:                  h.NoIndex,
		DisableCrowdsecAppsec:    h.DisableCrowdsecAppsec,
		DisableRequestBuffering:  h.DisableRequestBuffering,
		DisableResponseBuffering: h.DisableResponseBuffering,
		UpstreamCompression:      h.UpstreamCompression,
		FancyIndex:               h.FancyIndex,
		XFrameOptions:            h.XFrameOptions,
		AuthRequest:              h.AuthRequest,
		AuthRequestUpstream:      h.AuthRequestUpstream,

		npmplusColumns: columnsFor(dialect, KindProxy, h.Meta, h.CertificateID),
		Meta:           metaForWrite(dialect, KindProxy, h.Meta),
	}, nil
}

// checkAccessListNames guards against writing a host without the protection
// its labels asked for: an unresolved name must never silently become
// "public".
func (h *ProxyHost) checkAccessListNames() error {
	if len(h.AccessListNames) > 0 {
		return fmt.Errorf("unresolved access list name(s): %v", h.AccessListNames)
	}
	for _, l := range h.Locations {
		if len(l.AccessListNames) > 0 {
			return fmt.Errorf("location %s: unresolved access list name(s): %v", l.Path, l.AccessListNames)
		}
	}
	return nil
}

// npmplusOnly implements the unsupportedFields interface.
func (h *ProxyHost) npmplusOnly() []string {
	var out []string
	if h.HTTP3Support {
		out = append(out, "ssl.http3")
	}
	if h.NoIndex {
		out = append(out, "noindex")
	}
	if h.DisableCrowdsecAppsec {
		out = append(out, "crowdsec_appsec")
	}
	if h.DisableRequestBuffering {
		out = append(out, "request_buffering")
	}
	if h.DisableResponseBuffering {
		out = append(out, "response_buffering")
	}
	if h.UpstreamCompression {
		out = append(out, "upstream_compression")
	}
	if h.FancyIndex {
		out = append(out, "fancyindex")
	}
	if h.XFrameOptions != "" {
		out = append(out, "x_frame_options")
	}
	if h.AuthRequest != "" && h.AuthRequest != "none" {
		out = append(out, "auth_request")
	}
	if h.AuthRequestUpstream != "" {
		out = append(out, "auth_request_upstream")
	}
	if h.LocationConfig != "" {
		out = append(out, "location_config")
	}
	if h.AccessListType != "" {
		out = append(out, "access_list_type")
	}
	if h.Meta.Directory() != "" {
		out = append(out, "group")
	}
	return out
}

// stripPlus removes every NPMplus-only setting.
//
// Against upstream nginx-proxy-manager those fields are not just unsendable,
// they must not take part in the comparison either: the server never returns
// them, so a desired state that still carries them would differ from the live
// one on every single run.
func (h *ProxyHost) stripPlus() {
	h.HTTP3Support = false
	h.NoIndex = false
	h.DisableCrowdsecAppsec = false
	h.DisableRequestBuffering = false
	h.DisableResponseBuffering = false
	h.UpstreamCompression = false
	h.FancyIndex = false
	h.XFrameOptions = ""
	h.AuthRequest = ""
	h.AuthRequestUpstream = ""
	h.LocationConfig = ""
	h.Meta.ClearDirectory()
	for i := range h.Locations {
		h.Locations[i].stripPlus()
	}
}

// ---------------------------------------------------------------------------
// Redirection hosts
// ---------------------------------------------------------------------------

// The redirection payload is identical on both flavours apart from the
// NPMplus-only HTTP/3 switch, so one struct with an omitted field serves both.
type redirectPayload struct {
	DomainNames       []string      `json:"domain_names"`
	ForwardScheme     string        `json:"forward_scheme"`
	ForwardDomainName string        `json:"forward_domain_name"`
	ForwardHTTPCode   int           `json:"forward_http_code"`
	PreservePath      Flag          `json:"preserve_path"`
	CertificateID     CertificateID `json:"certificate_id"`
	SSLForced         Flag          `json:"ssl_forced"`
	HSTSEnabled       Flag          `json:"hsts_enabled"`
	HSTSSubdomains    Flag          `json:"hsts_subdomains"`
	HTTP2Support      *Flag         `json:"http2_support,omitempty"`
	HTTP3Support      *Flag         `json:"npmplus_http3_support,omitempty"`
	BlockExploits     *Flag         `json:"block_exploits,omitempty"`
	AdvancedConfig    string        `json:"advanced_config"`
	*npmplusColumns
	Meta Meta `json:"meta,omitempty"`
}

// Payload implements Resource.
func (h *RedirectionHost) Payload(dialect Dialect) (any, error) {
	flavour := dialect.Flavour
	if err := checkLetsEncrypt(flavour, h.CertificateID, h.Meta); err != nil {
		return nil, err
	}
	payload := &redirectPayload{
		DomainNames:       NormalizeDomains(h.DomainNames),
		ForwardScheme:     keepSchemeFor(dialect, h.ForwardScheme),
		ForwardDomainName: h.ForwardDomainName,
		ForwardHTTPCode:   h.ForwardHTTPCode,
		PreservePath:      h.PreservePath,
		CertificateID:     h.CertificateID,
		SSLForced:         h.SSLForced,
		HSTSEnabled:       h.HSTSEnabled,
		HSTSSubdomains:    h.HSTSSubdomains,
		HTTP2Support:      flagIf(dialect, KindRedirect, "http2_support", h.HTTP2Support),
		BlockExploits:     flagIf(dialect, KindRedirect, "block_exploits", h.BlockExploits),
		AdvancedConfig:    advancedFor(dialect, KindRedirect, h.Meta, h.ResourceKey(), h.AdvancedConfig),
		npmplusColumns:    columnsFor(dialect, KindRedirect, h.Meta, h.CertificateID),
		Meta:              metaForWrite(dialect, KindRedirect, h.Meta),
	}
	if flavour != FlavourNPM {
		http3 := h.HTTP3Support
		payload.HTTP3Support = &http3
	}
	return payload, nil
}

// npmplusOnly implements the unsupportedFields interface.
func (h *RedirectionHost) npmplusOnly() []string { return plusList(h.HTTP3Support, h.Meta) }

// stripPlus implements the plusStripper interface.
func (h *RedirectionHost) stripPlus() {
	h.HTTP3Support = false
	h.Meta.ClearDirectory()
}

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// NPMplus types the stream ports as strings (and accepts "8080-8090" or
// "$server_port"), upstream NPM as integers.
type streamPayloadNPMplus struct {
	IncomingPort   string `json:"incoming_port"`
	ForwardingHost string `json:"forwarding_host"`
	ForwardingPort string `json:"forwarding_port"`
	TCPForwarding  Flag   `json:"tcp_forwarding"`
	UDPForwarding  Flag   `json:"udp_forwarding"`
	CertificateID  int    `json:"certificate_id"`
	// ProxyProtocol is a ProxyProtocolLevel or, for the releases that type
	// it so, a boolean; see proxyProtocolFor.
	ProxyProtocol  any    `json:"npmplus_proxy_protocol_forwarding"`
	ProxyTLS       Flag   `json:"npmplus_proxy_tls"`
	AdvancedConfig string `json:"npmplus_advanced_config"`
	Description    string `json:"npmplus_description"`
	*npmplusColumns
	Meta Meta `json:"meta,omitempty"`
}

type streamPayloadNPM struct {
	IncomingPort   int    `json:"incoming_port"`
	ForwardingHost string `json:"forwarding_host"`
	ForwardingPort int    `json:"forwarding_port"`
	TCPForwarding  Flag   `json:"tcp_forwarding"`
	UDPForwarding  Flag   `json:"udp_forwarding"`
	// Same reasoning as trust_forwarded_proto above: stream certificates
	// arrived in NPM 2.12, and 2.11 rejects the body over the field.
	CertificateID *CertificateID `json:"certificate_id,omitempty"`
	Meta          Meta           `json:"meta,omitempty"`
}

// Payload implements Resource.
func (s *Stream) Payload(dialect Dialect) (any, error) {
	flavour := dialect.Flavour
	if flavour == FlavourNPM {
		if s.IncomingPort.Int() == 0 || s.ForwardingPort.Int() == 0 {
			return nil, fmt.Errorf("upstream nginx-proxy-manager needs numeric stream ports, got %q -> %q",
				s.IncomingPort, s.ForwardingPort)
		}
		var certificate *CertificateID
		if dialect.SupportsStreamCertificate() {
			value := s.CertificateID
			certificate = &value
		}
		return &streamPayloadNPM{
			IncomingPort:   s.IncomingPort.Int(),
			ForwardingHost: s.ForwardingHost,
			ForwardingPort: s.ForwardingPort.Int(),
			TCPForwarding:  s.TCPForwarding,
			UDPForwarding:  s.UDPForwarding,
			CertificateID:  certificate,
			Meta:           metaForWrite(dialect, KindStream, s.Meta),
		}, nil
	}

	// NPMplus never issues a certificate for a stream, so "new" has no
	// meaning here and the schema types the field as a plain integer.
	if s.CertificateID.New {
		return nil, fmt.Errorf("certificate_id=%q is not supported for streams; reference an existing certificate id", CertificateNew)
	}
	description := s.Description
	if len(description) > MaxDescriptionLength {
		description = description[:MaxDescriptionLength]
	}
	return &streamPayloadNPMplus{
		IncomingPort:   s.IncomingPort.String(),
		ForwardingHost: s.ForwardingHost,
		ForwardingPort: s.ForwardingPort.String(),
		TCPForwarding:  s.TCPForwarding,
		UDPForwarding:  s.UDPForwarding,
		CertificateID:  s.CertificateID.ID,
		ProxyProtocol:  proxyProtocolFor(dialect, s.ProxyProtocol),
		ProxyTLS:       s.ProxyTLS,
		AdvancedConfig: advancedFor(dialect, KindStream, s.Meta, s.ResourceKey(), s.AdvancedConfig),
		Description:    description,
		npmplusColumns: columnsFor(dialect, KindStream, s.Meta, s.CertificateID),
		Meta:           metaForWrite(dialect, KindStream, s.Meta),
	}, nil
}

// npmplusOnly implements the unsupportedFields interface.
func (s *Stream) npmplusOnly() []string {
	var out []string
	if s.ProxyProtocol != 0 {
		out = append(out, "proxy_protocol")
	}
	if s.ProxyTLS {
		out = append(out, "proxy_tls")
	}
	if s.AdvancedConfig != "" {
		out = append(out, "advanced_config")
	}
	if s.Description != "" {
		out = append(out, "description")
	}
	if s.Meta.Directory() != "" {
		out = append(out, "group")
	}
	return out
}

// stripPlus implements the plusStripper interface.
func (s *Stream) stripPlus() {
	s.ProxyProtocol = 0
	s.ProxyTLS = false
	s.AdvancedConfig = ""
	s.Description = ""
	s.Meta.ClearDirectory()
}

// ---------------------------------------------------------------------------
// Dead (404) hosts
// ---------------------------------------------------------------------------

type deadPayload struct {
	DomainNames    []string      `json:"domain_names"`
	CertificateID  CertificateID `json:"certificate_id"`
	SSLForced      Flag          `json:"ssl_forced"`
	HSTSEnabled    Flag          `json:"hsts_enabled"`
	HSTSSubdomains Flag          `json:"hsts_subdomains"`
	HTTP2Support   *Flag         `json:"http2_support,omitempty"`
	HTTP3Support   *Flag         `json:"npmplus_http3_support,omitempty"`
	AdvancedConfig string        `json:"advanced_config"`
	*npmplusColumns
	Meta Meta `json:"meta,omitempty"`
}

// Payload implements Resource.
func (h *DeadHost) Payload(dialect Dialect) (any, error) {
	flavour := dialect.Flavour
	if err := checkLetsEncrypt(flavour, h.CertificateID, h.Meta); err != nil {
		return nil, err
	}
	payload := &deadPayload{
		DomainNames:    NormalizeDomains(h.DomainNames),
		CertificateID:  h.CertificateID,
		SSLForced:      h.SSLForced,
		HSTSEnabled:    h.HSTSEnabled,
		HSTSSubdomains: h.HSTSSubdomains,
		HTTP2Support:   flagIf(dialect, KindDead, "http2_support", h.HTTP2Support),
		AdvancedConfig: advancedFor(dialect, KindDead, h.Meta, h.ResourceKey(), h.AdvancedConfig),
		npmplusColumns: columnsFor(dialect, KindDead, h.Meta, h.CertificateID),
		Meta:           metaForWrite(dialect, KindDead, h.Meta),
	}
	if flavour != FlavourNPM {
		http3 := h.HTTP3Support
		payload.HTTP3Support = &http3
	}
	return payload, nil
}

// npmplusOnly implements the unsupportedFields interface.
func (h *DeadHost) npmplusOnly() []string { return plusList(h.HTTP3Support, h.Meta) }

// stripPlus implements the plusStripper interface.
func (h *DeadHost) stripPlus() {
	h.HTTP3Support = false
	h.Meta.ClearDirectory()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// plusList is the NPMplus-only report of the three kinds whose only such
// settings are HTTP/3 and the UI group.
func plusList(http3 Flag, meta Meta) []string {
	var out []string
	if http3 {
		out = append(out, "ssl.http3")
	}
	if meta.Directory() != "" {
		out = append(out, "group")
	}
	return out
}

// unsupportedFields is implemented by every resource that can carry
// NPMplus-only settings.
type unsupportedFields interface {
	npmplusOnly() []string
}

// plusStripper is implemented by every resource that can carry NPMplus-only
// settings.
type plusStripper interface {
	stripPlus()
}

// StripNPMplus removes every NPMplus-only setting from a resource, so the
// desired state matches what upstream nginx-proxy-manager can store and
// return. Call it once the flavour is known, before fingerprinting.
func StripNPMplus(r Resource) {
	if s, ok := r.(plusStripper); ok {
		s.stripPlus()
	}
}

// UnsupportedByNPM returns the NPMplus-only fields the resource uses. Against
// upstream nginx-proxy-manager they are silently left out of the request, and
// the reconcile loop reports them once instead of failing the write.
func UnsupportedByNPM(r Resource) []string {
	if u, ok := r.(unsupportedFields); ok {
		return u.npmplusOnly()
	}
	return nil
}

// conformer is implemented by every resource whose desired state depends on
// what the server can store.
type conformer interface {
	conform(dialect Dialect)
}

// Conform brings a desired resource in line with what the server can store
// and return: NPMplus-only settings go against upstream NPM, retired switches
// go against an NPMplus that no longer has them, and the resource records
// whether its ownership marker can be written. Call it once the dialect is
// known, before fingerprinting - a field the server drops would otherwise
// differ from the live resource on every single run.
func Conform(r Resource, dialect Dialect) {
	if dialect.Flavour == FlavourNPM {
		StripNPMplus(r)
	}
	if c, ok := r.(conformer); ok {
		c.conform(dialect)
	}
}

// Labels and API properties of the legacy switches.
const (
	labelHTTP2         = "ssl.http2"
	labelBlockExploits = "block_exploits"
	fieldHTTP2         = "http2_support"
	fieldBlockExploits = "block_exploits"
)

// retiredSwitches maps the label of each legacy switch to its API property.
var retiredSwitches = map[Kind][][2]string{
	KindProxy: {
		{"websockets", "allow_websocket_upgrade"},
		{labelHTTP2, fieldHTTP2},
		{labelBlockExploits, fieldBlockExploits},
		{"caching", "caching_enabled"},
		{"trust_forwarded_proto", "trust_forwarded_proto"},
	},
	KindRedirect: {
		{labelHTTP2, fieldHTTP2},
		{labelBlockExploits, fieldBlockExploits},
	},
	KindDead: {
		{labelHTTP2, fieldHTTP2},
	},
}

// Unavailable returns the labels of the switches this server does not offer
// for the kind: NPMplus retired them (websockets and HTTP/2 are always on
// there), upstream NPM before 2.12 has no trust_forwarded_proto. The caller
// reports those a label set explicitly.
func Unavailable(kind Kind, dialect Dialect) []string {
	var out []string
	for _, pair := range retiredSwitches[kind] {
		if !dialect.Accepts(kind, pair[1]) {
			out = append(out, pair[0])
		}
	}
	return out
}

// conform implements conformer.
func (h *ProxyHost) conform(d Dialect) {
	h.OwnerMarker = Flag(d.CarriesMarker(KindProxy))
	h.TrustForwardedProto = keepIf(d, KindProxy, "trust_forwarded_proto", h.TrustForwardedProto)
	h.HTTP2Support = keepIf(d, KindProxy, "http2_support", h.HTTP2Support)
	h.BlockExploits = keepIf(d, KindProxy, "block_exploits", h.BlockExploits)
	h.CachingEnabled = keepIf(d, KindProxy, "caching_enabled", h.CachingEnabled)
	h.AllowWebsocketUpgrade = keepIf(d, KindProxy, "allow_websocket_upgrade", h.AllowWebsocketUpgrade)
	if !d.Accepts(KindProxy, "locations/npmplus_location_config") {
		for i := range h.Locations {
			h.Locations[i].LocationConfig = ""
		}
	}
}

// conform implements conformer.
func (h *RedirectionHost) conform(d Dialect) {
	h.OwnerMarker = Flag(d.CarriesMarker(KindRedirect))
	h.ForwardScheme = keepSchemeFor(d, h.ForwardScheme)
	h.HTTP2Support = keepIf(d, KindRedirect, "http2_support", h.HTTP2Support)
	h.BlockExploits = keepIf(d, KindRedirect, "block_exploits", h.BlockExploits)
}

// conform implements conformer.
func (s *Stream) conform(d Dialect) {
	s.OwnerMarker = Flag(d.CarriesMarker(KindStream))
	// A server that only knows "on" and "off" stores level 2 as "on" and
	// reports it back as 1.
	if d.TypeOf(KindStream, fieldProxyProtocol) == "boolean" && s.ProxyProtocol > 1 {
		s.ProxyProtocol = 1
	}
}

// proxyProtocolFor writes the stream's proxy protocol in the type the server
// declares. Both kinds of release coerce 0 and 1 into their own type, but
// only the one that declares levels can express 2.
func proxyProtocolFor(d Dialect, level ProxyProtocolLevel) any {
	if d.TypeOf(KindStream, fieldProxyProtocol) == "boolean" {
		return level > 0
	}
	return level
}

// conform implements conformer.
func (h *DeadHost) conform(d Dialect) {
	h.OwnerMarker = Flag(d.CarriesMarker(KindDead))
	h.HTTP2Support = keepIf(d, KindDead, "http2_support", h.HTTP2Support)
}

// keepSchemeFor spells "keep the request's scheme" the way the server needs
// it. NPMplus writes the scheme verbatim into `return 301 <scheme>://...`, so
// it must get nginx' own "$scheme" wherever it accepts it - an "auto" there
// produces a redirect to auto://. Upstream NPM only knows "auto" and
// translates it itself.
func keepSchemeFor(d Dialect, scheme string) string {
	if scheme != SchemeAuto && scheme != SchemeKeep {
		return scheme
	}
	if d.Flavour == FlavourNPMplus && d.AcceptsValue(KindRedirect, "forward_scheme", SchemeKeep) {
		return SchemeKeep
	}
	if d.AcceptsValue(KindRedirect, "forward_scheme", SchemeAuto) {
		return SchemeAuto
	}
	return scheme
}

// Downgraded returns the labels whose value the server can only store in a
// weaker form: a proxy protocol v2 on an NPMplus release that only knows "on".
func Downgraded(r Resource, d Dialect) []string {
	if s, ok := r.(*Stream); ok && s.ProxyProtocol > 1 && d.TypeOf(KindStream, fieldProxyProtocol) == "boolean" {
		return []string{"proxy_protocol"}
	}
	return nil
}

// keepIf clears a switch the server does not store.
func keepIf(d Dialect, kind Kind, field string, value Flag) Flag {
	if !d.Accepts(kind, field) {
		return false
	}
	return value
}

// flagIf returns the switch for the request body, or nil to leave it out.
func flagIf(d Dialect, kind Kind, field string, value Flag) *Flag {
	if !d.Accepts(kind, field) {
		return nil
	}
	return &value
}

// textIf returns the text for the request body, or nil to leave it out.
func textIf(d Dialect, kind Kind, field, value string) *string {
	if !d.Accepts(kind, field) {
		return nil
	}
	return &value
}

// advancedFor renders the advanced configuration of a request: the ownership
// marker in front of what the labels configured.
func advancedFor(d Dialect, kind Kind, meta Meta, key, user string) string {
	if !d.CarriesMarker(kind) {
		return user
	}
	m, ok := markerFor(meta, key)
	if !ok {
		return user
	}
	return withMarker(user, m)
}

// columnsFor fills the properties NPMplus moved out of the meta object, for a
// server that has them.
func columnsFor(d Dialect, kind Kind, meta Meta, certificate CertificateID) *npmplusColumns {
	var columns npmplusColumns
	used := false
	if d.Accepts(kind, "npmplus_directory") {
		// Only a group the labels set (or that was adopted from the live
		// resource) is written; an absent key leaves the column alone.
		if group, ok := meta[MetaDirectory].(string); ok {
			group = strings.TrimSpace(group)
			columns.Directory = &group
			used = true
		}
	}
	if certificate.New && d.Accepts(kind, "npmplus_dns_challenge") {
		if challenge, _ := meta["dns_challenge"].(bool); challenge {
			columns.DNSChallenge = &challenge
			columns.DNSProvider, _ = meta["dns_provider"].(string)
			columns.DNSCredentials, _ = meta["dns_provider_credentials"].(string)
			if seconds := metaInt(meta["propagation_seconds"]); seconds > 0 {
				columns.PropagationSeconds = &seconds
			}
			used = true
		}
	}
	if !used {
		return nil
	}
	return &columns
}

// metaInt reads a number from the meta object, which holds an int when built
// here and a float64 after a JSON round trip.
func metaInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		return 0
	}
}

// idList never returns nil: both schemas type the field as an array, and a nil
// slice would marshal to `null`.
func idList(ids []int) []int {
	if ids == nil {
		return []int{}
	}
	return ids
}

// checkLetsEncrypt guards a certificate request that upstream NPM would
// reject: it takes the ACME account from the host's meta, where the address
// and the agreement are mandatory. NPMplus uses its own ACME_EMAIL instead and
// ignores both fields, so they are optional there.
func checkLetsEncrypt(flavour Flavour, certificate CertificateID, meta Meta) error {
	if flavour != FlavourNPM || !certificate.New {
		return nil
	}
	email, _ := meta["letsencrypt_email"].(string)
	agree, _ := meta["letsencrypt_agree"].(bool)
	if email == "" {
		return fmt.Errorf("upstream nginx-proxy-manager needs letsencrypt.email to request a certificate")
	}
	if !agree {
		return fmt.Errorf("upstream nginx-proxy-manager needs letsencrypt.agree=true to request a certificate")
	}
	return nil
}

// checkScheme rejects an upstream scheme the flavour cannot express. NPMplus
// added grpc/grpcs (plus the internal "path"/"empty" forms); upstream NPM
// only knows http and https.
func checkScheme(flavour Flavour, scheme string) error {
	if flavour != FlavourNPM {
		return nil
	}
	switch scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("forward_scheme %q is an NPMplus extension and not supported by upstream nginx-proxy-manager", scheme)
	}
}
