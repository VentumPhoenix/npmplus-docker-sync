package docker

import (
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
)

// FuzzParse throws arbitrary label names and values at the parser. Labels come
// from whoever writes a compose file, so the only acceptable outcomes are a
// target or an error - never a panic, and never a target without an identity.
func FuzzParse(f *testing.F) {
	seeds := []struct{ name, value string }{
		{"npm.proxy.domains", "app.example.com"},
		{"npm.proxy.port", "80a"},
		{"npm.proxy.1.domains", "a.example.com,b.example.com"},
		{"npm-proxy.ssl-hsts-subdomains", "yes"},
		{"npm.999999999999999999999.proxy.domains", "a.example.com"},
		{"npm.proxy.location.0.path", "/api"},
		{"npm.proxy.location..path", "/api"},
		{"npm.stream.incoming_port", "-1"},
		{"npm.404.domains", "..."},
		{"npm.proxy.certificate", "name:"},
		{"npm.", "x"},
		{"npm..", ""},
		{"npm.proxy.access_list", "1,,2"},
	}
	for _, seed := range seeds {
		f.Add(seed.name, seed.value)
	}

	f.Fuzz(func(t *testing.T, name, value string) {
		c := withLabels("fuzz", map[string]string{
			"npm.proxy.domains": "base.example.com",
			"npm.proxy.port":    "80",
			name:                value,
		})
		res := Parse(c, opts("npm"))

		for _, target := range res.Targets {
			if target.Key() == "" {
				t.Fatalf("target without identity from %q=%q: %+v", name, value, target)
			}
			if target.Kind == "" {
				t.Fatalf("target without kind from %q=%q", name, value)
			}
			for _, domain := range target.DomainNames {
				if strings.ContainsAny(domain, " /\\") {
					t.Fatalf("invalid domain %q accepted from %q=%q", domain, name, value)
				}
			}
			if target.ForwardPort < 0 || target.ForwardPort > 65535 {
				t.Fatalf("port %d out of range from %q=%q", target.ForwardPort, name, value)
			}
		}
	})
}

// FuzzSplitLabel exercises the grammar on its own: kind, index and field must
// always come back consistent, whatever the input looks like.
func FuzzSplitLabel(f *testing.F) {
	for _, seed := range []string{
		"proxy.domains", "1.proxy.domains", "proxy.1.domains", "404.domains",
		"domains", "1", "..", "-", "proxy", "99999999999999999999.domains",
		"proxy.location.0.path", "enable", "PROXY.DOMAINS",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, rest string) {
		kind, index, field, _, err := splitLabel(rest)
		if err != nil {
			return
		}
		if index < 0 {
			t.Fatalf("negative index %d from %q", index, rest)
		}
		if field == "" {
			t.Fatalf("empty field name from %q", rest)
		}
		if !kind.Valid() {
			t.Fatalf("invalid kind %q from %q", kind, rest)
		}
		if field != fields.Normalize(field) {
			t.Fatalf("field %q from %q is not normalised", field, rest)
		}
	})
}
