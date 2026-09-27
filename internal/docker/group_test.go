package docker

import (
	"strings"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// TestGroupLabel covers the three states of the group label: unset, named and
// explicitly cleared. The distinction is what keeps a group picked in the
// NPMplus UI from being wiped by a container that says nothing about groups.
func TestGroupLabel(t *testing.T) {
	t.Parallel()

	base := map[string]string{"npm.proxy.domains": "app.example.com", "npm.proxy.port": "8080"}

	tests := []struct {
		name  string
		label string
		value string
		want  *string
	}{
		{name: "unset", want: nil},
		{name: "canonical", label: "npm.proxy.group", value: "Production", want: pointer("Production")},
		{name: "directory alias", label: "npm.proxy.directory", value: "Production", want: pointer("Production")},
		{name: "folder alias", label: "npm.proxy.folder", value: "Home Lab", want: pointer("Home Lab")},
		{name: "trimmed", label: "npm.proxy.group", value: "  Production  ", want: pointer("Production")},
		{name: "none clears", label: "npm.proxy.group", value: "none", want: pointer("")},
		{name: "off clears", label: "npm.proxy.group", value: "OFF", want: pointer("")},
		// An empty label value is not "ungrouped": it is how a compose file
		// spells "I did not decide", and it has to fall through to the default.
		{name: "empty value", label: "npm.proxy.group", value: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			labels := map[string]string{}
			for k, v := range base {
				labels[k] = v
			}
			if tt.label != "" {
				labels[tt.label] = tt.value
			}

			res := Parse(withLabels("app", labels), opts("npm"))
			if len(res.Errors) > 0 {
				t.Fatalf("parse errors: %v", res.Errors)
			}
			if len(res.Targets) != 1 {
				t.Fatalf("got %d targets, want 1", len(res.Targets))
			}
			assertGroup(t, res.Targets[0].Group, tt.want)
		})
	}
}

// TestGroupAppliesToEveryKind: grouping is a property of the host list, and
// NPMplus groups all four of its lists.
func TestGroupAppliesToEveryKind(t *testing.T) {
	t.Parallel()

	c := withLabels("app", map[string]string{
		"npm.proxy.domains":          "app.example.com",
		"npm.proxy.port":             "8080",
		"npm.proxy.group":            "Apps",
		"npm.1.redirect.domains":     "old.example.com",
		"npm.1.redirect.to":          "app.example.com",
		"npm.1.redirect.group":       "Apps",
		"npm.2.stream.incoming_port": "5432",
		"npm.2.stream.group":         "Databases",
		"npm.3.404.domains":          "parked.example.com",
		"npm.3.404.group":            "Parked",
	})

	res := Parse(c, opts("npm"))
	if len(res.Errors) > 0 {
		t.Fatalf("parse errors: %v", res.Errors)
	}
	want := map[npm.Kind]string{
		npm.KindProxy:    "Apps",
		npm.KindRedirect: "Apps",
		npm.KindStream:   "Databases",
		npm.KindDead:     "Parked",
	}
	if len(res.Targets) != len(want) {
		t.Fatalf("got %d targets, want %d", len(res.Targets), len(want))
	}
	for _, target := range res.Targets {
		assertGroup(t, target.Group, pointer(want[target.Kind]))
	}
}

// TestGroupNameTooLong: NPMplus truncates a longer name when it moves the
// value into a column of its own, so it is refused with the label named
// instead of being silently cut in half.
func TestGroupNameTooLong(t *testing.T) {
	t.Parallel()

	c := withLabels("app", map[string]string{
		"npm.proxy.domains": "app.example.com",
		"npm.proxy.port":    "8080",
		"npm.proxy.group":   strings.Repeat("g", npm.MaxDirectoryLength+1),
	})

	res := Parse(c, opts("npm"))
	if len(res.Errors) != 1 {
		t.Fatalf("got %d errors, want 1: %v", len(res.Errors), res.Errors)
	}
	if got := res.Errors[0].Error(); !strings.Contains(got, "npm.proxy.group") {
		t.Errorf("error = %q, want it to name the label", got)
	}
	if len(res.Targets) != 0 {
		t.Errorf("got %d targets, want the definition to be dropped", len(res.Targets))
	}
}

// TestGroupFromEnvironment: a group can be set once for every container, and a
// single container opts back out with `none`.
func TestGroupFromEnvironment(t *testing.T) {
	t.Parallel()

	env := map[string]string{"NPM_DEFAULT_GROUP": "Docker"}
	defaults, warnings, err := fields.LoadDefaults(func(name string) string { return env[name] }, nil)
	if err != nil {
		t.Fatalf("LoadDefaults: %v", err)
	}
	if len(warnings) > 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}

	options := opts("npm")
	options.Defaults = defaults

	inherited := Parse(withLabels("app", map[string]string{
		"npm.proxy.domains": "app.example.com",
		"npm.proxy.port":    "8080",
	}), options)
	if len(inherited.Targets) != 1 {
		t.Fatalf("got %d targets, want 1 (errors: %v)", len(inherited.Targets), inherited.Errors)
	}
	assertGroup(t, inherited.Targets[0].Group, pointer("Docker"))

	overridden := Parse(withLabels("app", map[string]string{
		"npm.proxy.domains": "app.example.com",
		"npm.proxy.port":    "8080",
		"npm.proxy.group":   "none",
	}), options)
	if len(overridden.Targets) != 1 {
		t.Fatalf("got %d targets, want 1 (errors: %v)", len(overridden.Targets), overridden.Errors)
	}
	assertGroup(t, overridden.Targets[0].Group, pointer(""))
}

func pointer(s string) *string { return &s }

func assertGroup(t *testing.T, got, want *string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil:
		t.Errorf("Group = nil, want %q", *want)
	case want == nil:
		t.Errorf("Group = %q, want nil", *got)
	case *got != *want:
		t.Errorf("Group = %q, want %q", *got, *want)
	}
}
