package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// env builds a Getenv from a map.
func env(values map[string]string) Getenv {
	return func(key string) string { return values[key] }
}

func minimalEnv(extra map[string]string) map[string]string {
	values := map[string]string{
		"NPM_URL":      "http://npm:81",
		"NPM_IDENTITY": "admin@example.com",
		"NPM_SECRET":   "changeme",
	}
	for k, v := range extra {
		values[k] = v
	}
	return values
}

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(nil)), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"DockerHost", cfg.DockerHost, DefaultDockerHost},
		{"LabelPrefix", cfg.LabelPrefix, DefaultLabelPrefix},
		{"DebounceInterval", cfg.DebounceInterval, DefaultDebounceInterval},
		{"DebounceMaxWait", cfg.DebounceMaxWait, DefaultDebounceMaxWait},
		{"ResyncInterval", cfg.ResyncInterval, DefaultResyncInterval},
		{"DeleteOrphans", cfg.DeleteOrphans, true},
		{"AdoptExisting", cfg.AdoptExisting, true},
		{"DryRun", cfg.DryRun, false},
		{"LogLevel", cfg.LogLevel, slog.LevelInfo},
		{"LogFormat", cfg.LogFormat, "text"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestLoadValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		wantErr string
	}{
		{name: "missing url", values: map[string]string{"NPM_IDENTITY": "a@b.c", "NPM_SECRET": "x"}, wantErr: "NPM_URL"},
		{name: "missing identity", values: map[string]string{"NPM_URL": "http://npm:81", "NPM_SECRET": "x"}, wantErr: "NPM_IDENTITY"},
		{name: "missing secret", values: map[string]string{"NPM_URL": "http://npm:81", "NPM_IDENTITY": "a@b.c"}, wantErr: "NPM_SECRET"},
		{name: "url without scheme", values: minimalEnv(map[string]string{"NPM_URL": "npm:81"}), wantErr: "NPM_URL"},
		{name: "unsupported url scheme", values: minimalEnv(map[string]string{"NPM_URL": "ftp://npm"}), wantErr: "http or https"},
		{name: "bad docker host", values: minimalEnv(map[string]string{"DOCKER_HOST": "docker:2375"}), wantErr: "DOCKER_HOST"},
		{name: "bad duration", values: minimalEnv(map[string]string{"DEBOUNCE_INTERVAL": "soon"}), wantErr: "DEBOUNCE_INTERVAL"},
		{name: "bad boolean", values: minimalEnv(map[string]string{"DRY_RUN": "perhaps"}), wantErr: "DRY_RUN"},
		{name: "bad log level", values: minimalEnv(map[string]string{"LOG_LEVEL": "loud"}), wantErr: "LOG_LEVEL"},
		{name: "bad log format", values: minimalEnv(map[string]string{"LOG_FORMAT": "xml"}), wantErr: "LOG_FORMAT"},
		{name: "max wait below interval", values: minimalEnv(map[string]string{"DEBOUNCE_INTERVAL": "10s", "DEBOUNCE_MAX_WAIT": "1s"}), wantErr: "DEBOUNCE_MAX_WAIT"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(env(tc.values), nil)
			if err == nil {
				t.Fatalf("Load() error = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Load() error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(map[string]string{
		"DOCKER_HOST":       "tcp://docker-socket-proxy:2375",
		"LABEL_PREFIX":      "proxy.",
		"DEBOUNCE_INTERVAL": "500ms",
		"DEBOUNCE_MAX_WAIT": "10s",
		"RESYNC_INTERVAL":   "0",
		"DELETE_ORPHANS":    "no",
		"ADOPT_EXISTING":    "off",
		"DRY_RUN":           "yes",
		"LOG_LEVEL":         "debug",
		"LOG_FORMAT":        "json",
		"HEALTH_ADDR":       ":8080",
		"NPM_URL":           "https://npm.example.com/",
	})), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.NPMURL != "https://npm.example.com" {
		t.Errorf("NPMURL = %q, want the trailing slash trimmed", cfg.NPMURL)
	}
	if cfg.LabelPrefix != "proxy" {
		t.Errorf("LabelPrefix = %q, want the trailing dot trimmed", cfg.LabelPrefix)
	}
	if cfg.DebounceInterval != 500*time.Millisecond || cfg.DebounceMaxWait != 10*time.Second {
		t.Errorf("debounce = %v/%v", cfg.DebounceInterval, cfg.DebounceMaxWait)
	}
	if cfg.ResyncInterval != 0 {
		t.Errorf("ResyncInterval = %v, want 0 (disabled)", cfg.ResyncInterval)
	}
	if cfg.DeleteOrphans || cfg.AdoptExisting || !cfg.DryRun {
		t.Errorf("flags = %v/%v/%v", cfg.DeleteOrphans, cfg.AdoptExisting, cfg.DryRun)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.LogFormat != "json" || cfg.HealthAddr != ":8080" {
		t.Errorf("observability = %v/%v/%v", cfg.LogLevel, cfg.LogFormat, cfg.HealthAddr)
	}
}

func TestBareNumbersAreSeconds(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(map[string]string{"DEBOUNCE_INTERVAL": "5"})), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DebounceInterval != 5*time.Second {
		t.Errorf("DebounceInterval = %v, want 5s", cfg.DebounceInterval)
	}
}

func TestSecretFromFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	cfg, err := Load(env(map[string]string{
		"NPM_URL":         "http://npm:81",
		"NPM_IDENTITY":    "admin@example.com",
		"NPM_SECRET_FILE": path,
	}), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMSecret != "from-file" {
		t.Errorf("NPMSecret = %q, want the trimmed file contents", cfg.NPMSecret)
	}

	if _, err := Load(env(minimalEnv(map[string]string{"NPM_SECRET_FILE": "/nonexistent/secret"})), nil); err == nil {
		t.Error("Load() error = nil for a missing secret file")
	}
}

func TestAliasVariables(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(map[string]string{
		"NPM_BASE_URL": "http://npm:81",
		"NPM_EMAIL":    "admin@example.com",
		"NPM_PASSWORD": "changeme",
	}), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMURL == "" || cfg.NPMIdentity == "" || cfg.NPMSecret == "" {
		t.Errorf("aliases were not honoured: %+v", cfg.Redacted())
	}
}

func TestSecretIsNeverLogged(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(nil)), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.Redacted().NPMSecret; got != "***" {
		t.Errorf("Redacted().NPMSecret = %q, want ***", got)
	}
	if strings.Contains(cfg.LogValue().String(), "changeme") {
		t.Error("LogValue() leaks the secret")
	}
}

func TestParseBool(t *testing.T) {
	t.Parallel()

	truthy := []string{"1", "t", "true", "TRUE", "y", "yes", "on", "enable", "enabled"}
	falsy := []string{"0", "f", "false", "FALSE", "n", "no", "off", "disable", "disabled"}

	for _, raw := range truthy {
		if got, err := ParseBool(raw); err != nil || !got {
			t.Errorf("ParseBool(%q) = %v, %v; want true", raw, got, err)
		}
	}
	for _, raw := range falsy {
		if got, err := ParseBool(raw); err != nil || got {
			t.Errorf("ParseBool(%q) = %v, %v; want false", raw, got, err)
		}
	}
	if _, err := ParseBool("maybe"); err == nil {
		t.Error("ParseBool(\"maybe\") error = nil, want error")
	}
}

func TestLoadResourceKinds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    []npm.Kind
		wantErr bool
	}{
		{name: "default is everything", value: "", want: npm.Kinds},
		{name: "subset", value: "proxy,stream", want: []npm.Kind{npm.KindProxy, npm.KindStream}},
		{name: "aliases", value: "proxies redirection 404", want: []npm.Kind{npm.KindProxy, npm.KindRedirect, npm.KindDead}},
		{name: "duplicates collapse", value: "proxy,proxy", want: []npm.Kind{npm.KindProxy}},
		{name: "unknown kind", value: "gopher", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := Load(env(minimalEnv(map[string]string{"SYNC_KINDS": tc.value})), nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(cfg.Kinds, tc.want) {
				t.Errorf("Kinds = %v, want %v", cfg.Kinds, tc.want)
			}
		})
	}
}

func TestLoadIPResolutionDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(nil)), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ResolveIP {
		t.Error("ResolveIP should default to true so upstreams use container IPs")
	}
	if cfg.NPMNetwork != "" {
		t.Errorf("NPMNetwork = %q, want empty by default", cfg.NPMNetwork)
	}

	cfg, err = Load(env(minimalEnv(map[string]string{
		"RESOLVE_CONTAINER_IP": "false",
		"NPM_NETWORK":          "npm",
	})), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ResolveIP {
		t.Error("RESOLVE_CONTAINER_IP=false was not honoured")
	}
	if cfg.NPMNetwork != "npm" {
		t.Errorf("NPMNetwork = %q, want npm", cfg.NPMNetwork)
	}
}

func TestLoadFlavour(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		values  map[string]string
		want    npm.Flavour
		wantErr bool
	}{
		{name: "defaults to auto detection", values: nil, want: npm.FlavourAuto},
		{name: "british spelling", values: map[string]string{"NPM_FLAVOUR": "npmplus"}, want: npm.FlavourNPMplus},
		{name: "american spelling", values: map[string]string{"NPM_FLAVOR": "npm"}, want: npm.FlavourNPM},
		{name: "rejects an unknown value", values: map[string]string{"NPM_FLAVOUR": "traefik"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(env(minimalEnv(tt.values)), nil)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Load() = nil error, want a rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.NPMFlavour != tt.want {
				t.Errorf("NPMFlavour = %q, want %q", cfg.NPMFlavour, tt.want)
			}
		})
	}
}

func TestLoadNetworkStrictness(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(map[string]string{"NPM_NETWORK": "npm-frontend"})), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.StrictNetwork {
		t.Error("StrictNetwork = false; naming a network must make the choice strict by default")
	}

	cfg, err = Load(env(minimalEnv(map[string]string{
		"NPM_NETWORK":        "npm-frontend",
		"NPM_NETWORK_STRICT": "false",
	})), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.StrictNetwork {
		t.Error("StrictNetwork = true; NPM_NETWORK_STRICT=false must be honoured")
	}
}

func TestLoadPayloadLogging(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"LOG_PAYLOADS", "NPM_DEBUG_PAYLOADS"} {
		cfg, err := Load(env(minimalEnv(map[string]string{key: "true"})), nil)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if !cfg.LogPayloads {
			t.Errorf("%s=true did not enable payload logging", key)
		}
	}

	cfg, err := Load(env(minimalEnv(nil)), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.LogPayloads {
		t.Error("payload logging must be off by default: bodies can carry DNS credentials")
	}
}

// TestLoadBeta3Defaults pins the defaults of the settings added in
// v1.0.0-beta.3.
func TestLoadBeta3Defaults(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(nil)), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.ExposedByDefault {
		t.Error("NPM_EXPOSED_BY_DEFAULT should default to true")
	}
	if cfg.StrictLabels || cfg.MigrateFromRedth || cfg.CertificateAutoCreate {
		t.Error("the opt-in switches should default to false")
	}
	if cfg.CertificatePartial != certs.PartialPrimary {
		t.Errorf("CertificatePartial = %q, want primary", cfg.CertificatePartial)
	}
	if cfg.CertificatePoll != DefaultCertificatePoll {
		t.Errorf("CertificatePoll = %s, want %s", cfg.CertificatePoll, DefaultCertificatePoll)
	}
	if !reflect.DeepEqual(cfg.PortPreference, fields.DefaultPortPreference) {
		t.Errorf("PortPreference = %v, want %v", cfg.PortPreference, fields.DefaultPortPreference)
	}
	if got := cfg.Defaults.Value(npm.KindProxy, fields.Certificate); got != fields.Auto {
		t.Errorf("certificate default = %q, want auto", got)
	}
}

func TestLoadBeta3Settings(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(map[string]string{
		"NPM_EXPOSED_BY_DEFAULT":      "false",
		"STRICT_LABELS":               "true",
		"MIGRATE_FROM_REDTH":          "true",
		"NPM_CERTIFICATE_PARTIAL":     "none",
		"NPM_CERTIFICATE_AUTO_CREATE": "true",
		"CERTIFICATE_POLL_INTERVAL":   "30",
		"NPM_PORT_PREFERENCE":         "3000, 80",
		"NPM_CONTAINER_NAME":          "npmplus",
		"NPM_PROXY_WEBSOCKETS":        "false",
	})), []string{"NPM_PROXY_TYPO=1"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ExposedByDefault || !cfg.StrictLabels || !cfg.MigrateFromRedth || !cfg.CertificateAutoCreate {
		t.Errorf("switches = %+v", cfg)
	}
	if cfg.CertificatePartial != certs.PartialNone {
		t.Errorf("CertificatePartial = %q, want none", cfg.CertificatePartial)
	}
	if cfg.CertificatePoll != 30*time.Second {
		t.Errorf("CertificatePoll = %s, want 30s", cfg.CertificatePoll)
	}
	if !reflect.DeepEqual(cfg.PortPreference, []int{3000, 80}) {
		t.Errorf("PortPreference = %v, want [3000 80]", cfg.PortPreference)
	}
	if cfg.NPMContainer != "npmplus" {
		t.Errorf("NPMContainer = %q", cfg.NPMContainer)
	}
	if got := cfg.Defaults.Value(npm.KindProxy, fields.Websockets); got != "false" {
		t.Errorf("websockets default = %q, want false", got)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "NPM_PROXY_TYPO") {
		t.Errorf("Warnings = %v, want the unknown variable reported", cfg.Warnings)
	}
}

// Redth's environment block has to run unchanged, and each alias is announced
// once so the log says which canonical name it mapped to.
func TestRedthEnvironmentAliases(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(map[string]string{
		"NPM_URL":      "http://npm:81",
		"NPM_EMAIL":    "admin@example.com",
		"NPM_PASSWORD": "changeme",
	}), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMIdentity != "admin@example.com" || cfg.NPMSecret != "changeme" {
		t.Fatalf("credentials = %q / %q", cfg.NPMIdentity, cfg.Redacted().NPMSecret)
	}
	joined := strings.Join(cfg.Notices, "\n")
	for _, want := range []string{"NPM_EMAIL", "NPM_PASSWORD"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notices = %v, want %s to be announced", cfg.Notices, want)
		}
	}
}

// Any variable may come from a file, not just the password.
func TestAnyVariableFromFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "url")
	if err := os.WriteFile(path, []byte("http://npm-from-file:81\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(env(map[string]string{
		"NPM_URL_FILE": path,
		"NPM_IDENTITY": "admin@example.com",
		"NPM_SECRET":   "changeme",
	}), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMURL != "http://npm-from-file:81" {
		t.Errorf("NPMURL = %q, want the file content", cfg.NPMURL)
	}
}

func TestInvalidBeta3Settings(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"NPM_CERTIFICATE_PARTIAL": "sometimes",
		"NPM_PORT_PREFERENCE":     "http",
		"NPM_PROXY_AUTH_REQUEST":  "nope",
		"NPM_DEFAULT_HTTP3":       "maybe",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			if _, err := Load(env(minimalEnv(map[string]string{key: value})), nil); err == nil {
				t.Fatalf("Load() error = nil for %s=%s", key, value)
			}
		})
	}
}
