//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// The flavour detection decides which request dialect is used. It is a
// heuristic, so it deserves a test against the real thing.
func TestFlavourDetection(t *testing.T) {
	c := client(t)

	detected := c.Flavour()
	if detected != npm.FlavourNPM && detected != npm.FlavourNPMplus {
		t.Fatalf("Flavour() = %s, want a concrete dialect", detected)
	}

	// The image name is the only ground truth available here.
	wantPlus := strings.Contains(strings.ToLower(npmImage), "npmplus")
	if wantPlus && detected != npm.FlavourNPMplus {
		t.Errorf("Flavour() = %s for image %s", detected, npmImage)
	}
	if !wantPlus && detected != npm.FlavourNPM {
		t.Errorf("Flavour() = %s for image %s", detected, npmImage)
	}
	t.Logf("detected %s for %s (server version %q)", detected, npmImage, c.ServerVersion())
}

// NPMplus-only labels must work on NPMplus and must not break anything on
// upstream NPM - they are dropped there, with one warning, and the host is
// still created and served.
func TestNPMplusOnlyFields(t *testing.T) {
	c := client(t)
	const domain = "plusfields.test"

	startContainer(t, "it-plus", map[string]string{
		"npm.proxy.domains":         domain,
		"npm.proxy.port":            "80",
		"npm.proxy.noindex":         "true",
		"npm.proxy.x_frame_options": "sameorigin",
		"npm.proxy.ssl.http3":       "true",
	})
	run := mustSync(t)

	host := requireHost(t, c, npm.KindProxy, domain)
	proxy, ok := host.(*npm.ProxyHost)
	if !ok {
		t.Fatalf("unexpected resource %T", host)
	}

	if c.Flavour() == npm.FlavourNPMplus {
		if !proxy.NoIndex || proxy.XFrameOptions != "SAMEORIGIN" {
			t.Errorf("npmplus fields were not stored: %+v", proxy)
		}
		if strings.Contains(run.Output, "npmplus-only settings ignored") {
			t.Errorf("npmplus must not warn about its own fields:\n%s", run.Output)
		}
	} else if !strings.Contains(run.Output, "npmplus-only settings ignored") {
		t.Errorf("upstream npm should report the dropped fields once:\n%s", run.Output)
	}
	requireServed(t, domain)

	// Whatever the flavour: the second run must be a no-op.
	second := mustSync(t)
	if strings.Contains(second.Output, "updated resource") {
		t.Errorf("the configuration does not converge on %s:\n%s", c.Flavour(), second.Output)
	}
}

// Custom locations are the most complex payload the tool builds.
func TestCustomLocations(t *testing.T) {
	c := client(t)
	const domain = "locations.test"

	startContainer(t, "it-locations", map[string]string{
		"npm.proxy.domains":         domain,
		"npm.proxy.port":            "80",
		"npm.proxy.location.0.path": "/api",
		"npm.proxy.location.0.port": "80",
	})
	mustSync(t)

	host := requireHost(t, c, npm.KindProxy, domain)
	proxy, ok := host.(*npm.ProxyHost)
	if !ok {
		t.Fatalf("unexpected resource %T", host)
	}
	if len(proxy.Locations) != 1 || proxy.Locations[0].Path != "/api" {
		t.Fatalf("locations = %+v, want the /api block", proxy.Locations)
	}
	requireServed(t, domain)

	second := mustSync(t)
	if strings.Contains(second.Output, "updated resource") {
		t.Errorf("a host with locations does not converge:\n%s", second.Output)
	}
}

// The tool must not fight over a domain another host already serves.
func TestDomainConflictIsReported(t *testing.T) {
	c := client(t)
	ctx := context.Background()

	foreign := &npm.ProxyHost{
		DomainNames:   []string{"conflict-a.test", "conflict-b.test"},
		ForwardScheme: "http", ForwardHost: "npmsync-it-whoami", ForwardPort: 80,
		Meta: npm.Meta{},
	}
	created, err := c.Create(ctx, foreign)
	if err != nil {
		t.Fatalf("create the foreign host: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(ctx, npm.KindProxy, created.ResourceID()) })

	startContainer(t, "it-conflict", map[string]string{
		"npm.proxy.domains": "conflict-b.test",
		"npm.proxy.port":    "80",
	})
	run := mustSync(t)

	if !strings.Contains(run.Output, "domain already used by another host") {
		t.Errorf("the conflict should be reported in our own words:\n%s", run.Output)
	}
	if host := findByKey(resources(t, c, npm.KindProxy), "conflict-b.test"); host != nil {
		t.Errorf("a conflicting host was created anyway: %+v", host)
	}
}
