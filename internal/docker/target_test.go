package docker

import (
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

func multiHomed() Container {
	return Container{
		ID:   "app-id",
		Name: "app",
		Networks: []Network{
			{Name: "backend", IPv4: "172.31.0.9"},
			{Name: "npm-frontend", IPv4: "172.30.0.9"},
		},
	}
}

// TestIPAddressPrefersTheNamedNetwork is the happy path: the operator named a
// network and the container is on it.
func TestIPAddressPrefersTheNamedNetwork(t *testing.T) {
	t.Parallel()

	got := multiHomed().IPAddress([]string{"npm-frontend"}, true)
	if got != "172.30.0.9" {
		t.Errorf("IPAddress() = %q, want the address on npm-frontend", got)
	}
}

// TestStrictNetworkSkipsAForeignContainer covers the case that produced silent
// 502s: without strict mode the resolver falls through to whatever other
// network the container happens to be on, and NPM cannot route there.
func TestStrictNetworkSkipsAForeignContainer(t *testing.T) {
	t.Parallel()

	c := Container{
		ID: "lonely-id", Name: "lonely",
		Networks: []Network{{Name: "some-other-net", IPv4: "172.31.0.4"}},
	}

	if got := c.IPAddress([]string{"npm-frontend"}, false); got != "172.31.0.4" {
		t.Errorf("non-strict IPAddress() = %q, want the fallback address", got)
	}
	if got := c.IPAddress([]string{"npm-frontend"}, true); got != "" {
		t.Errorf("strict IPAddress() = %q, want no address at all", got)
	}
	if got := c.ResolveHost(ParseOptions{ResolveIP: true, PreferNetworks: []string{"npm-frontend"}, StrictNetworks: true}); got != "" {
		t.Errorf("strict ResolveHost() = %q; the container name is just as unreachable", got)
	}
}

// TestStrictNetworkErrorNamesTheNetworks: a skipped container must say why.
func TestStrictNetworkErrorNamesTheNetworks(t *testing.T) {
	t.Parallel()

	c := Container{
		ID: "lonely-id", Name: "lonely",
		Labels: map[string]string{
			"npm.enable":        "true",
			"npm.proxy.domains": "lonely.example.com",
			"npm.proxy.port":    "80",
		},
		Networks: []Network{{Name: "some-other-net", IPv4: "172.31.0.4"}},
	}

	res := Parse(c, ParseOptions{
		Prefix: "npm", ResolveIP: true, ExposedByDefault: true,
		PreferNetworks: []string{"npm-frontend"}, StrictNetworks: true,
	})
	if len(res.Targets) != 0 {
		t.Fatalf("Parse() returned %d targets, want none", len(res.Targets))
	}
	if len(res.Errors) != 1 {
		t.Fatalf("Parse() errors = %v, want exactly one", res.Errors)
	}
	msg := res.Errors[0].Error()
	for _, want := range []string{"npm-frontend", "some-other-net", "forward_host"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

// TestExplicitForwardHostBeatsStrictNetworks: an explicit upstream is always
// honoured, whatever networks the container is on.
func TestExplicitForwardHostBeatsStrictNetworks(t *testing.T) {
	t.Parallel()

	c := Container{
		ID: "lonely-id", Name: "lonely",
		Labels: map[string]string{
			"npm.enable":             "true",
			"npm.proxy.domains":      "lonely.example.com",
			"npm.proxy.port":         "80",
			"npm.proxy.forward_host": "10.1.2.3",
		},
		Networks: []Network{{Name: "some-other-net", IPv4: "172.31.0.4"}},
	}

	res := Parse(c, ParseOptions{
		Prefix: "npm", ResolveIP: true, ExposedByDefault: true,
		PreferNetworks: []string{"npm-frontend"}, StrictNetworks: true,
	})
	if len(res.Errors) != 0 {
		t.Fatalf("Parse() errors = %v, want none", res.Errors)
	}
	if len(res.Targets) != 1 || res.Targets[0].ForwardHost != "10.1.2.3" {
		t.Fatalf("Parse() = %+v, want the explicit upstream", res.Targets)
	}
}

func TestDedupeNetworks(t *testing.T) {
	t.Parallel()

	got := DedupeNetworks([]string{"npm-frontend", "npm-frontend", "socket-proxy", "", "socket-proxy"})
	want := []string{"npm-frontend", "socket-proxy"}
	if len(got) != len(want) {
		t.Fatalf("DedupeNetworks() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("DedupeNetworks() = %v, want %v", got, want)
		}
	}
}

// TestAccessListLabels covers both spellings plus the location default.
func TestAccessListLabels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		labels   map[string]string
		wantIDs  []int
		wantType string
	}{
		{
			name:     "no labels means public",
			labels:   map[string]string{},
			wantType: npm.AccessListPublic,
		},
		{
			name:     "legacy single id",
			labels:   map[string]string{"npm.proxy.access_list_id": "2"},
			wantIDs:  []int{2},
			wantType: npm.AccessListCustom,
		},
		{
			name:     "npmplus list",
			labels:   map[string]string{"npm.proxy.access_list_ids": "2,5"},
			wantIDs:  []int{2, 5},
			wantType: npm.AccessListCustom,
		},
		{
			name:     "explicit public wins over an id",
			labels:   map[string]string{"npm.proxy.access_list_ids": "2", "npm.proxy.access_list_type": "public"},
			wantType: npm.AccessListPublic,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			labels := map[string]string{
				"npm.enable":        "true",
				"npm.proxy.domains": "app.example.com",
				"npm.proxy.port":    "80",
			}
			for k, v := range tt.labels {
				labels[k] = v
			}
			res := Parse(Container{ID: "app-id", Name: "app", Labels: labels}, ParseOptions{Prefix: "npm", ExposedByDefault: true})
			if len(res.Errors) != 0 {
				t.Fatalf("Parse() errors = %v", res.Errors)
			}
			if len(res.Targets) != 1 {
				t.Fatalf("Parse() = %d targets, want 1", len(res.Targets))
			}
			got := res.Targets[0]
			if got.AccessListType != tt.wantType {
				t.Errorf("AccessListType = %q, want %q", got.AccessListType, tt.wantType)
			}
			if len(got.AccessListIDs) != len(tt.wantIDs) {
				t.Fatalf("AccessListIDs = %v, want %v", got.AccessListIDs, tt.wantIDs)
			}
			for i := range tt.wantIDs {
				if got.AccessListIDs[i] != tt.wantIDs[i] {
					t.Fatalf("AccessListIDs = %v, want %v", got.AccessListIDs, tt.wantIDs)
				}
			}
		})
	}
}

// TestLocationAccessListDefaultsToGlobal: NPMplus requires the two fields on
// every location, and "global" is the value that means "inherit from the host".
func TestLocationAccessListDefaultsToGlobal(t *testing.T) {
	t.Parallel()

	res := Parse(Container{
		ID: "app-id", Name: "app",
		Labels: map[string]string{
			"npm.enable":                           "true",
			"npm.proxy.domains":                    "app.example.com",
			"npm.proxy.port":                       "80",
			"npm.proxy.location.0.path":            "/api",
			"npm.proxy.location.1.path":            "/admin",
			"npm.proxy.location.1.access_list_ids": "4",
		},
	}, ParseOptions{Prefix: "npm", ExposedByDefault: true})
	if len(res.Errors) != 0 {
		t.Fatalf("Parse() errors = %v", res.Errors)
	}
	locations := res.Targets[0].Locations
	if len(locations) != 2 {
		t.Fatalf("locations = %d, want 2", len(locations))
	}
	if locations[0].AccessListType != npm.AccessListGlobal || len(locations[0].AccessListIDs) != 0 {
		t.Errorf("location 0 = %+v, want the inherited (global) access list", locations[0])
	}
	if locations[1].AccessListType != npm.AccessListCustom || len(locations[1].AccessListIDs) != 1 {
		t.Errorf("location 1 = %+v, want a custom access list", locations[1])
	}
}
