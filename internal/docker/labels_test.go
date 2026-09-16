package docker

import (
	"reflect"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

func opts(prefix string) ParseOptions {
	return ParseOptions{Prefix: prefix}
}

// container is a small helper for label-only fixtures.
func withLabels(name string, labels map[string]string) Container {
	return Container{ID: name + "-id", Name: name, Labels: labels}
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
			name: "minimal configuration",
			opts: opts("npm"),
			c: withLabels("whoami", map[string]string{
				"npm.enable":     "true",
				"npm.proxy.host": "whoami.example.com",
				"npm.proxy.port": "80",
			}),
			want: []*Target{{
				Kind: npm.KindProxy, Index: 0,
				ContainerID: "whoami-id", ContainerName: "whoami",
				DomainNames:   []string{"whoami.example.com"},
				ForwardScheme: "http", ForwardHost: "whoami", ForwardPort: 80,
				Websockets: true, BlockExploits: true, Enabled: true,
			}},
		},
		{
			name: "shorthand without the proxy segment",
			opts: opts("npm"),
			c: withLabels("app", map[string]string{
				"npm.enable": "yes",
				"npm.host":   "app.example.com",
				"npm.port":   "3000",
			}),
			want: []*Target{{
				Kind: npm.KindProxy, ContainerID: "app-id", ContainerName: "app",
				DomainNames:   []string{"app.example.com"},
				ForwardScheme: "http", ForwardHost: "app", ForwardPort: 3000,
				Websockets: true, BlockExploits: true, Enabled: true,
			}},
		},
		{
			name: "custom prefix",
			opts: opts("proxy"),
			c: withLabels("svc", map[string]string{
				"proxy.enable": "true",
				"proxy.host":   "svc.example.com",
				"proxy.port":   "8080",
			}),
			want: []*Target{{
				Kind: npm.KindProxy, ContainerID: "svc-id", ContainerName: "svc",
				DomainNames:   []string{"svc.example.com"},
				ForwardScheme: "http", ForwardHost: "svc", ForwardPort: 8080,
				Websockets: true, BlockExploits: true, Enabled: true,
			}},
		},
		{
			name: "full configuration",
			opts: opts("npm"),
			c: withLabels("api", map[string]string{
				"npm.enable":                    "1",
				"npm.proxy.host":                "api.example.com, www.api.example.com",
				"npm.proxy.port":                "8443",
				"npm.proxy.scheme":              "https",
				"npm.proxy.forward_host":        "internal-api",
				"npm.proxy.certificate_id":      "7",
				"npm.proxy.ssl.forced":          "true",
				"npm.proxy.ssl.http2":           "true",
				"npm.proxy.ssl.hsts":            "true",
				"npm.proxy.ssl.hsts_subdomains": "true",
				"npm.proxy.websockets":          "false",
				"npm.proxy.block_exploits":      "off",
				"npm.proxy.caching":             "true",
				"npm.proxy.access_list_id":      "2",
				"npm.proxy.advanced_config":     "client_max_body_size 0;",
			}),
			want: []*Target{{
				Kind: npm.KindProxy, ContainerID: "api-id", ContainerName: "api",
				DomainNames:   []string{"api.example.com", "www.api.example.com"},
				ForwardScheme: "https", ForwardHost: "internal-api", ForwardPort: 8443,
				CertificateID: npm.CertificateRef(7),
				SSLForced:     true, HTTP2Support: true, HSTSEnabled: true, HSTSSubdomains: true,
				Websockets: false, BlockExploits: false, Caching: true,
				AccessListID: 2, AdvancedConfig: "client_max_body_size 0;",
				Enabled: true,
			}},
		},
		{
			name: "new letsencrypt certificate",
			opts: opts("npm"),
			c: withLabels("blog", map[string]string{
				"npm.enable":               "true",
				"npm.proxy.host":           "blog.example.com",
				"npm.proxy.port":           "2368",
				"npm.proxy.certificate_id": "new",
				"npm.proxy.ssl.forced":     "true",
				"npm.letsencrypt.email":    "admin@example.com",
				"npm.letsencrypt.agree":    "true",
			}),
			want: []*Target{{
				Kind: npm.KindProxy, ContainerID: "blog-id", ContainerName: "blog",
				DomainNames:   []string{"blog.example.com"},
				ForwardScheme: "http", ForwardHost: "blog", ForwardPort: 2368,
				CertificateID: npm.NewCertificate(), SSLForced: true,
				LetsEncryptEmail: "admin@example.com", LetsEncryptAgree: true,
				Websockets: true, BlockExploits: true, Enabled: true,
			}},
		},
		{name: "no labels at all", opts: opts("npm"), c: withLabels("db", nil)},
		{
			name: "explicitly disabled container",
			opts: opts("npm"),
			c:    withLabels("db", map[string]string{"npm.enable": "false", "npm.host": "db.example.com"}),
		},
		{
			name:    "enabled but no host",
			opts:    opts("npm"),
			c:       withLabels("x", map[string]string{"npm.enable": "true", "npm.port": "80"}),
			wantErr: true,
		},
		{
			name:    "enabled but no port",
			opts:    opts("npm"),
			c:       withLabels("x", map[string]string{"npm.enable": "true", "npm.host": "x.example.com"}),
			wantErr: true,
		},
		{
			name: "port out of range",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com", "npm.port": "70000",
			}),
			wantErr: true,
		},
		{
			name: "invalid scheme",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com", "npm.port": "80", "npm.scheme": "ftp",
			}),
			wantErr: true,
		},
		{
			name: "invalid boolean",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com", "npm.port": "80", "npm.caching": "maybe",
			}),
			wantErr: true,
		},
		{
			name: "ssl forced without certificate",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com", "npm.port": "80", "npm.ssl.forced": "true",
			}),
			wantErr: true,
		},
		{
			name: "new certificate without agreeing to the terms",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com", "npm.port": "80",
				"npm.certificate_id": "new", "npm.letsencrypt.email": "a@example.com",
			}),
			wantErr: true,
		},
		{
			name: "domain with a path is rejected",
			opts: opts("npm"),
			c: withLabels("x", map[string]string{
				"npm.enable": "true", "npm.host": "x.example.com/foo", "npm.port": "80",
			}),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, errs := Parse(tc.c, tc.opts)
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("Parse() errors = none, want an error (targets %+v)", got)
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("Parse() errors = %v", errs)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Parse() =\n%s\nwant\n%s", format(got), format(tc.want))
			}
		})
	}
}

func TestParseIndexedLabels(t *testing.T) {
	t.Parallel()

	c := withLabels("multi", map[string]string{
		"npm.enable": "true",

		// index 0, implicit proxy
		"npm.0.proxy.host": "app.example.com",
		"npm.0.proxy.port": "8080",

		// index 1, a second proxy host of the same container
		"npm.1.proxy.host":   "admin.example.com",
		"npm.1.proxy.port":   "9090",
		"npm.1.proxy.scheme": "https",

		// index 2, a stream
		"npm.2.stream.incoming_port": "5432",
		"npm.2.stream.forward_port":  "5432",
		"npm.2.stream.udp":           "true",

		// index 3, a redirect
		"npm.3.redirect.host":           "old.example.com",
		"npm.3.redirect.forward_domain": "app.example.com",
		"npm.3.redirect.http_code":      "308",

		// index 4, a 404 host
		"npm.4.404.host": "parked.example.com",
	})

	targets, errs := Parse(c, opts("npm"))
	if len(errs) != 0 {
		t.Fatalf("Parse() errors = %v", errs)
	}
	if len(targets) != 5 {
		t.Fatalf("got %d targets, want 5:\n%s", len(targets), format(targets))
	}

	// Targets come back ordered by index, then by kind.
	wantKinds := []npm.Kind{npm.KindProxy, npm.KindProxy, npm.KindStream, npm.KindRedirect, npm.KindDead}
	for i, want := range wantKinds {
		if targets[i].Kind != want {
			t.Errorf("target %d kind = %s, want %s", i, targets[i].Kind, want)
		}
		if targets[i].Index != i {
			t.Errorf("target %d index = %d, want %d", i, targets[i].Index, i)
		}
	}

	if targets[1].ForwardScheme != "https" || targets[1].ForwardPort != 9090 {
		t.Errorf("index 1 = %+v, want https:9090", targets[1])
	}
	stream := targets[2]
	if stream.IncomingPort != 5432 || stream.ForwardingPort != 5432 || !stream.TCPForwarding || !stream.UDPForwarding {
		t.Errorf("stream = %+v, want tcp+udp 5432", stream)
	}
	redirect := targets[3]
	if redirect.ForwardDomainName != "app.example.com" || redirect.ForwardHTTPCode != 308 || !redirect.PreservePath {
		t.Errorf("redirect = %+v", redirect)
	}
	if targets[4].Key() != "parked.example.com" {
		t.Errorf("404 host key = %q", targets[4].Key())
	}
}

func TestParseUnindexedIsIndexZero(t *testing.T) {
	t.Parallel()

	c := withLabels("mixed", map[string]string{
		"npm.enable":               "true",
		"npm.proxy.host":           "app.example.com",
		"npm.proxy.port":           "80",
		"npm.stream.incoming_port": "5000",
		"npm.1.proxy.host":         "second.example.com",
		"npm.1.proxy.port":         "81",
	})

	targets, errs := Parse(c, opts("npm"))
	if len(errs) != 0 {
		t.Fatalf("Parse() errors = %v", errs)
	}
	if len(targets) != 3 {
		t.Fatalf("got %d targets, want 3:\n%s", len(targets), format(targets))
	}
	for _, target := range targets[:2] {
		if target.Index != 0 {
			t.Errorf("unindexed label produced index %d, want 0", target.Index)
		}
	}
	// The stream of index 0 defaults its forwarding port to the incoming one.
	if targets[1].Kind != npm.KindStream || targets[1].ForwardingPort != 5000 {
		t.Errorf("stream = %+v, want forwarding port 5000", targets[1])
	}
}

func TestParsePerIndexDisable(t *testing.T) {
	t.Parallel()

	c := withLabels("partly", map[string]string{
		"npm.enable":       "true",
		"npm.0.proxy.host": "a.example.com",
		"npm.0.proxy.port": "80",
		"npm.1.proxy.host": "b.example.com",
		"npm.1.proxy.port": "81",
		"npm.1.enable":     "false",
	})

	targets, errs := Parse(c, opts("npm"))
	if len(errs) != 0 {
		t.Fatalf("Parse() errors = %v", errs)
	}
	if len(targets) != 1 || targets[0].Key() != "a.example.com" {
		t.Fatalf("targets = %s, want only index 0", format(targets))
	}
}

func TestParseOneBrokenEntryKeepsTheOthers(t *testing.T) {
	t.Parallel()

	c := withLabels("half-broken", map[string]string{
		"npm.enable":       "true",
		"npm.0.proxy.host": "good.example.com",
		"npm.0.proxy.port": "80",
		"npm.1.proxy.host": "broken.example.com", // no port
	})

	targets, errs := Parse(c, opts("npm"))
	if len(targets) != 1 || targets[0].Key() != "good.example.com" {
		t.Fatalf("targets = %s, want the valid entry", format(targets))
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one", errs)
	}
}

func TestParseRedirect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		labels  map[string]string
		want    *Target
		wantErr bool
	}{
		{
			name: "defaults",
			labels: map[string]string{
				"npm.enable":                  "true",
				"npm.redirect.host":           "old.example.com",
				"npm.redirect.forward_domain": "new.example.com",
			},
			want: &Target{
				Kind: npm.KindRedirect, ContainerID: "r-id", ContainerName: "r",
				DomainNames:       []string{"old.example.com"},
				ForwardScheme:     "auto",
				ForwardDomainName: "new.example.com",
				ForwardHTTPCode:   301,
				PreservePath:      true,
				BlockExploits:     true,
				Enabled:           true,
			},
		},
		{
			name: "aliases and overrides",
			labels: map[string]string{
				"npm.enable":                 "true",
				"npm.redirect.from":          "old.example.com",
				"npm.redirect.to":            "new.example.com",
				"npm.redirect.code":          "302",
				"npm.redirect.scheme":        "https",
				"npm.redirect.preserve_path": "false",
			},
			want: &Target{
				Kind: npm.KindRedirect, ContainerID: "r-id", ContainerName: "r",
				DomainNames:       []string{"old.example.com"},
				ForwardScheme:     "https",
				ForwardDomainName: "new.example.com",
				ForwardHTTPCode:   302,
				PreservePath:      false,
				BlockExploits:     true,
				Enabled:           true,
			},
		},
		{
			name: "missing target domain",
			labels: map[string]string{
				"npm.enable":        "true",
				"npm.redirect.host": "old.example.com",
			},
			wantErr: true,
		},
		{
			name: "target with a scheme",
			labels: map[string]string{
				"npm.enable":                  "true",
				"npm.redirect.host":           "old.example.com",
				"npm.redirect.forward_domain": "https://new.example.com",
			},
			wantErr: true,
		},
		{
			name: "invalid status code",
			labels: map[string]string{
				"npm.enable":                  "true",
				"npm.redirect.host":           "old.example.com",
				"npm.redirect.forward_domain": "new.example.com",
				"npm.redirect.http_code":      "200",
			},
			wantErr: true,
		},
		{
			name: "invalid scheme",
			labels: map[string]string{
				"npm.enable":                  "true",
				"npm.redirect.host":           "old.example.com",
				"npm.redirect.forward_domain": "new.example.com",
				"npm.redirect.scheme":         "gopher",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			targets, errs := Parse(withLabels("r", tc.labels), opts("npm"))
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("Parse() errors = none, want an error")
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("Parse() errors = %v", errs)
			}
			if len(targets) != 1 || !reflect.DeepEqual(targets[0], tc.want) {
				t.Errorf("Parse() =\n%s\nwant\n%+v", format(targets), tc.want)
			}
		})
	}
}

func TestParseStream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		labels  map[string]string
		check   func(t *testing.T, target *Target)
		wantErr bool
	}{
		{
			name: "tcp by default",
			labels: map[string]string{
				"npm.enable":               "true",
				"npm.stream.incoming_port": "5432",
				"npm.stream.forward_port":  "5432",
			},
			check: func(t *testing.T, target *Target) {
				if !target.TCPForwarding || target.UDPForwarding {
					t.Errorf("protocols = tcp:%v udp:%v, want tcp only", target.TCPForwarding, target.UDPForwarding)
				}
				if target.Key() != "5432" {
					t.Errorf("Key() = %q, want the incoming port", target.Key())
				}
			},
		},
		{
			name: "port alias and protocol shorthand",
			labels: map[string]string{
				"npm.enable":          "true",
				"npm.stream.port":     "51820",
				"npm.stream.protocol": "udp",
			},
			check: func(t *testing.T, target *Target) {
				if target.IncomingPort != 51820 || target.ForwardingPort != 51820 {
					t.Errorf("ports = %d/%d, want 51820 on both sides", target.IncomingPort, target.ForwardingPort)
				}
				if target.TCPForwarding || !target.UDPForwarding {
					t.Errorf("protocols = tcp:%v udp:%v, want udp only", target.TCPForwarding, target.UDPForwarding)
				}
			},
		},
		{
			name: "explicit forwarding host",
			labels: map[string]string{
				"npm.enable":                 "true",
				"npm.stream.incoming_port":   "2222",
				"npm.stream.forwarding_host": "ssh-box",
				"npm.stream.forwarding_port": "22",
			},
			check: func(t *testing.T, target *Target) {
				if target.ForwardingHost != "ssh-box" || target.ForwardingPort != 22 {
					t.Errorf("upstream = %s:%d", target.ForwardingHost, target.ForwardingPort)
				}
			},
		},
		{
			name:    "missing incoming port",
			labels:  map[string]string{"npm.enable": "true", "npm.stream.forward_port": "5432"},
			wantErr: true,
		},
		{
			name: "no protocol at all",
			labels: map[string]string{
				"npm.enable":               "true",
				"npm.stream.incoming_port": "5432",
				"npm.stream.tcp":           "false",
				"npm.stream.udp":           "false",
			},
			wantErr: true,
		},
		{
			name: "invalid protocol",
			labels: map[string]string{
				"npm.enable":               "true",
				"npm.stream.incoming_port": "5432",
				"npm.stream.protocol":      "sctp",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			targets, errs := Parse(withLabels("s", tc.labels), opts("npm"))
			if tc.wantErr {
				if len(errs) == 0 {
					t.Fatalf("Parse() errors = none, want an error")
				}
				return
			}
			if len(errs) != 0 {
				t.Fatalf("Parse() errors = %v", errs)
			}
			if len(targets) != 1 {
				t.Fatalf("targets = %s, want exactly one stream", format(targets))
			}
			if targets[0].Kind != npm.KindStream {
				t.Fatalf("kind = %s, want stream", targets[0].Kind)
			}
			tc.check(t, targets[0])
		})
	}
}

func TestParseDeadHost(t *testing.T) {
	t.Parallel()

	for _, segment := range []string{"404", "dead"} {
		t.Run(segment, func(t *testing.T) {
			t.Parallel()

			targets, errs := Parse(withLabels("parked", map[string]string{
				"npm.enable":                    "true",
				"npm." + segment + ".host":      "parked.example.com",
				"npm." + segment + ".ssl.http2": "true",
			}), opts("npm"))
			if len(errs) != 0 {
				t.Fatalf("Parse() errors = %v", errs)
			}
			if len(targets) != 1 || targets[0].Kind != npm.KindDead {
				t.Fatalf("targets = %s, want one 404 host", format(targets))
			}
			if !targets[0].HTTP2Support {
				t.Error("http2 label was not applied")
			}
		})
	}
}

func TestParseAll(t *testing.T) {
	t.Parallel()

	containers := []Container{
		withLabels("ok", map[string]string{"npm.enable": "true", "npm.host": "ok.example.com", "npm.port": "80"}),
		withLabels("broken", map[string]string{"npm.enable": "true", "npm.host": "broken.example.com"}),
		withLabels("ignored", nil),
	}

	targets, errs := ParseAll(containers, opts("npm"))
	if len(targets) != 1 || targets[0].ContainerName != "ok" {
		t.Fatalf("targets = %s, want only the valid container", format(targets))
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want exactly one error", errs)
	}
}

func TestIsEnabled(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{"true": true, "1": true, "yes": true, "false": false, "": false, "nonsense": false}
	for value, want := range tests {
		c := withLabels("c", map[string]string{"npm.enable": value})
		if got := IsEnabled(c, "npm"); got != want {
			t.Errorf("IsEnabled(%q) = %v, want %v", value, got, want)
		}
	}
	if IsEnabled(withLabels("c", nil), "npm") {
		t.Error("IsEnabled() = true for a container without labels")
	}
}

func TestTargetKey(t *testing.T) {
	t.Parallel()

	proxy := &Target{Kind: npm.KindProxy, DomainNames: []string{"b.example.com", "a.example.com"}}
	if got := proxy.Key(); got != "a.example.com" {
		t.Errorf("Key() = %q, want the alphabetically first domain", got)
	}
	stream := &Target{Kind: npm.KindStream, IncomingPort: 5432}
	if got := stream.Key(); got != "5432" {
		t.Errorf("Key() = %q, want the incoming port", got)
	}
	if got := (&Target{Kind: npm.KindProxy}).Key(); got != "" {
		t.Errorf("Key() = %q, want empty without domains", got)
	}
}

// format renders targets for readable test failures.
func format(targets []*Target) string {
	out := ""
	for _, t := range targets {
		out += "  " + t.Describe() + "\n"
	}
	if out == "" {
		return "  <none>"
	}
	return out
}

func TestParseLocationBlocks(t *testing.T) {
	t.Parallel()

	targets, errs := Parse(withLabels("app", map[string]string{
		"npm.enable":     "true",
		"npm.proxy.host": "app.example.com",
		"npm.proxy.port": "8080",

		"npm.proxy.location.0.path":            "/api",
		"npm.proxy.location.0.forward_host":    "api-backend",
		"npm.proxy.location.0.forward_port":    "3000",
		"npm.proxy.location.0.advanced_config": "proxy_read_timeout 600s;",

		// Everything but the path defaults to the host's own upstream.
		"npm.proxy.location.1.path": "/static",
	}), opts("npm"))
	if len(errs) != 0 {
		t.Fatalf("Parse() errors = %v", errs)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %s, want one proxy host", format(targets))
	}

	locations := targets[0].Locations
	if len(locations) != 2 {
		t.Fatalf("locations = %+v, want 2", locations)
	}
	if locations[0].Path != "/api" || locations[0].ForwardHost != "api-backend" || locations[0].ForwardPort != 3000 {
		t.Errorf("location 0 = %+v", locations[0])
	}
	if locations[0].AdvancedConfig != "proxy_read_timeout 600s;" {
		t.Errorf("location 0 advanced config = %q", locations[0].AdvancedConfig)
	}
	if locations[1].Path != "/static" || locations[1].ForwardHost != "app" || locations[1].ForwardPort != 8080 {
		t.Errorf("location 1 = %+v, want the proxy host defaults", locations[1])
	}
	if locations[1].ForwardScheme != "http" {
		t.Errorf("location 1 scheme = %q, want the inherited scheme", locations[1].ForwardScheme)
	}
}

func TestParseLocationBlockErrors(t *testing.T) {
	t.Parallel()

	base := map[string]string{
		"npm.enable":     "true",
		"npm.proxy.host": "app.example.com",
		"npm.proxy.port": "8080",
	}

	tests := map[string]map[string]string{
		"missing path":      {"npm.proxy.location.0.forward_port": "3000"},
		"relative path":     {"npm.proxy.location.0.path": "api"},
		"invalid index":     {"npm.proxy.location.x.path": "/api"},
		"missing sub field": {"npm.proxy.location.0": "/api"},
		"invalid port":      {"npm.proxy.location.0.path": "/api", "npm.proxy.location.0.forward_port": "abc"},
		"port out of range": {"npm.proxy.location.0.path": "/api", "npm.proxy.location.0.forward_port": "70000"},
		"invalid scheme":    {"npm.proxy.location.0.path": "/api", "npm.proxy.location.0.forward_scheme": "ftp"},
	}

	for name, extra := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			labels := map[string]string{}
			for k, v := range base {
				labels[k] = v
			}
			for k, v := range extra {
				labels[k] = v
			}

			if _, errs := Parse(withLabels("app", labels), opts("npm")); len(errs) == 0 {
				t.Fatalf("Parse() errors = none, want an error")
			}
		})
	}
}
