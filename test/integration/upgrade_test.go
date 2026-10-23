//go:build integration

package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// TestUpgradeKeepsOwnership upgrades the server underneath the hosts this tool
// manages: hosts are created with NPM_IMAGE, then the npm container is replaced
// by NPM_UPGRADE_IMAGE on the same data volume, which runs the new release's
// database migrations - NPMplus' meta-to-columns migration among them, the one
// that empties the meta object of every host.
//
// Afterwards every host must still be recognised as ours, keep its group and
// keep being served, and the host of a removed container must still be
// deleted. The test leaves the stack on the upgraded image, so it runs on its
// own:
//
//	NPM_IMAGE=ghcr.io/zoeyvid/npmplus:latest \
//	NPM_UPGRADE_IMAGE=ghcr.io/zoeyvid/npmplus:develop \
//	go test -tags integration -run TestUpgradeKeepsOwnership ./test/integration/
func TestUpgradeKeepsOwnership(t *testing.T) {
	upgradeImage := strings.TrimSpace(os.Getenv("NPM_UPGRADE_IMAGE"))
	if upgradeImage == "" {
		t.Skip("set NPM_UPGRADE_IMAGE to the image to upgrade to")
	}

	plus := map[string]string{}
	if strings.Contains(strings.ToLower(npmImage), "npmplus") {
		plus["npm.proxy.group"] = "Upgrade"
	}
	keep := map[string]string{"npm.proxy.domains": "upgrade-keep.test", "npm.proxy.port": "80"}
	for k, v := range plus {
		keep[k] = v
	}
	startContainer(t, "it-upgrade-keep", keep)
	startContainer(t, "it-upgrade-gone", map[string]string{"npm.proxy.domains": "upgrade-gone.test", "npm.proxy.port": "80"})
	mustSync(t)

	before := client(t)
	requireHost(t, before, npm.KindProxy, "upgrade-keep.test")
	requireHost(t, before, npm.KindProxy, "upgrade-gone.test")

	upgrade(t, upgradeImage)

	after := client(t)
	t.Logf("after the upgrade: flavour %s, meta object %t", after.Flavour(), after.Dialect().Accepts(npm.KindProxy, "meta"))
	for _, key := range []string{"upgrade-keep.test", "upgrade-gone.test"} {
		host := requireHost(t, after, npm.KindProxy, key)
		if !npm.IsManaged(host.ResourceMeta()) || !host.OwnerMarked() {
			t.Fatalf("%s lost its owner in the upgrade: %+v", key, host.ResourceMeta())
		}
	}
	if len(plus) > 0 {
		if got := requireHost(t, after, npm.KindProxy, "upgrade-keep.test").ResourceMeta().Directory(); got != "Upgrade" {
			t.Errorf("group after the upgrade = %q, want Upgrade", got)
		}
	}

	if out := mustSync(t).Output; strings.Contains(out, "failed to update") || strings.Contains(out, "failed to create") {
		t.Fatalf("the first run after the upgrade failed to write:\n%s", out)
	}
	requireServed(t, "upgrade-keep.test")

	removeContainer("it-upgrade-gone")
	mustSync(t)
	requireNoHost(t, after, npm.KindProxy, "upgrade-gone.test")
	requireHost(t, after, npm.KindProxy, "upgrade-keep.test")
}

// upgrade replaces the npm container by the given image, keeping its volumes.
func upgrade(t *testing.T, image string) {
	t.Helper()

	previous := npmImage
	npmImage = image
	t.Cleanup(func() { npmImage = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if out, err := compose(ctx, "up", "-d", "--no-deps", "--pull", "always", "npm"); err != nil {
		t.Fatalf("upgrade the npm container to %s: %v\n%s", image, err, out)
	}
	if err := waitForAPI(ctx); err != nil {
		t.Fatalf("the upgraded npm api did not come up: %v\n%s", err, npmLogs())
	}
	// The API can answer before the migrations of the new release are done.
	waitFor(t, "a login after the upgrade", 3*time.Minute, func() bool {
		return login(ctx, npmIdentity, npmSecret) == nil
	})
}
