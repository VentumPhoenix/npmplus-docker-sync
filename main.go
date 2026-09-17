// Command npmplus-docker-sync keeps Nginx Proxy Manager / NPMplus routing
// resources in sync with the labels of running Docker containers.
//
// It is a single static binary with no runtime dependencies: point it at a
// Docker endpoint (ideally a docker-socket-proxy) and an NPM API, label your
// containers, and proxy hosts, redirections, streams and 404 hosts appear,
// change and disappear with them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/config"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/syncer"
)

// version is overwritten at build time via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// eventBuffer is how many Docker events may queue up before the listener
// blocks. Large enough for a `docker compose up` of a big stack.
const eventBuffer = 256

func main() {
	mode := modeDaemon
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Printf("npmplus-docker-sync %s (commit %s, built %s)\n", version, commit, date)
			return
		case "-h", "--help", "help":
			usage()
			return
		case "healthcheck":
			os.Exit(healthcheck(os.Getenv("HEALTH_ADDR")))
		case "validate":
			// Reads labels and reports what they would produce. Never writes,
			// and never even needs the NPM API, so it can run in CI. With a
			// file argument it does not even need Docker.
			mode = modeValidate
			if len(os.Args) > 2 {
				validateTarget = os.Args[2]
			}
		case "sync", "once", "--once":
			// One reconcile, then exit with a status a pipeline can read.
			mode = modeOnce
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
			usage()
			os.Exit(2)
		}
	}
	if mode == modeOnce && len(os.Args) > 2 && os.Args[2] != "--once" { //nolint:staticcheck // explicit is clearer here
		fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[2])
		os.Exit(2)
	}

	if err := run(mode); err != nil {
		slog.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// validateTarget is the rendered compose configuration `validate` was pointed
// at, empty when it should read the running containers instead.
var validateTarget string

// runMode is what this process was asked to do.
type runMode int

const (
	// modeDaemon watches Docker until it is told to stop.
	modeDaemon runMode = iota
	// modeOnce performs a single reconcile and exits.
	modeOnce
	// modeValidate parses the labels and reports, without touching NPM.
	modeValidate
)

func run(mode runMode) error {
	load := config.Load
	if mode == modeValidate {
		load = config.LoadForCheck
	}
	cfg, err := load(os.Getenv, os.Environ())
	if err != nil {
		return fmt.Errorf("configuration error:\n%w", err)
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting npmplus-docker-sync",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.Any("config", cfg))
	for _, notice := range cfg.Notices {
		log.Info(notice)
	}
	for _, warning := range cfg.Warnings {
		log.Warn(warning)
	}
	logDefaults(log, cfg)

	// Graceful shutdown: SIGINT/SIGTERM cancel the root context, every
	// component drains, and the worker flushes its state to the API.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// `validate <file>` works entirely offline: no Docker, no NPM.
	if mode == modeValidate && validateTarget != "" {
		return validateFile(validateTarget, docker.ParseOptions{
			Prefix:           cfg.LabelPrefix,
			Defaults:         cfg.Defaults,
			ExposedByDefault: cfg.ExposedByDefault,
			StrictLabels:     cfg.StrictLabels,
			PortPreference:   cfg.PortPreference,
		}, log)
	}

	dockerClient, err := docker.NewClient(cfg.DockerHost)
	if err != nil {
		return err
	}
	defer func() { _ = dockerClient.Close() }()

	parseOpts := docker.ParseOptions{
		Prefix:           cfg.LabelPrefix,
		ResolveIP:        cfg.ResolveIP,
		Defaults:         cfg.Defaults,
		ExposedByDefault: cfg.ExposedByDefault,
		StrictLabels:     cfg.StrictLabels,
		PortPreference:   cfg.PortPreference,
	}

	listener := docker.NewListener(dockerClient, parseOpts, log.With(slog.String("component", "docker")))
	if err := listener.Ping(ctx); err != nil {
		return fmt.Errorf("cannot reach docker at %s: %w", cfg.DockerHost, err)
	}
	log.Info("connected to docker", slog.String("host", cfg.DockerHost))

	// Which network an upstream address is taken from decides whether NPM can
	// reach it at all. When the operator named one, that is the only valid
	// answer: anything else is an address on a network NPM does not share, and
	// a proxy host pointing there is worse than none. Without NPM_NETWORK the
	// networks this container is on are the best available guess.
	ownNetworks := listener.DiscoverOwnNetworks(ctx) // also learns our container id
	switch {
	case cfg.NPMNetwork != "":
		parseOpts.PreferNetworks = []string{cfg.NPMNetwork}
		parseOpts.StrictNetworks = cfg.StrictNetwork
	case cfg.NPMContainer != "":
		// Redth's NPM_CONTAINER_NAME: whatever networks NPM itself is on are
		// the ones an upstream address has to come from.
		npmNetworks := listener.NetworksOfContainer(ctx, cfg.NPMContainer)
		parseOpts.PreferNetworks = docker.DedupeNetworks(append(npmNetworks, ownNetworks...))
		parseOpts.StrictNetworks = len(npmNetworks) > 0 && cfg.StrictNetwork
	default:
		parseOpts.PreferNetworks = docker.DedupeNetworks(ownNetworks)
	}
	selfID := listener.SelfID()
	listener = docker.NewListener(dockerClient, parseOpts, log.With(slog.String("component", "docker")))
	listener.SetSelfID(selfID)
	if summary, err := listener.Summarize(ctx); err == nil {
		log.Info("container overview",
			slog.Int("managed", summary.Managed),
			slog.Int("stopped", summary.Stopped),
			slog.Int("opted_out", summary.OptedOut),
			slog.Int("without_labels", summary.Unlabeled))
	}

	// validate never touches NPM: it is the pre-deploy check, and a broken
	// API must not make it fail.
	if mode == modeValidate {
		return validate(ctx, listener)
	}

	// Several sync instances may share one NPM. The daemon id is stable per
	// Docker host and needs no configuration; SYNC_INSTANCE_ID overrides it.
	instanceID := cfg.InstanceID
	if instanceID == "" {
		instanceID = listener.DaemonID(ctx)
	}
	if instanceID == "" {
		// Without an identity every resource carrying our marker is ours,
		// which is right for a single instance. Two instances against one NPM
		// would fight over each other's hosts, so say so once.
		log.Info("no sync instance id (docker /info is not available); " +
			"managing every resource with our marker. Set SYNC_INSTANCE_ID when " +
			"several instances share one npm, or allow INFO on the socket proxy")
	} else {
		log.Info("sync instance", slog.String("id", instanceID))
	}
	log.Debug("upstream host resolution",
		slog.Bool("resolve_ip", cfg.ResolveIP),
		slog.Bool("strict_networks", parseOpts.StrictNetworks),
		slog.String("preferred_networks", strings.Join(parseOpts.PreferNetworks, ",")))

	npmClient, err := npm.New(cfg.NPMURL, cfg.NPMIdentity, cfg.NPMSecret,
		npm.WithTimeout(cfg.NPMTimeout),
		npm.WithInsecureSkipVerify(cfg.NPMInsecureSkipVerify),
		npm.WithUserAgent("npmplus-docker-sync/"+version),
		npm.WithLogger(log.With(slog.String("component", "npm"))),
		npm.WithFlavour(cfg.NPMFlavour),
		npm.WithPayloadLogging(cfg.LogPayloads),
	)
	if err != nil {
		return err
	}
	if err := loginWithRetry(ctx, npmClient, log, time.Second); err != nil {
		return err
	}
	// NPM and NPMplus disagree about which properties a write payload may
	// carry, and both reject the other dialect outright, so settle this before
	// the first reconcile rather than on every failing request.
	if _, err := npmClient.DetectFlavour(ctx); err != nil {
		return fmt.Errorf("cannot determine the npm api flavour (pin it with NPM_FLAVOUR): %w", err)
	}

	worker := syncer.NewWorker(npmClient, listener, syncer.Options{
		Kinds:                 cfg.Kinds,
		DeleteOrphans:         cfg.DeleteOrphans,
		AdoptExisting:         cfg.AdoptExisting,
		DryRun:                cfg.DryRun,
		ResyncInterval:        cfg.ResyncInterval,
		ShutdownTimeout:       cfg.ShutdownTimeout,
		CertificatePartial:    cfg.CertificatePartial,
		CertificateAutoCreate: cfg.CertificateAutoCreate,
		CertificatePoll:       cfg.CertificatePoll,
		MigrateFromRedth:      cfg.MigrateFromRedth,
		OnStop:                cfg.OnStop,
		StopGrace:             cfg.StopGrace,
		InstanceID:            instanceID,
		LabelPrefix:           cfg.LabelPrefix,
		DeleteGuard:           cfg.DeleteGuard,
		DeleteGuardMin:        cfg.DeleteGuardMin,
	}, log.With(slog.String("component", "sync")))

	// One reconcile, then exit: the form a pipeline or a test harness wants.
	if mode == modeOnce {
		res, err := worker.Reconcile(ctx)
		log.Info("single reconcile finished", slog.Any("result", res))
		return err
	}

	events := make(chan docker.Event, eventBuffer)
	batches := make(chan []docker.Event, 1)
	debouncer := &syncer.Debouncer[docker.Event]{
		Interval: cfg.DebounceInterval,
		MaxWait:  cfg.DebounceMaxWait,
	}

	var wg sync.WaitGroup
	var workerErr error

	wg.Add(1)
	go func() {
		defer wg.Done()
		workerErr = worker.Run(ctx, batches)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		debouncer.Run(ctx, events, batches)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(events)
		if err := listener.Watch(ctx, events); err != nil {
			log.Error("docker event listener stopped", slog.String("error", err.Error()))
		}
	}()

	shutdownHealth := startHealthServer(cfg, worker, instanceID, log)

	<-ctx.Done()
	log.Info("shutdown signal received, draining")
	stop() // restore default signal handling: a second Ctrl-C kills instantly

	wg.Wait()
	shutdownHealth()
	log.Info("goodbye")
	return workerErr
}

// validate parses the labels of the running containers and reports what they
// would produce. It writes nothing and never contacts NPM, so it can run
// before a deploy - or in CI against a compose stack that was just started.
func validate(ctx context.Context, listener *docker.Listener) error {
	containers, err := listener.Containers(ctx)
	if err != nil {
		return err
	}
	return report(docker.Scan(containers, listener.Options()))
}

// loginClient is the slice of the NPM client the login retry needs, so the
// wait loop can be tested without a server that sleeps.
type loginClient interface {
	Login(ctx context.Context) error
	BaseURL() string
	AuthMode() npm.AuthMode
}

// loginWithRetry waits for NPM to become available; the container is often
// started in parallel with NPM itself.
func loginWithRetry(ctx context.Context, client loginClient, log *slog.Logger, backoff time.Duration) error {
	const maxAttempts = 10
	if backoff <= 0 {
		backoff = time.Second
	}

	for attempt := 1; ; attempt++ {
		err := client.Login(ctx)
		if err == nil {
			log.Info("npm api ready",
				slog.String("url", client.BaseURL()),
				slog.String("auth_mode", client.AuthMode().String()))
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt >= maxAttempts {
			return fmt.Errorf("login to %s failed after %d attempts: %w", client.BaseURL(), attempt, err)
		}
		log.Warn("npm login failed, retrying",
			slog.String("error", err.Error()),
			slog.Int("attempt", attempt),
			slog.Duration("retry_in", backoff))

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// startHealthServer exposes /healthz, /readyz, /status and /metrics when
// HEALTH_ADDR is set. It returns a shutdown function.
func startHealthServer(cfg *config.Config, worker *syncer.Worker, instance string, log *slog.Logger) func() {
	if cfg.HealthAddr == "" {
		return func() {}
	}

	srv := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           healthMux(cfg, worker, instance, log),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("health endpoint listening", slog.String("addr", cfg.HealthAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health endpoint failed", slog.String("error", err.Error()))
		}
	}()

	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

// healthMux builds the observability endpoints. It is separate from the server
// so the handlers can be exercised without binding a port.
func healthMux(cfg *config.Config, worker *syncer.Worker, instance string, log *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	// Readiness tracks the dependencies, not the workload: a single resource
	// NPM rejected (a typo in one label, say) leaves every other host in sync,
	// and restarting the container would not fix it. Those are reported as a
	// counter in the body instead. A Docker event stream that has been down
	// for minutes *is* a readiness problem: from then on the tool only reacts
	// on the periodic resync.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		status := worker.Status()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		down := status.Stream.Down(time.Now())
		switch {
		case status.LastRun.IsZero():
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no reconciliation yet\n"))
		case status.Err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "docker or the npm api is unreachable: %v\n", status.Err)
		case down > syncer.StreamDownGrace:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "docker event stream has been down for %s: %s\n",
				down.Round(time.Second), status.Stream.LastError)
		default:
			_, _ = fmt.Fprintf(w, "ok: %d managed hosts, %d failed, last sync %s\n",
				status.Managed, status.Failed, status.LastRun.UTC().Format(time.RFC3339))
			if status.Blocked != "" {
				_, _ = fmt.Fprintf(w, "deletions blocked: %s\n", status.Blocked)
			}
		}
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(statusDocument(worker, cfg, instance)); err != nil {
			log.Debug("status response failed", slog.String("error", err.Error()))
		}
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(renderMetrics(worker.Metrics(), worker.Status())))
	})

	return mux
}

// statusReport is the JSON document served by /status.
type statusReport struct {
	Version   string                  `json:"version"`
	Instance  string                  `json:"instance"`
	DryRun    bool                    `json:"dry_run"`
	LastRun   *time.Time              `json:"last_run,omitempty"`
	Managed   int                     `json:"managed"`
	Failed    int                     `json:"failed"`
	Error     string                  `json:"error,omitempty"`
	Blocked   string                  `json:"deletions_blocked,omitempty"`
	Stream    streamReport            `json:"event_stream"`
	Resources []syncer.ResourceStatus `json:"resources"`
}

// streamReport describes the Docker event subscription.
type streamReport struct {
	Connected bool   `json:"connected"`
	Since     string `json:"since,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// statusDocument assembles the /status payload.
func statusDocument(worker *syncer.Worker, cfg *config.Config, instance string) statusReport {
	status := worker.Status()
	report := statusReport{
		Version:   version,
		Instance:  instance,
		DryRun:    cfg.DryRun,
		Managed:   status.Managed,
		Failed:    status.Failed,
		Blocked:   status.Blocked,
		Resources: worker.Resources(),
		Stream: streamReport{
			Connected: status.Stream.Connected,
			LastError: status.Stream.LastError,
		},
	}
	if !status.LastRun.IsZero() {
		last := status.LastRun.UTC()
		report.LastRun = &last
	}
	if !status.Stream.Since.IsZero() {
		report.Stream.Since = status.Stream.Since.UTC().Format(time.RFC3339)
	}
	if status.Err != nil {
		report.Error = status.Err.Error()
	}
	if report.Resources == nil {
		report.Resources = []syncer.ResourceStatus{}
	}
	return report
}

// renderMetrics formats the worker counters as a Prometheus exposition.
func renderMetrics(m syncer.Metrics, status syncer.Status) string {
	var sb strings.Builder
	counter := func(name, help string, value uint64) {
		fmt.Fprintf(&sb, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
	}
	gauge := func(name, help string, value float64) {
		fmt.Fprintf(&sb, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, value)
	}

	counter("npmsync_reconcile_runs_total", "Reconcile runs performed.", m.Runs)
	counter("npmsync_reconcile_errors_total", "Reconcile runs that reported an error.", m.RunErrors)
	counter("npmsync_resources_created_total", "Resources created in NPM.", m.Created)
	counter("npmsync_resources_updated_total", "Resources updated in NPM.", m.Updated)
	counter("npmsync_resources_deleted_total", "Resources deleted from NPM.", m.Deleted)
	counter("npmsync_resources_failed_total", "Resource operations the API rejected.", m.Failed)
	counter("npmsync_resources_skipped_total", "Resources skipped (not ours, in backoff, protected).", m.Skipped)
	counter("npmsync_deletions_blocked_total", "Deletions the safety guard refused.", m.DeletionsBlocked)

	gauge("npmsync_managed_resources", "Resources currently under management.", float64(m.Managed))
	gauge("npmsync_event_stream_connected", "1 when the Docker event stream is subscribed.",
		boolGauge(status.Stream.Connected))
	gauge("npmsync_last_run_duration_seconds", "Duration of the most recent reconcile run.",
		m.LastDuration.Seconds())
	if !m.LastRun.IsZero() {
		gauge("npmsync_last_run_timestamp_seconds", "Unix time of the most recent reconcile run.",
			float64(m.LastRun.Unix()))
	}

	sb.WriteString("# HELP npmsync_managed_resources_by_kind Resources under management per collection.\n")
	sb.WriteString("# TYPE npmsync_managed_resources_by_kind gauge\n")
	for _, kind := range npm.Kinds {
		fmt.Fprintf(&sb, "npmsync_managed_resources_by_kind{kind=%q} %d\n", kind, m.ManagedByKind[kind])
	}

	sb.WriteString("# HELP npmsync_certificate_selections_total Certificates chosen, by match class.\n")
	sb.WriteString("# TYPE npmsync_certificate_selections_total counter\n")
	classes := make([]string, 0, len(m.CertificateClass))
	for class := range m.CertificateClass {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		fmt.Fprintf(&sb, "npmsync_certificate_selections_total{match=%q} %d\n", class, m.CertificateClass[class])
	}
	return sb.String()
}

func boolGauge(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// healthcheck is the container HEALTHCHECK probe: the runtime image is built
// FROM scratch, so there is no shell or curl to call the endpoint with. It
// returns 0 when HEALTH_ADDR is unset (nothing to probe) or /readyz answers
// 200, and 1 otherwise. Output goes to stdout so `docker inspect` shows it.
func healthcheck(addr string) int {
	if addr == "" {
		fmt.Println("HEALTH_ADDR not set, nothing to probe")
		return 0
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Printf("invalid HEALTH_ADDR %q: %v\n", addr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort(host, port) + "/readyz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil) // #nosec G704
	if err != nil {
		fmt.Println(err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req) // #nosec G704
	if err != nil {
		fmt.Println(err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("%s %s", resp.Status, body)
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// logDefaults reports the field defaults the environment changed, so the
// effective configuration of a run is visible without reading the code.
func logDefaults(log *slog.Logger, cfg *config.Config) {
	changed := cfg.Defaults.FromEnv()
	if len(changed) == 0 {
		log.Debug("field defaults: all built-in")
		return
	}
	for _, entry := range changed {
		log.Info("field default from environment",
			slog.String("kind", string(entry.Kind)),
			slog.String("field", entry.Field),
			slog.String("value", entry.Value),
			slog.String("source", entry.Source))
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func usage() {
	fmt.Print(`npmplus-docker-sync - sync Docker container labels to Nginx Proxy Manager / NPMplus

Manages proxy hosts, redirection hosts, streams and 404 hosts from container
labels such as npm.proxy.domains, npm.proxy.1.domains, npm.redirect.domains or
npm.stream.incoming_port. One label is usually enough:

  labels:
    npm.proxy.domains: "app.example.com"

Exclude a container with npm.enable=false.

Usage:
  npmplus-docker-sync            run the sync daemon (configured via environment)
  npmplus-docker-sync sync       perform one reconcile and exit
  npmplus-docker-sync validate [file]
                                 parse the labels of the running containers -
                                 or of a rendered compose configuration
                                 (docker compose config --format json) - and
                                 report what they would produce. Writes
                                 nothing, never contacts NPM, exits non-zero
                                 on an invalid label set.
  npmplus-docker-sync version    print version information
  npmplus-docker-sync healthcheck
                                 probe HEALTH_ADDR/readyz; exit 0 when ready
                                 (used by the container HEALTHCHECK)

Required environment:
  NPM_URL         base URL of the NPM/NPMplus API, e.g. http://npm:81
  NPM_IDENTITY    admin e-mail
  NPM_SECRET      admin password (or NPM_SECRET_FILE for Docker secrets)

Optional environment:
  NPM_FLAVOUR            api dialect: auto (default), npmplus or npm
  NPM_EXPOSED_BY_DEFAULT manage every labelled container (default true)
  NPM_CONTAINER_NAME     name of the npm container, for network discovery
  NPM_PORT_PREFERENCE    order of exposed ports to pick from (default 80,8080,3000,8000,443)
  NPM_DEFAULT_CERTIFICATE / NPM_<KIND>_<FIELD>
                         global defaults for any label field, e.g.
                         NPM_PROXY_SSL_FORCE, NPM_PROXY_WEBSOCKETS
  NPM_CERTIFICATE_PARTIAL   primary (default) or none, when no certificate
                         covers every domain of a host
  NPM_CERTIFICATE_AUTO_CREATE  request a certificate when none matches (default false)
  CERTIFICATE_POLL_INTERVAL    how often to look for new certificates (default 1m)
  MIGRATE_FROM_REDTH     take over hosts created by npm-docker-sync (default false)
  NPM_ON_STOP            stopped container: disable (default), keep or delete
  NPM_STOP_GRACE         ignore a stopped container for this long (default 1m)
  SYNC_INSTANCE_ID       only manage the resources of this instance
  DELETE_GUARD           refuse a run deleting more than this share (default 0.5)
  STRICT_LABELS          skip a resource that carries an unknown label (default false)
  DOCKER_HOST            unix:///var/run/docker.sock (default) or tcp://docker-socket-proxy:2375
  LABEL_PREFIX           label namespace (default "npm")
  SYNC_KINDS             resource types to manage (default proxy,redirect,stream,404)
  RESOLVE_CONTAINER_IP   use the container IP as upstream host (default true)
  NPM_NETWORK            the docker network that IP is taken from
  NPM_NETWORK_STRICT     skip containers that are not on NPM_NETWORK (default true)
  DEBOUNCE_INTERVAL      quiet period after an event burst (default 3s)
  DEBOUNCE_MAX_WAIT      hard cap for a burst (default 30s)
  RESYNC_INTERVAL        periodic full reconcile (default 5m, 0 disables)
  DELETE_ORPHANS         delete hosts whose container is gone (default true)
  ADOPT_EXISTING         take over pre-existing hosts for a labelled domain (default true)
  DRY_RUN                log intended changes without calling the API (default false)
  HEALTH_ADDR            expose /healthz and /readyz, e.g. :8080
  LOG_LEVEL              debug|info|warn|error (default info)
  LOG_FORMAT             text|json (default text)
  LOG_PAYLOADS           log full API request bodies at debug level (default false)

See https://github.com/VentumPhoenix/npmplus-docker-sync for the full documentation.
`)
}
