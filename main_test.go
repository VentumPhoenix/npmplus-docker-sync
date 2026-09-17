package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/config"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/syncer"
)

// TestHealthcheck covers the container HEALTHCHECK probe. The runtime image is
// FROM scratch, so this subcommand is the only way the health endpoint is ever
// queried from inside the container - a regression here shows up as a
// permanently unhealthy container, not as a failed build.
func TestHealthcheck(t *testing.T) {
	t.Parallel()

	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"managed":3}`))
	}))
	t.Cleanup(ready.Close)

	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(notReady.Close)

	// httptest listens on 127.0.0.1, so the address is reusable as HEALTH_ADDR.
	readyAddr := strings.TrimPrefix(ready.URL, "http://")
	notReadyAddr := strings.TrimPrefix(notReady.URL, "http://")

	tests := []struct {
		name string
		addr string
		want int
	}{
		// Disabled health endpoint must not fail the container.
		{name: "unset addr is a no-op success", addr: "", want: 0},
		{name: "readyz 200", addr: readyAddr, want: 0},
		{name: "readyz 503", addr: notReadyAddr, want: 1},
		{name: "missing port", addr: "garbage", want: 1},
		// A wildcard bind address is not dialable; it must be rewritten to
		// loopback before the request goes out.
		{name: "wildcard host is dialed on loopback", addr: ":" + portOf(t, readyAddr), want: 0},
		{name: "nothing listening", addr: "127.0.0.1:1", want: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := healthcheck(tc.addr); got != tc.want {
				t.Errorf("healthcheck(%q) = %d, want %d", tc.addr, got, tc.want)
			}
		})
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		t.Fatalf("no port in %q", addr)
	}
	return addr[i+1:]
}

// --------------------------------------------------------------------------
// observability endpoints
// --------------------------------------------------------------------------

// endpointWorker builds a worker with one reconcile run behind it, so the
// handlers have something to report.
func endpointWorker(t *testing.T) *syncer.Worker {
	t.Helper()

	target := &docker.Target{
		Kind: npm.KindProxy, Running: true,
		ContainerID: "web-id", ContainerName: "web",
		DomainNames:   []string{"web.example.com"},
		ForwardScheme: "http", ForwardHost: "web", ForwardPort: 80,
		Enabled: true,
	}
	api := &stubAPI{}
	worker := syncer.NewWorker(api, &stubSource{targets: []*docker.Target{target}}, syncer.Options{
		Kinds: npm.Kinds, DeleteOrphans: true, AdoptExisting: true,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := worker.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	return worker
}

func TestHealthEndpoints(t *testing.T) {
	t.Parallel()

	worker := endpointWorker(t)
	cfg := &config.Config{HealthAddr: ":0"}
	mux := healthMux(cfg, worker, "instance-a", slog.New(slog.NewTextHandler(io.Discard, nil)))

	t.Run("healthz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
			t.Errorf("/healthz = %d %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("readyz", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/readyz = %d %q", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "1 managed hosts") {
			t.Errorf("/readyz body = %q", rec.Body.String())
		}
	})

	t.Run("status", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/status = %d", rec.Code)
		}
		var report statusReport
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatalf("decode /status: %v (%s)", err, rec.Body.String())
		}
		if report.Instance != "instance-a" || report.Managed != 1 {
			t.Errorf("status = %+v", report)
		}
		if len(report.Resources) != 1 || report.Resources[0].Key != "web.example.com" {
			t.Errorf("status resources = %+v", report.Resources)
		}
		if !report.Resources[0].Running {
			t.Error("the resource should be reported as backed by a running container")
		}
	})

	t.Run("metrics", func(t *testing.T) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("/metrics = %d", rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{
			"npmsync_reconcile_runs_total 1",
			"npmsync_resources_created_total 1",
			"npmsync_managed_resources 1",
			`npmsync_managed_resources_by_kind{kind="proxy"} 1`,
			"# TYPE npmsync_event_stream_connected gauge",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("/metrics is missing %q:\n%s", want, body)
			}
		}
	})
}

// A stream that has been down for a long time must make the process unready:
// from then on it only reacts on the periodic resync.
func TestReadyzFollowsTheEventStream(t *testing.T) {
	t.Parallel()

	target := &docker.Target{
		Kind: npm.KindProxy, Running: true, ContainerID: "web-id", ContainerName: "web",
		DomainNames: []string{"web.example.com"}, ForwardScheme: "http",
		ForwardHost: "web", ForwardPort: 80, Enabled: true,
	}
	src := &stubSource{
		targets: []*docker.Target{target},
		stream:  docker.StreamStatus{Connected: false, Since: time.Now().Add(-time.Hour), LastError: "socket gone"},
	}
	worker := syncer.NewWorker(&stubAPI{}, src, syncer.Options{Kinds: npm.Kinds}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := worker.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	mux := healthMux(&config.Config{}, worker, "instance-a", slog.New(slog.NewTextHandler(io.Discard, nil)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "socket gone") {
		t.Errorf("/readyz body = %q, want the stream error", rec.Body.String())
	}
}

// The login loop must survive an NPM that is still starting up.
func TestLoginWithRetry(t *testing.T) {
	t.Parallel()

	var attempts int
	client := &stubLogin{fail: 2, attempts: &attempts}
	if err := loginWithRetry(context.Background(), client, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond); err != nil {
		t.Fatalf("loginWithRetry() error = %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}

	failing := &stubLogin{fail: 99, attempts: new(int)}
	if err := loginWithRetry(context.Background(), failing, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Millisecond); err == nil {
		t.Error("loginWithRetry() error = nil, want it to give up eventually")
	}
}

func TestLogDefaultsReportsTheEnvironment(t *testing.T) {
	t.Parallel()

	defaults, _, err := fields.LoadDefaults(func(key string) string {
		if key == "NPM_PROXY_WEBSOCKETS" {
			return "false"
		}
		return ""
	}, nil)
	if err != nil {
		t.Fatalf("LoadDefaults() error = %v", err)
	}

	var buffer bytes.Buffer
	logDefaults(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})),
		&config.Config{Defaults: defaults})
	logged := buffer.String()
	if !strings.Contains(logged, "NPM_PROXY_WEBSOCKETS") || !strings.Contains(logged, "websockets") {
		t.Errorf("logDefaults() = %q, want the changed default reported", logged)
	}
}

// --------------------------------------------------------------------------
// stubs
// --------------------------------------------------------------------------

// stubAPI accepts everything and remembers nothing beyond ids.
type stubAPI struct{ next int }

func (s *stubAPI) List(context.Context, npm.Kind) ([]npm.Resource, error) { return nil, nil }

func (s *stubAPI) Create(_ context.Context, resource npm.Resource) (npm.Resource, error) {
	s.next++
	resource.SetResourceID(s.next)
	resource.SetEnabled(true)
	return resource, nil
}

func (s *stubAPI) Update(_ context.Context, id int, resource npm.Resource) (npm.Resource, error) {
	resource.SetResourceID(id)
	return resource, nil
}

func (s *stubAPI) Delete(context.Context, npm.Kind, int) error                 { return nil }
func (s *stubAPI) SetEnabled(context.Context, npm.Kind, int, bool) error       { return nil }
func (s *stubAPI) ListCertificates(context.Context) ([]npm.Certificate, error) { return nil, nil }
func (s *stubAPI) ListAccessLists(context.Context) ([]npm.AccessList, error)   { return nil, nil }
func (s *stubAPI) Flavour() npm.Flavour                                        { return npm.FlavourNPMplus }
func (s *stubAPI) Recheck(context.Context) error                               { return nil }

// stubSource serves a fixed set of targets plus an event stream state.
type stubSource struct {
	targets []*docker.Target
	stream  docker.StreamStatus
}

func (s *stubSource) Snapshot(context.Context) (docker.Snapshot, error) {
	return docker.Snapshot{Targets: s.targets, Containers: len(s.targets) + 1}, nil
}

func (s *stubSource) StreamStatus() docker.StreamStatus { return s.stream }

// stubLogin fails the first `fail` attempts.
type stubLogin struct {
	fail     int
	attempts *int
}

func (s *stubLogin) Login(context.Context) error {
	*s.attempts++
	if *s.attempts <= s.fail {
		return errors.New("connection refused")
	}
	return nil
}

func (s *stubLogin) BaseURL() string        { return "http://npm:81" }
func (s *stubLogin) AuthMode() npm.AuthMode { return npm.AuthBearer }

// `validate <file>` is the pre-deploy check: no Docker, no NPM, and a non-zero
// exit code when a label set is broken.
func TestValidateComposeFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "stack.json")
	document := `{"services":{
		"app":{"container_name":"app","labels":{"npm.proxy.domains":"app.example.com","npm.proxy.port":"8080"}},
		"list":{"labels":["npm.proxy.domains=list.example.com","npm.proxy.port=80"]},
		"off":{"labels":{"npm.enable":"false"}}
	}}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	opts := docker.ParseOptions{Prefix: "npm", ExposedByDefault: true}
	if err := validateFile(path, opts, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("validateFile() error = %v", err)
	}

	broken := filepath.Join(dir, "broken.json")
	brokenDocument := `{"services":{"app":{"labels":{"npm.proxy.domains":"app.example.com","npm.proxy.port":"80a"}}}}`
	if err := os.WriteFile(broken, []byte(brokenDocument), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	if err := validateFile(broken, opts, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Error("validateFile() error = nil for a broken label set")
	}

	if err := validateFile(filepath.Join(dir, "missing.json"), opts, nil); err == nil {
		t.Error("validateFile() error = nil for a missing file")
	}

	notCompose := filepath.Join(dir, "plain.json")
	if err := os.WriteFile(notCompose, []byte(`{"version":"3"}`), 0o600); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	if err := validateFile(notCompose, opts, nil); err == nil {
		t.Error("validateFile() error = nil for a document without services")
	}
}

func TestDecodeLabels(t *testing.T) {
	t.Parallel()

	mapping, err := decodeLabels([]byte(`{"npm.proxy.port":8080,"npm.enable":true}`))
	if err != nil {
		t.Fatalf("decodeLabels(mapping) error = %v", err)
	}
	if mapping["npm.proxy.port"] != "8080" || mapping["npm.enable"] != "true" {
		t.Errorf("decodeLabels(mapping) = %v", mapping)
	}

	list, err := decodeLabels([]byte(`["npm.proxy.domains=a.example.com","npm.enable=false"]`))
	if err != nil {
		t.Fatalf("decodeLabels(list) error = %v", err)
	}
	if list["npm.proxy.domains"] != "a.example.com" || list["npm.enable"] != "false" {
		t.Errorf("decodeLabels(list) = %v", list)
	}

	if _, err := decodeLabels([]byte(`"nonsense"`)); err == nil {
		t.Error("decodeLabels(string) error = nil")
	}
}
