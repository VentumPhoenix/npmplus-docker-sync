// Package fields is the single source of truth for every configurable
// property of a managed NPM resource.
//
// One table defines, per resource kind, the canonical label name, the
// spellings accepted as aliases (including the ones used by
// Redth/npm-docker-sync), the matching environment variable, the built-in
// default and the NPM/NPMplus API field it ends up in. Label parsing, the
// global defaults, the start-up log and the generated documentation all read
// from this table, so they cannot drift apart.
package fields

import (
	"fmt"
	"sort"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Type is the value domain of a field. It decides how a label value is
// parsed and which values the documentation advertises.
type Type int

// The supported value domains.
const (
	TypeString Type = iota
	TypeBool
	TypeInt
	TypePort
	TypeEnum
	TypeDomains
	TypeList
	TypeCertificate
)

// String implements fmt.Stringer and doubles as the documented value domain.
func (t Type) String() string {
	switch t {
	case TypeBool:
		return "bool"
	case TypeInt:
		return "int"
	case TypePort:
		return "port"
	case TypeEnum:
		return "enum"
	case TypeDomains:
		return "domains"
	case TypeList:
		return "list"
	case TypeCertificate:
		return "certificate"
	default:
		return "string"
	}
}

// DefaultPortPreference is the order in which an exposed container port is
// picked when a container exposes several of them (NPM_PORT_PREFERENCE).
var DefaultPortPreference = []int{80, 8080, 3000, 8000, 443}

// Auto is the default value of every field whose built-in default is derived
// at runtime (the certificate, the upstream port, ssl_forced).
const Auto = "auto"

// Field describes one configurable property.
type Field struct {
	// Name is the canonical label spelling, e.g. "ssl.hsts_subdomains".
	Name string
	// Aliases are additional accepted spellings. They also produce
	// environment variable aliases, which is how the Redth names
	// (NPM_PROXY_SSL_FORCE, NPM_PROXY_HSTS_SUBDOMAINS, ...) keep working.
	Aliases []string
	// Type is the value domain.
	Type Type
	// Enum lists the accepted values of a TypeEnum field (lower case).
	Enum []string
	// Default is the built-in default, "" meaning "unset".
	Default string
	// APIField is the NPM/NPMplus property the value is written to.
	APIField string
	// Plus marks a field that only exists on NPMplus.
	Plus bool
	// Invert marks a field whose API property has the opposite meaning
	// ("crowdsec_appsec: true" writes npmplus_crowdsec_appsec: false).
	Invert bool
	// Doc is the one line description used by the generated field table.
	Doc string
}

// Canonical field names. Referencing them as constants keeps the parser and
// the table honest about spelling.
const (
	Enable  = "enable"
	Enabled = "enabled"

	Domains        = "domains"
	Certificate    = "certificate"
	SSLForced      = "ssl.forced"
	HTTP2          = "ssl.http2"
	HTTP3          = "ssl.http3"
	HSTS           = "ssl.hsts"
	HSTSSubdomains = "ssl.hsts_subdomains"
	AdvancedConfig = "advanced_config"

	LEEmail        = "letsencrypt.email"
	LEAgree        = "letsencrypt.agree"
	LEDNSChallenge = "letsencrypt.dns_challenge"
	LEDNSProvider  = "letsencrypt.dns_provider"
	LEDNSCreds     = "letsencrypt.dns_credentials" //nolint:gosec // field name, not a credential
	LEPropagation  = "letsencrypt.propagation_seconds"

	ForwardHost   = "forward_host"
	ForwardPort   = "forward_port"
	ForwardScheme = "forward_scheme"

	Websockets          = "websockets"
	Caching             = "caching"
	BlockExploits       = "block_exploits"
	TrustForwardedProto = "trust_forwarded_proto"
	AccessList          = "access_list"
	AccessListType      = "access_list_type"
	AuthRequest         = "auth_request"
	AuthRequestUpstream = "auth_request_upstream"
	LocationConfig      = "location_config"
	NoIndex             = "noindex"
	CrowdsecAppsec      = "crowdsec_appsec"
	RequestBuffering    = "request_buffering"
	ResponseBuffering   = "response_buffering"
	UpstreamCompression = "upstream_compression"
	FancyIndex          = "fancyindex"
	XFrameOptions       = "x_frame_options"
	Location            = "location"
	ResolveIP           = "resolve_ip"

	ForwardDomain = "forward_domain"
	HTTPCode      = "http_code"
	PreservePath  = "preserve_path"

	IncomingPort  = "incoming_port"
	TCP           = "tcp"
	UDP           = "udp"
	Protocol      = "protocol"
	ProxyProtocol = "proxy_protocol"
	ProxyTLS      = "proxy_tls"
	Description   = "description"
)

// Enum values.
var (
	// SchemeValues are the upstream schemes NPMplus accepts. Upstream NPM
	// only knows http and https, which payloads.go enforces.
	SchemeValues = []string{"http", "https", "grpc", "grpcs"}
	// RedirectSchemeValues are the schemes of a redirection host.
	RedirectSchemeValues = []string{"auto", "http", "https"}
	// AuthRequestValues are the NPMplus forward-auth integrations.
	AuthRequestValues = []string{
		"none", "anubis", "tinyauth", "oauth2proxy", "voidauth",
		"authelia", "authentik", "authentik-send-basic-auth",
	}
	// XFrameOptionsValues are the accepted X-Frame-Options settings. They are
	// matched case-insensitively and written back in NPMplus' spelling.
	XFrameOptionsValues = []string{"deny", "sameorigin", "upstream", "none"}
	// AccessListTypeValues are the access list modes of a proxy host.
	AccessListTypeValues = []string{"public", "custom"}
	// LocationAccessListTypeValues additionally allow inheriting from the host.
	LocationAccessListTypeValues = []string{"global", "public", "custom"}
	// LocationTypeValues are the nginx location modifiers.
	LocationTypeValues = []string{"prefix", "exact", "regex", "iregex", "prefer", "named"}
	// ProxyProtocolValues are the stream PROXY protocol versions.
	ProxyProtocolValues = []string{"off", "v1", "v2"}
	// StreamProtocolValues are the transport shorthands of a stream.
	StreamProtocolValues = []string{"tcp", "udp", "both"}
)

// certificateField is shared by every kind that can carry a certificate.
func certificateField() Field {
	return Field{
		Name:     Certificate,
		Aliases:  []string{"certificate_id", "cert", "ssl.certificate", "ssl.certificate.id", "ssl.cert"},
		Type:     TypeCertificate,
		Default:  Auto,
		APIField: "certificate_id",
		Doc:      "auto, a certificate id, a domain, name:<nice name>, new or none",
	}
}

// sslFields are the TLS switches shared by proxy, redirection and 404 hosts.
func sslFields() []Field {
	return []Field{
		certificateField(),
		{
			Name: SSLForced, Aliases: []string{"ssl.force", "force_ssl", "ssl_forced"},
			Type: TypeBool, Default: Auto, APIField: "ssl_forced",
			Doc: "redirect http to https; auto means \"on as soon as a certificate is attached\"",
		},
		{
			Name: HTTP2, Aliases: []string{"http2", "http2_support"},
			Type: TypeBool, Default: "true", APIField: "http2_support",
			Doc: "HTTP/2 support",
		},
		{
			Name: HTTP3, Aliases: []string{"http3", "http3_support", "quic"},
			Type: TypeBool, Default: "true", APIField: "npmplus_http3_support", Plus: true,
			Doc: "HTTP/3 (QUIC) support",
		},
		{
			Name: HSTS, Aliases: []string{"hsts", "hsts_enabled"},
			Type: TypeBool, Default: "false", APIField: "hsts_enabled",
			Doc: "send Strict-Transport-Security (hard to undo, opt-in)",
		},
		{
			Name: HSTSSubdomains, Aliases: []string{"hsts_subdomains", "hsts.sub"},
			Type: TypeBool, Default: "false", APIField: "hsts_subdomains",
			Doc: "extend HSTS to all subdomains",
		},
	}
}

// letsencryptFields configure a certificate request (certificate: new).
func letsencryptFields() []Field {
	return []Field{
		{
			Name: LEEmail, Aliases: []string{"le.email", "certificate.email"},
			Type: TypeString, APIField: "meta.letsencrypt_email",
			Doc: "contact address (upstream NPM only; NPMplus uses its own ACME_EMAIL)",
		},
		{
			Name: LEAgree, Aliases: []string{"le.agree", "certificate.agree"},
			Type: TypeBool, Default: "false", APIField: "meta.letsencrypt_agree",
			Doc: "accept the Let's Encrypt terms (upstream NPM only)",
		},
		{
			Name: LEDNSChallenge, Aliases: []string{"le.dns_challenge", "dns_challenge"},
			Type: TypeBool, Default: "false", APIField: "meta.dns_challenge",
			Doc: "use a DNS-01 challenge instead of HTTP-01",
		},
		{
			Name: LEDNSProvider, Aliases: []string{"le.dns_provider", "dns_provider"},
			Type: TypeString, APIField: "meta.dns_provider",
			Doc: "certbot DNS plugin name",
		},
		{
			Name: LEDNSCreds, Aliases: []string{"le.dns_credentials", "dns_credentials", "letsencrypt.dns_provider_credentials"},
			Type: TypeString, APIField: "meta.dns_provider_credentials",
			Doc: "credentials file content for the DNS plugin",
		},
		{
			Name: LEPropagation, Aliases: []string{"le.propagation_seconds", "propagation_seconds"},
			Type: TypeInt, Default: "0", APIField: "meta.propagation_seconds",
			Doc: "how long to wait for the DNS record to propagate",
		},
	}
}

// enabledField switches a single resource on or off.
func enabledField() Field {
	return Field{
		Name: Enabled, Aliases: []string{Enable},
		Type: TypeBool, Default: "true", APIField: "enabled",
		Doc: "create the resource in the disabled state when false",
	}
}

// domainsField is the host name list. Since v1.0.0-beta.3 `host` no longer
// means the domain for proxy hosts and streams (it is the upstream, as in
// Redth/npm-docker-sync), so it is only an alias where no upstream exists.
func domainsField(hostAlias bool) Field {
	f := Field{
		Name: Domains, Aliases: []string{"domain", "domain_names", "domain_name"},
		Type: TypeDomains, APIField: "domain_names",
		Doc: "domain names served by this resource (required)",
	}
	if hostAlias {
		f.Aliases = append(f.Aliases, "host", "hosts")
	}
	return f
}

// Table is the field table, one entry per resource kind. Location blocks
// reuse the proxy entries through LocationFields.
var Table = map[npm.Kind][]Field{
	npm.KindProxy:    proxyFields(),
	npm.KindRedirect: redirectFields(),
	npm.KindStream:   streamFields(),
	npm.KindDead:     deadFields(),
}

func proxyFields() []Field {
	out := make([]Field, 0, 25)
	out = append(out, []Field{
		domainsField(false),
		{
			Name: ForwardHost, Aliases: []string{"host", "upstream", "forward_ip", "ip", "target"},
			Type: TypeString, APIField: "forward_host",
			Doc: "upstream address; defaults to the container itself",
		},
		{
			Name: ForwardPort, Aliases: []string{"port", "upstream_port"},
			Type: TypePort, Default: Auto, APIField: "forward_port",
			Doc: "upstream port; auto reads it from the container's exposed ports",
		},
		{
			Name: ResolveIP, Aliases: []string{"resolve_container_ip"},
			Type: TypeBool, APIField: "forward_host",
			Doc: "use the container IP as upstream; defaults to RESOLVE_CONTAINER_IP",
		},
		{
			Name: ForwardScheme, Aliases: []string{"scheme", "upstream_scheme"},
			Type: TypeEnum, Enum: SchemeValues, Default: "http", APIField: "forward_scheme",
			Doc: "how to talk to the upstream",
		},
		{
			Name: Websockets, Aliases: []string{"websocket", "ws", "allow_websocket_upgrade"},
			Type: TypeBool, Default: "true", APIField: "allow_websocket_upgrade",
			Doc: "allow websocket upgrades",
		},
		{
			Name: Caching, Aliases: []string{"cache", "caching_enabled"},
			Type: TypeBool, Default: "false", APIField: "caching_enabled",
			Doc: "cache static assets (can break dynamic content)",
		},
		{
			Name: BlockExploits, Aliases: []string{"block_common_exploits", "exploits"},
			Type: TypeBool, Default: "true", APIField: "block_exploits",
			Doc: "block common exploit patterns",
		},
		{
			Name: TrustForwardedProto, Aliases: []string{"trust_proto"},
			Type: TypeBool, Default: "false", APIField: "trust_forwarded_proto",
			Doc: "trust X-Forwarded-Proto (only behind another proxy)",
		},
		{
			Name: AccessList, Aliases: []string{"access_list_id", "access_list_ids", "access_lists", "accesslist", "accesslist.id", "accesslist.ids"},
			Type: TypeList, APIField: "npmplus_access_list_ids",
			Doc: "access list ids or names, comma separated",
		},
		{
			Name: AccessListType, Aliases: []string{"accesslist.type"},
			Type: TypeEnum, Enum: AccessListTypeValues, APIField: "npmplus_access_list_type", Plus: true,
			Doc: "public or custom; derived from access_list when unset",
		},
		{
			Name: AuthRequest, Aliases: []string{"auth", "forward_auth"},
			Type: TypeEnum, Enum: AuthRequestValues, Default: "none", APIField: "npmplus_auth_request", Plus: true,
			Doc: "forward authentication integration",
		},
		{
			Name: AuthRequestUpstream, Aliases: []string{"auth.upstream"},
			Type: TypeString, APIField: "npmplus_auth_request_upstream", Plus: true,
			Doc: "address of the auth service",
		},
		{
			Name: LocationConfig, Type: TypeString, APIField: "npmplus_location_config", Plus: true,
			Doc: "nginx snippet inserted into location /",
		},
		{
			Name: AdvancedConfig, Aliases: []string{"advanced", "custom_config", "advanced.config"},
			Type: TypeString, APIField: "advanced_config",
			Doc: "nginx snippet inserted into the server block",
		},
		{
			Name: NoIndex, Aliases: []string{"no_index"},
			Type: TypeBool, Default: "false", APIField: "npmplus_noindex", Plus: true,
			Doc: "send X-Robots-Tag: noindex and block scrapers",
		},
		{
			Name: CrowdsecAppsec, Aliases: []string{"crowdsec", "appsec"},
			Type: TypeBool, Default: "true", APIField: "npmplus_crowdsec_appsec", Plus: true, Invert: true,
			Doc: "run requests through the CrowdSec AppSec component",
		},
		{
			Name: RequestBuffering, Aliases: []string{"proxy_request_buffering"},
			Type: TypeBool, Default: "true", APIField: "npmplus_proxy_request_buffering", Plus: true, Invert: true,
			Doc: "buffer the request body before passing it upstream",
		},
		{
			Name: ResponseBuffering, Aliases: []string{"proxy_response_buffering"},
			Type: TypeBool, Default: "true", APIField: "npmplus_proxy_response_buffering", Plus: true, Invert: true,
			Doc: "buffer the upstream response",
		},
		{
			Name: UpstreamCompression, Aliases: []string{"compression"},
			Type: TypeBool, Default: "false", APIField: "npmplus_upstream_compression", Plus: true,
			Doc: "let the upstream compress instead of nginx",
		},
		{
			Name: FancyIndex, Aliases: []string{"fancy_index"},
			Type: TypeBool, Default: "false", APIField: "npmplus_fancyindex", Plus: true,
			Doc: "pretty directory listings",
		},
		{
			Name: XFrameOptions, Aliases: []string{"xframe_options", "frame_options"},
			Type: TypeEnum, Enum: XFrameOptionsValues, APIField: "npmplus_x_frame_options", Plus: true,
			Doc: "X-Frame-Options header; unset keeps the NPMplus default",
		},
		enabledField(),
	}...)
	out = append(out, sslFields()...)
	out = append(out, letsencryptFields()...)
	return out
}

func redirectFields() []Field {
	out := make([]Field, 0, 8)
	out = append(out, []Field{
		domainsField(true),
		{
			Name: ForwardDomain, Aliases: []string{"forward_domain_name", "to", "target", "destination"},
			Type: TypeString, APIField: "forward_domain_name",
			Doc: "domain to redirect to (required)",
		},
		{
			Name: ForwardScheme, Aliases: []string{"scheme"},
			Type: TypeEnum, Enum: RedirectSchemeValues, Default: Auto, APIField: "forward_scheme",
			Doc: "scheme of the redirect target",
		},
		{
			Name: HTTPCode, Aliases: []string{"code", "forward_http_code", "status"},
			Type: TypeInt, Default: "301", APIField: "forward_http_code",
			Doc: "redirect status code (300-308)",
		},
		{
			Name: PreservePath, Aliases: []string{"preserve"},
			Type: TypeBool, Default: "true", APIField: "preserve_path",
			Doc: "append the original path to the target",
		},
		{
			Name: BlockExploits, Aliases: []string{"block_common_exploits", "exploits"},
			Type: TypeBool, Default: "true", APIField: "block_exploits",
			Doc: "block common exploit patterns",
		},
		{
			Name: AdvancedConfig, Aliases: []string{"advanced", "custom_config", "advanced.config"},
			Type: TypeString, APIField: "advanced_config",
			Doc: "nginx snippet inserted into the server block",
		},
		enabledField(),
	}...)
	out = append(out, sslFields()...)
	out = append(out, letsencryptFields()...)
	return out
}

func streamFields() []Field {
	return []Field{
		{
			Name: IncomingPort, Aliases: []string{"port", "listen_port", "incoming.port", "listen"},
			Type: TypePort, APIField: "incoming_port",
			Doc: "port NPM listens on (required)",
		},
		{
			Name: ForwardHost, Aliases: []string{"host", "forward.host", "forwarding_host", "upstream", "forward_ip", "ip", "target"},
			Type: TypeString, APIField: "forwarding_host",
			Doc: "upstream address; defaults to the container itself",
		},
		{
			// No "auto" here: a stream forwards to the port it listens on,
			// which is a far better guess than a port the image exposes.
			Name: ForwardPort, Aliases: []string{"forward.port", "forwarding_port"},
			Type: TypePort, APIField: "forwarding_port",
			Doc: "upstream port; defaults to the incoming port",
		},
		{
			Name: ResolveIP, Aliases: []string{"resolve_container_ip"},
			Type: TypeBool, APIField: "forwarding_host",
			Doc: "use the container IP as upstream; defaults to RESOLVE_CONTAINER_IP",
		},
		{
			Name: TCP, Aliases: []string{"forward.tcp", "tcp_forwarding"},
			Type: TypeBool, Default: "true", APIField: "tcp_forwarding",
			Doc: "forward TCP",
		},
		{
			Name: UDP, Aliases: []string{"forward.udp", "udp_forwarding"},
			Type: TypeBool, Default: "false", APIField: "udp_forwarding",
			Doc: "forward UDP",
		},
		{
			Name: Protocol, Type: TypeEnum, Enum: StreamProtocolValues, APIField: "tcp_forwarding/udp_forwarding",
			Doc: "shorthand for the tcp and udp switches",
		},
		{
			Name: ProxyProtocol, Aliases: []string{"proxy_protocol_forwarding"},
			Type: TypeEnum, Enum: ProxyProtocolValues, Default: "off",
			APIField: "npmplus_proxy_protocol_forwarding", Plus: true,
			Doc: "send a PROXY protocol header upstream",
		},
		{
			Name: ProxyTLS, Aliases: []string{"proxy_ssl"},
			Type: TypeBool, Default: "false", APIField: "npmplus_proxy_tls", Plus: true,
			Doc: "terminate TLS towards the upstream",
		},
		{
			Name: AdvancedConfig, Aliases: []string{"advanced", "custom_config", "advanced.config"},
			Type: TypeString, APIField: "npmplus_advanced_config", Plus: true,
			Doc: "nginx snippet inserted into the stream block",
		},
		{
			Name: Description, Aliases: []string{"comment", "note"},
			Type: TypeString, APIField: "npmplus_description", Plus: true,
			Doc: "free text shown in the UI; defaults to the container name",
		},
		{
			Name: Certificate, Aliases: []string{"certificate_id", "cert", "ssl", "ssl.certificate", "ssl.certificate.id"},
			Type: TypeCertificate, Default: Auto, APIField: "certificate_id",
			Doc: "auto, a certificate id, a domain or name:<nice name>",
		},
		enabledField(),
	}
}

func deadFields() []Field {
	out := make([]Field, 0, 3)
	out = append(out, []Field{
		domainsField(true),
		{
			Name: AdvancedConfig, Aliases: []string{"advanced", "custom_config", "advanced.config"},
			Type: TypeString, APIField: "advanced_config",
			Doc: "nginx snippet inserted into the server block",
		},
		enabledField(),
	}...)
	out = append(out, sslFields()...)
	out = append(out, letsencryptFields()...)
	return out
}

// LocationFields are the fields a custom location block accepts. They are the
// per-location subset of the proxy fields plus the location's own path and
// type.
var LocationFields = []Field{
	{Name: "path", Type: TypeString, APIField: "path", Doc: "location path (required)"},
	{
		Name: "type", Aliases: []string{"location_type", "modifier"},
		Type: TypeEnum, Enum: LocationTypeValues, Default: "prefix", APIField: "location_type", Plus: true,
		Doc: "prefix, exact, regex, iregex, prefer or named",
	},
	{Name: ForwardHost, Aliases: []string{"host", "upstream"}, Type: TypeString, APIField: "forward_host", Doc: "upstream address; inherited from the host"},
	{Name: ForwardPort, Aliases: []string{"port"}, Type: TypePort, APIField: "forward_port", Doc: "upstream port; inherited from the host"},
	{Name: ForwardScheme, Aliases: []string{"scheme"}, Type: TypeEnum, Enum: SchemeValues, APIField: "forward_scheme", Doc: "upstream scheme; inherited from the host"},
	{Name: AdvancedConfig, Aliases: []string{"advanced", "advanced.config"}, Type: TypeString, APIField: "advanced_config", Doc: "nginx snippet for this location"},
	{Name: LocationConfig, Type: TypeString, APIField: "npmplus_location_config", Plus: true, Doc: "nginx snippet inside the location block"},
	{Name: AccessList, Aliases: []string{"access_list_id", "access_list_ids", "access_lists", "accesslist"}, Type: TypeList, APIField: "npmplus_access_list_ids", Plus: true, Doc: "access list ids or names"},
	{Name: AccessListType, Type: TypeEnum, Enum: LocationAccessListTypeValues, APIField: "npmplus_access_list_type", Plus: true, Doc: "global, public or custom"},
	{Name: Enabled, Aliases: []string{Enable}, Type: TypeBool, Default: "true", APIField: "npmplus_enabled", Plus: true, Doc: "disable a single location"},
	{Name: NoIndex, Type: TypeBool, APIField: "npmplus_noindex", Plus: true, Doc: "inherited from the host"},
	{Name: CrowdsecAppsec, Aliases: []string{"crowdsec", "appsec"}, Type: TypeBool, APIField: "npmplus_crowdsec_appsec", Plus: true, Invert: true, Doc: "inherited from the host"},
	{Name: RequestBuffering, Type: TypeBool, APIField: "npmplus_proxy_request_buffering", Plus: true, Invert: true, Doc: "inherited from the host"},
	{Name: ResponseBuffering, Type: TypeBool, APIField: "npmplus_proxy_response_buffering", Plus: true, Invert: true, Doc: "inherited from the host"},
	{Name: UpstreamCompression, Aliases: []string{"compression"}, Type: TypeBool, APIField: "npmplus_upstream_compression", Plus: true, Doc: "inherited from the host"},
	{Name: FancyIndex, Aliases: []string{"fancy_index"}, Type: TypeBool, APIField: "npmplus_fancyindex", Plus: true, Doc: "inherited from the host"},
	{Name: XFrameOptions, Aliases: []string{"xframe_options"}, Type: TypeEnum, Enum: XFrameOptionsValues, APIField: "npmplus_x_frame_options", Plus: true, Doc: "inherited from the host"},
	{Name: AuthRequest, Aliases: []string{"auth"}, Type: TypeEnum, Enum: AuthRequestValues, APIField: "npmplus_auth_request", Plus: true, Doc: "inherited from the host"},
	{Name: AuthRequestUpstream, Type: TypeString, APIField: "npmplus_auth_request_upstream", Plus: true, Doc: "inherited from the host"},
}

// index is the lookup table built from Table: normalised spelling -> field.
var (
	index         = map[npm.Kind]map[string]Field{}
	locationIndex = map[string]Field{}
	// inverseAliases maps the explicit "disable_*" spelling of an inverted
	// field to its positive counterpart.
	inverseAliases = map[string]string{
		normalize("disable_crowdsec_appsec"):          CrowdsecAppsec,
		normalize("disable_crowdsec"):                 CrowdsecAppsec,
		normalize("disable_request_buffering"):        RequestBuffering,
		normalize("disable_proxy_request_buffering"):  RequestBuffering,
		normalize("disable_response_buffering"):       ResponseBuffering,
		normalize("disable_proxy_response_buffering"): ResponseBuffering,
	}
)

func init() {
	for kind, list := range Table {
		m := make(map[string]Field, len(list)*2)
		for _, f := range list {
			for _, spelling := range f.spellings() {
				m[spelling] = f
			}
		}
		index[kind] = m
	}
	for _, f := range LocationFields {
		for _, spelling := range f.spellings() {
			locationIndex[spelling] = f
		}
	}
}

// spellings returns the canonical name and every alias, normalised.
func (f Field) spellings() []string {
	out := make([]string, 0, len(f.Aliases)+1)
	out = append(out, normalize(f.Name))
	for _, alias := range f.Aliases {
		out = append(out, normalize(alias))
	}
	return out
}

// IsInverseAlias reports whether name is the explicit "disable_x" spelling of
// an inverted field, and returns the canonical field name.
func IsInverseAlias(name string) (string, bool) {
	field, ok := inverseAliases[normalize(name)]
	return field, ok
}

// normalize folds the three separators users mix ('.', '_' and '-') into one
// and lower-cases the result, so ssl.hsts.subdomains, ssl.hsts_subdomains and
// ssl-hsts-subdomains are the same field.
func normalize(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.NewReplacer("_", ".", "-", ".").Replace(name)
	for strings.Contains(name, "..") {
		name = strings.ReplaceAll(name, "..", ".")
	}
	return strings.Trim(name, ".")
}

// Normalize exposes the label-name normalisation used for lookups.
func Normalize(name string) string { return normalize(name) }

// Lookup resolves a label field name (any accepted spelling) for a kind.
func Lookup(kind npm.Kind, name string) (Field, bool) {
	f, ok := index[kind][normalize(name)]
	return f, ok
}

// LookupLocation resolves a field name inside a location block.
func LookupLocation(name string) (Field, bool) {
	f, ok := locationIndex[normalize(name)]
	return f, ok
}

// Kinds returns the resource kinds in their canonical order.
func Kinds() []npm.Kind { return npm.Kinds }

// List returns the fields of a kind sorted by canonical name.
func List(kind npm.Kind) []Field {
	list := append([]Field(nil), Table[kind]...)
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// Suggest returns the closest known field name for a misspelling, or "" when
// nothing is close enough. It powers the "did you mean" hint for unknown
// labels.
func Suggest(kind npm.Kind, name string) string {
	return suggestFrom(index[kind], name)
}

// SuggestLocation is Suggest for location fields.
func SuggestLocation(name string) string { return suggestFrom(locationIndex, name) }

func suggestFrom(candidates map[string]Field, name string) string {
	normalized := normalize(name)
	if normalized == "" {
		return ""
	}
	// Allow roughly one typo per four characters, at least one.
	budget := len(normalized)/4 + 1
	best, bestDistance := "", budget+1
	names := make([]string, 0, len(candidates))
	for spelling := range candidates {
		names = append(names, spelling)
	}
	sort.Strings(names) // deterministic on ties
	for _, spelling := range names {
		d := levenshtein(normalized, spelling)
		if d < bestDistance {
			best, bestDistance = candidates[spelling].Name, d
		}
	}
	if bestDistance > budget {
		return ""
	}
	return best
}

// levenshtein returns the edit distance between a and b.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}

// EnumHint renders the accepted values of an enum field for an error message.
func (f Field) EnumHint() string {
	return strings.Join(f.Enum, ", ")
}

// ValidateEnum matches a value against the field's enum, case-insensitively.
func (f Field) ValidateEnum(value string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, allowed := range f.Enum {
		if v == allowed {
			return allowed, nil
		}
	}
	return "", fmt.Errorf("%q is not one of: %s", value, f.EnumHint())
}
