package npm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// serverOwned fields never say anything about a configuration difference:
// they are assigned by NPM and differ between a stored resource and the
// payload that produced it by definition.
var serverOwned = map[string]struct{}{
	"id":            {},
	"created_on":    {},
	"modified_on":   {},
	"owner_user_id": {},
	"enabled":       {},
}

// Diff renders the configuration fields in which two resources differ, as
// "field: live → desired".
//
// It exists so a dry-run says what would actually change, and so an edit made
// in the NPM UI is named in the log line that overwrites it - "would update"
// alone leaves the operator guessing.
func Diff(current, desired Resource) string {
	a, aOK := resourceMap(current)
	b, bOK := resourceMap(desired)
	if !aOK || !bOK {
		return ""
	}

	keys := make([]string, 0, len(b))
	seen := make(map[string]struct{}, len(b))
	for _, source := range []map[string]json.RawMessage{a, b} {
		for key := range source {
			if _, skip := serverOwned[key]; skip {
				continue
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	parts := make([]string, 0, 4)
	for _, key := range keys {
		before, after := render(a[key]), render(b[key])
		if before == after {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s → %s", key, before, after))
	}
	return strings.Join(parts, ", ")
}

// resourceMap encodes a resource into its JSON fields.
func resourceMap(r Resource) (map[string]json.RawMessage, bool) {
	if r == nil {
		return nil, false
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, false
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}
	return out, true
}

// render normalises a value for display: an absent field reads as "unset",
// and an object or array is re-encoded so key order cannot fake a difference.
func render(value json.RawMessage) string {
	if len(value) == 0 {
		return "unset"
	}
	var decoded any
	if err := json.Unmarshal(value, &decoded); err != nil {
		return string(value)
	}
	normalised, err := json.Marshal(decoded)
	if err != nil {
		return string(value)
	}
	out := string(normalised)
	const limit = 120
	if len(out) > limit {
		out = out[:limit] + "…"
	}
	return out
}
