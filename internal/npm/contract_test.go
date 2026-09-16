package npm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
			AdvancedConfig:        "client_max_body_size 0;",
			Enabled:               true,
			Locations: []Location{{
				Path:           "/api",
				ForwardScheme:  "http",
				ForwardHost:    "172.20.0.6",
				ForwardPort:    8080,
				AdvancedConfig: "proxy_read_timeout 300;",
				AccessListIDs:  []int{3},
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
			AdvancedConfig: "return 404;",
			Enabled:        true,
			Meta:           meta,
		},
	}
}

func TestPayloadsSatisfyTheAPISchemas(t *testing.T) {
	t.Parallel()

	for _, flavour := range []Flavour{FlavourNPMplus, FlavourNPM} {
		t.Run(string(flavour), func(t *testing.T) {
			t.Parallel()
			resources := sampleResources(t)

			for _, kind := range Kinds {
				for _, method := range []string{"post", "put"} {
					t.Run(fmt.Sprintf("%s-%s", kind, method), func(t *testing.T) {
						resource := resources[kind]

						// NPMplus stream payloads only exist with a numeric
						// certificate reference, which the sample has.
						payload, err := resource.Payload(flavour)
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

						schema := loadSchema(t, string(flavour), fmt.Sprintf("%s-%s.json", kind, method))
						if err := schema.Validate(instance); err != nil {
							t.Errorf("%s %s %s payload violates the API schema:\n%v\n\npayload: %s",
								flavour, kind, method, err, raw)
						}
					})
				}
			}
		})
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
			payload, err := resources[kind].Payload(flavour)
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
	payload, err := sampleResources(t)[kind].Payload(flavour)
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
			name:     "http3 on upstream npm",
			flavour:  FlavourNPM,
			resource: &ProxyHost{DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80, HTTP3Support: true},
		},
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
			if _, err := tt.resource.Payload(tt.flavour); err == nil {
				t.Fatalf("Payload(%s) = nil error, want a rejection", tt.flavour)
			}
		})
	}
}

func loadSchema(t *testing.T, flavour, name string) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("testdata", "schema", flavour, name)
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
	if err := compiler.AddResource(path, document); err != nil {
		t.Fatalf("add %s: %v", path, err)
	}
	schema, err := compiler.Compile(path)
	if err != nil {
		t.Fatalf("compile %s: %v", path, err)
	}
	return schema
}
