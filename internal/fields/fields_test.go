package fields

import (
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

func TestNormalizeFoldsSeparators(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{
		"ssl.hsts.subdomains", "ssl.hsts_subdomains", "ssl-hsts-subdomains",
		"SSL.HSTS.Subdomains", " ssl_hsts.subdomains ",
	} {
		if got := Normalize(spelling); got != "ssl.hsts.subdomains" {
			t.Errorf("Normalize(%q) = %q", spelling, got)
		}
	}
}

func TestLookupResolvesEverySpelling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind  npm.Kind
		name  string
		want  string
		found bool
	}{
		{npm.KindProxy, "domains", Domains, true},
		{npm.KindProxy, "domain", Domains, true},
		{npm.KindProxy, "host", ForwardHost, true},
		{npm.KindProxy, "port", ForwardPort, true},
		{npm.KindProxy, "ssl.force", SSLForced, true},
		{npm.KindProxy, "ssl.certificate.id", Certificate, true},
		{npm.KindProxy, "ssl-hsts-subdomains", HSTSSubdomains, true},
		{npm.KindProxy, "block_common_exploits", BlockExploits, true},
		{npm.KindProxy, "accesslist.id", AccessList, true},
		{npm.KindProxy, "certificate_id", Certificate, true},
		{npm.KindStream, "incoming.port", IncomingPort, true},
		{npm.KindStream, "forward.host", ForwardHost, true},
		{npm.KindStream, "forward.tcp", TCP, true},
		{npm.KindStream, "ssl", Certificate, true},
		{npm.KindDead, "host", Domains, true},
		{npm.KindRedirect, "to", ForwardDomain, true},
		// `host` is the upstream for a proxy, so it must not be a domain.
		{npm.KindProxy, "nonsense", "", false},
	}

	for _, tc := range tests {
		got, ok := Lookup(tc.kind, tc.name)
		if ok != tc.found {
			t.Errorf("Lookup(%s, %q) found = %t, want %t", tc.kind, tc.name, ok, tc.found)
			continue
		}
		if ok && got.Name != tc.want {
			t.Errorf("Lookup(%s, %q) = %q, want %q", tc.kind, tc.name, got.Name, tc.want)
		}
	}
}

func TestSuggest(t *testing.T) {
	t.Parallel()

	if got := Suggest(npm.KindProxy, "ssl.forcd"); got != SSLForced {
		t.Errorf("Suggest(ssl.forcd) = %q, want %q", got, SSLForced)
	}
	if got := Suggest(npm.KindProxy, "websockts"); got != Websockets {
		t.Errorf("Suggest(websockts) = %q, want %q", got, Websockets)
	}
	if got := Suggest(npm.KindProxy, "completely.unrelated.nonsense"); got != "" {
		t.Errorf("Suggest(nonsense) = %q, want no suggestion", got)
	}
}

func TestInverseAliases(t *testing.T) {
	t.Parallel()

	for alias, want := range map[string]string{
		"disable_crowdsec_appsec":    CrowdsecAppsec,
		"disable-request-buffering":  RequestBuffering,
		"disable.response.buffering": ResponseBuffering,
	} {
		got, ok := IsInverseAlias(alias)
		if !ok || got != want {
			t.Errorf("IsInverseAlias(%q) = %q/%t, want %q", alias, got, ok, want)
		}
	}
	if _, ok := IsInverseAlias("crowdsec_appsec"); ok {
		t.Error("the positive spelling must not be treated as an inverse alias")
	}
}

func TestBuiltInDefaults(t *testing.T) {
	t.Parallel()

	defaults, warnings, err := LoadDefaults(func(string) string { return "" }, nil)
	if err != nil {
		t.Fatalf("LoadDefaults() error = %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}

	want := map[string]string{
		Certificate:         Auto,
		SSLForced:           Auto,
		HTTP2:               "true",
		HTTP3:               "true",
		HSTS:                "false",
		HSTSSubdomains:      "false",
		BlockExploits:       "true",
		Websockets:          "true",
		Caching:             "false",
		TrustForwardedProto: "false",
		AuthRequest:         "none",
		XFrameOptions:       "",
		NoIndex:             "false",
		CrowdsecAppsec:      "true",
		RequestBuffering:    "true",
		ResponseBuffering:   "true",
		Enabled:             "true",
		ForwardScheme:       "http",
		ForwardPort:         Auto,
	}
	for field, value := range want {
		if got := defaults.Value(npm.KindProxy, field); got != value {
			t.Errorf("default %s = %q, want %q", field, got, value)
		}
	}
	if got := defaults.Value(npm.KindRedirect, HTTPCode); got != "301" {
		t.Errorf("redirect http_code = %q, want 301", got)
	}
	if got := defaults.Value(npm.KindStream, TCP); got != "true" {
		t.Errorf("stream tcp = %q, want true", got)
	}
	if got := defaults.Value(npm.KindStream, UDP); got != "false" {
		t.Errorf("stream udp = %q, want false", got)
	}
}

func TestEnvironmentDefaults(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"NPM_PROXY_SSL_FORCE":       "true",
		"NPM_PROXY_HSTS_SUBDOMAINS": "yes",
		"NPM_PROXY_WEBSOCKETS":      "false",
		"NPM_DEFAULT_HTTP3":         "false",
		"NPM_DEFAULT_CERTIFICATE":   "auto",
		"NPM_STREAM_UDP":            "true",
		"NPM_REDIRECT_HTTP_CODE":    "302",
		"NPM_PROXY_X_FRAME_OPTIONS": "SAMEORIGIN",
	}
	defaults, warnings, err := LoadDefaults(func(k string) string { return env[k] }, envList(env, "NPM_PROXY_TYPO=1"))
	if err != nil {
		t.Fatalf("LoadDefaults() error = %v", err)
	}

	checks := []struct {
		kind  npm.Kind
		field string
		want  string
	}{
		{npm.KindProxy, SSLForced, "true"},
		{npm.KindProxy, HSTSSubdomains, "true"},
		{npm.KindProxy, Websockets, "false"},
		{npm.KindProxy, HTTP3, "false"},
		{npm.KindProxy, XFrameOptions, "sameorigin"},
		{npm.KindStream, UDP, "true"},
		{npm.KindRedirect, HTTPCode, "302"},
		// Not overridden: still the built-in value.
		{npm.KindProxy, HTTP2, "true"},
	}
	for _, c := range checks {
		if got := defaults.Value(c.kind, c.field); got != c.want {
			t.Errorf("%s/%s = %q, want %q", c.kind, c.field, got, c.want)
		}
	}
	if src := defaults.Source(npm.KindProxy, SSLForced); src != "NPM_PROXY_SSL_FORCE" {
		t.Errorf("source = %q, want NPM_PROXY_SSL_FORCE", src)
	}
	if src := defaults.Source(npm.KindProxy, HTTP2); src != "builtin" {
		t.Errorf("source = %q, want builtin", src)
	}
	if len(warnings) != 1 || warnings[0] != "NPM_PROXY_TYPO" {
		t.Errorf("warnings = %v, want the unknown variable reported", warnings)
	}
}

func TestEnvironmentDefaultsAreValidated(t *testing.T) {
	t.Parallel()

	env := map[string]string{"NPM_PROXY_WEBSOCKETS": "maybe"}
	if _, _, err := LoadDefaults(func(k string) string { return env[k] }, nil); err == nil {
		t.Fatal("LoadDefaults() error = nil, want a rejection of the invalid boolean")
	}

	env = map[string]string{"NPM_PROXY_AUTH_REQUEST": "nope"}
	if _, _, err := LoadDefaults(func(k string) string { return env[k] }, nil); err == nil {
		t.Fatal("LoadDefaults() error = nil, want a rejection of the invalid enum")
	}
}

func TestEveryFieldHasAnEnvironmentVariable(t *testing.T) {
	t.Parallel()

	for _, kind := range npm.Kinds {
		for _, f := range List(kind) {
			names := EnvNames(kind, f)
			if len(names) < 2 {
				t.Errorf("%s/%s has no environment variable", kind, f.Name)
			}
			if f.APIField == "" {
				t.Errorf("%s/%s does not name an API field", kind, f.Name)
			}
			if f.Type == TypeEnum && len(f.Enum) == 0 {
				t.Errorf("%s/%s is an enum without values", kind, f.Name)
			}
		}
	}
}

// envList renders a map as os.Environ() would, plus extra entries.
func envList(env map[string]string, extra ...string) []string {
	out := make([]string, 0, len(env)+len(extra))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return append(out, extra...)
}
