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

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
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
	// DefaultCertificatePoll is how often the certificate list is checked for
	// changes, so a certificate created in the UI reaches its hosts without a
	// restart.
	DefaultCertificatePoll = time.Minute
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
	StrictNetwork bool
	// NPMContainer is the name of the NPM/NPMplus container. When set, its
	// networks are used for upstream resolution (Redth: NPM_CONTAINER_NAME).
	NPMContainer string
	// ExposedByDefault manages every container carrying labels of the
	// namespace, without requiring npm.enable=true.
	ExposedByDefault bool
	// StrictLabels skips a resource whose labels contain an unknown field.
	StrictLabels bool
	// PortPreference orders the exposed ports when a container has several.
	PortPreference []int
	// Defaults are the effective per-field defaults from the environment.
	Defaults *fields.Defaults
	// CertificatePartial decides what happens when no certificate covers
	// every domain of a host.
	CertificatePartial certs.Partial
	// CertificateAutoCreate requests a Let's Encrypt certificate when the
	// automatic selection finds nothing.
	CertificateAutoCreate bool
	// CertificatePoll is how often the certificate list is polled.
	CertificatePoll time.Duration
	// MigrateFromRedth takes over hosts created by Redth/npm-docker-sync.
	MigrateFromRedth bool
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

	// Warnings are non-fatal configuration problems, reported once at start.
	Warnings []string
	// Notices record which alias spelling of a variable was used, so a
	// configuration copied from another project is traceable in the log.
	Notices []string
}

// Getenv mirrors os.Getenv and is injected for testability.
type Getenv func(string) string

// Load reads the configuration using the supplied lookup function and the
// process environment. Pass os.Getenv and os.Environ() in production code;
// environ is only read to warn about misspelled NPM_<KIND>_<FIELD> variables.
func Load(getenv Getenv, environ []string) (*Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	// Every variable may also be supplied as <NAME>_FILE, which is how
	// Docker and Podman secrets are mounted.
	getenv = fileAware(getenv)

	var notices []string
	// alias resolves the first variable that is set and records when it was
	// not the canonical one.
	alias := func(fallback string, keys ...string) string {
		for i, key := range keys {
			v := strings.TrimSpace(getenv(key))
			if v == "" {
				continue
			}
			if i > 0 {
				notices = append(notices,
					fmt.Sprintf("%s is an accepted alias for %s", key, keys[0]))
			}
			return v
		}
		return fallback
	}

	cfg := &Config{
		NPMURL:      strings.TrimRight(alias("", "NPM_URL", "NPM_BASE_URL"), "/"),
		NPMIdentity: alias("", "NPM_IDENTITY", "NPM_EMAIL", "NPM_USER"),
		DockerHost:  str(getenv, DefaultDockerHost, "DOCKER_HOST"),
		LabelPrefix: strings.TrimSuffix(str(getenv, DefaultLabelPrefix, "LABEL_PREFIX"), "."),
		NPMNetwork:  alias("", "NPM_NETWORK", "NPM_DOCKER_NETWORK"),
		LogFormat:   strings.ToLower(str(getenv, "text", "LOG_FORMAT")),
		HealthAddr:  str(getenv, "", "HEALTH_ADDR"),
		// Redth spells it NPM_CONTAINER_NAME.
		NPMContainer: alias("", "NPM_CONTAINER", "NPM_CONTAINER_NAME"),
	}

	secret, err := secretValue(getenv, "NPM_SECRET", "NPM_PASSWORD")
	if err != nil {
		return nil, err
	}
	cfg.NPMSecret = secret
	if secret != "" && strings.TrimSpace(getenv("NPM_SECRET")) == "" && strings.TrimSpace(getenv("NPM_SECRET_FILE")) == "" {
		notices = append(notices, "NPM_PASSWORD is an accepted alias for NPM_SECRET")
	}

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

	cfg.ExposedByDefault, err = boolean(getenv, true, "NPM_EXPOSED_BY_DEFAULT", "EXPOSED_BY_DEFAULT")
	collect(err)
	cfg.StrictLabels, err = boolean(getenv, false, "STRICT_LABELS", "NPM_STRICT_LABELS")
	collect(err)
	cfg.MigrateFromRedth, err = boolean(getenv, false, "MIGRATE_FROM_REDTH")
	collect(err)
	cfg.CertificateAutoCreate, err = boolean(getenv, false, "NPM_CERTIFICATE_AUTO_CREATE")
	collect(err)
	cfg.CertificatePoll, err = duration(getenv, DefaultCertificatePoll, "CERTIFICATE_POLL_INTERVAL")
	collect(err)
	cfg.CertificatePartial, err = certificatePartial(getenv)
	collect(err)
	cfg.PortPreference, err = portPreference(str(getenv, "", "NPM_PORT_PREFERENCE"))
	collect(err)

	// The per-field defaults (NPM_PROXY_*, NPM_DEFAULT_*) are resolved from
	// the same table the labels are parsed with, so the two cannot drift.
	defaults, warnings, defaultsErr := fields.LoadDefaults(getenv, environ)
	cfg.Defaults = defaults
	collect(defaultsErr)
	for _, name := range warnings {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("%s does not name a known field and is ignored", name))
	}

	cfg.Notices = notices
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
		slog.Bool("exposed_by_default", c.ExposedByDefault),
		slog.Bool("strict_labels", c.StrictLabels),
		slog.String("certificate_partial", string(c.CertificatePartial)),
		slog.Bool("certificate_auto_create", c.CertificateAutoCreate),
		slog.Duration("certificate_poll", c.CertificatePoll),
		slog.Bool("migrate_from_redth", c.MigrateFromRedth),
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
func ParseBool(raw string) (bool, error) { return fields.ParseBool(raw) }

// fileAware wraps a lookup so that <NAME>_FILE is read from disk when it is
// set. Docker and Podman secrets are mounted as files, and a password that
// never enters the environment cannot leak through `docker inspect`.
func fileAware(getenv Getenv) Getenv {
	return func(key string) string {
		if strings.HasSuffix(key, "_FILE") {
			return getenv(key)
		}
		if path := strings.TrimSpace(getenv(key + "_FILE")); path != "" {
			//nolint:gosec // the path is operator supplied configuration
			if raw, err := os.ReadFile(path); err == nil {
				return strings.TrimSpace(string(raw))
			}
		}
		return getenv(key)
	}
}

// certificatePartial reads NPM_CERTIFICATE_PARTIAL.
func certificatePartial(getenv Getenv) (certs.Partial, error) {
	partial, err := certs.ParsePartial(str(getenv, "", "NPM_CERTIFICATE_PARTIAL"))
	if err != nil {
		return partial, fmt.Errorf("NPM_CERTIFICATE_PARTIAL: %w", err)
	}
	return partial, nil
}

// portPreference parses NPM_PORT_PREFERENCE, the order in which an exposed
// container port is picked when there is more than one.
func portPreference(raw string) ([]int, error) {
	if strings.TrimSpace(raw) == "" {
		return fields.DefaultPortPreference, nil
	}
	var out []int
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("NPM_PORT_PREFERENCE: %q is not a port number", part)
		}
		if n < 1 || n > 65535 {
			return nil, fmt.Errorf("NPM_PORT_PREFERENCE: %d is out of range (1-65535)", n)
		}
		out = append(out, n)
	}
	return out, nil
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
