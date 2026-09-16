package syncer

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// payloadOf runs the whole chain a real reconcile run takes - labels, target,
// resource, request body - and returns the JSON that would go over the wire.
func payloadOf(t *testing.T, c docker.Container, flavour npm.Flavour, list []npm.Certificate) []byte {
	t.Helper()

	res := docker.Parse(c, docker.ParseOptions{Prefix: "npm", ExposedByDefault: true})
	if len(res.Errors) > 0 {
		t.Fatalf("parse errors: %v", res.Errors)
	}
	if len(res.Targets) != 1 {
		t.Fatalf("expected exactly one target, got %d", len(res.Targets))
	}
	target := res.Targets[0]

	selection, err := certs.Resolve(target.Certificate, target.Domains(), list, certs.Options{Partial: certs.PartialPrimary})
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	certificate := npm.CertificateRef(selection.ID)
	if selection.New {
		certificate = npm.NewCertificate()
	}

	payload, err := BuildResource(target, certificate).Payload(flavour)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return body
}

// TestGoldenRedthAndOwnSpelling is the compatibility guarantee of section 1.2:
// the same host written in Redth's syntax and in ours produces byte-identical
// request bodies.
func TestGoldenRedthAndOwnSpelling(t *testing.T) {
	t.Parallel()

	list := []npm.Certificate{{ID: 4, Provider: "letsencrypt", DomainNames: []string{"*.example.com"}, ExpiresOn: notBefore}}

	redth := docker.Container{ID: "web-id", Name: "web", Labels: map[string]string{
		"npm.proxy.domains":               "app.example.com",
		"npm.proxy.host":                  "192.168.1.50",
		"npm.proxy.port":                  "8080",
		"npm.proxy.scheme":                "http",
		"npm.proxy.ssl.force":             "true",
		"npm.proxy.ssl.http2":             "true",
		"npm.proxy.ssl.hsts":              "true",
		"npm.proxy.ssl.hsts.subdomains":   "true",
		"npm.proxy.websockets":            "true",
		"npm.proxy.caching":               "false",
		"npm.proxy.block_common_exploits": "true",
		"npm.proxy.advanced.config":       "client_max_body_size 0;",
	}}
	ours := docker.Container{ID: "web-id", Name: "web", Labels: map[string]string{
		"npm.proxy.domains":             "app.example.com",
		"npm.proxy.forward_host":        "192.168.1.50",
		"npm.proxy.forward_port":        "8080",
		"npm.proxy.forward_scheme":      "http",
		"npm.proxy.ssl_forced":          "true",
		"npm.proxy.ssl_http2":           "true",
		"npm.proxy.ssl_hsts":            "true",
		"npm.proxy.ssl_hsts_subdomains": "true",
		"npm.proxy.websockets":          "true",
		"npm.proxy.caching":             "false",
		"npm.proxy.block_exploits":      "true",
		"npm.proxy.advanced_config":     "client_max_body_size 0;",
	}}

	left := payloadOf(t, redth, npm.FlavourNPMplus, list)
	right := payloadOf(t, ours, npm.FlavourNPMplus, list)
	if !bytes.Equal(left, right) {
		t.Errorf("payloads differ\nredth:\n%s\nours:\n%s", left, right)
	}
}

// TestGoldenMinimalProxyHost pins the defaults of section 3.3: one label has to
// be enough for a complete, HTTPS-enabled host.
func TestGoldenMinimalProxyHost(t *testing.T) {
	t.Parallel()

	c := docker.Container{
		ID: "homepage-id", Name: "homepage",
		Labels: map[string]string{"npm.proxy.domains": "homepage.home.example.com"},
		Ports:  []docker.PortBinding{{Private: 3000, Type: "tcp"}},
	}
	list := []npm.Certificate{{ID: 12, Provider: "letsencrypt", DomainNames: []string{"*.home.example.com"}, ExpiresOn: notBefore}}

	want := `{
  "domain_names": [
    "homepage.home.example.com"
  ],
  "forward_scheme": "http",
  "forward_host": "homepage",
  "forward_port": 3000,
  "certificate_id": 12,
  "ssl_forced": true,
  "hsts_enabled": false,
  "hsts_subdomains": false,
  "trust_forwarded_proto": false,
  "http2_support": true,
  "npmplus_http3_support": true,
  "block_exploits": true,
  "caching_enabled": false,
  "allow_websocket_upgrade": true,
  "npmplus_access_list_ids": [],
  "npmplus_access_list_type": "public",
  "advanced_config": "",
  "npmplus_location_config": "",
  "locations": [],
  "npmplus_noindex": false,
  "npmplus_crowdsec_appsec": false,
  "npmplus_proxy_request_buffering": false,
  "npmplus_proxy_response_buffering": false,
  "npmplus_upstream_compression": false,
  "npmplus_fancyindex": false,
  "npmplus_auth_request": "none",
  "npmplus_auth_request_upstream": "",
  "meta": {
    "managed_by": "npmplus-docker-sync",
    "managed_container": "homepage",
    "managed_index": 0
  }
}`
	if got := string(payloadOf(t, c, npm.FlavourNPMplus, list)); got != want {
		t.Errorf("payload mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestGoldenUpstreamNPMPayload shows the same host against upstream
// nginx-proxy-manager: the NPMplus-only fields are simply absent.
func TestGoldenUpstreamNPMPayload(t *testing.T) {
	t.Parallel()

	c := docker.Container{
		ID: "homepage-id", Name: "homepage",
		Labels: map[string]string{"npm.proxy.domains": "homepage.home.example.com", "npm.proxy.port": "3000"},
	}

	want := `{
  "domain_names": [
    "homepage.home.example.com"
  ],
  "forward_scheme": "http",
  "forward_host": "homepage",
  "forward_port": 3000,
  "certificate_id": 0,
  "ssl_forced": false,
  "hsts_enabled": false,
  "hsts_subdomains": false,
  "trust_forwarded_proto": false,
  "http2_support": true,
  "block_exploits": true,
  "caching_enabled": false,
  "allow_websocket_upgrade": true,
  "access_list_id": 0,
  "advanced_config": "",
  "locations": [],
  "meta": {
    "managed_by": "npmplus-docker-sync",
    "managed_container": "homepage",
    "managed_index": 0
  }
}`
	if got := string(payloadOf(t, c, npm.FlavourNPM, nil)); got != want {
		t.Errorf("payload mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}

// TestGoldenStream covers Redth's stream labels end to end, including the
// certificate chosen by domain.
func TestGoldenStream(t *testing.T) {
	t.Parallel()

	c := docker.Container{ID: "db-id", Name: "db", Labels: map[string]string{
		"npm.stream.incoming.port": "5432",
		"npm.stream.forward.host":  "10.0.0.9",
		"npm.stream.forward.port":  "5432",
		"npm.stream.forward.tcp":   "true",
		"npm.stream.forward.udp":   "false",
		"npm.stream.ssl":           "db.example.com",
	}}
	list := []npm.Certificate{{ID: 7, Provider: "letsencrypt", DomainNames: []string{"db.example.com"}, ExpiresOn: notBefore}}

	want := `{
  "incoming_port": "5432",
  "forwarding_host": "10.0.0.9",
  "forwarding_port": "5432",
  "tcp_forwarding": true,
  "udp_forwarding": false,
  "certificate_id": 7,
  "npmplus_proxy_protocol_forwarding": 0,
  "npmplus_proxy_tls": false,
  "npmplus_advanced_config": "",
  "npmplus_description": "db",
  "meta": {
    "managed_by": "npmplus-docker-sync",
    "managed_container": "db",
    "managed_index": 0
  }
}`
	if got := string(payloadOf(t, c, npm.FlavourNPMplus, list)); got != want {
		t.Errorf("payload mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
}
