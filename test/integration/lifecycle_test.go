//go:build integration

package integration

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// TestProxyHostLifecycle is the core promise of the tool: a label creates a
// host, nginx serves it, a changed label updates it and a removed container
// takes it away again.
func TestProxyHostLifecycle(t *testing.T) {
	c := client(t)
	const domain = "lifecycle.test"

	startContainer(t, "it-lifecycle", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t)

	host := requireHost(t, c, npm.KindProxy, domain)
	proxy, ok := host.(*npm.ProxyHost)
	if !ok {
		t.Fatalf("unexpected resource %T", host)
	}
	if proxy.ForwardPort != 80 {
		t.Errorf("forward_port = %d, want 80", proxy.ForwardPort)
	}
	if !npm.IsManaged(proxy.Meta) {
		t.Errorf("meta = %+v, want the ownership marker", proxy.Meta)
	}
	requireServed(t, domain)

	// A second run must change nothing at all.
	second := mustSync(t)
	if strings.Contains(second.Output, "updated resource") {
		t.Errorf("the second run updated a converged host:\n%s", second.Output)
	}

	// Change a label: same host id, new configuration.
	id := host.ResourceID()
	removeContainer("it-lifecycle")
	startContainer(t, "it-lifecycle", map[string]string{
		"npm.proxy.domains":         domain,
		"npm.proxy.port":            "80",
		"npm.proxy.websockets":      "false",
		"npm.proxy.advanced_config": "add_header X-Integration 1;",
	})
	mustSync(t)

	updated := requireHost(t, c, npm.KindProxy, domain)
	if updated.ResourceID() != id {
		t.Errorf("the host was recreated (%d -> %d) instead of updated", id, updated.ResourceID())
	}
	if proxy, ok := updated.(*npm.ProxyHost); ok && bool(proxy.AllowWebsocketUpgrade) {
		t.Error("websockets should be off after the label change")
	}

	// Remove the container: the host goes with it.
	removeContainer("it-lifecycle")
	mustSync(t)
	requireNoHost(t, c, npm.KindProxy, domain)
}

// Every resource kind has to survive a create/update/delete round trip against
// the real API - the schemas differ per kind and per flavour.
func TestAllKindsRoundTrip(t *testing.T) {
	c := client(t)

	startContainer(t, "it-kinds", map[string]string{
		"npm.proxy.domains":             "kinds-proxy.test",
		"npm.proxy.port":                "80",
		"npm.1.redirect.domains":        "kinds-redirect.test",
		"npm.1.redirect.forward_domain": "kinds-proxy.test",
		"npm.1.redirect.http_code":      "308",
		"npm.2.stream.incoming_port":    streamPort,
		"npm.2.stream.forward_port":     "80",
		"npm.3.404.domains":             "kinds-parked.test",
	})
	mustSync(t)

	requireHost(t, c, npm.KindProxy, "kinds-proxy.test")
	requireHost(t, c, npm.KindRedirect, "kinds-redirect.test")
	requireHost(t, c, npm.KindDead, "kinds-parked.test")
	requireHost(t, c, npm.KindStream, streamPort)

	// The redirect answers with the status code the label asked for.
	requireStatus(t, "kinds-redirect.test", http.StatusPermanentRedirect)
	// The 404 host answers, but not with the backend.
	requireStatus(t, "kinds-parked.test", http.StatusNotFound)

	removeContainer("it-kinds")
	mustSync(t)
	for kind, key := range map[npm.Kind]string{
		npm.KindProxy:    "kinds-proxy.test",
		npm.KindRedirect: "kinds-redirect.test",
		npm.KindDead:     "kinds-parked.test",
		npm.KindStream:   streamPort,
	} {
		requireNoHost(t, c, kind, key)
	}
}

// A container that stops must not lose its host: it is disabled, and enabled
// again when the container comes back - with the same id.
func TestStopStartKeepsTheHost(t *testing.T) {
	c := client(t)
	const domain = "stopstart.test"

	startContainer(t, "it-stopstart", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t, "NPM_STOP_GRACE=0")
	id := requireHost(t, c, npm.KindProxy, domain).ResourceID()

	docker(t, "stop", "it-stopstart")
	mustSync(t, "NPM_STOP_GRACE=0")

	stopped := requireHost(t, c, npm.KindProxy, domain)
	if stopped.ResourceID() != id {
		t.Fatalf("the host was recreated while the container was stopped (%d -> %d)", id, stopped.ResourceID())
	}
	if stopped.IsEnabled() {
		t.Error("the host should be disabled while its container is stopped")
	}

	docker(t, "start", "it-stopstart")
	mustSync(t, "NPM_STOP_GRACE=0")

	restarted := requireHost(t, c, npm.KindProxy, domain)
	if restarted.ResourceID() != id || !restarted.IsEnabled() {
		t.Errorf("after the restart: id %d enabled %t, want id %d enabled", restarted.ResourceID(), restarted.IsEnabled(), id)
	}
	requireServed(t, domain)
}

// The grace period exists so a restart or a recreate produces no churn at all.
func TestStopGraceSwallowsTheRestart(t *testing.T) {
	c := client(t)
	const domain = "grace.test"

	startContainer(t, "it-grace", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t)
	id := requireHost(t, c, npm.KindProxy, domain).ResourceID()

	docker(t, "restart", "it-grace")
	run := mustSync(t, "NPM_STOP_GRACE=5m")

	host := requireHost(t, c, npm.KindProxy, domain)
	if host.ResourceID() != id || !host.IsEnabled() {
		t.Errorf("a restart inside the grace period changed the host: %+v", host)
	}
	if strings.Contains(run.Output, "toggled resource") {
		t.Errorf("the grace period should prevent any toggle:\n%s", run.Output)
	}
}

// Renaming a container keeps the host: the identity is the domain, not the
// container.
func TestRenameKeepsTheHost(t *testing.T) {
	c := client(t)
	const domain = "rename.test"

	startContainer(t, "it-rename", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t)
	id := requireHost(t, c, npm.KindProxy, domain).ResourceID()

	docker(t, "rename", "it-rename", "it-renamed")
	t.Cleanup(func() { removeContainer("it-renamed") })
	mustSync(t)

	host := requireHost(t, c, npm.KindProxy, domain)
	if host.ResourceID() != id {
		t.Errorf("the rename recreated the host (%d -> %d)", id, host.ResourceID())
	}
	if got := host.ResourceMeta().Container(); got != "it-renamed" {
		t.Errorf("meta container = %q, want the new name", got)
	}
}

// A host somebody created by hand for a labelled domain is adopted, and one
// for a domain nobody labels is never touched.
func TestAdoptionLeavesForeignHostsAlone(t *testing.T) {
	c := client(t)
	ctx := context.Background()

	manual := &npm.ProxyHost{
		DomainNames: []string{"manual.test"}, ForwardScheme: "http",
		ForwardHost: "npmsync-it-whoami", ForwardPort: 80, Meta: npm.Meta{},
	}
	created, err := c.Create(ctx, manual)
	if err != nil {
		t.Fatalf("create the manual host: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, npm.KindProxy, created.ResourceID()) })

	adopted := &npm.ProxyHost{
		DomainNames: []string{"adopt.test"}, ForwardScheme: "http",
		ForwardHost: "npmsync-it-whoami", ForwardPort: 80, Meta: npm.Meta{},
	}
	adoptedLive, err := c.Create(ctx, adopted)
	if err != nil {
		t.Fatalf("create the host to adopt: %v", err)
	}

	startContainer(t, "it-adopt", map[string]string{
		"npm.proxy.domains": "adopt.test",
		"npm.proxy.port":    "80",
	})
	mustSync(t)

	taken := requireHost(t, c, npm.KindProxy, "adopt.test")
	if taken.ResourceID() != adoptedLive.ResourceID() {
		t.Errorf("the existing host was not adopted (%d -> %d)", adoptedLive.ResourceID(), taken.ResourceID())
	}
	if !npm.IsManaged(taken.ResourceMeta()) {
		t.Error("the adopted host should carry our marker")
	}

	untouched := requireHost(t, c, npm.KindProxy, "manual.test")
	if npm.IsManaged(untouched.ResourceMeta()) {
		t.Error("a host nobody labels must never be stamped as ours")
	}

	// And it survives the removal of every labelled container.
	removeContainer("it-adopt")
	mustSync(t)
	requireHost(t, c, npm.KindProxy, "manual.test")
}

// Hosts created by Redth/npm-docker-sync are left alone until the migration is
// asked for explicitly.
func TestRedthMigration(t *testing.T) {
	c := client(t)
	ctx := context.Background()

	redth := &npm.ProxyHost{
		DomainNames: []string{"redth.test"}, ForwardScheme: "http",
		ForwardHost: "npmsync-it-whoami", ForwardPort: 80,
		Meta: npm.Meta{npm.MetaManagedBy: npm.RedthManagedByValue, "container_id": "abc123"},
	}
	created, err := c.Create(ctx, redth)
	if err != nil {
		t.Fatalf("create the redth host: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, npm.KindProxy, created.ResourceID()) })

	startContainer(t, "it-redth", map[string]string{
		"npm.proxy.domains": "redth.test",
		"npm.proxy.port":    "80",
	})

	mustSync(t)
	host := requireHost(t, c, npm.KindProxy, "redth.test")
	if !npm.IsRedth(host.ResourceMeta()) {
		t.Fatalf("the redth host was taken over without being asked: %+v", host.ResourceMeta())
	}

	mustSync(t, "MIGRATE_FROM_REDTH=true")
	migrated := requireHost(t, c, npm.KindProxy, "redth.test")
	if !npm.IsManaged(migrated.ResourceMeta()) {
		t.Errorf("meta = %+v, want our marker after the migration", migrated.ResourceMeta())
	}
	if migrated.ResourceMeta()["container_id"] != "abc123" {
		t.Errorf("meta = %+v, want redth's bookkeeping preserved", migrated.ResourceMeta())
	}
}

// The daemon path: no `sync --once`, but the event stream and the debouncer.
func TestDaemonReactsToEvents(t *testing.T) {
	c := client(t)
	const domain = "daemon.test"

	stop := startDaemon(t, "DEBOUNCE_INTERVAL=1s", "HEALTH_ADDR=127.0.0.1:18080")
	defer stop()

	startContainer(t, "it-daemon", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	waitFor(t, "the daemon to create the host", 60*time.Second, func() bool {
		return findByKey(resources(t, c, npm.KindProxy), domain) != nil
	})
	requireServed(t, domain)

	// The readiness endpoint has to be green while the stream is connected.
	resp, err := http.Get("http://127.0.0.1:18080/readyz") //nolint:noctx // short test request
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", resp.StatusCode)
	}

	removeContainer("it-daemon")
	waitFor(t, "the daemon to delete the host", 60*time.Second, func() bool {
		return findByKey(resources(t, c, npm.KindProxy), domain) == nil
	})
}
