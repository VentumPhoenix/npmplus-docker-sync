package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/config"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// DefaultPrefix is the label namespace used when none is configured.
const DefaultPrefix = "npm"

// Label grammar:
//
//	<prefix>.enable                       opt-in for the whole container
//	<prefix>[.<index>].<kind>.<field>     one resource
//	<prefix>[.<index>].<field>            shorthand, <kind> defaults to proxy
//	<prefix>.<index>.enable               switch a single index off
//
// <index> is any non-negative integer and defaults to 0, so a container with
// a single service can keep the short spelling (npm.proxy.host).
const (
	fieldEnable = "enable"

	// shared fields
	fieldHost           = "host"
	fieldCertificateID  = "certificate_id"
	fieldSSLForced      = "ssl.forced"
	fieldHTTP2          = "ssl.http2"
	fieldHTTP3          = "ssl.http3"
	fieldHSTS           = "ssl.hsts"
	fieldHSTSSubdomains = "ssl.hsts_subdomains"
	fieldBlockExploits  = "block_exploits"
	fieldAdvancedConfig = "advanced_config"
	fieldEnabled        = "enabled"
	fieldResolveIP      = "resolve_ip"
	fieldLEEmail        = "letsencrypt.email"
	fieldLEAgree        = "letsencrypt.agree"
	fieldLEDNSChallenge = "letsencrypt.dns_challenge"
	fieldLEDNSProvider  = "letsencrypt.dns_provider"
	fieldLEDNSCreds     = "letsencrypt.dns_credentials" //nolint:gosec
	fieldLEPropagation  = "letsencrypt.propagation_seconds"

	// proxy fields
	fieldPort           = "port"
	fieldScheme         = "scheme"
	fieldForwardHost    = "forward_host"
	fieldWebsockets     = "websockets"
	fieldCaching        = "caching"
	fieldTrustProto     = "trust_forwarded_proto"
	fieldAccessListID   = "access_list_id"
	fieldAccessListIDs  = "access_list_ids"
	fieldAccessListType = "access_list_type"
	fieldLocation       = "location"

	// redirection fields
	fieldForwardDomain = "forward_domain"
	fieldHTTPCode      = "http_code"
	fieldPreservePath  = "preserve_path"

	// stream fields
	fieldIncomingPort = "incoming_port"
	fieldForwardPort  = "forward_port"
	fieldTCP          = "tcp"
	fieldUDP          = "udp"
)

// aliases maps alternative field spellings to the canonical field name, per
// kind. They exist so labels read naturally for each resource type.
var aliases = map[npm.Kind]map[string]string{
	npm.KindProxy: {
		"enable":       fieldEnabled,
		"domain":       fieldHost,
		"domains":      fieldHost,
		"domain_name":  fieldHost,
		"forward_ip":   fieldForwardHost,
		"upstream":     fieldForwardHost,
		"forward_port": fieldPort,
		"access_list":  fieldAccessListIDs,
		"access_lists": fieldAccessListIDs,
	},
	npm.KindRedirect: {
		"enable":              fieldEnabled,
		"domain":              fieldHost,
		"domains":             fieldHost,
		"from":                fieldHost,
		"forward_domain_name": fieldForwardDomain,
		"target":              fieldForwardDomain,
		"to":                  fieldForwardDomain,
		"forward_http_code":   fieldHTTPCode,
		"code":                fieldHTTPCode,
	},
	npm.KindStream: {
		"enable":          fieldEnabled,
		"port":            fieldIncomingPort,
		"listen_port":     fieldIncomingPort,
		"forwarding_host": fieldForwardHost,
		"forwarding_port": fieldForwardPort,
		"forward_ip":      fieldForwardHost,
		"tcp_forwarding":  fieldTCP,
		"udp_forwarding":  fieldUDP,
		"protocol":        "protocol",
	},
	npm.KindDead: {
		"enable":  fieldEnabled,
		"domain":  fieldHost,
		"domains": fieldHost,
	},
}

// kindAliases maps the label segment to a resource kind.
var kindAliases = map[string]npm.Kind{
	"proxy":       npm.KindProxy,
	"redirect":    npm.KindRedirect,
	"redirection": npm.KindRedirect,
	"stream":      npm.KindStream,
	"dead":        npm.KindDead,
	"404":         npm.KindDead,
}

// IsEnabled reports whether a container opted into synchronisation.
func IsEnabled(c Container, prefix string) bool {
	raw, ok := c.Labels[normalizePrefix(prefix)+fieldEnable]
	if !ok {
		return false
	}
	enabled, err := config.ParseBool(raw)
	return err == nil && enabled
}

// Parse converts the labels of one container into its desired NPM resources.
// Containers without the opt-in label yield no targets and no errors.
// Invalid entries are reported individually so one broken definition cannot
// hide the valid ones.
func Parse(c Container, opts ParseOptions) ([]*Target, []error) {
	prefix := normalizePrefix(opts.Prefix)
	if !IsEnabled(c, prefix) {
		return nil, nil
	}

	groups, disabled, errs := groupLabels(c, prefix)

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

	targets := make([]*Target, 0, len(keys))
	for _, key := range keys {
		if disabled[key.index] {
			continue
		}
		fields := groups[key]
		target, err := buildTarget(c, opts, key, fields)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		targets = append(targets, target)
	}
	return targets, errs
}

// ParseAll converts a list of containers, collecting per-container errors
// instead of aborting the whole reconcile run.
func ParseAll(containers []Container, opts ParseOptions) ([]*Target, []error) {
	var (
		targets []*Target
		errs    []error
	)
	for _, c := range containers {
		parsed, parseErrs := Parse(c, opts)
		targets = append(targets, parsed...)
		for _, err := range parseErrs {
			errs = append(errs, fmt.Errorf("container %s: %w", displayName(c), err))
		}
	}
	return targets, errs
}

// entryKey identifies one resource definition inside a container.
type entryKey struct {
	kind  npm.Kind
	index int
}

// groupLabels sorts the container labels into per-(kind,index) field sets.
func groupLabels(c Container, prefix string) (map[entryKey]fieldSet, map[int]bool, []error) {
	groups := make(map[entryKey]fieldSet)
	disabled := make(map[int]bool)
	var errs []error

	names := make([]string, 0, len(c.Labels))
	for name := range c.Labels {
		names = append(names, name)
	}
	sort.Strings(names) // deterministic error order

	for _, name := range names {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		value := strings.TrimSpace(c.Labels[name])
		rest := strings.TrimPrefix(name, prefix)
		if rest == "" {
			continue
		}

		segments := strings.Split(rest, ".")

		// Grammar: [<kind>|<index>[.<kind>]].<field>
		// The resource type is checked first so the "404" alias is not
		// mistaken for a numeric index.
		index := 0
		kind := npm.KindProxy
		kindSet := false

		if resolved, ok := kindAliases[segments[0]]; ok {
			kind, kindSet = resolved, true
			segments = segments[1:]
		} else if n, err := strconv.Atoi(segments[0]); err == nil {
			if n < 0 {
				errs = append(errs, fmt.Errorf("label %s: index must not be negative", name))
				continue
			}
			index = n
			segments = segments[1:]
			if len(segments) == 0 {
				errs = append(errs, fmt.Errorf("label %s: missing field after the index", name))
				continue
			}
			if resolved, ok := kindAliases[segments[0]]; ok {
				kind, kindSet = resolved, true
				segments = segments[1:]
			}
		}

		if len(segments) == 0 {
			errs = append(errs, fmt.Errorf("label %s: missing field after the resource type", name))
			continue
		}

		// "<prefix>.enable" is the container-wide opt-in (already handled by
		// IsEnabled); "<prefix>.<index>.enable" switches one index off.
		if !kindSet && len(segments) == 1 && segments[0] == fieldEnable {
			if !strings.Contains(strings.TrimPrefix(name, prefix), ".") {
				continue // container-wide opt-in
			}
			enabled, err := config.ParseBool(value)
			if err != nil {
				errs = append(errs, fmt.Errorf("label %s: %w", name, err))
				continue
			}
			disabled[index] = !enabled
			continue
		}

		field := canonicalField(kind, strings.Join(segments, "."))
		key := entryKey{kind: kind, index: index}
		if _, ok := groups[key]; !ok {
			groups[key] = fieldSet{
				prefix: prefix, kind: kind, index: index,
				values: map[string]string{}, labels: map[string]string{},
			}
		}
		groups[key].values[field] = value
		groups[key].labels[field] = name
	}

	return groups, disabled, errs
}

// canonicalField resolves aliases and strips a redundant "proxy." prefix that
// users may repeat inside an explicit kind (npm.proxy.proxy.host).
func canonicalField(kind npm.Kind, field string) string {
	if alias, ok := aliases[kind][field]; ok {
		return alias
	}
	return field
}

func kindOrder(kind npm.Kind) int {
	for i, k := range npm.Kinds {
		if k == kind {
			return i
		}
	}
	return len(npm.Kinds)
}

func normalizePrefix(prefix string) string {
	prefix = strings.TrimSuffix(strings.TrimSpace(prefix), ".")
	if prefix == "" {
		prefix = DefaultPrefix
	}
	return prefix + "."
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

// fieldSet holds the labels of one resource definition and reports errors
// using the original label names.
type fieldSet struct {
	prefix string
	kind   npm.Kind
	index  int
	values map[string]string
	labels map[string]string
}

// name reconstructs the label a field came from (or would come from).
func (f fieldSet) name(field string) string {
	if label, ok := f.labels[field]; ok {
		return label
	}
	return fmt.Sprintf("%s%d.%s.%s", f.prefix, f.index, f.kind, field)
}

func (f fieldSet) string(field string) string { return f.values[field] }

func (f fieldSet) stringDefault(field, fallback string) string {
	if v := f.values[field]; v != "" {
		return v
	}
	return fallback
}

func (f fieldSet) has(field string) bool { return f.values[field] != "" }

func (f fieldSet) boolean(field string, fallback bool) (bool, error) {
	raw := f.values[field]
	if raw == "" {
		return fallback, nil
	}
	v, err := config.ParseBool(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %w", f.name(field), err)
	}
	return v, nil
}

func (f fieldSet) integer(field string, fallback int) (int, error) {
	raw := f.values[field]
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback, fmt.Errorf("%s: %q is not a number", f.name(field), raw)
	}
	return n, nil
}

func (f fieldSet) port(field string) (int, error) {
	n, err := f.integer(field, 0)
	if err != nil {
		return 0, err
	}
	if n != 0 && (n < 1 || n > 65535) {
		return 0, fmt.Errorf("%s: %d is out of range (1-65535)", f.name(field), n)
	}
	return n, nil
}

func (f fieldSet) domains(field string) ([]string, error) {
	domains := npm.NormalizeDomains(splitList(f.values[field]))
	for _, d := range domains {
		if strings.ContainsAny(d, " /\\:") {
			return nil, fmt.Errorf("%s: %q is not a valid domain name", f.name(field), d)
		}
	}
	return domains, nil
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
