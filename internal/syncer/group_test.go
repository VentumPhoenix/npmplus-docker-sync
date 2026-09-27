package syncer

import (
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// groupLabelsFor is the minimum label set of a kind plus a group.
func groupLabelsFor(kind npm.Kind, group string) map[string]string {
	labels := map[string]string{}
	for k, v := range baseLabels[kind] {
		labels[k] = v
	}
	if group != "" {
		labels["npm."+string(kind)+".group"] = group
	}
	return labels
}

// TestGroupReachesTheMeta: NPMplus groups its host lists by meta.directory, so
// that is where the label has to land - and nowhere else, because the write
// schema has no column for it.
func TestGroupReachesTheMeta(t *testing.T) {
	t.Parallel()

	for _, kind := range npm.Kinds {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			resource, _ := buildFrom(t, kind, groupLabelsFor(kind, "Production"))
			if got := resource.ResourceMeta().Directory(); got != "Production" {
				t.Errorf("meta.directory = %q, want %q", got, "Production")
			}

			// Without a group label the key is absent, not empty: that is what
			// AdoptMeta keys off.
			bare, _ := buildFrom(t, kind, groupLabelsFor(kind, ""))
			if _, present := bare.ResourceMeta()[npm.MetaDirectory]; present {
				t.Errorf("meta carries %q without a group label: %s", npm.MetaDirectory, describeJSON(bare))
			}
		})
	}
}

// TestGroupChangesTheFingerprint: a group that is not part of the fingerprint
// would never be written after the resource exists.
func TestGroupChangesTheFingerprint(t *testing.T) {
	t.Parallel()

	for _, kind := range npm.Kinds {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()

			grouped, _ := buildFrom(t, kind, groupLabelsFor(kind, "Production"))
			ungrouped, _ := buildFrom(t, kind, groupLabelsFor(kind, ""))
			if grouped.Fingerprint() == ungrouped.Fingerprint() {
				t.Errorf("the group does not reach the fingerprint: %s", describeJSON(grouped))
			}

			other, _ := buildFrom(t, kind, groupLabelsFor(kind, "Staging"))
			if grouped.Fingerprint() == other.Fingerprint() {
				t.Error("two different groups fingerprint identically")
			}
		})
	}
}

// TestGroupPickedInTheUIIsKept is the whole reason the label has three states:
// a container that says nothing about groups must not drag a host out of the
// group somebody sorted it into by hand. Were the group not adopted, the
// fingerprint would differ forever and every Docker event would rewrite it.
func TestGroupPickedInTheUIIsKept(t *testing.T) {
	t.Parallel()

	desired, _ := buildFrom(t, npm.KindProxy, groupLabelsFor(npm.KindProxy, ""))

	live, _ := buildFrom(t, npm.KindProxy, groupLabelsFor(npm.KindProxy, ""))
	live.ResourceMeta().SetDirectory("Sorted by hand")

	desired.AdoptServerState(live)
	if got := desired.ResourceMeta().Directory(); got != "Sorted by hand" {
		t.Fatalf("meta.directory = %q, want the live group to be adopted", got)
	}
	if desired.Fingerprint() != live.Fingerprint() {
		t.Error("the adopted group still reports a drift, which would update on every event")
	}
}

// TestGroupNoneClearsTheLiveGroup: `none` is the one spelling that does take a
// host out of its group, so it survives the adoption above.
func TestGroupNoneClearsTheLiveGroup(t *testing.T) {
	t.Parallel()

	desired, _ := buildFrom(t, npm.KindProxy, groupLabelsFor(npm.KindProxy, "none"))

	live, _ := buildFrom(t, npm.KindProxy, groupLabelsFor(npm.KindProxy, ""))
	live.ResourceMeta().SetDirectory("Production")

	desired.AdoptServerState(live)
	if got := desired.ResourceMeta().Directory(); got != "" {
		t.Errorf("meta.directory = %q, want the group to be cleared", got)
	}
	if desired.Fingerprint() == live.Fingerprint() {
		t.Error("clearing the group is not reported as a change, so it would never be written")
	}
}
