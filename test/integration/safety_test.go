//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// A typo in one label must never delete a production host. This is the
// end-to-end version of the regression test: a real container, a real API.
func TestTypoDoesNotDeleteTheHost(t *testing.T) {
	c := client(t)
	const domain = "typo.test"

	startContainer(t, "it-typo", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t)
	id := requireHost(t, c, npm.KindProxy, domain).ResourceID()

	// Recreate the container with a broken port label.
	removeContainer("it-typo")
	startContainer(t, "it-typo", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80a",
	})

	run := mustSync(t)
	host := requireHost(t, c, npm.KindProxy, domain)
	if host.ResourceID() != id {
		t.Fatalf("the host was recreated (%d -> %d)", id, host.ResourceID())
	}
	if !strings.Contains(run.Output, "protected") && !strings.Contains(run.Output, "could not be read") {
		t.Errorf("the run should say it protected the host:\n%s", run.Output)
	}
	requireServed(t, domain)
}

// Changing LABEL_PREFIX turns every managed host into an orphan. That is a
// configuration mistake, and the tool has to refuse to act on it.
func TestPrefixChangeDoesNotDelete(t *testing.T) {
	c := client(t)

	for _, name := range []string{"it-prefix-a", "it-prefix-b", "it-prefix-c"} {
		startContainer(t, name, map[string]string{
			"npm.proxy.domains": name + ".test",
			"npm.proxy.port":    "80",
		})
	}
	mustSync(t)
	for _, name := range []string{"it-prefix-a", "it-prefix-b", "it-prefix-c"} {
		requireHost(t, c, npm.KindProxy, name+".test")
	}

	run := syncOnce(t, "LABEL_PREFIX=other")
	for _, name := range []string{"it-prefix-a", "it-prefix-b", "it-prefix-c"} {
		requireHost(t, c, npm.KindProxy, name+".test")
	}
	if !strings.Contains(run.Output, "refusing to delete") {
		t.Errorf("the run should refuse to delete:\n%s", run.Output)
	}
}

// Two instances against one NPM: neither may delete the other's hosts.
func TestTwoInstancesCoexist(t *testing.T) {
	c := client(t)

	startContainer(t, "it-instance-a", map[string]string{
		"npm.proxy.domains": "instance-a.test",
		"npm.proxy.port":    "80",
	})
	mustSync(t, "SYNC_INSTANCE_ID=instance-a")
	idA := requireHost(t, c, npm.KindProxy, "instance-a.test").ResourceID()

	// The second instance sees a container of its own - and the host of the
	// first one, which is none of its business.
	removeContainer("it-instance-a")
	startContainer(t, "it-instance-b", map[string]string{
		"npm.proxy.domains": "instance-b.test",
		"npm.proxy.port":    "80",
	})
	mustSync(t, "SYNC_INSTANCE_ID=instance-b")

	if host := findByKey(resources(t, c, npm.KindProxy), "instance-a.test"); host == nil {
		t.Fatal("instance-b deleted the host of instance-a")
	} else if host.ResourceID() != idA {
		t.Errorf("the host of instance-a was replaced (%d -> %d)", idA, host.ResourceID())
	}
	requireHost(t, c, npm.KindProxy, "instance-b.test")

	// Cleanup: each instance removes its own.
	removeContainer("it-instance-b")
	mustSync(t, "SYNC_INSTANCE_ID=instance-b")
	mustSync(t, "SYNC_INSTANCE_ID=instance-a", "DELETE_GUARD=off")
	requireNoHost(t, c, npm.KindProxy, "instance-a.test")
}

// DRY_RUN has to be exactly that: it reports and changes nothing.
func TestDryRunChangesNothing(t *testing.T) {
	c := client(t)
	const domain = "dryrun.test"

	startContainer(t, "it-dryrun", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})

	run := mustSync(t, "DRY_RUN=true")
	if !strings.Contains(run.Output, "[dry-run] would create resource") {
		t.Errorf("the dry run should announce the create:\n%s", run.Output)
	}
	requireNoHost(t, c, npm.KindProxy, domain)

	mustSync(t)
	requireHost(t, c, npm.KindProxy, domain)

	// And a dry-run update reports a field diff instead of writing.
	removeContainer("it-dryrun")
	startContainer(t, "it-dryrun", map[string]string{
		"npm.proxy.domains":    domain,
		"npm.proxy.port":       "80",
		"npm.proxy.websockets": "false",
	})
	update := mustSync(t, "DRY_RUN=true")
	if !strings.Contains(update.Output, "would update resource") || !strings.Contains(update.Output, "allow_websocket_upgrade") {
		t.Errorf("the dry run should show the field diff:\n%s", update.Output)
	}
	host := requireHost(t, c, npm.KindProxy, domain)
	if proxy, ok := host.(*npm.ProxyHost); ok && !proxy.AllowWebsocketUpgrade {
		t.Error("the dry run wrote the change")
	}
}

// An NPM that is unreachable mid-run must not produce any deletion, and the
// next run must reconcile cleanly.
func TestNPMRestartDoesNotDelete(t *testing.T) {
	c := client(t)
	const domain = "restart.test"

	startContainer(t, "it-npmrestart", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t)
	id := requireHost(t, c, npm.KindProxy, domain).ResourceID()

	docker(t, "restart", "npmsync-it-npm")
	// While NPM is down the run fails - and fails without deleting anything.
	_ = syncOnce(t)

	if err := waitForAPI(context.Background()); err != nil {
		t.Fatalf("npm did not come back: %v", err)
	}
	after := client(t)
	mustSync(t)

	host := requireHost(t, after, npm.KindProxy, domain)
	if host.ResourceID() != id {
		t.Errorf("the host was recreated across the restart (%d -> %d)", id, host.ResourceID())
	}
	_ = c
}

// The socket proxy is the recommended deployment, so the tool has to work
// through it - including the endpoints it denies.
func TestThroughSocketProxy(t *testing.T) {
	c := client(t)
	const domain = "socketproxy.test"

	startContainer(t, "it-socketproxy", map[string]string{
		"npm.proxy.domains": domain,
		"npm.proxy.port":    "80",
	})
	mustSync(t, "DOCKER_HOST=tcp://127.0.0.1:"+env("SOCKET_PROXY_PORT", "8182"))
	requireHost(t, c, npm.KindProxy, domain)
}

// Wrong credentials must fail loudly, and a password from a file must work.
func TestCredentials(t *testing.T) {
	startContainer(t, "it-credentials", map[string]string{
		"npm.proxy.domains": "credentials.test",
		"npm.proxy.port":    "80",
	})

	wrong := syncOnce(t, "NPM_SECRET=definitely-wrong")
	if !wrong.Failed() {
		t.Errorf("a wrong password should fail the run:\n%s", wrong.Output)
	}

	file := t.TempDir() + "/secret"
	if err := os.WriteFile(file, []byte(npmSecret+"\n"), 0o600); err != nil {
		t.Fatalf("write the secret file: %v", err)
	}
	mustSync(t, "NPM_SECRET=", "NPM_SECRET_FILE="+file)

	c := client(t)
	requireHost(t, c, npm.KindProxy, "credentials.test")
}

// validate never writes, and it reports a broken label set with a non-zero
// exit code - which is what makes it usable before a deploy.
func TestValidateSubcommand(t *testing.T) {
	c := client(t)

	startContainer(t, "it-validate", map[string]string{
		"npm.proxy.domains": "validate.test",
		"npm.proxy.port":    "80",
	})
	run := validate(t)
	if run.Failed() {
		t.Fatalf("validate failed on a healthy container: %v\n%s", run.Err, run.Output)
	}
	requireNoHost(t, c, npm.KindProxy, "validate.test")

	startContainer(t, "it-validate-broken", map[string]string{
		"npm.proxy.domains": "validate-broken.test",
		"npm.proxy.port":    "not-a-port",
	})
	broken := validate(t)
	if !broken.Failed() {
		t.Errorf("validate should fail on a broken label set:\n%s", broken.Output)
	}
	if !strings.Contains(broken.Output, "not-a-port") && !strings.Contains(broken.Output, "is not a number") {
		t.Errorf("validate should name the problem:\n%s", broken.Output)
	}
}
