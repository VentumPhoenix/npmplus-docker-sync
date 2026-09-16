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
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return fmt.Errorf("configuration error:\n%w", err)
	}

	log := newLogger(cfg)
	slog.SetDefault(log)
	log.Info("starting npmplus-docker-sync",
		slog.String("version", version),
		slog.String("commit", commit),
		slog.Any("config", cfg))

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
		Prefix:    cfg.LabelPrefix,
		ResolveIP: cfg.ResolveIP,
	}
	if cfg.NPMNetwork != "" {
		parseOpts.PreferNetworks = append(parseOpts.PreferNetworks, cfg.NPMNetwork)
	}

	listener := docker.NewListener(dockerClient, parseOpts, log.With(slog.String("component", "docker")))
	if err := listener.Ping(ctx); err != nil {
		return fmt.Errorf("cannot reach docker at %s: %w", cfg.DockerHost, err)
	}
	log.Info("connected to docker", slog.String("host", cfg.DockerHost))

	// Upstream IPs are preferred from networks this container shares with the
	// target, because those are the ones NPM can reach too.
	if cfg.ResolveIP {
		parseOpts.PreferNetworks = append(parseOpts.PreferNetworks, listener.DiscoverOwnNetworks(ctx)...)
		listener = docker.NewListener(dockerClient, parseOpts, log.With(slog.String("component", "docker")))
		log.Debug("upstream host resolution",
			slog.Bool("resolve_ip", cfg.ResolveIP),
			slog.String("preferred_networks", strings.Join(parseOpts.PreferNetworks, ",")))
	}

	npmClient, err := npm.New(cfg.NPMURL, cfg.NPMIdentity, cfg.NPMSecret,
		npm.WithTimeout(cfg.NPMTimeout),
		npm.WithInsecureSkipVerify(cfg.NPMInsecureSkipVerify),
		npm.WithUserAgent("npmplus-docker-sync/"+version),
		npm.WithLogger(log.With(slog.String("component", "npm"))),
	)
	if err != nil {
		return err
	}
	if err := loginWithRetry(ctx, npmClient, log); err != nil {
		return err
	}

	worker := syncer.NewWorker(npmClient, listener, syncer.Options{
		Kinds:           cfg.Kinds,
		DeleteOrphans:   cfg.DeleteOrphans,
		AdoptExisting:   cfg.AdoptExisting,
		DryRun:          cfg.DryRun,
		ResyncInterval:  cfg.ResyncInterval,
		ShutdownTimeout: cfg.ShutdownTimeout,
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
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		lastRun, managed, err := worker.Status()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		switch {
		case lastRun.IsZero():
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("no reconciliation yet\n"))
		case err != nil:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = fmt.Fprintf(w, "last reconciliation failed: %v\n", err)
		default:
			_, _ = fmt.Fprintf(w, "ok: %d managed hosts, last sync %s\n", managed, lastRun.UTC().Format(time.RFC3339))
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
labels such as npm.enable, npm.proxy.host, npm.1.redirect.host or
npm.2.stream.incoming_port.

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
  DOCKER_HOST            unix:///var/run/docker.sock (default) or tcp://docker-socket-proxy:2375
  LABEL_PREFIX           label namespace (default "npm")
  SYNC_KINDS             resource types to manage (default proxy,redirect,stream,404)
  RESOLVE_CONTAINER_IP   use the container IP as upstream host (default true)
  NPM_NETWORK            docker network to prefer when resolving that IP
  DEBOUNCE_INTERVAL      quiet period after an event burst (default 3s)
  DEBOUNCE_MAX_WAIT      hard cap for a burst (default 30s)
  RESYNC_INTERVAL        periodic full reconcile (default 5m, 0 disables)
  DELETE_ORPHANS         delete hosts whose container is gone (default true)
  ADOPT_EXISTING         take over pre-existing hosts for a labelled domain (default true)
  DRY_RUN                log intended changes without calling the API (default false)
  HEALTH_ADDR            expose /healthz and /readyz, e.g. :8080
  LOG_LEVEL              debug|info|warn|error (default info)
  LOG_FORMAT             text|json (default text)

See https://github.com/VentumPhoenix/npmplus-docker-sync for the full documentation.
`)
}
