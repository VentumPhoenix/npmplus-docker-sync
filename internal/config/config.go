// Package config loads and validates the runtime configuration from the
// process environment. Everything is injectable so the loader can be unit
// tested without touching the real environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Defaults used when the corresponding environment variable is unset.
const (
	DefaultDockerHost       = "unix:///var/run/docker.sock"
	DefaultLabelPrefix      = "npm"
	DefaultDebounceInterval = 3 * time.Second
	DefaultDebounceMaxWait  = 30 * time.Second
	DefaultResyncInterval   = 5 * time.Minute
	DefaultHTTPTimeout      = 30 * time.Second
)

// Config is the fully resolved runtime configuration.
type Config struct {
	// NPM / NPMplus API.
	NPMURL                string
	NPMIdentity           string
	NPMSecret             string
	NPMTimeout            time.Duration
	NPMInsecureSkipVerify bool
	// NPMFlavour pins the API dialect instead of probing for it.
	NPMFlavour npm.Flavour

	// Docker endpoint. Either a unix socket or a TCP endpoint pointing at a
	// docker-socket-proxy (recommended, see SECURITY.md).
	DockerHost string

	// Behaviour.
	LabelPrefix string
	ResolveIP   bool
	NPMNetwork  string
	// StrictNetwork skips a container that is not attached to NPMNetwork
	// instead of falling back to an address NPM cannot route to. Only has an
	// effect when NPMNetwork is set.
	StrictNetwork    bool
	Kinds            []npm.Kind
	DebounceInterval time.Duration
	DebounceMaxWait  time.Duration
	ResyncInterval   time.Duration
	DeleteOrphans    bool
	AdoptExisting    bool
	DryRun           bool
	ShutdownTimeout  time.Duration

	// Observability.
	LogLevel   slog.Level
	LogFormat  string
	HealthAddr string
	// LogPayloads mirrors full API request bodies into the debug log.
	LogPayloads bool
}

// Getenv mirrors os.Getenv and is injected for testability.
type Getenv func(string) string

// Load reads the configuration using the supplied lookup function. Pass
// os.Getenv in production code.
func Load(getenv Getenv) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	cfg := &Config{
		NPMURL:      strings.TrimRight(str(getenv, "", "NPM_URL", "NPM_BASE_URL"), "/"),
		NPMIdentity: str(getenv, "", "NPM_IDENTITY", "NPM_EMAIL", "NPM_USER"),
		DockerHost:  str(getenv, DefaultDockerHost, "DOCKER_HOST"),
		LabelPrefix: strings.TrimSuffix(str(getenv, DefaultLabelPrefix, "LABEL_PREFIX"), "."),
		NPMNetwork:  str(getenv, "", "NPM_NETWORK", "NPM_DOCKER_NETWORK"),
		LogFormat:   strings.ToLower(str(getenv, "text", "LOG_FORMAT")),
		HealthAddr:  str(getenv, "", "HEALTH_ADDR"),
	}

	secret, err := secretValue(getenv, "NPM_SECRET", "NPM_PASSWORD")
	if err != nil {
		return nil, err
	}
	cfg.NPMSecret = secret

	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg.NPMTimeout, err = duration(getenv, DefaultHTTPTimeout, "NPM_TIMEOUT")
	collect(err)
	cfg.DebounceInterval, err = duration(getenv, DefaultDebounceInterval, "DEBOUNCE_INTERVAL")
	collect(err)
	cfg.DebounceMaxWait, err = duration(getenv, DefaultDebounceMaxWait, "DEBOUNCE_MAX_WAIT")
	collect(err)
	cfg.ResyncInterval, err = duration(getenv, DefaultResyncInterval, "RESYNC_INTERVAL")
	collect(err)
	cfg.ShutdownTimeout, err = duration(getenv, DefaultHTTPTimeout, "SHUTDOWN_TIMEOUT")
	collect(err)

	cfg.NPMInsecureSkipVerify, err = boolean(getenv, false, "NPM_INSECURE_SKIP_VERIFY")
	collect(err)
	cfg.DeleteOrphans, err = boolean(getenv, true, "DELETE_ORPHANS")
	collect(err)
	cfg.AdoptExisting, err = boolean(getenv, true, "ADOPT_EXISTING")
	collect(err)
	cfg.DryRun, err = boolean(getenv, false, "DRY_RUN")
	collect(err)
	cfg.ResolveIP, err = boolean(getenv, true, "RESOLVE_CONTAINER_IP")
	collect(err)
	cfg.StrictNetwork, err = boolean(getenv, true, "NPM_NETWORK_STRICT")
	collect(err)
	cfg.LogPayloads, err = boolean(getenv, false, "LOG_PAYLOADS", "NPM_DEBUG_PAYLOADS")
	collect(err)

	cfg.NPMFlavour, err = npm.ParseFlavour(str(getenv, "", "NPM_FLAVOUR", "NPM_FLAVOR"))
	collect(err)

	cfg.Kinds, err = kinds(str(getenv, "", "SYNC_KINDS", "RESOURCE_KINDS"))
	collect(err)

	cfg.LogLevel, err = logLevel(str(getenv, "info", "LOG_LEVEL"))
	collect(err)

	collect(cfg.validate())

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var errs []error

	//nolint:staticcheck
	switch {
	case c.NPMURL == "":
		errs = append(errs, errors.New("NPM_URL is required (e.g. http://npm:81)"))
	default:
		u, err := url.Parse(c.NPMURL)
		//nolint:gocritic
		if err != nil {
			errs = append(errs, fmt.Errorf("NPM_URL is not a valid URL: %w", err))
		} else if u.Scheme == "" || u.Host == "" {
			errs = append(errs, fmt.Errorf("NPM_URL %q must be absolute, e.g. http://npm:81", c.NPMURL))
		} else if u.Scheme != "http" && u.Scheme != "https" {
			errs = append(errs, fmt.Errorf("NPM_URL must use http or https, got %q", u.Scheme))
		}
	}

	if c.NPMIdentity == "" {
		errs = append(errs, errors.New("NPM_IDENTITY is required (the NPM admin e-mail)"))
	}
	if c.NPMSecret == "" {
		errs = append(errs, errors.New("NPM_SECRET is required (use NPM_SECRET_FILE for Docker secrets)"))
	}

	switch {
	case strings.HasPrefix(c.DockerHost, "unix://"),
		strings.HasPrefix(c.DockerHost, "tcp://"),
		strings.HasPrefix(c.DockerHost, "npipe://"),
		strings.HasPrefix(c.DockerHost, "ssh://"):
	default:
		errs = append(errs, fmt.Errorf("DOCKER_HOST %q must start with unix://, tcp://, npipe:// or ssh://", c.DockerHost))
	}

	if c.LabelPrefix == "" {
		errs = append(errs, errors.New("LABEL_PREFIX must not be empty"))
	}
	if c.DebounceInterval <= 0 {
		errs = append(errs, errors.New("DEBOUNCE_INTERVAL must be greater than zero"))
	}
	if c.DebounceMaxWait > 0 && c.DebounceMaxWait < c.DebounceInterval {
		errs = append(errs, errors.New("DEBOUNCE_MAX_WAIT must be >= DEBOUNCE_INTERVAL"))
	}
	if len(c.Kinds) == 0 {
		errs = append(errs, errors.New("SYNC_KINDS must list at least one resource type"))
	}
	if c.LogFormat != "text" && c.LogFormat != "json" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be text or json, got %q", c.LogFormat))
	}

	return errors.Join(errs...)
}

// Redacted returns a copy that is safe to log.
func (c *Config) Redacted() Config {
	clone := *c
	if clone.NPMSecret != "" {
		clone.NPMSecret = "***"
	}
	return clone
}

// LogValue implements slog.LogValuer so the secret never reaches a log sink.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("npm_url", c.NPMURL),
		slog.String("npm_identity", c.NPMIdentity),
		slog.String("docker_host", c.DockerHost),
		slog.String("label_prefix", c.LabelPrefix),
		slog.String("resource_kinds", kindList(c.Kinds)),
		slog.String("npm_flavour", c.NPMFlavour.String()),
		slog.Bool("resolve_container_ip", c.ResolveIP),
		slog.String("npm_network", c.NPMNetwork),
		slog.Bool("npm_network_strict", c.NPMNetwork != "" && c.StrictNetwork),
		slog.Duration("debounce_interval", c.DebounceInterval),
		slog.Duration("resync_interval", c.ResyncInterval),
		slog.Bool("delete_orphans", c.DeleteOrphans),
		slog.Bool("adopt_existing", c.AdoptExisting),
		slog.Bool("dry_run", c.DryRun),
	)
}

// kinds parses the list of NPM resource types to manage. An empty value
// means all of them.
func kinds(raw string) ([]npm.Kind, error) {
	if strings.TrimSpace(raw) == "" {
		return npm.Kinds, nil
	}

	var (
		out  []npm.Kind
		seen = make(map[npm.Kind]struct{})
	)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		var kind npm.Kind
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "proxy", "proxies", "proxy-hosts", "proxy_hosts":
			kind = npm.KindProxy
		case "redirect", "redirects", "redirection", "redirection-hosts":
			kind = npm.KindRedirect
		case "stream", "streams":
			kind = npm.KindStream
		case "dead", "404", "dead-hosts", "dead_hosts":
			kind = npm.KindDead
		default:
			return nil, fmt.Errorf("SYNC_KINDS: unknown resource type %q (want proxy, redirect, stream or 404)", part)
		}
		if _, dup := seen[kind]; dup {
			continue
		}
		seen[kind] = struct{}{}
		out = append(out, kind)
	}
	return out, nil
}

// kindList renders the kinds for logging.
func kindList(list []npm.Kind) string {
	names := make([]string, 0, len(list))
	for _, k := range list {
		names = append(names, string(k))
	}
	return strings.Join(names, ",")
}

// str returns the first non-empty value of the given keys.
func str(getenv Getenv, fallback string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
	}
	return fallback
}

// secretValue supports the "<KEY>_FILE" convention used by Docker/Podman
// secrets so credentials never have to live in the environment.
func secretValue(getenv Getenv, keys ...string) (string, error) {
	for _, key := range keys {
		if path := strings.TrimSpace(getenv(key + "_FILE")); path != "" {
			//nolint:gosec
			raw, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read %s_FILE: %w", key, err)
			}
			return strings.TrimSpace(string(raw)), nil
		}
		if v := getenv(key); v != "" {
			return v, nil
		}
	}
	return "", nil
}

func duration(getenv Getenv, fallback time.Duration, key string) (time.Duration, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return fallback, nil
	}
	// Bare numbers are interpreted as seconds for convenience.
	if n, err := strconv.Atoi(raw); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: invalid duration %q", key, raw)
	}
	return d, nil
}

// boolean returns the value of the first key that is set.
func boolean(getenv Getenv, fallback bool, keys ...string) (bool, error) {
	for _, key := range keys {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			continue
		}
		b, err := ParseBool(raw)
		if err != nil {
			return fallback, fmt.Errorf("%s: %w", key, err)
		}
		return b, nil
	}
	return fallback, nil
}

// ParseBool accepts the usual strconv values plus the human friendly
// yes/no/on/off spellings commonly used in Docker labels.
func ParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on", "enable", "enabled":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", raw)
	}
}

func logLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL: unknown level %q", raw)
	}
}
