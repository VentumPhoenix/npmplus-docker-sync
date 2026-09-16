package syncer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// fakeAPI is an in-memory stand-in for the NPM API covering all four
// collections.
type fakeAPI struct {
	mu sync.Mutex

	resources map[npm.Kind]map[int]npm.Resource
	nextID    int

	listErr    map[npm.Kind]error
	createErr  error
	updateErr  error
	deleteErr  error
	calls      []string
	inFlight   int
	concurrent bool
}

func newFakeAPI(seed ...npm.Resource) *fakeAPI {
	f := &fakeAPI{
		resources: make(map[npm.Kind]map[int]npm.Resource),
		nextID:    1,
		listErr:   make(map[npm.Kind]error),
	}
	for _, r := range seed {
		if r.ResourceID() == 0 {
			r.SetResourceID(f.nextID)
		}
		if r.ResourceID() >= f.nextID {
			f.nextID = r.ResourceID() + 1
		}
		f.bucket(r.Kind())[r.ResourceID()] = r
	}
	return f
}

func (f *fakeAPI) bucket(kind npm.Kind) map[int]npm.Resource {
	if f.resources[kind] == nil {
		f.resources[kind] = make(map[int]npm.Resource)
	}
	return f.resources[kind]
}

func (f *fakeAPI) enter(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.inFlight++
	if f.inFlight > 1 {
		f.concurrent = true
	}
	f.mu.Unlock()
}

func (f *fakeAPI) leave() {
	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
}

func (f *fakeAPI) List(ctx context.Context, kind npm.Kind) ([]npm.Resource, error) {
	f.enter("list " + string(kind))
	defer f.leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.listErr[kind]; err != nil {
		return nil, err
	}
	out := make([]npm.Resource, 0, len(f.resources[kind]))
	for _, r := range f.resources[kind] {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeAPI) Create(ctx context.Context, resource npm.Resource) (npm.Resource, error) {
	f.enter(fmt.Sprintf("create %s %s", resource.Kind(), resource.ResourceKey()))
	defer f.leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resource.SetResourceID(f.nextID)
	f.nextID++
	f.bucket(resource.Kind())[resource.ResourceID()] = resource
	return resource, nil
}

func (f *fakeAPI) Update(ctx context.Context, id int, resource npm.Resource) (npm.Resource, error) {
	f.enter(fmt.Sprintf("update %s %d", resource.Kind(), id))
	defer f.leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	resource.SetResourceID(id)
	f.bucket(resource.Kind())[id] = resource
	return resource, nil
}

func (f *fakeAPI) Delete(ctx context.Context, kind npm.Kind, id int) error {
	f.enter(fmt.Sprintf("delete %s %d", kind, id))
	defer f.leave()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.resources[kind], id)
	return nil
}

func (f *fakeAPI) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if len(c) >= len(prefix) && c[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func (f *fakeAPI) count(kind npm.Kind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resources[kind])
}

// fakeSource returns a fixed set of targets.
type fakeSource struct {
	mu      sync.Mutex
	targets []*docker.Target
	err     error
}

func (s *fakeSource) Targets(context.Context) ([]*docker.Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targets, s.err
}

func (s *fakeSource) set(targets ...*docker.Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = targets
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

func proxyTarget(name, domain string, port int) *docker.Target {
	return &docker.Target{
		Kind:          npm.KindProxy,
		ContainerID:   name + "-id",
		ContainerName: name,
		DomainNames:   []string{domain},
		ForwardScheme: "http",
		ForwardHost:   name,
		ForwardPort:   port,
		Websockets:    true,
		BlockExploits: true,
		Enabled:       true,
	}
}

func redirectTarget(name, from, to string) *docker.Target {
	return &docker.Target{
		Kind:              npm.KindRedirect,
		ContainerID:       name + "-id",
		ContainerName:     name,
		DomainNames:       []string{from},
		ForwardScheme:     "auto",
		ForwardDomainName: to,
		ForwardHTTPCode:   301,
		PreservePath:      true,
		BlockExploits:     true,
		Enabled:           true,
	}
}

func streamTarget(name string, incoming, forward int) *docker.Target {
	return &docker.Target{
		Kind:           npm.KindStream,
		ContainerID:    name + "-id",
		ContainerName:  name,
		IncomingPort:   incoming,
		ForwardingHost: name,
		ForwardingPort: forward,
		TCPForwarding:  true,
		Enabled:        true,
	}
}

func deadTarget(name, domain string) *docker.Target {
	return &docker.Target{
		Kind:          npm.KindDead,
		ContainerID:   name + "-id",
		ContainerName: name,
		DomainNames:   []string{domain},
		Enabled:       true,
	}
}

// managed builds a live resource as this tool would have created it.
func managed(id int, target *docker.Target) npm.Resource {
	r := BuildResource(target)
	r.SetResourceID(id)
	return r
}

func defaultOptions() Options {
	return Options{DeleteOrphans: true, AdoptExisting: true, Kinds: npm.Kinds}
}

// ---------------------------------------------------------------------------
// tests
// ---------------------------------------------------------------------------

func TestReconcile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		opts    Options
		seed    []npm.Resource
		targets []*docker.Target
		want    Result
		wantAPI func(t *testing.T, api *fakeAPI)
	}{
		{
			name:    "creates a missing proxy host",
			opts:    defaultOptions(),
			targets: []*docker.Target{proxyTarget("whoami", "whoami.example.com", 80)},
			want:    Result{Created: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("create proxy"); got != 1 {
					t.Errorf("create calls = %d, want 1", got)
				}
			},
		},
		{
			name: "creates one resource of every kind",
			opts: defaultOptions(),
			targets: []*docker.Target{
				proxyTarget("web", "web.example.com", 80),
				redirectTarget("web", "old.example.com", "web.example.com"),
				streamTarget("db", 5432, 5432),
				deadTarget("parked", "parked.example.com"),
			},
			want: Result{Created: 4},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				for _, kind := range npm.Kinds {
					if got := api.count(kind); got != 1 {
						t.Errorf("%s resources = %d, want 1", kind, got)
					}
				}
			},
		},
		{
			name:    "updates a changed proxy host",
			opts:    defaultOptions(),
			seed:    []npm.Resource{managed(3, proxyTarget("whoami", "whoami.example.com", 8080))},
			targets: []*docker.Target{proxyTarget("whoami", "whoami.example.com", 80)},
			want:    Result{Updated: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("update proxy 3"); got != 1 {
					t.Errorf("update calls = %d, want 1", got)
				}
			},
		},
		{
			name:    "leaves an identical resource untouched",
			opts:    defaultOptions(),
			seed:    []npm.Resource{managed(3, proxyTarget("whoami", "whoami.example.com", 80))},
			targets: []*docker.Target{proxyTarget("whoami", "whoami.example.com", 80)},
			want:    Result{Unchanged: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("update"); got != 0 {
					t.Errorf("update calls = %d, want 0 (idempotent)", got)
				}
			},
		},
		{
			name:    "updates a changed stream",
			opts:    defaultOptions(),
			seed:    []npm.Resource{managed(4, streamTarget("db", 5432, 15432))},
			targets: []*docker.Target{streamTarget("db", 5432, 5432)},
			want:    Result{Updated: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("update stream 4"); got != 1 {
					t.Errorf("stream update calls = %d, want 1", got)
				}
			},
		},
		{
			name: "same domain as proxy and 404 host does not collide",
			opts: defaultOptions(),
			targets: []*docker.Target{
				proxyTarget("web", "app.example.com", 80),
				deadTarget("web", "app.example.com"),
			},
			want: Result{Created: 2},
		},
		{
			name: "deletes an orphaned resource of any kind",
			opts: defaultOptions(),
			seed: []npm.Resource{
				managed(9, proxyTarget("gone", "gone.example.com", 80)),
				managed(10, streamTarget("gone", 9999, 9999)),
			},
			want: Result{Deleted: 2},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("delete proxy 9") + api.callCount("delete stream 10"); got != 2 {
					t.Errorf("delete calls = %d, want 2", got)
				}
			},
		},
		{
			name: "never deletes resources it does not manage",
			opts: defaultOptions(),
			seed: []npm.Resource{
				&npm.ProxyHost{ID: 9, DomainNames: []string{"manual.example.com"}, ForwardHost: "manual", ForwardPort: 80, ForwardScheme: "http"},
				&npm.Stream{ID: 11, IncomingPort: 25565, ForwardingHost: "mc", ForwardingPort: 25565, TCPForwarding: true},
			},
			want: Result{},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("delete"); got != 0 {
					t.Errorf("delete calls = %d, want 0 for unmanaged resources", got)
				}
			},
		},
		{
			name: "keeps orphans when deletion is disabled",
			opts: Options{DeleteOrphans: false, AdoptExisting: true, Kinds: npm.Kinds},
			seed: []npm.Resource{managed(9, proxyTarget("gone", "gone.example.com", 80))},
			want: Result{Skipped: 1},
		},
		{
			name: "skips an unmanaged resource when adoption is disabled",
			opts: Options{DeleteOrphans: true, AdoptExisting: false, Kinds: npm.Kinds},
			seed: []npm.Resource{
				&npm.ProxyHost{ID: 4, DomainNames: []string{"whoami.example.com"}, ForwardHost: "elsewhere", ForwardPort: 9999, ForwardScheme: "http"},
			},
			targets: []*docker.Target{proxyTarget("whoami", "whoami.example.com", 80)},
			want:    Result{Skipped: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("update"); got != 0 {
					t.Errorf("update calls = %d, want 0", got)
				}
			},
		},
		{
			name: "adopts an unmanaged resource when allowed",
			opts: defaultOptions(),
			seed: []npm.Resource{
				&npm.ProxyHost{ID: 4, DomainNames: []string{"whoami.example.com"}, ForwardHost: "elsewhere", ForwardPort: 9999, ForwardScheme: "http"},
			},
			targets: []*docker.Target{proxyTarget("whoami", "whoami.example.com", 80)},
			want:    Result{Updated: 1},
		},
		{
			name: "dry run touches nothing",
			opts: Options{DeleteOrphans: true, AdoptExisting: true, DryRun: true, Kinds: npm.Kinds},
			seed: []npm.Resource{managed(9, proxyTarget("gone", "gone.example.com", 80))},
			targets: []*docker.Target{
				proxyTarget("whoami", "whoami.example.com", 80),
				streamTarget("db", 5432, 5432),
			},
			want: Result{Created: 2, Deleted: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				writes := api.callCount("create") + api.callCount("update") + api.callCount("delete")
				if writes != 0 {
					t.Errorf("write calls = %d, want 0 in dry-run mode", writes)
				}
			},
		},
		{
			name: "duplicate keys within one kind: first definition wins",
			opts: defaultOptions(),
			targets: []*docker.Target{
				proxyTarget("b-container", "dup.example.com", 80),
				proxyTarget("a-container", "dup.example.com", 81),
			},
			want: Result{Created: 1, Skipped: 1},
		},
		{
			name: "duplicate stream ports are rejected once",
			opts: defaultOptions(),
			targets: []*docker.Target{
				streamTarget("a", 5432, 5432),
				streamTarget("b", 5432, 5433),
			},
			want: Result{Created: 1, Skipped: 1},
		},
		{
			name: "kinds can be restricted",
			opts: Options{DeleteOrphans: true, AdoptExisting: true, Kinds: []npm.Kind{npm.KindProxy}},
			targets: []*docker.Target{
				proxyTarget("web", "web.example.com", 80),
				streamTarget("db", 5432, 5432),
			},
			want: Result{Created: 1},
			wantAPI: func(t *testing.T, api *fakeAPI) {
				if got := api.callCount("list stream"); got != 0 {
					t.Errorf("stream list calls = %d, want 0 when the kind is disabled", got)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			api := newFakeAPI(tc.seed...)
			w := NewWorker(api, &fakeSource{targets: tc.targets}, tc.opts, nil)

			got, err := w.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Result = %+v, want %+v", got, tc.want)
			}
			if tc.wantAPI != nil {
				tc.wantAPI(t, api)
			}
			if api.concurrent {
				t.Error("API calls overlapped; writes must stay sequential")
			}
		})
	}
}

func TestReconcileMultipleResourcesFromOneContainer(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	targets := []*docker.Target{
		{
			Kind: npm.KindProxy, Index: 0, ContainerName: "multi", ContainerID: "multi-id",
			DomainNames: []string{"app.example.com"}, ForwardScheme: "http",
			ForwardHost: "172.20.0.5", ForwardPort: 8080, Websockets: true, Enabled: true,
		},
		{
			Kind: npm.KindProxy, Index: 1, ContainerName: "multi", ContainerID: "multi-id",
			DomainNames: []string{"admin.example.com"}, ForwardScheme: "http",
			ForwardHost: "172.20.0.5", ForwardPort: 9090, Websockets: true, Enabled: true,
		},
		{
			Kind: npm.KindStream, Index: 2, ContainerName: "multi", ContainerID: "multi-id",
			IncomingPort: 5432, ForwardingHost: "172.20.0.5", ForwardingPort: 5432,
			TCPForwarding: true, Enabled: true,
		},
	}
	w := NewWorker(api, &fakeSource{targets: targets}, defaultOptions(), nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Created != 3 {
		t.Fatalf("Result = %+v, want three resources created", res)
	}
	if got := w.Cache().LenKind(npm.KindProxy); got != 2 {
		t.Errorf("cached proxy hosts = %d, want 2", got)
	}
	if got := w.Cache().LenKind(npm.KindStream); got != 1 {
		t.Errorf("cached streams = %d, want 1", got)
	}

	// The index is recorded so logs and meta can point back at the label.
	entry, ok := w.Cache().Get(npm.KindProxy, "admin.example.com")
	if !ok || entry.Index != 1 || entry.Container != "multi" {
		t.Errorf("cache entry = %+v, %v; want index 1 of container multi", entry, ok)
	}
}

func TestReconcileIsIdempotentAcrossRuns(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := &fakeSource{targets: []*docker.Target{
		proxyTarget("whoami", "whoami.example.com", 80),
		redirectTarget("whoami", "old.example.com", "whoami.example.com"),
		streamTarget("db", 5432, 5432),
		deadTarget("parked", "parked.example.com"),
	}}
	w := NewWorker(api, src, defaultOptions(), nil)

	if res, err := w.Reconcile(context.Background()); err != nil || res.Created != 4 {
		t.Fatalf("first run = %+v, err = %v", res, err)
	}
	for i := 0; i < 3; i++ {
		res, err := w.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("run %d error = %v", i, err)
		}
		if res.Changed() {
			t.Fatalf("run %d changed state: %+v", i, res)
		}
		if res.Unchanged != 4 {
			t.Fatalf("run %d = %+v, want four unchanged resources", i, res)
		}
	}
	if got := api.callCount("create"); got != 4 {
		t.Errorf("create calls = %d, want 4 across all runs", got)
	}
}

// A target requesting `certificate_id=new` must not request a new certificate
// on every reconcile once NPM issued one.
func TestReconcileAdoptsIssuedCertificate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		target *docker.Target
	}{
		{name: "proxy", target: proxyTarget("blog", "blog.example.com", 2368)},
		{name: "redirect", target: redirectTarget("blog", "old.example.com", "blog.example.com")},
		{name: "dead", target: deadTarget("parked", "parked.example.com")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := tc.target
			target.CertificateID = npm.NewCertificate()
			target.LetsEncryptEmail = "admin@example.com"
			target.LetsEncryptAgree = true

			issued := managed(12, target)
			switch r := issued.(type) {
			case *npm.ProxyHost:
				r.CertificateID = npm.CertificateRef(5)
			case *npm.RedirectionHost:
				r.CertificateID = npm.CertificateRef(5)
			case *npm.DeadHost:
				r.CertificateID = npm.CertificateRef(5)
			}

			api := newFakeAPI(issued)
			w := NewWorker(api, &fakeSource{targets: []*docker.Target{target}}, defaultOptions(), nil)

			res, err := w.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if res.Unchanged != 1 || res.Updated != 0 {
				t.Fatalf("Result = %+v, want the issued certificate to be adopted", res)
			}
		})
	}
}

func TestReconcileSkipsUnavailableCollection(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	// NPMplus forks may not expose every collection.
	api.listErr[npm.KindStream] = &npm.APIError{Status: http.StatusNotFound, Method: "GET", Path: "/api/nginx/streams"}

	src := &fakeSource{targets: []*docker.Target{
		proxyTarget("web", "web.example.com", 80),
		streamTarget("db", 5432, 5432),
	}}
	w := NewWorker(api, src, defaultOptions(), nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want the missing collection to be skipped", err)
	}
	if res.Created != 1 {
		t.Errorf("Result = %+v, want the proxy host to be created anyway", res)
	}
}

func TestReconcileReportsListFailure(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.listErr[npm.KindRedirect] = errors.New("boom")
	w := NewWorker(api, &fakeSource{targets: []*docker.Target{proxyTarget("web", "web.example.com", 80)}}, defaultOptions(), nil)

	res, err := w.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile() error = nil, want the list failure")
	}
	if res.Created != 1 {
		t.Errorf("Result = %+v, want other kinds to be processed regardless", res)
	}
}

func TestReconcileCollectsErrors(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	api.createErr = errors.New("database is locked")
	src := &fakeSource{targets: []*docker.Target{
		proxyTarget("a", "a.example.com", 80),
		proxyTarget("b", "b.example.com", 80),
	}}
	w := NewWorker(api, src, defaultOptions(), nil)

	res, err := w.Reconcile(context.Background())
	if err == nil {
		t.Fatal("Reconcile() error = nil, want the collected failures")
	}
	if res.Failed != 2 {
		t.Errorf("Failed = %d, want 2 (both resources attempted)", res.Failed)
	}
}

func TestReconcilePropagatesSourceError(t *testing.T) {
	t.Parallel()

	w := NewWorker(newFakeAPI(), &fakeSource{err: errors.New("docker down")}, defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile() error = nil, want the docker error")
	}
}

func TestWorkerRunFlushesOnShutdown(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := &fakeSource{}
	w := NewWorker(api, src, Options{DeleteOrphans: true, AdoptExisting: true, Kinds: npm.Kinds, ShutdownTimeout: 5 * time.Second}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	triggers := make(chan []docker.Event, 1)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, triggers) }()

	// A container appears after the initial sync but shutdown starts before
	// the event is processed: the final flush must still create the resource.
	src.set(proxyTarget("late", "late.example.com", 80))
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	if got := api.callCount("create proxy late.example.com"); got != 1 {
		t.Errorf("create calls = %d, want the shutdown flush to create the resource", got)
	}
}

func TestWorkerRunFlushDoesNotDeleteOrphans(t *testing.T) {
	t.Parallel()

	// A managed proxy host whose container is already gone. That is what a
	// `docker compose down` of the whole stack looks like from here: the
	// labelled containers stop first, so the source reports no targets at all.
	api := newFakeAPI(managed(7, proxyTarget("gone", "gone.example.com", 80)))
	src := &fakeSource{}
	w := NewWorker(api, src, Options{DeleteOrphans: true, AdoptExisting: true, Kinds: npm.Kinds, ShutdownTimeout: 5 * time.Second}, nil)

	// Cancelled up front so the initial reconciliation cannot delete anything
	// either; only the shutdown flush gets to touch the API.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	triggers := make(chan []docker.Event, 1)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, triggers) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}

	if got := api.callCount("delete"); got != 0 {
		t.Errorf("delete calls = %d, want 0: the shutdown flush must not delete orphans", got)
	}
	if got := api.count(npm.KindProxy); got != 1 {
		t.Errorf("proxy hosts = %d, want the orphan to survive the shutdown flush", got)
	}
}

func TestWorkerRunHandlesTriggers(t *testing.T) {
	t.Parallel()

	api := newFakeAPI()
	src := &fakeSource{}
	w := NewWorker(api, src, defaultOptions(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	triggers := make(chan []docker.Event, 1)
	go func() { _ = w.Run(ctx, triggers) }()

	src.set(proxyTarget("web", "web.example.com", 80), streamTarget("db", 5432, 5432))
	triggers <- []docker.Event{{ContainerID: "web-id", Name: "web", Action: "start"}}

	deadline := time.After(3 * time.Second)
	for api.callCount("create proxy web.example.com") == 0 || api.callCount("create stream 5432") == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the triggered reconcile")
		case <-time.After(10 * time.Millisecond):
		}
	}

	lastRun, managedCount, err := w.Status()
	if lastRun.IsZero() || err != nil {
		t.Errorf("Status() = %v, %d, %v; want a successful run", lastRun, managedCount, err)
	}
	if managedCount != 2 {
		t.Errorf("managed resources = %d, want 2", managedCount)
	}
}

func TestBuildResourceStampsOwnership(t *testing.T) {
	t.Parallel()

	for _, target := range []*docker.Target{
		proxyTarget("web", "web.example.com", 80),
		redirectTarget("web", "old.example.com", "web.example.com"),
		streamTarget("db", 5432, 5432),
		deadTarget("parked", "parked.example.com"),
	} {
		t.Run(string(target.Kind), func(t *testing.T) {
			resource := BuildResource(target)
			if resource == nil {
				t.Fatal("BuildResource() = nil")
			}
			if resource.Kind() != target.Kind {
				t.Errorf("Kind() = %s, want %s", resource.Kind(), target.Kind)
			}
			if !resource.ResourceMeta().ManagedBy(npm.ManagedByValue) {
				t.Error("resource must carry the ownership marker")
			}
			if got := resource.ResourceMeta().Container(); got != target.ContainerName {
				t.Errorf("meta container = %q, want %q", got, target.ContainerName)
			}
			if resource.ResourceKey() != target.Key() {
				t.Errorf("ResourceKey() = %q, target.Key() = %q", resource.ResourceKey(), target.Key())
			}
			if _, ok := resource.ResourceMeta()["letsencrypt_email"]; ok {
				t.Error("letsencrypt meta must be omitted when no certificate is requested")
			}
		})
	}
}

func TestBuildResourceLetsEncryptMeta(t *testing.T) {
	t.Parallel()

	target := proxyTarget("blog", "blog.example.com", 2368)
	target.CertificateID = npm.NewCertificate()
	target.LetsEncryptEmail = "admin@example.com"
	target.LetsEncryptAgree = true
	target.DNSChallenge = true
	target.DNSProvider = "cloudflare"
	target.DNSCredentials = "dns_cloudflare_api_token=secret"
	target.PropagationSeconds = 60

	meta := BuildResource(target).ResourceMeta()
	if meta["letsencrypt_email"] != "admin@example.com" || meta["letsencrypt_agree"] != true {
		t.Errorf("meta = %+v, want the letsencrypt label values", meta)
	}
	if meta["dns_provider"] != "cloudflare" || meta["propagation_seconds"] != 60 {
		t.Errorf("meta = %+v, want the DNS challenge settings", meta)
	}
}

// Resources created by an earlier release carry the old ownership marker.
// They must be adopted and re-stamped, and cleaned up when orphaned.
func TestReconcileHandlesLegacyOwnershipMarker(t *testing.T) {
	t.Parallel()

	legacy := func(id int, target *docker.Target) npm.Resource {
		r := BuildResource(target)
		r.SetResourceID(id)
		r.ResourceMeta()[npm.MetaManagedBy] = npm.LegacyManagedByValues[0]
		return r
	}

	t.Run("adopts and re-stamps", func(t *testing.T) {
		t.Parallel()

		target := proxyTarget("web", "web.example.com", 80)
		api := newFakeAPI(legacy(5, target))
		w := NewWorker(api, &fakeSource{targets: []*docker.Target{target}}, defaultOptions(), nil)

		res, err := w.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if res.Updated != 1 {
			t.Fatalf("Result = %+v, want the legacy resource to be updated", res)
		}

		live, _ := api.List(context.Background(), npm.KindProxy)
		if !live[0].ResourceMeta().ManagedBy(npm.ManagedByValue) {
			t.Errorf("meta = %+v, want the new ownership marker", live[0].ResourceMeta())
		}
	})

	t.Run("deletes legacy orphans", func(t *testing.T) {
		t.Parallel()

		api := newFakeAPI(legacy(6, proxyTarget("gone", "gone.example.com", 80)))
		w := NewWorker(api, &fakeSource{}, defaultOptions(), nil)

		res, err := w.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("Reconcile() error = %v", err)
		}
		if res.Deleted != 1 {
			t.Errorf("Result = %+v, want the legacy orphan removed", res)
		}
	})
}
