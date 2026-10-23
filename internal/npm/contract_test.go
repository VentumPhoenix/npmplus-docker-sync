package npm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dlclark/regexp2"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The request schemas in testdata/schema are the real ones, vendored from the
// NPM and NPMplus repositories by scripts/vendor-schemas.py. Every one of them
// sets `additionalProperties: false`, so this test fails the moment a payload
// struct grows a field the server does not accept - which is exactly how
// v1.0.0-beta.1 shipped a client that could not write anything at all
// ("400 data must NOT have additional properties").
//
// Re-run the script after an NPM or NPMplus upgrade to see the drift.

// sampleResources returns one fully populated resource per kind. Everything
// that can be set from a label is set, so the schema sees the widest payload
// the tool can produce.
func sampleResources(t *testing.T) map[Kind]Resource {
	t.Helper()
	meta := Meta{
		MetaManagedBy:              ManagedByValue,
		MetaContainer:              "whoami",
		MetaIndex:                  0,
		"letsencrypt_agree":        true,
		"letsencrypt_email":        "admin@example.com",
		"dns_challenge":            true,
		"dns_provider":             "cloudflare",
		"dns_provider_credentials": "dns_cloudflare_api_token = secret",
		"propagation_seconds":      30,
		MetaDirectory:              "Production",
	}
	return map[Kind]Resource{
		KindProxy: &ProxyHost{
			DomainNames:           []string{"app.example.com", "www.app.example.com"},
			ForwardScheme:         "https",
			ForwardHost:           "172.20.0.5",
			ForwardPort:           8443,
			CertificateID:         CertificateRef(7),
			SSLForced:             true,
			HSTSEnabled:           true,
			HSTSSubdomains:        true,
			TrustForwardedProto:   true,
			HTTP2Support:          true,
			BlockExploits:         true,
			CachingEnabled:        true,
			AllowWebsocketUpgrade: true,
			AccessListIDs:         []int{2},
			AccessListType:        AccessListCustom,
			AdvancedConfig:        "client_max_body_size 0;",
			Enabled:               true,

			HTTP3Support:             true,
			NoIndex:                  true,
			DisableCrowdsecAppsec:    true,
			DisableRequestBuffering:  true,
			DisableResponseBuffering: true,
			UpstreamCompression:      true,
			FancyIndex:               true,
			XFrameOptions:            "DENY",
			AuthRequest:              "authelia",
			AuthRequestUpstream:      "http://authelia:9091",
			LocationConfig:           "add_header X-Location 1;",

			Locations: []Location{{
				Path:           "/api",
				LocationType:   LocationPrefer,
				ForwardScheme:  "http",
				ForwardHost:    "172.20.0.6",
				ForwardPort:    8080,
				AdvancedConfig: "proxy_read_timeout 300;",
				LocationConfig: "add_header X-Api 1;",
				AccessListIDs:  []int{3},
				AccessListType: AccessListCustom,
				Enabled:        boolPointer(true),

				NoIndex:                  true,
				DisableCrowdsecAppsec:    true,
				DisableRequestBuffering:  true,
				DisableResponseBuffering: true,
				UpstreamCompression:      true,
				FancyIndex:               true,
				XFrameOptions:            "SAMEORIGIN",
				AuthRequest:              "authentik",
				AuthRequestUpstream:      "http://authentik:9000",
			}},
			Meta: meta,
		},
		KindRedirect: &RedirectionHost{
			DomainNames:       []string{"old.example.com"},
			ForwardScheme:     "auto",
			ForwardDomainName: "app.example.com",
			ForwardHTTPCode:   301,
			PreservePath:      true,
			CertificateID:     CertificateRef(7),
			SSLForced:         true,
			HSTSEnabled:       true,
			HSTSSubdomains:    true,
			HTTP2Support:      true,
			HTTP3Support:      true,
			BlockExploits:     true,
			AdvancedConfig:    "add_header X-Redirected 1;",
			Enabled:           true,
			Meta:              meta,
		},
		KindStream: &Stream{
			IncomingPort:   PortOf(5432),
			ForwardingHost: "172.20.0.7",
			ForwardingPort: PortOf(5432),
			TCPForwarding:  true,
			UDPForwarding:  true,
			CertificateID:  CertificateRef(7),
			ProxyProtocol:  2,
			ProxyTLS:       true,
			AdvancedConfig: "proxy_timeout 30s;",
			Description:    "postgres",
			Enabled:        true,
			Meta:           meta,
		},
		KindDead: &DeadHost{
			DomainNames:    []string{"parked.example.com"},
			CertificateID:  CertificateRef(7),
			SSLForced:      true,
			HSTSEnabled:    true,
			HSTSSubdomains: true,
			HTTP2Support:   true,
			HTTP3Support:   true,
			AdvancedConfig: "return 404;",
			Enabled:        true,
			Meta:           meta,
		},
	}
}

func TestPayloadsSatisfyTheAPISchemas(t *testing.T) {
	t.Parallel()

	for _, flavour := range []Flavour{FlavourNPMplus, FlavourNPM} {
		for _, ref := range vendoredRefs(t, string(flavour)) {
			t.Run(string(flavour)+"/"+ref, func(t *testing.T) {
				t.Parallel()
				schema := vendoredWriteSchema(t, string(flavour), ref)
				// The dialect the client reads from GET /api/schema ...
				validateAgainst(t, Dialect{Flavour: flavour, Schema: schema}, ref)
				// ... and the one it falls back to without: it must reach
				// exactly the same verdicts on every field a payload decides
				// about, or a server without a schema endpoint breaks.
				_, hasMeta := schema[KindProxy]["meta"]
				builtin := Dialect{Flavour: flavour, MetaRetired: !hasMeta}
				assertSameVerdicts(t, Dialect{Flavour: flavour, Schema: schema}, builtin)
				validateAgainst(t, builtin, ref)
			})
		}
	}
}

// vendoredRefs lists the upstream versions whose schemas are checked in. Every
// supported NPM and NPMplus release has one directory, so a schema change
// between two of them fails the build instead of somebody's instance.
func vendoredRefs(t *testing.T, flavour string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join("testdata", "schema", flavour))
	if err != nil {
		t.Fatalf("read schema directory of %s: %v", flavour, err)
	}
	var refs []string
	for _, entry := range entries {
		if entry.IsDir() {
			refs = append(refs, entry.Name())
		}
	}
	if len(refs) == 0 {
		t.Fatalf("no vendored schemas for %s; run: make schemas", flavour)
	}
	return refs
}

// decidedFields are the properties a payload sends or leaves out depending on
// the server.
var decidedFields = map[Kind][]string{
	KindProxy: {
		"meta", "advanced_config", "trust_forwarded_proto", "http2_support", "block_exploits",
		"caching_enabled", "allow_websocket_upgrade", "npmplus_directory", "npmplus_dns_challenge",
		"npmplus_dns_provider", "npmplus_dns_provider_credentials", "npmplus_propagation_seconds",
		"locations/npmplus_location_config",
	},
	KindRedirect: {
		"meta", "advanced_config", "http2_support", "block_exploits", "npmplus_directory",
		"npmplus_dns_challenge", "npmplus_dns_provider", "npmplus_dns_provider_credentials",
		"npmplus_propagation_seconds",
	},
	KindStream: {"meta", "npmplus_advanced_config", "npmplus_directory", "certificate_id"},
	KindDead: {
		"meta", "advanced_config", "http2_support", "npmplus_directory", "npmplus_dns_challenge",
		"npmplus_dns_provider", "npmplus_dns_provider_credentials", "npmplus_propagation_seconds",
	},
}

func assertSameVerdicts(t *testing.T, fromSchema, builtin Dialect) {
	t.Helper()
	for kind, names := range decidedFields {
		for _, field := range names {
			if got, want := builtin.Accepts(kind, field), fromSchema.Accepts(kind, field); got != want {
				t.Errorf("built-in knowledge says %s accepts %s = %t, the schema says %t", kind, field, got, want)
			}
		}
	}
	for _, scheme := range []string{SchemeAuto, SchemeKeep} {
		got := builtin.AcceptsValue(KindRedirect, "forward_scheme", scheme)
		if want := fromSchema.AcceptsValue(KindRedirect, "forward_scheme", scheme); got != want {
			t.Errorf("built-in knowledge says a redirection accepts scheme %q = %t, the schema says %t", scheme, got, want)
		}
	}
	if fromSchema.Flavour == FlavourNPMplus {
		got, want := builtin.TypeOf(KindStream, fieldProxyProtocol), fromSchema.TypeOf(KindStream, fieldProxyProtocol)
		if got != want {
			t.Errorf("built-in knowledge types the proxy protocol as %q, the schema as %q", got, want)
		}
	}
}

// vendoredWriteSchema assembles the vendored request schemas of one ref into
// the document GET /api/schema serves, and reads it the way the client does.
func vendoredWriteSchema(t *testing.T, flavour, ref string) WriteSchema {
	t.Helper()
	schema, err := parseWriteSchema(vendoredSchemaDocument(t, flavour, ref))
	if err != nil {
		t.Fatalf("parseWriteSchema() error: %v", err)
	}
	return schema
}

// vendoredSchemaDocument is the GET /api/schema document of one vendored ref.
func vendoredSchemaDocument(t *testing.T, flavour, ref string) []byte {
	t.Helper()
	paths := map[string]map[string]any{}
	for _, kind := range Kinds {
		base := strings.TrimPrefix(kind.Path(), "/api")
		for method, path := range map[string]string{"post": base, "put": base + "/{id}"} {
			raw, err := os.ReadFile(filepath.Join("testdata", "schema", flavour, ref, fmt.Sprintf("%s-%s.json", kind, method))) //nolint:gosec // fixed test fixture path
			if err != nil {
				t.Fatalf("read schema: %v", err)
			}
			if paths[path] == nil {
				paths[path] = map[string]any{}
			}
			paths[path][method] = map[string]any{
				"requestBody": map[string]any{
					"content": map[string]any{"application/json": map[string]any{"schema": json.RawMessage(raw)}},
				},
			}
		}
	}
	document, err := json.Marshal(map[string]any{"paths": paths})
	if err != nil {
		t.Fatalf("marshal the schema document: %v", err)
	}
	return document
}

func validateAgainst(t *testing.T, dialect Dialect, ref string) {
	t.Helper()

	flavour := dialect.Flavour
	resources := sampleResources(t)
	requests := map[string]Resource{}
	for _, kind := range Kinds {
		requests[string(kind)] = resources[kind]
	}
	// A certificate request carries the DNS challenge - in the meta object
	// or, since NPMplus moved it, in columns of its own.
	for _, kind := range []Kind{KindProxy, KindRedirect, KindDead} {
		resource := sampleResources(t)[kind]
		switch r := resource.(type) {
		case *ProxyHost:
			r.CertificateID = NewCertificate()
		case *RedirectionHost:
			r.CertificateID = NewCertificate()
		case *DeadHost:
			r.CertificateID = NewCertificate()
		}
		requests[string(kind)+"+new-certificate"] = resource
	}

	for name, resource := range requests {
		kind := resource.Kind()
		for _, method := range []string{"post", "put"} {
			t.Run(fmt.Sprintf("%s-%s", name, method), func(t *testing.T) {
				payload, err := resource.Payload(dialect)
				if err != nil {
					t.Fatalf("Payload(%s) error: %v", flavour, err)
				}

				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatalf("marshal payload: %v", err)
				}

				var instance any
				if err := json.Unmarshal(raw, &instance); err != nil {
					t.Fatalf("decode payload: %v", err)
				}

				schema := loadSchema(t, string(flavour), ref, fmt.Sprintf("%s-%s.json", kind, method))
				if err := schema.Validate(instance); err != nil {
					t.Errorf("%s %s %s payload violates the API schema:\n%v\n\npayload: %s",
						flavour, kind, method, err, raw)
				}
				if !dialect.KeepsOwnership(kind) {
					t.Errorf("%s %s: the server could not store who owns the resource", flavour, kind)
				}
			})
		}
	}
}

// TestPayloadsOmitWriteProtectedFields pins the two properties that broke every
// write in v1.0.0-beta.1. They are rejected by NPMplus and, in the case of
// `enabled`, ignored or refused elsewhere, so no payload may ever carry them.
func TestPayloadsOmitWriteProtectedFields(t *testing.T) {
	t.Parallel()

	forbidden := map[string]string{
		"enabled":       "toggled through POST <collection>/{id}/enable and /disable",
		"id":            "assigned by the server",
		"created_on":    "assigned by the server",
		"modified_on":   "assigned by the server",
		"owner_user_id": "assigned by the server",
		"access_lists":  "an expansion of the response, not an input",
		"certificate":   "an expansion of the response, not an input",
		"owner":         "an expansion of the response, not an input",
	}

	for _, flavour := range []Flavour{FlavourNPMplus, FlavourNPM} {
		resources := sampleResources(t)
		for _, kind := range Kinds {
			payload, err := resources[kind].Payload(Dialect{Flavour: flavour})
			if err != nil {
				t.Fatalf("Payload(%s, %s) error: %v", flavour, kind, err)
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal %s payload: %v", kind, err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("decode %s payload: %v", kind, err)
			}
			for field, why := range forbidden {
				if _, present := fields[field]; present {
					t.Errorf("%s %s payload contains %q, which is %s", flavour, kind, field, why)
				}
			}
		}
	}

	// NPMplus removed access_list_id; upstream NPM never had the npmplus_ ones.
	assertField(t, FlavourNPMplus, KindProxy, "npmplus_access_list_ids", true)
	assertField(t, FlavourNPMplus, KindProxy, "npmplus_access_list_type", true)
	assertField(t, FlavourNPMplus, KindProxy, "access_list_id", false)
	assertField(t, FlavourNPM, KindProxy, "access_list_id", true)
	assertField(t, FlavourNPM, KindProxy, "npmplus_access_list_ids", false)
}

func assertField(t *testing.T, flavour Flavour, kind Kind, field string, want bool) {
	t.Helper()
	payload, err := sampleResources(t)[kind].Payload(Dialect{Flavour: flavour})
	if err != nil {
		t.Fatalf("Payload(%s, %s) error: %v", flavour, kind, err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if _, present := fields[field]; present != want {
		t.Errorf("%s %s payload: field %q present = %t, want %t", flavour, kind, field, present, want)
	}
}

// TestUnsupportedFeaturesAreRejected covers the other half of the contract: a
// configuration a flavour cannot express must fail before the request, with a
// message naming the feature, rather than as an opaque 400.
func TestUnsupportedFeaturesAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		flavour  Flavour
		resource Resource
	}{
		{
			name:     "grpc upstream on upstream npm",
			flavour:  FlavourNPM,
			resource: &ProxyHost{DomainNames: []string{"a.example.com"}, ForwardScheme: "grpc", ForwardHost: "a", ForwardPort: 80},
		},
		{
			name:     "several access lists on upstream npm",
			flavour:  FlavourNPM,
			resource: &ProxyHost{DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80, AccessListIDs: []int{1, 2}},
		},
		{
			name:     "new certificate for an npmplus stream",
			flavour:  FlavourNPMplus,
			resource: &Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432), CertificateID: NewCertificate()},
		},
		{
			name:     "port range on upstream npm",
			flavour:  FlavourNPM,
			resource: &Stream{IncomingPort: PortText("8080-8090"), ForwardingHost: "db", ForwardingPort: PortOf(8080)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := tt.resource.Payload(Dialect{Flavour: tt.flavour}); err == nil {
				t.Fatalf("Payload(%s) = nil error, want a rejection", tt.flavour)
			}
		})
	}
}

// TestNPMplusOnlyFieldsAreDroppedNotRejected: a better default must not break
// upstream nginx-proxy-manager. HTTP/3 and friends are simply left out of the
// request, and the caller is told which fields those were.
func TestNPMplusOnlyFieldsAreDroppedNotRejected(t *testing.T) {
	t.Parallel()

	host := &ProxyHost{
		DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80,
		HTTP3Support: true, NoIndex: true, XFrameOptions: "DENY",
	}
	payload, err := host.Payload(Dialect{Flavour: FlavourNPM})
	if err != nil {
		t.Fatalf("Payload(npm) error = %v, want the npmplus fields to be dropped", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "npmplus_") {
		t.Errorf("payload carries npmplus fields: %s", body)
	}

	unsupported := UnsupportedByNPM(host)
	want := []string{"ssl.http3", "noindex", "x_frame_options"}
	if !reflect.DeepEqual(unsupported, want) {
		t.Errorf("UnsupportedByNPM() = %v, want %v", unsupported, want)
	}
}

func boolPointer(b bool) *bool { return &b }

func loadSchema(t *testing.T, flavour, ref, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("testdata", "schema", flavour, ref, name)
	raw, err := os.Open(path) //nolint:gosec // fixed test fixture path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer func() { _ = raw.Close() }()

	document, err := jsonschema.UnmarshalJSON(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	compiler := jsonschema.NewCompiler()
	// The servers validate with Ajv, whose patterns are ECMAScript: NPMplus
	// uses lookaheads that Go's RE2 does not support.
	compiler.UseRegexpEngine(ecmaRegexp)
	if err := compiler.AddResource(path, document); err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	schema, err := compiler.Compile(path)
	if err != nil {
		t.Fatalf("compile %s: %v", path, err)
	}
	return schema
}

// ecmaRegexp compiles JSON schema patterns the way the servers' validator does.
func ecmaRegexp(pattern string) (jsonschema.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript|regexp2.Unicode)
	if err != nil {
		return nil, err
	}
	return ecmaPattern{re}, nil
}

type ecmaPattern struct{ re *regexp2.Regexp }

func (p ecmaPattern) MatchString(s string) bool {
	matched, err := p.re.MatchString(s)
	return err == nil && matched
}

func (p ecmaPattern) String() string { return p.re.String() }
