package fields

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Environment variable prefixes. A field of kind <k> is defaulted by
// NPM_<K>_<FIELD>; NPM_DEFAULT_<FIELD> applies to every kind that has the
// field. Both spellings accept the field aliases, which is what makes the
// Redth names (NPM_PROXY_SSL_FORCE, NPM_PROXY_HSTS_SUBDOMAINS, ...) work.
const (
	envPrefix        = "NPM_"
	envDefaultPrefix = "NPM_DEFAULT_"
)

// envKind is the environment token of a resource kind.
func envKind(kind npm.Kind) string {
	if kind == npm.KindDead {
		return "DEAD" // "404" is not a legal variable name
	}
	return strings.ToUpper(string(kind))
}

// envName renders one environment variable name.
func envName(prefix, field string) string {
	return prefix + strings.ToUpper(strings.ReplaceAll(normalize(field), ".", "_"))
}

// EnvNames returns the environment variables that set the field's default,
// strongest first: the kind specific ones (canonical spelling first, then the
// aliases) followed by the cross-kind NPM_DEFAULT_* ones.
func EnvNames(kind npm.Kind, f Field) []string {
	out := make([]string, 0, 2*(len(f.Aliases)+1))
	seen := make(map[string]struct{}, cap(out))
	add := func(name string) {
		if _, dup := seen[name]; dup {
			return
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	kindPrefix := envPrefix + envKind(kind) + "_"
	add(envName(kindPrefix, f.Name))
	for _, alias := range f.Aliases {
		add(envName(kindPrefix, alias))
	}
	add(envName(envDefaultPrefix, f.Name))
	for _, alias := range f.Aliases {
		add(envName(envDefaultPrefix, alias))
	}
	return out
}

// Defaults holds the effective default of every field, together with where it
// came from. It is nil-safe: the zero value serves the built-in defaults.
type Defaults struct {
	values map[string]string
	source map[string]string
}

// Entry is one row of the effective-defaults table.
type Entry struct {
	Kind   npm.Kind
	Field  string
	Value  string
	Source string // "builtin" or the environment variable name
}

func key(kind npm.Kind, field string) string { return string(kind) + "|" + normalize(field) }

// LoadDefaults resolves the default of every field from the environment.
// environ is the process environment (os.Environ()) and is only used to warn
// about variables that look like a field default but match no field.
func LoadDefaults(getenv func(string) string, environ []string) (*Defaults, []string, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	d := &Defaults{
		values: make(map[string]string),
		source: make(map[string]string),
	}

	var errs []error
	known := make(map[string]struct{})

	for _, kind := range npm.Kinds {
		for _, f := range Table[kind] {
			value, source := f.Default, "builtin"
			for _, name := range EnvNames(kind, f) {
				known[name] = struct{}{}
				if raw := strings.TrimSpace(getenv(name)); raw != "" && source == "builtin" {
					value, source = raw, name
				}
			}
			if source != "builtin" {
				normalized, err := f.Parse(value)
				if err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", source, err))
					continue
				}
				value = normalized
			}
			d.values[key(kind, f.Name)] = value
			d.source[key(kind, f.Name)] = source
		}
	}

	warnings := unknownEnv(environ, known)
	if len(errs) > 0 {
		return d, warnings, errors.Join(errs...)
	}
	return d, warnings, nil
}

// unknownEnv reports variables in the NPM_<KIND>_ / NPM_DEFAULT_ namespace
// that do not name a field - almost always a typo that would otherwise be
// ignored without a trace.
func unknownEnv(environ []string, known map[string]struct{}) []string {
	prefixes := make([]string, 0, 1+len(npm.Kinds))
	prefixes = append(prefixes, envDefaultPrefix)
	for _, kind := range npm.Kinds {
		prefixes = append(prefixes, envPrefix+envKind(kind)+"_")
	}

	var warnings []string
	for _, entry := range environ {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, isKnown := known[name]; isKnown {
			continue
		}
		for _, prefix := range prefixes {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			warnings = append(warnings, name)
			break
		}
	}
	sort.Strings(warnings)
	return warnings
}

// Value returns the effective default of a field.
func (d *Defaults) Value(kind npm.Kind, field string) string {
	if d != nil {
		if v, ok := d.values[key(kind, field)]; ok {
			return v
		}
	}
	if f, ok := Lookup(kind, field); ok {
		return f.Default
	}
	return ""
}

// Source returns where the effective default came from ("builtin" or the
// environment variable name).
func (d *Defaults) Source(kind npm.Kind, field string) string {
	if d != nil {
		if v, ok := d.source[key(kind, field)]; ok {
			return v
		}
	}
	return "builtin"
}

// Entries returns every effective default, sorted by kind and field. Used for
// the start-up log and the generated documentation.
func (d *Defaults) Entries() []Entry {
	out := make([]Entry, 0, len(Table)*16)
	for _, kind := range npm.Kinds {
		for _, f := range List(kind) {
			out = append(out, Entry{
				Kind:   kind,
				Field:  f.Name,
				Value:  d.Value(kind, f.Name),
				Source: d.Source(kind, f.Name),
			})
		}
	}
	return out
}

// FromEnv returns only the defaults that the environment changed.
func (d *Defaults) FromEnv() []Entry {
	var out []Entry
	for _, e := range d.Entries() {
		if e.Source != "builtin" {
			out = append(out, e)
		}
	}
	return out
}

// Parse validates and canonicalises a raw value for the field. It is used for
// environment defaults; label values go through the same rules in the parser.
func (f Field) Parse(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch f.Type {
	case TypeBool:
		if strings.EqualFold(raw, Auto) && f.Default == Auto {
			return Auto, nil
		}
		b, err := ParseBool(raw)
		if err != nil {
			return "", err
		}
		return strconv.FormatBool(b), nil
	case TypeInt:
		if _, err := strconv.Atoi(raw); err != nil {
			return "", fmt.Errorf("%q is not a number", raw)
		}
		return raw, nil
	case TypePort:
		if strings.EqualFold(raw, Auto) {
			return Auto, nil
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return "", fmt.Errorf("%q is not a number", raw)
		}
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("%d is out of range (1-65535)", n)
		}
		return raw, nil
	case TypeEnum:
		return f.ValidateEnum(raw)
	case TypeString, TypeDomains, TypeList, TypeCertificate:
		return raw, nil
	default:
		return raw, nil
	}
}

// ParseBool accepts the usual strconv values plus the human friendly
// yes/no/on/off spellings commonly used in Docker labels.
func ParseBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "t", "true", "y", "yes", "on", "enable", "enabled":
		return true, nil
	case "0", "f", "false", "n", "no", "off", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", raw)
	}
}
