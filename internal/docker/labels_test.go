package docker

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

func opts(prefix string) ParseOptions {
	return ParseOptions{Prefix: prefix, ExposedByDefault: true}
}

// withLabels is a small helper for label-only fixtures.
func withLabels(name string, labels map[string]string) Container {
	return Container{ID: name + "-id", Name: name, Labels: labels}
}

// withPorts adds exposed ports to a fixture.
func withPorts(c Container, ports ...int) Container {
	for _, p := range ports {
		c.Ports = append(c.Ports, PortBinding{Private: p, Type: "tcp"})
	}
	return c
}

// autoCert is the default certificate wish.
var autoCert = certs.Spec{Mode: certs.ModeAuto, Raw: fields.Auto}

// proxyTarget returns a target with every built-in default applied, so a test
// only has to spell out what it actually exercises.
func proxyTarget(name string, mutate func(*Target)) *Target {
	t := &Target{
		Kind:          npm.KindProxy,
		Running:       true,
		ContainerID:   name + "-id",
		ContainerName: name,
		Certificate:   autoCert,
		HTTP2Support:  true,
		HTTP3Support:  true,
		ForwardScheme: "http",
		ForwardHost:   name,
		Websockets:    true,
		BlockExploits: true,
		Enabled:       true,

		CrowdsecAppsec:    true,
		RequestBuffering:  true,
		ResponseBuffering: true,
		AuthRequest:       "none",
		AccessListType:    npm.AccessListPublic,
	}
	if mutate != nil {
		mutate(t)
	}
	return t
}

func TestParseProxy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    ParseOptions
		c       Container
		want    []*Target
		wantErr bool
	}{
		{
			name: "one label is enough",
			opts: opts("npm"),
			c: withPorts(withLabels("homepage", map[string]string{
				"npm.proxy.domains": "homepage.example.com",
			}), 3000),
			want: []*Target{proxyTarget("homepage", func(t *Target) {
				t.DomainNames = []string{"homepage.example.com"}
				t.ForwardPort = 3000
			})},
		},
		{
			name: "shorthand without the proxy segment",
			opts: opts("npm"),
			c: withLabels("app", map[string]string{
				"npm.domains": "app.example.com",
				"npm.port":    "3000",
			}),
			want: []*Target{proxyTarget("app", func(t *Target) {
				t.DomainNames = []string{"app.example.com"}
				t.ForwardPort = 3000
			})},
		},
		{
			name: "redth spelling: host is the upstream",
			opts: opts("npm"),
			c: withLabels("kuma", map[string]string{
				"npm.proxy.domains": "kuma.example.com",
				"npm.proxy.host":    "192.168.1.50",
				"npm.proxy.port":    "3001",
			}),
			want: []*Target{proxyTarget("kuma", func(t *Target) {
				t.DomainNames = []string{"kuma.example.com"}
				t.ForwardHost = "192.168.1.50"
				t.ForwardPort = 3001
			})},
		},
		{
			name: "custom prefix",
			opts: opts("proxy"),
			c: withLabels("svc", map[string]string{
				"proxy.domains": "svc.example.com",
				"proxy.port":    "8080",
			}),
			want: []*Target{proxyTarget("svc", func(t *Target) {
				t.DomainNames = []string{"svc.example.com"}
				t.ForwardPort = 8080
			})},
		},
		{
			name: "dash separated namespace",
			opts: opts("npm"),
			c: withLabels("dash", map[string]string{
				"npm-proxy.domains": "dash.example.com",
				"npm-proxy.port":    "8080",
			}),
			want: []*Target{proxyTarget("dash", func(t *Target) {
				t.DomainNames = []string{"dash.example.com"}
				t.ForwardPort = 8080
			})},
		},
		{
			name: "full configuration",
			opts: opts("npm"),
			c: withLabels("api", map[string]string{
				"npm.proxy.domains":             "api.example.com, www.api.example.com",
				"npm.proxy.port":                "8443",
				"npm.proxy.scheme":              "https",
				"npm.proxy.host":                "internal-api",
				"npm.proxy.certificate":         "7",
				"npm.proxy.ssl.forced":          "true",
				"npm.proxy.ssl.http2":           "true",
				"npm.proxy.ssl.http3":           "false",
				"npm.proxy.ssl.hsts":            "true",
				"npm.proxy.ssl.hsts_subdomains": "true",
				"npm.proxy.websockets":          "false",
				"npm.proxy.block_exploits":      "off",
				"npm.proxy.caching":             "true",
				"npm.proxy.access_list":         "2",
				"npm.proxy.advanced_config":     "client_max_body_size 0;",
				"npm.proxy.noindex":             "true",
				"npm.proxy.crowdsec_appsec":     "false",
				"npm.proxy.x_frame_options":     "sameorigin",
				"npm.proxy.auth_request":        "authelia",
			}),
			want: []*Target{proxyTarget("api", func(t *Target) {
				t.DomainNames = []string{"api.example.com", "www.api.example.com"}
				t.ForwardScheme = "https"
				t.ForwardHost = "internal-api"
				t.ForwardPort = 8443
				t.Certificate = certs.Spec{Mode: certs.ModeID, ID: 7, Raw: "7"}
				t.SSLForced = boolPtr(true)
				t.HSTSEnabled = true
				t.HSTSSubdomains = true
				t.HTTP3Support = false
				t.Websockets = false
				t.BlockExploits = false
				t.Caching = true
				t.AccessListIDs = []int{2}
				t.AccessListType = npm.AccessListCustom
				t.AdvancedConfig = "client_max_body_size 0;"
				t.NoIndex = true
				t.CrowdsecAppsec = false
				t.XFrameOptions = "SAMEORIGIN"
				t.AuthRequest = "authelia"
				t.ExplicitPlus = []string{"auth_request", "crowdsec_appsec", "noindex", "ssl.http3", "x_frame_options"}
			})},
		},
		{
			name: "access list by name",
			opts: opts("npm"),
			c: withLabels("kuma", map[string]string{
				"npm.proxy.domains":     "kuma.example.com",
				"npm.proxy.port":        "3001",
				"npm.proxy.access_list": "Intern, 4",
			}),
			want: []*Target{proxyTarget("kuma", func(t *Target) {
				t.DomainNames = []string{"kuma.example.com"}
				t.ForwardPort = 3001
				t.AccessListIDs = []int{4}
				t.AccessListNames = []string{"Intern"}
				t.AccessListType = npm.AccessListCustom
			})},
		},
		{
			name: "explicit disable alias inverts the value",
			opts: opts("npm"),
			c: withLabels("inv", map[string]string{
				"npm.proxy.domains":                   "inv.example.com",
				"npm.proxy.port":                      "80",
				"npm.proxy.disable_crowdsec_appsec":   "true",
				"npm.proxy.disable_request_buffering": "true",
			}),
			want: []*Target{proxyTarget("inv", func(t *Target) {
				t.DomainNames = []string{"inv.example.com"}
				t.ForwardPort = 80
				t.CrowdsecAppsec = false
				t.RequestBuffering = false
				t.ExplicitPlus = []string{"crowdsec_appsec", "request_buffering"}
			})},
		},
		{
			name: "new letsencrypt certificate",
			opts: opts("npm"),
			c: withLabels("blog", map[string]string{
				"npm.proxy.domains":     "blog.example.com",
				"npm.proxy.port":        "2368",
				"npm.proxy.certificate": "new",
				"npm.letsencrypt.email": "admin@example.com",
				"npm.letsencrypt.agree": "true",
			}),
			want: []*Target{proxyTarget("blog", func(t *Target) {
				t.DomainNames = []string{"blog.example.com"}
				t.ForwardPort = 2368
				t.Certificate = certs.Spec{Mode: certs.ModeNew, Raw: "new"}
				t.LetsEncryptEmail = "admin@example.com"
				t.LetsEncryptAgree = true
			})},
		},
		{name: "no labels at all", opts: opts("npm"), c: withLabels("db", nil)},
		{
			name: "explicitly disabled container",
			opts: opts("npm"),
			c:    withLabels("db", map[string]string{"npm.enable": "false", "npm.domains": "db.example.com"}),
		},
		{
			name: "opt-in required when exposed_by_default is off",
			opts: ParseOptions{Prefix: "npm"},
			c:    withLabels("db", map[string]string{"npm.domains": "db.example.com", "npm.port": "80"}),
		},
		{
			name:    "domains missing",
			opts:    opts("npm"),
			c:       withLabels("x", map[string]string{"npm.port": "80"}),
			wantErr: true,
		},
		{
			name: "host that looks like a domain is rejected",
			opts: opts("npm"),
			c: withLabels("old", map[string]string{
				"npm.proxy.host": "old.example.com",
				"npm.proxy.port": "80",
			}),
			wantErr: true,
		},
		{
			name:    "port cannot be guessed",
			opts:    opts("npm"),
			c:       withLabels("noport", map[string]string{"npm.proxy.domains": "noport.example.com"}),
			wantErr: true,
		},
		{
			name: "ambiguous ports need the preference list",
			opts: opts("npm"),
			c: withPorts(withLabels("multi", map[string]string{
				"npm.proxy.domains": "multi.example.com",
			}), 9000, 9001),
			wantErr: true,
		},
		{
			name: "port preference decides",
			opts: ParseOptions{Prefix: "npm", ExposedByDefault: true, PortPreference: []int{9001}},
			c: withPorts(withLabels("multi", map[string]string{
				"npm.proxy.domains": "multi.example.com",
			}), 9000, 9001),
			want: []*Target{proxyTarget("multi", func(t *Target) {
				t.DomainNames = []string{"multi.example.com"}
				t.ForwardPort = 9001
			})},
		},
		{
			name: "invalid enum value",
			opts: opts("npm"),
			c: withLabels("bad", map[string]string{
				"npm.proxy.domains":      "bad.example.com",
				"npm.proxy.port":         "80",
				"npm.proxy.auth_request": "nope",
			}),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Parse(tc.c, tc.opts)
			if tc.wantErr {
				if len(res.Errors) == 0 {
					t.Fatalf("expected an error, got targets %+v", res.Targets)
				}
				return
			}
			if len(res.Errors) > 0 {
				t.Fatalf("unexpected errors: %v", res.Errors)
			}
			if !reflect.DeepEqual(res.Targets, tc.want) {
				t.Errorf("targets mismatch\n got: %s\nwant: %s", dump(res.Targets), dump(tc.want))
			}
		})
	}
}

// TestRedthSpellingMatchesOurs is the compatibility guarantee of section 1.2:
// the same configuration written in Redth's syntax and in ours has to produce
// the very same target.
func TestRedthSpellingMatchesOurs(t *testing.T) {
	t.Parallel()

	redth := withLabels("app", map[string]string{
		"npm.proxy.1.domains":               "app.example.com",
		"npm.proxy.1.host":                  "10.0.0.5",
		"npm.proxy.1.port":                  "8080",
		"npm.proxy.1.ssl.force":             "true",
		"npm.proxy.1.ssl.certificate.id":    "3",
		"npm.proxy.1.ssl.hsts":              "true",
		"npm.proxy.1.ssl.hsts.subdomains":   "true",
		"npm.proxy.1.block_common_exploits": "false",
		"npm.proxy.1.accesslist.id":         "2",
	})
	ours := withLabels("app", map[string]string{
		"npm.1.proxy.domains":             "app.example.com",
		"npm.1.proxy.forward_host":        "10.0.0.5",
		"npm.1.proxy.forward_port":        "8080",
		"npm.1.proxy.ssl_forced":          "true",
		"npm.1.proxy.certificate":         "3",
		"npm.1.proxy.ssl-hsts":            "true",
		"npm.1.proxy.ssl_hsts_subdomains": "true",
		"npm.1.proxy.block_exploits":      "false",
		"npm.1.proxy.access_list":         "2",
	})

	left := Parse(redth, opts("npm"))
	right := Parse(ours, opts("npm"))
	if len(left.Errors) > 0 || len(right.Errors) > 0 {
		t.Fatalf("unexpected errors: %v / %v", left.Errors, right.Errors)
	}
	if !reflect.DeepEqual(left.Targets, right.Targets) {
		t.Errorf("spellings differ\nredth: %s\nours:  %s", dump(left.Targets), dump(right.Targets))
	}
	if len(left.Targets) != 1 || left.Targets[0].Index != 1 {
		t.Fatalf("expected one target at index 1, got %s", dump(left.Targets))
	}
}

// TestAliasTable walks the alias table of section 1.3 label by label.
func TestAliasTable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		label string
		value string
		check func(*Target) bool
	}{
		{"npm.proxy.domains", "a.example.com", func(t *Target) bool { return t.DomainNames[0] == "a.example.com" }},
		{"npm.proxy.domain", "a.example.com", func(t *Target) bool { return t.DomainNames[0] == "a.example.com" }},
		{"npm.domains", "a.example.com", func(t *Target) bool { return t.DomainNames[0] == "a.example.com" }},
		{"npm.proxy.host", "10.0.0.1", func(t *Target) bool { return t.ForwardHost == "10.0.0.1" }},
		{"npm.proxy.port", "8081", func(t *Target) bool { return t.ForwardPort == 8081 }},
		{"npm.proxy.scheme", "https", func(t *Target) bool { return t.ForwardScheme == "https" }},
		{"npm.proxy.ssl.force", "true", func(t *Target) bool { return t.SSLForced != nil && *t.SSLForced }},
		{"npm.proxy.ssl.certificate.id", "9", func(t *Target) bool { return t.Certificate.ID == 9 }},
		{"npm.proxy.ssl.http2", "false", func(t *Target) bool { return !t.HTTP2Support }},
		{"npm.proxy.ssl.hsts", "true", func(t *Target) bool { return t.HSTSEnabled }},
		{"npm.proxy.ssl.hsts.subdomains", "true", func(t *Target) bool { return t.HSTSSubdomains }},
		{"npm.proxy.caching", "true", func(t *Target) bool { return t.Caching }},
		{"npm.proxy.block_common_exploits", "false", func(t *Target) bool { return !t.BlockExploits }},
		{"npm.proxy.websockets", "false", func(t *Target) bool { return !t.Websockets }},
		{"npm.proxy.accesslist.id", "5", func(t *Target) bool { return len(t.AccessListIDs) == 1 && t.AccessListIDs[0] == 5 }},
		{"npm.proxy.advanced.config", "x;", func(t *Target) bool { return t.AdvancedConfig == "x;" }},
		{"npm.certificate_id", "4", func(t *Target) bool { return t.Certificate.ID == 4 }},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			labels := map[string]string{"npm.proxy.port": "80", tc.label: tc.value}
			if !strings.HasSuffix(tc.label, ".domain") && !strings.HasSuffix(tc.label, ".domains") {
				labels["npm.proxy.domains"] = "base.example.com"
			}
			res := Parse(withLabels("alias", labels), opts("npm"))
			if len(res.Errors) > 0 {
				t.Fatalf("unexpected errors: %v", res.Errors)
			}
			if len(res.Targets) != 1 {
				t.Fatalf("expected one target, got %d", len(res.Targets))
			}
			if !tc.check(res.Targets[0]) {
				t.Errorf("%s=%s did not reach the target: %s", tc.label, tc.value, dump(res.Targets))
			}
		})
	}
}

func TestStreamLabels(t *testing.T) {
	t.Parallel()

	res := Parse(withLabels("db", map[string]string{
		"npm.stream.incoming.port": "5432",
		"npm.stream.forward.host":  "10.0.0.9",
		"npm.stream.forward.port":  "5432",
		"npm.stream.forward.tcp":   "true",
		"npm.stream.forward.udp":   "false",
		"npm.stream.ssl":           "db.example.com",
	}), opts("npm"))
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Targets) != 1 {
		t.Fatalf("expected one target, got %d", len(res.Targets))
	}
	got := res.Targets[0]
	if got.Kind != npm.KindStream || got.IncomingPort != 5432 || got.ForwardingPort != 5432 {
		t.Errorf("unexpected stream: %s", dump(res.Targets))
	}
	if got.ForwardingHost != "10.0.0.9" || !got.TCPForwarding || got.UDPForwarding {
		t.Errorf("unexpected forwarding: %s", dump(res.Targets))
	}
	if got.Certificate.Mode != certs.ModeDomain || got.Certificate.Domain != "db.example.com" {
		t.Errorf("unexpected certificate: %+v", got.Certificate)
	}
	if got.Description != "db" {
		t.Errorf("description should default to the container name, got %q", got.Description)
	}
}

func TestStreamForwardPortDefaultsToIncoming(t *testing.T) {
	t.Parallel()

	res := Parse(withLabels("db", map[string]string{"npm.stream.port": "5432"}), opts("npm"))
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if got := res.Targets[0].ForwardingPort; got != 5432 {
		t.Errorf("forwarding port = %d, want 5432", got)
	}
}

func TestRedirectAndDeadHosts(t *testing.T) {
	t.Parallel()

	res := Parse(withLabels("old", map[string]string{
		"npm.redirect.domains":        "old.example.com",
		"npm.redirect.forward_domain": "new.example.com",
		"npm.redirect.http_code":      "308",
		"npm.404.domains":             "gone.example.com",
	}), opts("npm"))
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("expected two targets, got %s", dump(res.Targets))
	}
	redirect, dead := res.Targets[0], res.Targets[1]
	if redirect.Kind != npm.KindRedirect || redirect.ForwardDomainName != "new.example.com" || redirect.ForwardHTTPCode != 308 {
		t.Errorf("unexpected redirect: %s", dump(res.Targets[:1]))
	}
	if redirect.ForwardScheme != fields.Auto || !redirect.PreservePath {
		t.Errorf("redirect defaults are wrong: %s", dump(res.Targets[:1]))
	}
	if dead.Kind != npm.KindDead || dead.DomainNames[0] != "gone.example.com" {
		t.Errorf("unexpected 404 host: %s", dump(res.Targets[1:]))
	}
}

func TestUnknownLabelWarns(t *testing.T) {
	t.Parallel()

	c := withLabels("typo", map[string]string{
		"npm.proxy.domains":   "typo.example.com",
		"npm.proxy.port":      "80",
		"npm.proxy.ssl.forcd": "true",
	})

	res := Parse(c, opts("npm"))
	if len(res.Targets) != 1 {
		t.Fatalf("the resource should still be created, got %s", dump(res.Targets))
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "ssl.forced") {
		t.Fatalf("expected a suggestion for ssl.forced, got %v", res.Warnings)
	}

	strict := opts("npm")
	strict.StrictLabels = true
	res = Parse(c, strict)
	if len(res.Targets) != 0 {
		t.Errorf("STRICT_LABELS should skip the resource, got %s", dump(res.Targets))
	}
}

func TestDefaultsFromEnvironment(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"NPM_PROXY_WEBSOCKETS": "false",
		"NPM_PROXY_SSL_FORCE":  "true",
		"NPM_DEFAULT_HTTP3":    "false",
	}
	defaults, _, err := fields.LoadDefaults(func(k string) string { return env[k] }, nil)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}

	o := opts("npm")
	o.Defaults = defaults
	res := Parse(withLabels("app", map[string]string{
		"npm.proxy.domains":    "app.example.com",
		"npm.proxy.port":       "80",
		"npm.proxy.websockets": "true", // the label wins over the environment
	}), o)
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	got := res.Targets[0]
	if !got.Websockets {
		t.Error("the label should override NPM_PROXY_WEBSOCKETS")
	}
	if got.SSLForced == nil || !*got.SSLForced {
		t.Error("NPM_PROXY_SSL_FORCE should apply")
	}
	if got.HTTP3Support {
		t.Error("NPM_DEFAULT_HTTP3 should apply")
	}
}

func TestLocations(t *testing.T) {
	t.Parallel()

	res := Parse(withLabels("app", map[string]string{
		"npm.proxy.domains":               "app.example.com",
		"npm.proxy.port":                  "80",
		"npm.proxy.location.0.path":       "/api",
		"npm.proxy.location.0.type":       "prefer",
		"npm.proxy.location.0.port":       "9000",
		"npm.proxy.location.0.noindex":    "true",
		"npm.proxy.location.1.path":       "/static",
		"npm.proxy.location.1.fancyindex": "true",
	}), opts("npm"))
	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	locations := res.Targets[0].Locations
	if len(locations) != 2 {
		t.Fatalf("expected two locations, got %d", len(locations))
	}
	if locations[0].Path != "/api" || locations[0].LocationType != npm.LocationPrefer || locations[0].ForwardPort != 9000 {
		t.Errorf("unexpected first location: %+v", locations[0])
	}
	if !locations[0].NoIndex || locations[0].DisableCrowdsecAppsec {
		t.Errorf("location switches are wrong: %+v", locations[0])
	}
	if locations[1].ForwardPort != 80 || !locations[1].FancyIndex {
		t.Errorf("unexpected second location: %+v", locations[1])
	}
}

func TestManagedClassification(t *testing.T) {
	t.Parallel()

	containers := []Container{
		withLabels("a", map[string]string{"npm.proxy.domains": "a.example.com"}),
		withLabels("b", map[string]string{"npm.enable": "false"}),
		withLabels("c", nil),
		withLabels("d", map[string]string{"npm.enable": "true"}),
	}
	got := Classify(containers, opts("npm"))
	want := Summary{Managed: 2, OptedOut: 1, Unlabeled: 1}
	if got != want {
		t.Errorf("classification = %+v, want %+v", got, want)
	}
}

func TestSelfContainerIsIgnored(t *testing.T) {
	t.Parallel()

	o := opts("npm")
	o.SelfID = "self-id"
	res := Parse(withLabels("self", map[string]string{"npm.proxy.domains": "self.example.com"}), o)
	if len(res.Targets) != 0 {
		t.Errorf("the sync container must never manage itself, got %s", dump(res.Targets))
	}
}

func TestParseAllReportsContainerName(t *testing.T) {
	t.Parallel()

	res := ParseAll([]Container{
		withLabels("broken", map[string]string{"npm.proxy.port": "80"}),
	}, opts("npm"))
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error(), "broken") {
		t.Fatalf("expected an error naming the container, got %v", res.Errors)
	}
}

func boolPtr(b bool) *bool { return &b }

// dump renders targets for a readable diff.
func dump(targets []*Target) string {
	var sb strings.Builder
	for _, t := range targets {
		fmt.Fprintf(&sb, "%+v\n", *t)
	}
	return sb.String()
}

// TestEnableSemantics separates the three meanings of `enable`: the container
// switch, the per-index switch and a resource's own flag.
func TestEnableSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels map[string]string
		want   int
		check  func(*testing.T, []*Target)
	}{
		{
			name: "container opt-out",
			labels: map[string]string{
				"npm.enable":        "false",
				"npm.proxy.domains": "a.example.com",
				"npm.proxy.port":    "80",
			},
			want: 0,
		},
		{
			name: "index opt-out",
			labels: map[string]string{
				"npm.proxy.domains":   "a.example.com",
				"npm.proxy.port":      "80",
				"npm.1.proxy.domains": "b.example.com",
				"npm.1.proxy.port":    "80",
				"npm.1.enable":        "false",
			},
			want: 1,
			check: func(t *testing.T, targets []*Target) {
				if targets[0].DomainNames[0] != "a.example.com" {
					t.Errorf("the wrong index survived: %s", dump(targets))
				}
			},
		},
		{
			name: "resource flag stays a field",
			labels: map[string]string{
				"npm.proxy.domains": "a.example.com",
				"npm.proxy.port":    "80",
				"npm.proxy.enable":  "false",
			},
			want: 1,
			check: func(t *testing.T, targets []*Target) {
				if targets[0].Enabled {
					t.Error("npm.proxy.enable=false should create a disabled host")
				}
			},
		},
		{
			name: "404 is a kind, not an index",
			labels: map[string]string{
				"npm.proxy.domains": "a.example.com",
				"npm.proxy.port":    "80",
				"npm.404.domains":   "parked.example.com",
				"npm.404.enable":    "false",
			},
			want: 2,
			check: func(t *testing.T, targets []*Target) {
				if !targets[0].Enabled {
					t.Error("the proxy host must stay enabled")
				}
				if targets[1].Enabled {
					t.Error("npm.404.enable=false should only disable the 404 host")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Parse(withLabels("app", tc.labels), opts("npm"))
			if len(res.Errors) > 0 {
				t.Fatalf("unexpected errors: %v", res.Errors)
			}
			if len(res.Targets) != tc.want {
				t.Fatalf("got %d targets, want %d: %s", len(res.Targets), tc.want, dump(res.Targets))
			}
			if tc.check != nil {
				tc.check(t, res.Targets)
			}
		})
	}
}

// TestScanProtectsBrokenContainers is the parser half of the "a typo must not
// delete a host" guarantee: a container whose labels cannot be read is
// reported, so the reconcile loop can leave its resources alone.
func TestScanProtectsBrokenContainers(t *testing.T) {
	t.Parallel()

	containers := []Container{
		withPorts(withLabels("good", map[string]string{"npm.proxy.domains": "good.example.com"}), 80),
		withLabels("broken", map[string]string{
			"npm.proxy.domains": "broken.example.com",
			"npm.proxy.port":    "80a", // the typo from the bug report
		}),
	}

	snapshot, res := Scan(containers, opts("npm"))
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if len(snapshot.Targets) != 1 || snapshot.Targets[0].ContainerName != "good" {
		t.Fatalf("targets = %s, want only the valid container", dump(snapshot.Targets))
	}
	if !snapshot.IsProtected("broken", "broken-id") {
		t.Error("the container with the broken label must be protected from deletion")
	}
	if snapshot.IsProtected("good", "good-id") {
		t.Error("a healthy container must not be protected")
	}
	if snapshot.Containers != 2 {
		t.Errorf("Containers = %d, want 2", snapshot.Containers)
	}
}

// A stopped container still produces its resources - without an address and
// without ports, which is exactly what marks them as "do not reconfigure".
func TestStoppedContainerStillYieldsTargets(t *testing.T) {
	t.Parallel()

	c := withLabels("web", map[string]string{"npm.proxy.domains": "web.example.com"})
	c.State = "exited"

	res := Parse(c, opts("npm"))
	if len(res.Errors) != 0 {
		t.Fatalf("a stopped container must not produce errors: %v", res.Errors)
	}
	if len(res.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(res.Targets))
	}
	target := res.Targets[0]
	if target.Running {
		t.Error("the target should know its container is stopped")
	}
	if target.Complete() {
		t.Error("a stopped container has no address, so the target is incomplete")
	}
	if target.Key() != "web.example.com" {
		t.Errorf("Key() = %q, want the identity to survive", target.Key())
	}
}

func TestContainerRunning(t *testing.T) {
	t.Parallel()

	for state, want := range map[string]bool{
		"running": true, "restarting": true, "paused": true, "": true,
		"exited": false, "created": false, "dead": false, "removing": false,
	} {
		if got := (Container{State: state}).Running(); got != want {
			t.Errorf("Running(%q) = %t, want %t", state, got, want)
		}
	}
}

// location_config is a field of the proxy host, not a location block - the two
// only look alike after separator normalisation.
func TestLocationConfigIsNotALocationBlock(t *testing.T) {
	t.Parallel()

	res := Parse(withLabels("app", map[string]string{
		"npm.proxy.domains":         "app.example.com",
		"npm.proxy.port":            "80",
		"npm.proxy.location_config": "add_header X-Test 1;",
		"npm.proxy.location.0.path": "/api",
	}), opts("npm"))

	if len(res.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
	target := res.Targets[0]
	if target.LocationConfig != "add_header X-Test 1;" {
		t.Errorf("LocationConfig = %q", target.LocationConfig)
	}
	if len(target.Locations) != 1 || target.Locations[0].Path != "/api" {
		t.Errorf("locations = %+v, want exactly the /api block", target.Locations)
	}
}
