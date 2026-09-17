package syncer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// TestTypoDoesNotDeleteTheHost is the regression test for the worst failure
// this tool can have: one unparsable label must never take a production host
// down. The container is still there, its labels simply cannot be read, so its
// resources are left exactly as they are.
func TestTypoDoesNotDeleteTheHost(t *testing.T) {
	t.Parallel()

	target := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI(managed(1, target))
	src := newFakeSource(target)
	w := NewWorker(api, src, defaultOptions(), nil)

	if res, err := w.Reconcile(context.Background()); err != nil || res.Unchanged != 1 {
		t.Fatalf("Reconcile() = %+v, %v; want the host unchanged", res, err)
	}

	// The label now reads `npm.port=80a`: the container yields no targets and
	// is reported as protected.
	src.set()
	src.protect("web", "web-id")

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 0 {
		t.Errorf("Result = %+v, want no deletion", res)
	}
	if live, _ := api.List(context.Background(), npm.KindProxy); len(live) != 1 {
		t.Fatalf("the host was deleted: %d left", len(live))
	}
}

// A container that is merely stopped keeps its host; only the enabled flag
// follows it. Its id must survive, so a restart does not produce a new host.
func TestStoppedContainerDisablesInsteadOfDeleting(t *testing.T) {
	t.Parallel()

	running := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI(managed(7, running))
	src := newFakeSource(running)

	opts := defaultOptions()
	opts.OnStop = docker.OnStopDisable
	opts.StopGrace = 0 // no waiting in the test
	w := NewWorker(api, src, opts, nil)

	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Docker now reports the container as exited: no address, no ports.
	stopped := proxyTarget("web", "web.example.com", 80)
	stopped.Running = false
	stopped.ForwardHost = ""
	stopped.ForwardPort = 0
	src.set(stopped)

	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	live := api.get(npm.KindProxy, 7)
	if live == nil {
		t.Fatal("the host was deleted although the container only stopped")
	}
	if live.IsEnabled() {
		t.Error("the host should be disabled while its container is stopped")
	}
	host, ok := live.(*npm.ProxyHost)
	if !ok {
		t.Fatal("not a proxy host")
	}
	if host.ForwardHost != "web" || host.ForwardPort != 80 {
		t.Errorf("the configuration was overwritten with the empty state: %+v", host)
	}

	// Back up: the host is enabled again, same id.
	src.set(running)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if live := api.get(npm.KindProxy, 7); live == nil || !live.IsEnabled() {
		t.Errorf("the host should be enabled again, got %+v", live)
	}
}

// Within the grace period a stopped container is not touched at all, so a
// restart or a `docker compose up` recreate produces no disable/enable cycle.
func TestStopGraceSwallowsARestart(t *testing.T) {
	t.Parallel()

	running := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI(managed(7, running))
	src := newFakeSource(running)

	opts := defaultOptions()
	opts.StopGrace = time.Minute
	w := NewWorker(api, src, opts, nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	stopped := proxyTarget("web", "web.example.com", 80)
	stopped.Running = false
	src.set(stopped)

	before := api.callCount("set-enabled proxy 7 false")
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if api.callCount("set-enabled proxy 7 false") != before {
		t.Errorf("a container inside the grace period must not be disabled: %v", api.callList())
	}
}

// NPM_ON_STOP=delete is the opt-in that restores the old behaviour.
func TestOnStopDelete(t *testing.T) {
	t.Parallel()

	running := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI(managed(7, running))
	src := newFakeSource(running)

	opts := defaultOptions()
	opts.OnStop = docker.OnStopDelete
	opts.StopGrace = 0
	opts.DeleteGuard = 0 // the guard is tested on its own
	w := NewWorker(api, src, opts, nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	stopped := proxyTarget("web", "web.example.com", 80)
	stopped.Running = false
	src.set(stopped)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 1 {
		t.Errorf("Result = %+v, want the host deleted", res)
	}
}

// Two sync instances against one NPM: each one manages its own hosts and
// leaves the other's alone.
func TestInstancesDoNotDeleteEachOther(t *testing.T) {
	t.Parallel()

	mine := proxyTarget("mine", "mine.example.com", 80)
	theirs := proxyTarget("theirs", "theirs.example.com", 80)

	live := managed(1, mine)
	live.ResourceMeta()[npm.MetaInstance] = "instance-a"
	foreign := managed(2, theirs)
	foreign.ResourceMeta()[npm.MetaInstance] = "instance-b"

	api := newFakeAPI(live, foreign)
	opts := defaultOptions()
	opts.InstanceID = "instance-a"
	opts.DeleteGuard = 0
	w := NewWorker(api, newFakeSource(mine), opts, nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 0 {
		t.Errorf("Result = %+v, want the other instance's host untouched", res)
	}
	if api.get(npm.KindProxy, 2) == nil {
		t.Error("the host of instance-b was deleted")
	}
}

// A host of another instance is not updated either, even when a local
// container claims the same key.
func TestForeignInstanceHostIsNotUpdated(t *testing.T) {
	t.Parallel()

	target := proxyTarget("web", "web.example.com", 80)
	foreign := managed(3, target)
	foreign.ResourceMeta()[npm.MetaInstance] = "somewhere-else"

	api := newFakeAPI(foreign)
	opts := defaultOptions()
	opts.InstanceID = "here"
	w := NewWorker(api, newFakeSource(target), opts, nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Skipped != 1 || res.Updated != 0 || res.Created != 0 {
		t.Errorf("Result = %+v, want the foreign host skipped", res)
	}
}

// A host written before instance stamping existed is adopted once.
func TestUnstampedHostIsAdopted(t *testing.T) {
	t.Parallel()

	target := proxyTarget("web", "web.example.com", 80)
	old := managed(4, target)
	delete(old.ResourceMeta(), npm.MetaInstance)

	api := newFakeAPI(old)
	opts := defaultOptions()
	opts.InstanceID = "here"
	w := NewWorker(api, newFakeSource(target), opts, nil)

	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	live := api.get(npm.KindProxy, 4)
	if live == nil {
		t.Fatal("the host disappeared")
	}
	if got := live.ResourceMeta().Instance(); got != "here" {
		t.Errorf("instance = %q, want the host re-stamped", got)
	}
}

// The delete guard is the last line of defence: a run that would wipe the
// managed hosts is stopped and reported.
func TestDeleteGuardBlocksMassDeletion(t *testing.T) {
	t.Parallel()

	var seeds []npm.Resource
	var targets []*docker.Target
	for _, name := range []string{"a", "b", "c", "d"} {
		target := proxyTarget(name, name+".example.com", 80)
		targets = append(targets, target)
		seeds = append(seeds, managed(0, target))
	}

	api := newFakeAPI(seeds...)
	src := newFakeSource(targets...)
	opts := defaultOptions()
	opts.DeleteGuard = 0.5
	w := NewWorker(api, src, opts, nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	// Every label disappeared at once - a changed prefix, a filtered socket
	// proxy, NPM_EXPOSED_BY_DEFAULT=false. Whatever it was, it is not a
	// reason to empty NPM.
	src.set()
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 0 {
		t.Fatalf("Result = %+v, want the deletions refused", res)
	}
	if live, _ := api.List(context.Background(), npm.KindProxy); len(live) != 4 {
		t.Errorf("%d hosts left, want all four", len(live))
	}
	if status := w.Status(); !strings.Contains(status.Blocked, "every managed resource") {
		t.Errorf("Status().Blocked = %q, want the reason", status.Blocked)
	}
	if m := w.Metrics(); m.DeletionsBlocked != 4 {
		t.Errorf("blocked deletions = %d, want 4", m.DeletionsBlocked)
	}
}

// A small setup must still be able to delete: the guard only looks at the
// share once a run reaches DELETE_GUARD_MIN deletions.
func TestDeleteGuardAllowsSmallRuns(t *testing.T) {
	t.Parallel()

	target := proxyTarget("web", "web.example.com", 80)
	api := newFakeAPI(managed(1, target))
	src := newFakeSource(target)
	w := NewWorker(api, src, defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	src.set()
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 1 {
		t.Errorf("Result = %+v, want the single orphan deleted", res)
	}
}

// An empty container list means Docker (or the socket proxy) is not telling
// the truth, never that every container is gone.
func TestEmptyContainerListNeverDeletes(t *testing.T) {
	t.Parallel()

	var seeds []npm.Resource
	var targets []*docker.Target
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		target := proxyTarget(name, name+".example.com", 80)
		targets = append(targets, target)
		seeds = append(seeds, managed(0, target))
	}
	api := newFakeAPI(seeds...)
	src := newFakeSource(targets...)
	w := NewWorker(api, src, defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	src.set()
	src.containers = -1 // docker answered with an empty list
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 0 {
		t.Errorf("Result = %+v, want nothing deleted", res)
	}
}

// A changed LABEL_PREFIX turns every managed host into an orphan. That is a
// configuration mistake, not an instruction to delete.
func TestPrefixChangeBlocksDeletion(t *testing.T) {
	t.Parallel()

	var seeds []npm.Resource
	for _, name := range []string{"a", "b", "c"} {
		resource := managed(0, proxyTarget(name, name+".example.com", 80))
		resource.ResourceMeta()[npm.MetaPrefix] = "npm"
		seeds = append(seeds, resource)
	}

	api := newFakeAPI(seeds...)
	opts := defaultOptions()
	opts.LabelPrefix = "traefik" // the operator renamed the namespace
	w := NewWorker(api, newFakeSource(), opts, nil)

	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if res.Deleted != 0 {
		t.Fatalf("Result = %+v, want the deletions refused", res)
	}
	if status := w.Status(); !strings.Contains(status.Blocked, "label prefix") {
		t.Errorf("Status().Blocked = %q, want the prefix to be named", status.Blocked)
	}
}

// The readiness probe has to see a broken event stream: reconnects are silent,
// and a tool that only reacts on the periodic resync is not healthy.
func TestReadinessFollowsTheEventStream(t *testing.T) {
	t.Parallel()

	src := &streamingSource{fakeSource: newFakeSource()}
	w := NewWorker(newFakeAPI(), src, defaultOptions(), nil)
	if _, err := w.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	src.status = docker.StreamStatus{Connected: true, Since: time.Now()}
	if !w.Status().Ready() {
		t.Error("a connected stream must be ready")
	}

	src.status = docker.StreamStatus{Connected: false, Since: time.Now().Add(-10 * time.Second), LastError: "boom"}
	if !w.Status().Ready() {
		t.Error("a short reconnect must not flip the readiness probe")
	}

	src.status = docker.StreamStatus{Connected: false, Since: time.Now().Add(-10 * time.Minute), LastError: "boom"}
	status := w.Status()
	if status.Ready() {
		t.Error("a stream that has been down for ten minutes must not be ready")
	}
	if status.Stream.LastError != "boom" {
		t.Errorf("Stream.LastError = %q", status.Stream.LastError)
	}
}

// streamingSource is a fakeSource that also reports event stream health.
type streamingSource struct {
	*fakeSource
	status docker.StreamStatus
}

func (s *streamingSource) StreamStatus() docker.StreamStatus { return s.status }
