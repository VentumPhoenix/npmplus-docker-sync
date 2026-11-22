package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// known is an auth status read from an NPMplus that has an upstream for the
// given providers.
func known(providers ...string) docker.AuthStatus {
	return docker.AuthStatus{State: docker.AuthKnown, Providers: providers, Source: "container npmplus", ContainerID: "npmplus-id"}
}

// authTarget is a proxy target protected by a provider, with optional
// locations.
func authTarget(provider, fallback string, locations ...npm.Location) *docker.Target {
	t := proxyTarget("app", "app.example.com", 80)
	t.AuthRequest = provider
	t.AuthRequestFallback = fallback
	t.Locations = locations
	return t
}

// location is a location block as the label parser builds it: one without its
// own auth_request label carries the host's, as a copy.
func location(path, auth, upstream, fallback string) npm.Location {
	enabled := true
	return npm.Location{
		Path: path, ForwardScheme: "http", ForwardHost: "app", ForwardPort: 80,
		Enabled: &enabled, AuthRequest: auth, AuthRequestUpstream: upstream, AuthRequestFallback: fallback,
	}
}

func disabled(l npm.Location) npm.Location {
	off := false
	l.Enabled = &off
	return l
}

func TestDecideAuthFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target *docker.Target
		status docker.AuthStatus
		// want is the mode, "" for "written as labelled".
		want    string
		missing []string
		parts   []string
	}{
		{
			name:   "upstream configured in npmplus",
			target: authTarget("authentik", ""),
			status: known("authentik"),
		},
		{
			name: "override on the host counts as an upstream",
			target: func() *docker.Target {
				t := authTarget("authentik", "")
				t.AuthRequestUpstream = "http://authentik:9000"
				return t
			}(),
			status: known(),
		},
		{
			name:    "missing upstream closes the host by default",
			target:  authTarget("authentik", ""),
			status:  known("authelia"),
			want:    fields.AuthFallbackClosed,
			missing: []string{"authentik"},
			parts:   []string{"host"},
		},
		{
			name:    "authentik-send-basic-auth uses the authentik upstream",
			target:  authTarget("authentik-send-basic-auth", fields.AuthFallbackOpen),
			status:  known(),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"host"},
		},
		{
			name:   "authentik-send-basic-auth is satisfied by the authentik upstream",
			target: authTarget("authentik-send-basic-auth", ""),
			status: known("authentik"),
		},
		{
			name:    "open on the host",
			target:  authTarget("authentik", fields.AuthFallbackOpen),
			status:  known(),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"host"},
		},
		{
			name: "inheriting locations are affected with the host",
			target: authTarget("authentik", fields.AuthFallbackOpen,
				location("/api", "authentik", "", ""),
				location("/global", npm.AuthRequestGlobal, "", ""),
				location("/other", "authelia", "", "")),
			status:  known("authelia"),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"host", "location /api", "location /global"},
		},
		{
			name: "a location with closed closes the whole host",
			target: authTarget("authentik", fields.AuthFallbackOpen,
				location("/admin", "authentik", "", fields.AuthFallbackClosed)),
			status:  known(),
			want:    fields.AuthFallbackClosed,
			missing: []string{"authentik"},
			parts:   []string{"host", "location /admin"},
		},
		{
			name: "only a location misses its provider",
			target: authTarget("authelia", "",
				location("/api", "authentik", "", fields.AuthFallbackOpen)),
			status:  known("authelia"),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"location /api"},
		},
		{
			name: "a location without its own fallback inherits the host's",
			target: authTarget("authelia", fields.AuthFallbackOpen,
				location("/api", "authentik", "", "")),
			status:  known("authelia"),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"location /api"},
		},
		{
			name: "a location missing its provider closes the host by default",
			target: authTarget("authelia", "",
				location("/api", "authentik", "", "")),
			status:  known("authelia"),
			want:    fields.AuthFallbackClosed,
			missing: []string{"authentik"},
			parts:   []string{"location /api"},
		},
		{
			name: "a location override serves every location of that provider",
			target: authTarget("none", "",
				location("/a", "authentik", "", ""),
				location("/b", "authentik", "http://authentik:9000", "")),
			status: known(),
		},
		{
			name: "the override of a disabled location does not count",
			target: authTarget("none", fields.AuthFallbackOpen,
				location("/a", "authentik", "", ""),
				disabled(location("/b", "authentik", "http://authentik:9000", ""))),
			status:  known(),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"location /a"},
		},
		{
			name: "a disabled location is not rendered and does not count",
			target: authTarget("none", "",
				disabled(location("/a", "authentik", "", ""))),
			status: known(),
		},
		{
			name: "a location override does not serve the host's provider",
			target: authTarget("authentik", fields.AuthFallbackOpen,
				location("/a", "authentik", "http://authentik:9000", "")),
			status:  known(),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authentik"},
			parts:   []string{"host", "location /a"},
		},
		{
			name: "two providers missing at once",
			target: authTarget("authentik", fields.AuthFallbackOpen,
				location("/api", "authelia", "", "")),
			status:  known("tinyauth"),
			want:    fields.AuthFallbackOpen,
			missing: []string{"authelia", "authentik"},
			parts:   []string{"host", "location /api"},
		},
		{
			name:   "unknown status never falls back",
			target: authTarget("authentik", fields.AuthFallbackOpen),
			status: docker.AuthStatus{State: docker.AuthUnknown, Reason: "forbidden"},
		},
		{
			name:   "unchecked status never falls back",
			target: authTarget("authentik", ""),
		},
		{
			name:   "no forward authentication",
			target: authTarget("none", fields.AuthFallbackOpen),
			status: known(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := decideAuthFallback(tt.target, tt.status)
			if got.Mode != tt.want {
				t.Fatalf("mode = %q, want %q (decision %+v)", got.Mode, tt.want, got)
			}
			if !slices.Equal(got.Missing, tt.missing) {
				t.Errorf("missing = %v, want %v", got.Missing, tt.missing)
			}
			if !slices.Equal(got.Parts, tt.parts) {
				t.Errorf("parts = %v, want %v", got.Parts, tt.parts)
			}
			// The same input must give the same answer: the decision is
			// taken again on every run.
			if again := decideAuthFallback(tt.target, tt.status); again.signature() != got.signature() {
				t.Errorf("decision is not deterministic: %q then %q", got.signature(), again.signature())
			}
		})
	}
}

func TestAuthDecisionReason(t *testing.T) {
	t.Parallel()

	d := decideAuthFallback(authTarget("authentik", ""), known())
	want := "no AUTH_REQUEST_AUTHENTIK_UPSTREAM in NPMplus and no override"
	if d.Reason() != want {
		t.Errorf("Reason() = %q, want %q", d.Reason(), want)
	}
}

func TestApplyAuthFallback(t *testing.T) {
	t.Parallel()

	t.Run("open strips what has no upstream", func(t *testing.T) {
		t.Parallel()
		target := authTarget("authentik", fields.AuthFallbackOpen,
			location("/api", "authentik", "", ""),
			location("/global", npm.AuthRequestGlobal, "", ""),
			location("/other", "authelia", "http://authelia:9091", ""))
		got := applyAuthFallback(target, decideAuthFallback(target, known()))

		if got.AuthRequest != "none" || got.AuthRequestUpstream != "" || !got.Enabled {
			t.Errorf("host = %q/%q enabled=%t, want none, no upstream, enabled",
				got.AuthRequest, got.AuthRequestUpstream, got.Enabled)
		}
		for i, want := range []string{"none", "none", "authelia"} {
			if got.Locations[i].AuthRequest != want {
				t.Errorf("location %s auth_request = %q, want %q", got.Locations[i].Path, got.Locations[i].AuthRequest, want)
			}
		}
		if got.Locations[2].AuthRequestUpstream != "http://authelia:9091" {
			t.Errorf("an unaffected location lost its upstream: %q", got.Locations[2].AuthRequestUpstream)
		}
		// The target itself is what the next run starts from again.
		if target.AuthRequest != "authentik" || target.Locations[0].AuthRequest != "authentik" {
			t.Error("applyAuthFallback modified its input")
		}
	})

	t.Run("open on one location leaves the host alone", func(t *testing.T) {
		t.Parallel()
		target := authTarget("authelia", "", location("/api", "authentik", "", fields.AuthFallbackOpen))
		got := applyAuthFallback(target, decideAuthFallback(target, known("authelia")))
		if got.AuthRequest != "authelia" || got.Locations[0].AuthRequest != "none" || !got.Enabled {
			t.Errorf("host %q, location %q, enabled %t; want authelia, none, enabled",
				got.AuthRequest, got.Locations[0].AuthRequest, got.Enabled)
		}
	})

	t.Run("closed disables and keeps the forward authentication", func(t *testing.T) {
		t.Parallel()
		target := authTarget("authelia", "", location("/api", "authentik", "", ""))
		got := applyAuthFallback(target, decideAuthFallback(target, known("authelia")))
		if got.Enabled {
			t.Error("closed host is still enabled")
		}
		if got.AuthRequest != "authelia" || got.Locations[0].AuthRequest != "authentik" {
			t.Errorf("closed host lost its forward authentication: %q / %q", got.AuthRequest, got.Locations[0].AuthRequest)
		}
		if !target.Enabled {
			t.Error("applyAuthFallback modified its input")
		}
	})
}

// --------------------------------------------------------------------------
// the worker
// --------------------------------------------------------------------------

// renderingAPI plays an NPMplus that renders a proxy host when it is written:
// one whose forward authentication has no upstream is stored but reported
// offline, the way internalNginx.configure leaves it.
type renderingAPI struct {
	*fakeAPI
	mu       sync.Mutex
	upstream map[string]bool
}

func (r *renderingAPI) setUpstreams(providers ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.upstream = map[string]bool{}
	for _, p := range providers {
		r.upstream[p] = true
	}
}

func (r *renderingAPI) render(resource npm.Resource) npm.Resource {
	host, ok := resource.(*npm.ProxyHost)
	if !ok || !host.IsEnabled() {
		return resource
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := docker.AuthProvider(host.AuthRequest)
	online := provider == "" || host.AuthRequestUpstream != "" || r.upstream[provider]
	message := ""
	if !online {
		message = "nginx: [emerg] invalid URL prefix in /data/nginx/proxy_host/1.conf:42"
	}
	return withServerStatus(resource, online, message)
}

func (r *renderingAPI) Create(ctx context.Context, resource npm.Resource) (npm.Resource, error) {
	created, err := r.fakeAPI.Create(ctx, resource)
	if err != nil {
		return nil, err
	}
	return r.store(r.render(created)), nil
}

func (r *renderingAPI) Update(ctx context.Context, id int, resource npm.Resource) (npm.Resource, error) {
	updated, err := r.fakeAPI.Update(ctx, id, resource)
	if err != nil {
		return nil, err
	}
	return r.store(r.render(updated)), nil
}

func (r *renderingAPI) store(resource npm.Resource) npm.Resource {
	r.fakeAPI.mu.Lock()
	defer r.fakeAPI.mu.Unlock()
	r.bucket(resource.Kind())[resource.ResourceID()] = cloneResource(resource)
	return resource
}

// withServerStatus returns a copy of a resource carrying the nginx status the
// server reports.
func withServerStatus(r npm.Resource, online bool, message string) npm.Resource {
	raw, err := json.Marshal(r)
	if err != nil {
		panic(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		panic(err)
	}
	fields["npmplus_nginx_online"] = online
	fields["npmplus_nginx_err"] = message
	raw, err = json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	out := &npm.ProxyHost{}
	if err := json.Unmarshal(raw, out); err != nil {
		panic(err)
	}
	return out
}

func storedHost(t *testing.T, api *fakeAPI, id int) *npm.ProxyHost {
	t.Helper()
	host, ok := api.get(npm.KindProxy, id).(*npm.ProxyHost)
	if !ok {
		t.Fatalf("no proxy host %d stored (calls %v)", id, api.callList())
	}
	return host
}

func reconcileOK(t *testing.T, w *Worker) Result {
	t.Helper()
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	return res
}

func TestClosedFallbackDisablesAnExistingHostFirst(t *testing.T) {
	t.Parallel()

	labelled := authTarget("authentik", "")
	api := newFakeAPI(managed(1, labelled))
	src := newFakeSource(labelled)
	src.auth = known()
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	host := storedHost(t, api, 1)
	if host.IsEnabled() {
		t.Fatalf("host is still enabled; calls %v", api.callList())
	}
	if host.AuthRequest != "authentik" {
		t.Errorf("closed host was written as %q, want its forward authentication kept", host.AuthRequest)
	}
	fallbacks := w.Fallbacks()
	if len(fallbacks) != 1 || fallbacks[0].Mode != fields.AuthFallbackClosed || !fallbacks[0].Created {
		t.Errorf("Fallbacks() = %+v, want one closed, existing host", fallbacks)
	}

	// Converged: no write, no toggle on the next runs.
	before := len(api.callList())
	for range 3 {
		if res := reconcileOK(t, w); res.Changed() {
			t.Fatalf("closed host does not converge: %+v, calls %v", res, api.callList()[before:])
		}
	}
}

func TestClosedFallbackDisablesBeforeItUpdates(t *testing.T) {
	t.Parallel()

	labelled := authTarget("authentik", "")
	stale := proxyTarget("app", "app.example.com", 8080) // a different port: an update is due
	stale.AuthRequest = "authentik"
	api := newFakeAPI(managed(1, stale))
	src := newFakeSource(labelled)
	src.auth = known()
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	calls := api.callList()
	disable, update := slices.Index(calls, "set-enabled proxy 1 false"), slices.Index(calls, "update proxy 1")
	if disable < 0 || update < 0 || disable > update {
		t.Errorf("calls = %v, want the disable before the update", calls)
	}
}

func TestClosedFallbackDoesNotCreateAHost(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := newFakeSource(authTarget("authentik", ""))
	src.auth = known()
	w := NewWorker(api, src, Options{}, nil)

	res := reconcileOK(t, w)
	if api.count(npm.KindProxy) != 0 || api.callCount("create") != 0 {
		t.Fatalf("a closed host was created: calls %v", api.callList())
	}
	if res.Skipped != 1 {
		t.Errorf("skipped = %d, want 1", res.Skipped)
	}
	fallbacks := w.Fallbacks()
	if len(fallbacks) != 1 || fallbacks[0].Created || fallbacks[0].Reason == "" {
		t.Errorf("Fallbacks() = %+v, want one host that was not created, with a reason", fallbacks)
	}
}

func TestOpenFallbackWritesTheHostWithoutForwardAuth(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := newFakeSource(authTarget("authentik", fields.AuthFallbackOpen,
		location("/api", "authentik", "", "")))
	src.auth = known()
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	host := storedHost(t, api, 1)
	if !host.IsEnabled() || host.AuthRequest != "none" || host.Locations[0].AuthRequest != "none" {
		t.Errorf("host enabled=%t auth=%q location=%q, want enabled without forward authentication",
			host.IsEnabled(), host.AuthRequest, host.Locations[0].AuthRequest)
	}
	before := len(api.callList())
	if res := reconcileOK(t, w); res.Changed() {
		t.Fatalf("open host does not converge: %+v, calls %v", res, api.callList()[before:])
	}
}

// TestFallbackRecovers: once NPMplus has the upstream, the next run writes
// what the labels say - the forward authentication back, the host enabled -
// without anybody touching the host or restarting the tool.
func TestFallbackRecovers(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{fields.AuthFallbackOpen, fields.AuthFallbackClosed} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()

			labelled := authTarget("authentik", mode)
			api := newFakeAPI(managed(1, labelled))
			src := newFakeSource(labelled)
			src.auth = known()
			w := NewWorker(api, src, Options{}, nil)
			reconcileOK(t, w)

			src.mu.Lock()
			src.auth = known("authentik")
			src.mu.Unlock()
			reconcileOK(t, w)

			host := storedHost(t, api, 1)
			if !host.IsEnabled() || host.AuthRequest != "authentik" {
				t.Errorf("after recovery: enabled=%t auth=%q, want enabled with authentik (calls %v)",
					host.IsEnabled(), host.AuthRequest, api.callList())
			}
			if len(w.Fallbacks()) != 0 {
				t.Errorf("Fallbacks() = %+v after recovery, want none", w.Fallbacks())
			}
			before := len(api.callList())
			if res := reconcileOK(t, w); res.Changed() {
				t.Fatalf("recovered host does not converge: %+v, calls %v", res, api.callList()[before:])
			}
		})
	}
}

// TestUnknownStatusWritesAsLabelled: a status that could not be read is the
// behaviour before auth_request_fallback existed, exactly.
func TestUnknownStatusWritesAsLabelled(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := newFakeSource(authTarget("authentik", fields.AuthFallbackOpen))
	src.auth = docker.AuthStatus{State: docker.AuthUnknown, Reason: "docker refused to inspect"}
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	host := storedHost(t, api, 1)
	if !host.IsEnabled() || host.AuthRequest != "authentik" {
		t.Errorf("enabled=%t auth=%q, want the host written as labelled", host.IsEnabled(), host.AuthRequest)
	}
	if len(w.Fallbacks()) != 0 {
		t.Errorf("Fallbacks() = %+v, want none for an unknown status", w.Fallbacks())
	}
	if got := w.Status().Auth; got.State != docker.AuthUnknown || got.Reason == "" {
		t.Errorf("Status().Auth = %+v, want the unknown state and its reason", got)
	}
}

func TestFallbackIsLoggedOnce(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	api := newFakeAPI()
	src := newFakeSource(authTarget("authentik", fields.AuthFallbackOpen))
	src.auth = known()
	w := NewWorker(api, src, Options{}, log)
	for range 3 {
		reconcileOK(t, w)
	}
	if n := strings.Count(buf.String(), "serving the host without it"); n != 1 {
		t.Errorf("the open fallback was logged %d times, want once:\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "no AUTH_REQUEST_AUTHENTIK_UPSTREAM in NPMplus and no override") {
		t.Errorf("the log does not say why:\n%s", buf.String())
	}
}

func TestOfflineHostIsReportedAfterAWrite(t *testing.T) {
	t.Parallel()

	api := &renderingAPI{fakeAPI: newFakeAPI()}
	src := newFakeSource(authTarget("authentik", ""))
	src.auth = docker.AuthStatus{State: docker.AuthUnknown, Reason: "not found"}
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	if got := w.Status().Offline; got != 1 {
		t.Fatalf("Status().Offline = %d, want 1", got)
	}
	resources := w.Resources()
	if len(resources) != 1 || !resources[0].Offline || !strings.Contains(resources[0].NginxError, "invalid URL prefix") {
		t.Errorf("Resources() = %+v, want the host offline with the nginx error", resources)
	}
	if m := w.Metrics(); m.Offline != 1 {
		t.Errorf("Metrics().Offline = %d, want 1", m.Offline)
	}
}

// TestOfflineHostIsRewrittenOncePerChange: a host NPMplus reports offline is
// written once more although nothing differs - and then not again until the
// auth status changes, so a host that stays broken is not rewritten in a loop.
func TestOfflineHostIsRewrittenOncePerChange(t *testing.T) {
	t.Parallel()

	labelled := authTarget("authentik", "")
	api := &renderingAPI{fakeAPI: newFakeAPI(withServerStatus(managed(1, labelled), false, "nginx: [emerg] broken"))}
	src := newFakeSource(labelled)
	src.auth = docker.AuthStatus{State: docker.AuthUnknown, Reason: "not found"}
	w := NewWorker(api, src, Options{}, nil)

	reconcileOK(t, w)
	reconcileOK(t, w)
	reconcileOK(t, w)
	if got := api.callCount("update proxy 1"); got != 1 {
		t.Fatalf("offline host was written %d times under one status, want once (calls %v)", got, api.callList())
	}

	// NPMplus restarted with the upstream: a new status, one more write -
	// which now brings the host online.
	api.setUpstreams("authentik")
	src.mu.Lock()
	src.auth = known("authentik")
	src.auth.Started = "2026-11-22T10:00:00Z"
	src.mu.Unlock()
	reconcileOK(t, w)
	reconcileOK(t, w)
	if got := api.callCount("update proxy 1"); got != 2 {
		t.Fatalf("offline host was written %d times in all, want 2 (calls %v)", got, api.callList())
	}
	if got := w.Status().Offline; got != 0 {
		t.Errorf("Status().Offline = %d after the upstream appeared, want 0", got)
	}
}

func TestOfflineDoesNotFailReadiness(t *testing.T) {
	t.Parallel()

	api := &renderingAPI{fakeAPI: newFakeAPI()}
	src := newFakeSource(authTarget("authentik", ""))
	src.auth = docker.AuthStatus{State: docker.AuthUnknown, Reason: "not found"}
	w := NewWorker(api, src, Options{}, nil)
	reconcileOK(t, w)
	if status := w.Status(); status.Offline != 1 || !status.Ready() {
		t.Errorf("Status() = %+v, want one offline host and still ready", status)
	}
}

func TestInertFallbackIsReported(t *testing.T) {
	t.Parallel()

	target := authTarget("none", fields.AuthFallbackOpen, location("/api", "none", "", fields.AuthFallbackClosed))
	target.Explicit = []string{fields.AuthRequestFallback}
	got := inertFallback(target)
	want := []string{fields.AuthRequestFallback, "location./api." + fields.AuthRequestFallback}
	if !slices.Equal(got, want) {
		t.Errorf("inertFallback() = %v, want %v", got, want)
	}
	if got := inertFallback(authTarget("authentik", fields.AuthFallbackOpen)); len(got) != 0 {
		t.Errorf("inertFallback() = %v for a host with forward authentication, want none", got)
	}
}
