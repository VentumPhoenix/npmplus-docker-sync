package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

	cfg, err := Load(env(minimalEnv(nil)))
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
			_, err := Load(env(tc.values))
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
	})))
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

	cfg, err := Load(env(minimalEnv(map[string]string{"DEBOUNCE_INTERVAL": "5"})))
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
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMSecret != "from-file" {
		t.Errorf("NPMSecret = %q, want the trimmed file contents", cfg.NPMSecret)
	}

	if _, err := Load(env(minimalEnv(map[string]string{"NPM_SECRET_FILE": "/nonexistent/secret"}))); err == nil {
		t.Error("Load() error = nil for a missing secret file")
	}
}

func TestAliasVariables(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(map[string]string{
		"NPM_BASE_URL": "http://npm:81",
		"NPM_EMAIL":    "admin@example.com",
		"NPM_PASSWORD": "changeme",
	}))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.NPMURL == "" || cfg.NPMIdentity == "" || cfg.NPMSecret == "" {
		t.Errorf("aliases were not honoured: %+v", cfg.Redacted())
	}
}

func TestSecretIsNeverLogged(t *testing.T) {
	t.Parallel()

	cfg, err := Load(env(minimalEnv(nil)))
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

			cfg, err := Load(env(minimalEnv(map[string]string{"SYNC_KINDS": tc.value})))
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

	cfg, err := Load(env(minimalEnv(nil)))
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
	})))
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
