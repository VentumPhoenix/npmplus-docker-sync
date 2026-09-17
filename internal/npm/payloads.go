package npm

import "fmt"

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
	LocationConfig string `json:"npmplus_location_config"`
	AccessListIDs  []int  `json:"npmplus_access_list_ids"`
	AccessListType string `json:"npmplus_access_list_type"`

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
	TrustForwardedProto   Flag                     `json:"trust_forwarded_proto"`
	HTTP2Support          Flag                     `json:"http2_support"`
	HTTP3Support          Flag                     `json:"npmplus_http3_support"`
	BlockExploits         Flag                     `json:"block_exploits"`
	CachingEnabled        Flag                     `json:"caching_enabled"`
	AllowWebsocketUpgrade Flag                     `json:"allow_websocket_upgrade"`
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

	Meta Meta `json:"meta"`
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
	Meta                  Meta                 `json:"meta"`
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
			AdvancedConfig:        h.AdvancedConfig,
			Locations:             locations,
			Meta:                  h.Meta,
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
			LocationConfig:           l.LocationConfig,
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
		TrustForwardedProto:   h.TrustForwardedProto,
		HTTP2Support:          h.HTTP2Support,
		HTTP3Support:          h.HTTP3Support,
		BlockExploits:         h.BlockExploits,
		CachingEnabled:        h.CachingEnabled,
		AllowWebsocketUpgrade: h.AllowWebsocketUpgrade,
		AccessListIDs:         idList(ids),
		AccessListType:        listType,
		AdvancedConfig:        h.AdvancedConfig,
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

		Meta: h.Meta,
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
	HTTP2Support      Flag          `json:"http2_support"`
	HTTP3Support      *Flag         `json:"npmplus_http3_support,omitempty"`
	BlockExploits     Flag          `json:"block_exploits"`
	AdvancedConfig    string        `json:"advanced_config"`
	Meta              Meta          `json:"meta"`
}

// Payload implements Resource.
func (h *RedirectionHost) Payload(dialect Dialect) (any, error) {
	flavour := dialect.Flavour
	if err := checkLetsEncrypt(flavour, h.CertificateID, h.Meta); err != nil {
		return nil, err
	}
	payload := &redirectPayload{
		DomainNames:       NormalizeDomains(h.DomainNames),
		ForwardScheme:     h.ForwardScheme,
		ForwardDomainName: h.ForwardDomainName,
		ForwardHTTPCode:   h.ForwardHTTPCode,
		PreservePath:      h.PreservePath,
		CertificateID:     h.CertificateID,
		SSLForced:         h.SSLForced,
		HSTSEnabled:       h.HSTSEnabled,
		HSTSSubdomains:    h.HSTSSubdomains,
		HTTP2Support:      h.HTTP2Support,
		BlockExploits:     h.BlockExploits,
		AdvancedConfig:    h.AdvancedConfig,
		Meta:              h.Meta,
	}
	if flavour != FlavourNPM {
		http3 := h.HTTP3Support
		payload.HTTP3Support = &http3
	}
	return payload, nil
}

// npmplusOnly implements the unsupportedFields interface.
func (h *RedirectionHost) npmplusOnly() []string {
	if h.HTTP3Support {
		return []string{"ssl.http3"}
	}
	return nil
}

// stripPlus implements the plusStripper interface.
func (h *RedirectionHost) stripPlus() { h.HTTP3Support = false }

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// NPMplus types the stream ports as strings (and accepts "8080-8090" or
// "$server_port"), upstream NPM as integers.
type streamPayloadNPMplus struct {
	IncomingPort   string             `json:"incoming_port"`
	ForwardingHost string             `json:"forwarding_host"`
	ForwardingPort string             `json:"forwarding_port"`
	TCPForwarding  Flag               `json:"tcp_forwarding"`
	UDPForwarding  Flag               `json:"udp_forwarding"`
	CertificateID  int                `json:"certificate_id"`
	ProxyProtocol  ProxyProtocolLevel `json:"npmplus_proxy_protocol_forwarding"`
	ProxyTLS       Flag               `json:"npmplus_proxy_tls"`
	AdvancedConfig string             `json:"npmplus_advanced_config"`
	Description    string             `json:"npmplus_description"`
	Meta           Meta               `json:"meta"`
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
	Meta          Meta           `json:"meta"`
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
			Meta:           s.Meta,
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
		ProxyProtocol:  s.ProxyProtocol,
		ProxyTLS:       s.ProxyTLS,
		AdvancedConfig: s.AdvancedConfig,
		Description:    description,
		Meta:           s.Meta,
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
	return out
}

// stripPlus implements the plusStripper interface.
func (s *Stream) stripPlus() {
	s.ProxyProtocol = 0
	s.ProxyTLS = false
	s.AdvancedConfig = ""
	s.Description = ""
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
	HTTP2Support   Flag          `json:"http2_support"`
	HTTP3Support   *Flag         `json:"npmplus_http3_support,omitempty"`
	AdvancedConfig string        `json:"advanced_config"`
	Meta           Meta          `json:"meta"`
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
		HTTP2Support:   h.HTTP2Support,
		AdvancedConfig: h.AdvancedConfig,
		Meta:           h.Meta,
	}
	if flavour != FlavourNPM {
		http3 := h.HTTP3Support
		payload.HTTP3Support = &http3
	}
	return payload, nil
}

// npmplusOnly implements the unsupportedFields interface.
func (h *DeadHost) npmplusOnly() []string {
	if h.HTTP3Support {
		return []string{"ssl.http3"}
	}
	return nil
}

// stripPlus implements the plusStripper interface.
func (h *DeadHost) stripPlus() { h.HTTP3Support = false }

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

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
