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

// ---------------------------------------------------------------------------
// Proxy hosts
// ---------------------------------------------------------------------------

type locationPayloadNPMplus struct {
	Path           string `json:"path"`
	ForwardScheme  string `json:"forward_scheme"`
	ForwardHost    string `json:"forward_host"`
	ForwardPort    int    `json:"forward_port"`
	AdvancedConfig string `json:"advanced_config"`
	AccessListIDs  []int  `json:"npmplus_access_list_ids"`
	AccessListType string `json:"npmplus_access_list_type"`
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
	SSLForced             bool                     `json:"ssl_forced"`
	HSTSEnabled           bool                     `json:"hsts_enabled"`
	HSTSSubdomains        bool                     `json:"hsts_subdomains"`
	TrustForwardedProto   bool                     `json:"trust_forwarded_proto"`
	HTTP2Support          bool                     `json:"http2_support"`
	HTTP3Support          bool                     `json:"npmplus_http3_support"`
	BlockExploits         bool                     `json:"block_exploits"`
	CachingEnabled        bool                     `json:"caching_enabled"`
	AllowWebsocketUpgrade bool                     `json:"allow_websocket_upgrade"`
	AccessListIDs         []int                    `json:"npmplus_access_list_ids"`
	AccessListType        string                   `json:"npmplus_access_list_type"`
	AdvancedConfig        string                   `json:"advanced_config"`
	Locations             []locationPayloadNPMplus `json:"locations"`
	Meta                  Meta                     `json:"meta"`
}

type proxyPayloadNPM struct {
	DomainNames           []string             `json:"domain_names"`
	ForwardScheme         string               `json:"forward_scheme"`
	ForwardHost           string               `json:"forward_host"`
	ForwardPort           int                  `json:"forward_port"`
	CertificateID         CertificateID        `json:"certificate_id"`
	SSLForced             bool                 `json:"ssl_forced"`
	HSTSEnabled           bool                 `json:"hsts_enabled"`
	HSTSSubdomains        bool                 `json:"hsts_subdomains"`
	TrustForwardedProto   bool                 `json:"trust_forwarded_proto"`
	HTTP2Support          bool                 `json:"http2_support"`
	BlockExploits         bool                 `json:"block_exploits"`
	CachingEnabled        bool                 `json:"caching_enabled"`
	AllowWebsocketUpgrade bool                 `json:"allow_websocket_upgrade"`
	AccessListID          int                  `json:"access_list_id"`
	AdvancedConfig        string               `json:"advanced_config"`
	Locations             []locationPayloadNPM `json:"locations"`
	Meta                  Meta                 `json:"meta"`
}

// Payload implements Resource.
func (h *ProxyHost) Payload(flavour Flavour) (any, error) {
	if err := checkScheme(flavour, h.ForwardScheme); err != nil {
		return nil, err
	}
	ids, listType := h.accessLists()

	if flavour == FlavourNPM {
		if h.HTTP3Support {
			return nil, fmt.Errorf("npmplus_http3_support is not supported by upstream nginx-proxy-manager")
		}
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
		return &proxyPayloadNPM{
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
			Path:           l.Path,
			ForwardScheme:  l.ForwardScheme,
			ForwardHost:    l.ForwardHost,
			ForwardPort:    l.ForwardPort,
			AdvancedConfig: l.AdvancedConfig,
			AccessListIDs:  idList(locationIDs),
			AccessListType: listType,
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
		Locations:             locations,
		Meta:                  h.Meta,
	}, nil
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
	PreservePath      bool          `json:"preserve_path"`
	CertificateID     CertificateID `json:"certificate_id"`
	SSLForced         bool          `json:"ssl_forced"`
	HSTSEnabled       bool          `json:"hsts_enabled"`
	HSTSSubdomains    bool          `json:"hsts_subdomains"`
	HTTP2Support      bool          `json:"http2_support"`
	HTTP3Support      *bool         `json:"npmplus_http3_support,omitempty"`
	BlockExploits     bool          `json:"block_exploits"`
	AdvancedConfig    string        `json:"advanced_config"`
	Meta              Meta          `json:"meta"`
}

// Payload implements Resource.
func (h *RedirectionHost) Payload(flavour Flavour) (any, error) {
	if flavour == FlavourNPM && h.HTTP3Support {
		return nil, fmt.Errorf("npmplus_http3_support is not supported by upstream nginx-proxy-manager")
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

// ---------------------------------------------------------------------------
// Streams
// ---------------------------------------------------------------------------

// NPMplus types the stream ports as strings (and accepts "8080-8090" or
// "$server_port"), upstream NPM as integers.
type streamPayloadNPMplus struct {
	IncomingPort   string `json:"incoming_port"`
	ForwardingHost string `json:"forwarding_host"`
	ForwardingPort string `json:"forwarding_port"`
	TCPForwarding  bool   `json:"tcp_forwarding"`
	UDPForwarding  bool   `json:"udp_forwarding"`
	CertificateID  int    `json:"certificate_id"`
	Meta           Meta   `json:"meta"`
}

type streamPayloadNPM struct {
	IncomingPort   int           `json:"incoming_port"`
	ForwardingHost string        `json:"forwarding_host"`
	ForwardingPort int           `json:"forwarding_port"`
	TCPForwarding  bool          `json:"tcp_forwarding"`
	UDPForwarding  bool          `json:"udp_forwarding"`
	CertificateID  CertificateID `json:"certificate_id"`
	Meta           Meta          `json:"meta"`
}

// Payload implements Resource.
func (s *Stream) Payload(flavour Flavour) (any, error) {
	if flavour == FlavourNPM {
		if s.IncomingPort.Int() == 0 || s.ForwardingPort.Int() == 0 {
			return nil, fmt.Errorf("upstream nginx-proxy-manager needs numeric stream ports, got %q -> %q",
				s.IncomingPort, s.ForwardingPort)
		}
		return &streamPayloadNPM{
			IncomingPort:   s.IncomingPort.Int(),
			ForwardingHost: s.ForwardingHost,
			ForwardingPort: s.ForwardingPort.Int(),
			TCPForwarding:  s.TCPForwarding,
			UDPForwarding:  s.UDPForwarding,
			CertificateID:  s.CertificateID,
			Meta:           s.Meta,
		}, nil
	}

	// NPMplus never issues a certificate for a stream, so "new" has no
	// meaning here and the schema types the field as a plain integer.
	if s.CertificateID.New {
		return nil, fmt.Errorf("certificate_id=%q is not supported for streams; reference an existing certificate id", CertificateNew)
	}
	return &streamPayloadNPMplus{
		IncomingPort:   s.IncomingPort.String(),
		ForwardingHost: s.ForwardingHost,
		ForwardingPort: s.ForwardingPort.String(),
		TCPForwarding:  s.TCPForwarding,
		UDPForwarding:  s.UDPForwarding,
		CertificateID:  s.CertificateID.ID,
		Meta:           s.Meta,
	}, nil
}

// ---------------------------------------------------------------------------
// Dead (404) hosts
// ---------------------------------------------------------------------------

type deadPayload struct {
	DomainNames    []string      `json:"domain_names"`
	CertificateID  CertificateID `json:"certificate_id"`
	SSLForced      bool          `json:"ssl_forced"`
	HSTSEnabled    bool          `json:"hsts_enabled"`
	HSTSSubdomains bool          `json:"hsts_subdomains"`
	HTTP2Support   bool          `json:"http2_support"`
	HTTP3Support   *bool         `json:"npmplus_http3_support,omitempty"`
	AdvancedConfig string        `json:"advanced_config"`
	Meta           Meta          `json:"meta"`
}

// Payload implements Resource.
func (h *DeadHost) Payload(flavour Flavour) (any, error) {
	if flavour == FlavourNPM && h.HTTP3Support {
		return nil, fmt.Errorf("npmplus_http3_support is not supported by upstream nginx-proxy-manager")
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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// idList never returns nil: both schemas type the field as an array, and a nil
// slice would marshal to `null`.
func idList(ids []int) []int {
	if ids == nil {
		return []int{}
	}
	return ids
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
