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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
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
		}
	}

	if err := run(); err != nil {
		slog.Error("fatal", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Getenv, os.Environ())
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
			slog.Int("opted_out", summary.OptedOut),
			slog.Int("without_labels", summary.Unlabeled))
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
	if err := loginWithRetry(ctx, npmClient, log); err != nil {
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
	}, log.With(slog.String("component", "sync")))

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

	shutdownHealth := startHealthServer(cfg, worker, log)

	<-ctx.Done()
	log.Info("shutdown signal received, draining")
	stop() // restore default signal handling: a second Ctrl-C kills instantly

	wg.Wait()
	shutdownHealth()
	log.Info("goodbye")
	return workerErr
}

// loginWithRetry waits for NPM to become available; the container is often
// started in parallel with NPM itself.
func loginWithRetry(ctx context.Context, client *npm.Client, log *slog.Logger) error {
	const maxAttempts = 10
	backoff := time.Second

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

// startHealthServer exposes /healthz and /readyz when HEALTH_ADDR is set.
// It returns a shutdown function.
func startHealthServer(cfg *config.Config, worker *syncer.Worker, log *slog.Logger) func() {
	if cfg.HealthAddr == "" {
		return func() {}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	// Readiness tracks the dependencies, not the workload: a single resource
	// NPM rejected (a typo in one label, say) leaves every other host in sync,
	// and restarting the container would not fix it. Those are reported as a
	// counter in the body instead.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		status := worker.Status()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		switch {
		case status.LastRun.IsZero():
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no reconciliation yet\n"))
		case status.Err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "docker or the npm api is unreachable: %v\n", status.Err)
		default:
			_, _ = fmt.Fprintf(w, "ok: %d managed hosts, %d failed, last sync %s\n",
				status.Managed, status.Failed, status.LastRun.UTC().Format(time.RFC3339))
		}
	})

	srv := &http.Server{
		Addr:              cfg.HealthAddr,
		Handler:           mux,
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
