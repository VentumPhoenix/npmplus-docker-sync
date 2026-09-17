package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// DefaultPrefix is the label namespace used when none is configured.
const DefaultPrefix = "npm"

// Label grammar (both spellings of every part are accepted):
//
//	<prefix>.enable                           manage / ignore the container
//	<prefix>.<kind>.<field>                   one resource, index 0
//	<prefix>.<kind>.<index>.<field>           Redth's index position
//	<prefix>.<index>.<kind>.<field>           our own index position
//	<prefix>.<field>                          shorthand, <kind> defaults to proxy
//	<prefix>.<index>.enable                   switch one index off
//
// The namespace separator may be "." or "-" (npm-proxy.domains), and inside a
// field name ".", "_" and "-" are interchangeable, so ssl.hsts.subdomains,
// ssl.hsts_subdomains and ssl-hsts-subdomains are the same field. A missing
// index means index 0.
const fieldEnable = fields.Enable

// kindAliases maps the label segment to a resource kind.
var kindAliases = map[string]npm.Kind{
	"proxy":       npm.KindProxy,
	"proxies":     npm.KindProxy,
	"redirect":    npm.KindRedirect,
	"redirection": npm.KindRedirect,
	"stream":      npm.KindStream,
	"dead":        npm.KindDead,
	"404":         npm.KindDead,
}

// Result is the outcome of parsing one or many containers. Warnings are
// problems that do not invalidate a resource (an unknown label, a domain no
// certificate covers); Errors are definitions that had to be dropped.
type Result struct {
	Targets  []*Target
	Errors   []error
	Warnings []string
	// Skipped marks a container whose resource was dropped because of an
	// unknown label under STRICT_LABELS.
	Skipped bool
}

// Managed reports whether a container is synchronised, and why not when it is
// not.
//
// With NPM_EXPOSED_BY_DEFAULT (the default) any container carrying at least
// one label of the namespace is managed and `npm.enable=false` opts out. With
// the flag turned off the historic behaviour applies: `npm.enable=true` is
// required.
func Managed(c Container, opts ParseOptions) (bool, string) {
	if opts.SelfID != "" && c.ID == opts.SelfID {
		return false, "own container"
	}
	prefix := normalizePrefix(opts.Prefix)

	if raw, ok := lookupEnable(c, prefix); ok {
		enabled, err := fields.ParseBool(raw)
		switch {
		case err != nil:
			return false, fmt.Sprintf("invalid %senable=%q", prefix, raw)
		case !enabled:
			return false, "opted out"
		default:
			return true, ""
		}
	}
	if !opts.ExposedByDefault {
		return false, "no opt-in"
	}
	if !hasFieldLabel(c, prefix) {
		return false, "no labels"
	}
	return true, ""
}

// IsEnabled reports whether a container is synchronised.
func IsEnabled(c Container, prefix string) bool {
	managed, _ := Managed(c, ParseOptions{Prefix: prefix, ExposedByDefault: true})
	return managed
}

// lookupEnable finds the container-wide opt-in label in either separator
// spelling.
func lookupEnable(c Container, prefix string) (string, bool) {
	for name, value := range c.Labels {
		rest, ok := trimPrefix(name, prefix)
		if !ok {
			continue
		}
		if fields.Normalize(rest) == fieldEnable {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

// hasFieldLabel reports whether the container carries at least one label of
// the namespace that names a field (so a lone `npm.enable` does not count).
func hasFieldLabel(c Container, prefix string) bool {
	for name := range c.Labels {
		rest, ok := trimPrefix(name, prefix)
		if !ok {
			continue
		}
		if fields.Normalize(rest) == fieldEnable {
			continue
		}
		if _, _, _, _, err := splitLabel(rest); err == nil {
			return true
		}
	}
	return false
}

// Summary counts how the containers of one run were classified.
type Summary struct {
	Managed   int
	OptedOut  int
	Unlabeled int
	// Stopped counts managed containers that are not running.
	Stopped int
}

// Classify counts managed, opted out and unlabelled containers for the
// start-up overview.
func Classify(containers []Container, opts ParseOptions) Summary {
	var s Summary
	for _, c := range containers {
		switch managed, reason := Managed(c, opts); {
		case managed:
			s.Managed++
			if !c.Running() {
				s.Stopped++
			}
		case reason == "opted out":
			s.OptedOut++
		default:
			s.Unlabeled++
		}
	}
	return s
}

// Parse converts the labels of one container into its desired NPM resources.
// Containers that are not managed yield nothing. Invalid entries are reported
// individually so one broken definition cannot hide the valid ones.
func Parse(c Container, opts ParseOptions) Result {
	var res Result
	if managed, _ := Managed(c, opts); !managed {
		return res
	}

	groups, disabled, parse := groupLabels(c, opts)
	res.Errors = append(res.Errors, parse.Errors...)
	res.Warnings = append(res.Warnings, parse.Warnings...)

	keys := make([]entryKey, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].index != keys[j].index {
			return keys[i].index < keys[j].index
		}
		return kindOrder(keys[i].kind) < kindOrder(keys[j].kind)
	})

	for _, key := range keys {
		if disabled[key.index] {
			continue
		}
		f := groups[key]
		if f.tainted && opts.StrictLabels {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("%s#%d %s: skipped because of an unknown label (STRICT_LABELS)",
					displayName(c), key.index, key.kind))
			res.Skipped = true
			continue
		}
		target, err := buildTarget(c, opts, key, f)
		if err != nil {
			res.Errors = append(res.Errors, err)
			continue
		}
		res.Targets = append(res.Targets, target)
	}
	return res
}

// ParseAll converts a list of containers, collecting per-container problems
// instead of aborting the whole reconcile run.
func ParseAll(containers []Container, opts ParseOptions) Result {
	var res Result
	for _, c := range containers {
		one := Parse(c, opts)
		res.Targets = append(res.Targets, one.Targets...)
		for _, err := range one.Errors {
			res.Errors = append(res.Errors, fmt.Errorf("container %s: %w", displayName(c), err))
		}
		for _, warning := range one.Warnings {
			res.Warnings = append(res.Warnings, fmt.Sprintf("container %s: %s", displayName(c), warning))
		}
	}
	return res
}

// Scan parses every container into the snapshot one reconcile run works from.
//
// A container whose labels could not be parsed is recorded as *protected*: its
// resources are still in NPM, the tool simply cannot tell what they should
// look like right now. Deleting them because of a typo in one label would turn
// a warning into an outage.
func Scan(containers []Container, opts ParseOptions) (Snapshot, Result) {
	snapshot := Snapshot{
		Protected:  make(map[string]struct{}),
		Containers: len(containers),
	}
	var res Result

	for _, c := range containers {
		one := Parse(c, opts)
		snapshot.Targets = append(snapshot.Targets, one.Targets...)
		for _, err := range one.Errors {
			res.Errors = append(res.Errors, fmt.Errorf("container %s: %w", displayName(c), err))
		}
		for _, warning := range one.Warnings {
			res.Warnings = append(res.Warnings, fmt.Sprintf("container %s: %s", displayName(c), warning))
		}
		if len(one.Errors) > 0 || (one.Skipped && opts.StrictLabels) {
			if c.Name != "" {
				snapshot.Protected[c.Name] = struct{}{}
			}
			if c.ID != "" {
				snapshot.Protected[c.ID] = struct{}{}
			}
		}
	}

	snapshot.Summary = Classify(containers, opts)
	return snapshot, res
}

// entryKey identifies one resource definition inside a container.
type entryKey struct {
	kind  npm.Kind
	index int
}

// splitLabel parses the part of a label name that follows the namespace into
// the resource kind, the index and the canonical field name.
//
// Kind and index may appear in either order, which is what makes Redth's
// `proxy.1.domains` and our own `1.proxy.domains` equivalent.
func splitLabel(rest string) (kind npm.Kind, index int, field string, kindSet bool, err error) {
	tokens := strings.Split(fields.Normalize(rest), ".")
	kind, index = npm.KindProxy, 0
	indexSet := false

	for len(tokens) > 0 {
		token := tokens[0]
		if resolved, ok := kindAliases[token]; ok && !kindSet {
			kind, kindSet = resolved, true
			tokens = tokens[1:]
			continue
		}
		if n, convErr := strconv.Atoi(token); convErr == nil && !indexSet {
			if n < 0 {
				return kind, 0, "", kindSet, fmt.Errorf("index must not be negative")
			}
			index, indexSet = n, true
			tokens = tokens[1:]
			continue
		}
		break
	}

	field = strings.Join(tokens, ".")
	if field == "" {
		return kind, index, "", kindSet, fmt.Errorf("missing field name")
	}
	return kind, index, field, kindSet, nil
}

// groupLabels sorts the container labels into per-(kind,index) field sets.
func groupLabels(c Container, opts ParseOptions) (map[entryKey]fieldSet, map[int]bool, Result) {
	prefix := normalizePrefix(opts.Prefix)
	groups := make(map[entryKey]fieldSet)
	disabled := make(map[int]bool)
	var res Result

	names := make([]string, 0, len(c.Labels))
	for name := range c.Labels {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error order

	for _, name := range names {
		rest, ok := trimPrefix(name, prefix)
		if !ok || rest == "" {
			continue
		}
		value := strings.TrimSpace(c.Labels[name])

		// "<prefix>.enable" is the container-wide switch, handled by Managed.
		if fields.Normalize(rest) == fieldEnable {
			continue
		}

		kind, index, field, kindSet, err := splitLabel(rest)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Errorf("label %s: %w", name, err))
			continue
		}

		// "<prefix>.<index>.enable" switches one index off entirely; with a
		// resource type ("npm.proxy.enable") it is that resource's own flag.
		if field == fieldEnable && !kindSet {
			enabled, parseErr := fields.ParseBool(value)
			if parseErr != nil {
				res.Errors = append(res.Errors, fmt.Errorf("label %s: %w", name, parseErr))
				continue
			}
			disabled[index] = !enabled
			continue
		}

		key := entryKey{kind: kind, index: index}
		if _, exists := groups[key]; !exists {
			groups[key] = newFieldSet(prefix, kind, index, opts.Defaults)
		}
		set := groups[key]

		canonical, inverted, known := resolveField(kind, field)
		if !known {
			warning := fmt.Sprintf("unknown label %s", name)
			if suggestion := suggest(kind, field); suggestion != "" {
				warning += fmt.Sprintf(" - did you mean %s%s.%s?", prefix, kind, suggestion)
			}
			res.Warnings = append(res.Warnings, warning)
			set.tainted = true
			groups[key] = set
			continue
		}
		if inverted {
			b, parseErr := fields.ParseBool(value)
			if parseErr != nil {
				res.Errors = append(res.Errors, fmt.Errorf("label %s: %w", name, parseErr))
				continue
			}
			value = strconv.FormatBool(!b)
		}

		set.values[canonical] = value
		set.labels[canonical] = name
		groups[key] = set
	}

	return groups, disabled, res
}

// resolveField maps any accepted spelling to the canonical field name. The
// second return value reports an explicit "disable_x" alias of an inverted
// field, whose value has to be negated.
func resolveField(kind npm.Kind, field string) (canonical string, inverted, known bool) {
	// The table comes first: "location_config" normalises to "location.config"
	// and would otherwise look like a malformed location block.
	if f, ok := fields.Lookup(kind, field); ok {
		return fields.Normalize(f.Name), false, true
	}
	if strings.HasPrefix(field, fields.Location+".") {
		return locationField(field)
	}
	if positive, ok := fields.IsInverseAlias(field); ok {
		if f, found := fields.Lookup(kind, positive); found {
			return fields.Normalize(f.Name), true, true
		}
	}
	return "", false, false
}

// locationField validates "location.<n>.<field>" and canonicalises the part
// after the index.
func locationField(field string) (canonical string, inverted, known bool) {
	rest := strings.TrimPrefix(field, fields.Location+".")
	number, sub, ok := strings.Cut(rest, ".")
	if !ok {
		return "", false, false
	}
	if _, err := strconv.Atoi(number); err != nil {
		return "", false, false
	}
	if f, found := fields.LookupLocation(sub); found {
		return fields.Location + "." + number + "." + fields.Normalize(f.Name), false, true
	}
	if positive, isInverse := fields.IsInverseAlias(sub); isInverse {
		if f, found := fields.LookupLocation(positive); found {
			return fields.Location + "." + number + "." + fields.Normalize(f.Name), true, true
		}
	}
	return "", false, false
}

// suggest returns a "did you mean" hint for an unknown field.
func suggest(kind npm.Kind, field string) string {
	if strings.HasPrefix(field, fields.Location+".") && looksLikeLocationBlock(field) {
		rest := strings.TrimPrefix(field, fields.Location+".")
		if _, sub, ok := strings.Cut(rest, "."); ok {
			if hint := fields.SuggestLocation(sub); hint != "" {
				return fields.Location + ".<n>." + hint
			}
		}
		return ""
	}
	return fields.Suggest(kind, field)
}

// looksLikeLocationBlock reports whether a field name is
// "location.<n>.<something>" rather than a field that merely starts with
// "location" (such as location_config).
func looksLikeLocationBlock(field string) bool {
	rest := strings.TrimPrefix(field, fields.Location+".")
	number, _, ok := strings.Cut(rest, ".")
	if !ok {
		return false
	}
	_, err := strconv.Atoi(number)
	return err == nil
}

func kindOrder(kind npm.Kind) int {
	for i, k := range npm.Kinds {
		if k == kind {
			return i
		}
	}
	return len(npm.Kinds)
}

// normalizePrefix returns the namespace without a trailing separator.
func normalizePrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	prefix = strings.TrimRight(prefix, ".-")
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return prefix
}

// trimPrefix strips "<prefix>." or "<prefix>-" from a label name.
func trimPrefix(name, prefix string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
		return "", false
	}
	rest := name[len(prefix):]
	if rest == "" {
		return "", false
	}
	if rest[0] != '.' && rest[0] != '-' && rest[0] != '_' {
		return "", false
	}
	return rest[1:], true
}

func displayName(c Container) string {
	if c.Name != "" {
		return c.Name
	}
	if len(c.ID) > 12 {
		return c.ID[:12]
	}
	return c.ID
}

// ---------------------------------------------------------------------------
// field access
// ---------------------------------------------------------------------------

// fieldSet holds the labels of one resource definition, falls back to the
// configured defaults and reports errors using the original label names.
type fieldSet struct {
	prefix   string
	kind     npm.Kind
	index    int
	values   map[string]string
	labels   map[string]string
	defaults *fields.Defaults
	// tainted marks a field set that contained an unknown label.
	tainted bool
}

func newFieldSet(prefix string, kind npm.Kind, index int, defaults *fields.Defaults) fieldSet {
	return fieldSet{
		prefix:   prefix,
		kind:     kind,
		index:    index,
		values:   map[string]string{},
		labels:   map[string]string{},
		defaults: defaults,
	}
}

// name reconstructs the label a field came from (or would come from).
func (f fieldSet) name(field string) string {
	key := fields.Normalize(field)
	if label, ok := f.labels[key]; ok {
		return label
	}
	if f.index == 0 {
		return fmt.Sprintf("%s.%s.%s", f.prefix, f.kind, field)
	}
	return fmt.Sprintf("%s.%s.%d.%s", f.prefix, f.kind, f.index, field)
}

// source names where a value came from, for error messages about defaults.
func (f fieldSet) source(field string) string {
	key := fields.Normalize(field)
	if _, ok := f.values[key]; ok {
		return f.name(field)
	}
	if src := f.defaults.Source(f.kind, field); src != "builtin" {
		return src
	}
	return f.name(field)
}

// has reports whether a label set the field explicitly.
func (f fieldSet) has(field string) bool {
	v, ok := f.values[fields.Normalize(field)]
	return ok && v != ""
}

// string returns the effective value: the label, else the configured or
// built-in default.
func (f fieldSet) string(field string) string {
	if v, ok := f.values[fields.Normalize(field)]; ok && v != "" {
		return v
	}
	return f.defaults.Value(f.kind, field)
}

// raw returns the label value only, without any default.
func (f fieldSet) raw(field string) string { return f.values[fields.Normalize(field)] }

func (f fieldSet) boolean(field string) (bool, error) {
	raw := f.string(field)
	if raw == "" {
		return false, nil
	}
	v, err := fields.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", f.source(field), err)
	}
	return v, nil
}

// booleanAuto parses a tri-state switch: nil means "auto".
func (f fieldSet) booleanAuto(field string) (*bool, error) {
	raw := strings.TrimSpace(f.string(field))
	if raw == "" || strings.EqualFold(raw, fields.Auto) {
		return nil, nil //nolint:nilnil // nil is the "auto" case
	}
	v, err := fields.ParseBool(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.source(field), err)
	}
	return &v, nil
}

func (f fieldSet) integer(field string) (int, error) {
	raw := f.string(field)
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", f.source(field), raw)
	}
	return n, nil
}

// port returns the port of a field, 0 when unset and -1 when it is "auto".
func (f fieldSet) port(field string) (int, error) {
	raw := strings.TrimSpace(f.string(field))
	switch {
	case raw == "":
		return 0, nil
	case strings.EqualFold(raw, fields.Auto):
		return -1, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", f.source(field), raw)
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s: %d is out of range (1-65535)", f.source(field), n)
	}
	return n, nil
}

// enum validates a value against the field's enum, case-insensitively.
func (f fieldSet) enum(field string) (string, error) {
	raw := strings.TrimSpace(f.string(field))
	if raw == "" {
		return "", nil
	}
	definition, ok := fields.Lookup(f.kind, field)
	if !ok {
		return raw, nil
	}
	value, err := definition.ValidateEnum(raw)
	if err != nil {
		return "", fmt.Errorf("%s: %w", f.source(field), err)
	}
	return value, nil
}

func (f fieldSet) domains(field string) ([]string, error) {
	domains := npm.NormalizeDomains(splitList(f.string(field)))
	for _, d := range domains {
		if err := validDomain(d); err != nil {
			return nil, fmt.Errorf("%s: %w", f.source(field), err)
		}
	}
	return domains, nil
}

// validDomain rejects what NPM would reject anyway - and, more importantly,
// what would normalise away to nothing and leave a resource without an
// identity.
func validDomain(domain string) error {
	if domain == "" {
		return fmt.Errorf("%q is not a valid domain name", domain)
	}
	if strings.ContainsAny(domain, " /\\:") {
		return fmt.Errorf("%q is not a valid domain name", domain)
	}
	for _, label := range strings.Split(domain, ".") {
		if label == "" {
			return fmt.Errorf("%q is not a valid domain name (empty label)", domain)
		}
	}
	return nil
}

// splitList splits a comma, semicolon or whitespace separated label value.
func splitList(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == ';'
	})
}
