// Package certs picks the TLS certificate of a host.
//
// The selection is deliberately deterministic and explainable: every result
// carries the class it was matched by and the pattern that matched, so the
// log line says *why* a host ended up with a certificate.
package certs

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Mode is what a `certificate` label asks for.
type Mode int

// The accepted certificate modes.
const (
	// ModeAuto picks the best matching certificate (the default).
	ModeAuto Mode = iota
	// ModeID pins a certificate id.
	ModeID
	// ModeDomain names a domain the certificate must cover.
	ModeDomain
	// ModeName names a certificate by its nice_name ("name:My wildcard").
	ModeName
	// ModeNew requests a fresh Let's Encrypt certificate.
	ModeNew
	// ModeNone deliberately attaches no certificate.
	ModeNone
)

// String implements fmt.Stringer.
func (m Mode) String() string {
	switch m {
	case ModeID:
		return "id"
	case ModeDomain:
		return "domain"
	case ModeName:
		return "name"
	case ModeNew:
		return "new"
	case ModeNone:
		return "none"
	default:
		return "auto"
	}
}

// Spec is a parsed `certificate` label value.
type Spec struct {
	Mode   Mode
	ID     int
	Domain string
	Name   string
	Raw    string
}

// String implements fmt.Stringer.
func (s Spec) String() string {
	switch s.Mode {
	case ModeID:
		return strconv.Itoa(s.ID)
	case ModeDomain:
		return s.Domain
	case ModeName:
		return "name:" + s.Name
	default:
		return s.Mode.String()
	}
}

// AutoSpec is the default: pick the best matching certificate.
var AutoSpec = Spec{Mode: ModeAuto, Raw: "auto"}

// ParseSpec reads a `certificate` label or NPM_DEFAULT_CERTIFICATE value.
func ParseSpec(raw string) (Spec, error) {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	switch lower {
	case "", "auto":
		return Spec{Mode: ModeAuto, Raw: trimmed}, nil
	case "new", "letsencrypt", "le", "request":
		return Spec{Mode: ModeNew, Raw: trimmed}, nil
	case "none", "off", "false", "no", "0", "disable", "disabled":
		return Spec{Mode: ModeNone, Raw: trimmed}, nil
	}
	if rest, ok := cutPrefixFold(trimmed, "name:"); ok {
		name := strings.TrimSpace(rest)
		if name == "" {
			return Spec{}, fmt.Errorf("%q is missing the certificate name after \"name:\"", raw)
		}
		return Spec{Mode: ModeName, Name: name, Raw: trimmed}, nil
	}
	if id, err := strconv.Atoi(trimmed); err == nil {
		if id < 0 {
			return Spec{}, fmt.Errorf("certificate id %d must not be negative", id)
		}
		if id == 0 {
			return Spec{Mode: ModeNone, Raw: trimmed}, nil
		}
		return Spec{Mode: ModeID, ID: id, Raw: trimmed}, nil
	}
	if looksLikeDomain(lower) {
		return Spec{Mode: ModeDomain, Domain: lower, Raw: trimmed}, nil
	}
	return Spec{}, fmt.Errorf("%q is not a certificate id, a domain, name:<nice name>, auto, new or none", raw)
}

func cutPrefixFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

func looksLikeDomain(s string) bool {
	if strings.ContainsAny(s, " /\\:") {
		return false
	}
	return strings.Contains(s, ".")
}

// Class ranks how well a certificate fits a set of domains. Lower is better.
type Class int

// The match classes, best first.
const (
	// ClassExactOnly means the certificate covers exactly these domains.
	ClassExactOnly Class = iota + 1
	// ClassExactExtra means every domain matches exactly, plus further SANs.
	ClassExactExtra
	// ClassMixed means every domain is covered, some exactly, some by a
	// wildcard.
	ClassMixed
	// ClassWildcard means every domain is covered by a wildcard.
	ClassWildcard
	// ClassNone means the certificate does not cover the domains.
	ClassNone
)

// String implements fmt.Stringer and is what the log line shows.
func (c Class) String() string {
	switch c {
	case ClassExactOnly:
		return "exact"
	case ClassExactExtra:
		return "exact+sans"
	case ClassMixed:
		return "mixed"
	case ClassWildcard:
		return "wildcard"
	default:
		return "none"
	}
}

// Partial is the policy for domains no single certificate covers.
type Partial string

// The partial-coverage policies.
const (
	// PartialPrimary uses a certificate for the first domain and warns about
	// the rest (the default).
	PartialPrimary Partial = "primary"
	// PartialNone attaches no certificate at all.
	PartialNone Partial = "none"
)

// ParsePartial resolves NPM_CERTIFICATE_PARTIAL.
func ParsePartial(raw string) (Partial, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "primary", "first":
		return PartialPrimary, nil
	case "none", "off", "skip":
		return PartialNone, nil
	default:
		return PartialPrimary, fmt.Errorf("unknown value %q (want primary or none)", raw)
	}
}

// Selection is the outcome of a certificate lookup.
type Selection struct {
	// ID is the certificate to attach, 0 for none.
	ID int
	// New requests a fresh Let's Encrypt certificate.
	New bool
	// Class is how the certificate matched.
	Class Class
	// Pattern is the certificate domain that matched the first host domain.
	Pattern string
	// Uncovered lists host domains the certificate does not cover.
	Uncovered []string
	// Kept reports that an already attached certificate was retained.
	Kept bool
}

// Options tune the automatic selection.
type Options struct {
	// Current is the certificate id the live resource already carries.
	Current int
	// Partial is the policy when no certificate covers every domain.
	Partial Partial
	// Now is the reference time for expiry checks (zero means time.Now).
	Now time.Time
}

// Resolve turns a spec into a concrete certificate id.
//
// For ModeAuto the candidates are ranked by class, then by remaining
// validity, then by id; an already attached certificate that is still valid
// and still covers the domains is kept unless a candidate matches in a
// strictly better class, so a newly imported wildcard cannot shuffle existing
// hosts around.
func Resolve(spec Spec, domains []string, list []npm.Certificate, opts Options) (Selection, error) {
	switch spec.Mode {
	case ModeNone:
		return Selection{}, nil
	case ModeNew:
		return Selection{New: true}, nil
	case ModeID:
		return Selection{ID: spec.ID, Class: ClassNone, Pattern: "id"}, nil
	case ModeName:
		for _, c := range byID(list) {
			if strings.EqualFold(strings.TrimSpace(c.NiceName), spec.Name) {
				return Selection{ID: c.ID, Pattern: c.NiceName, Class: classify(c, domains)}, nil
			}
		}
		return Selection{}, fmt.Errorf("no certificate named %q", spec.Name)
	case ModeDomain:
		sel, ok := best([]string{spec.Domain}, list, opts)
		if !ok {
			return Selection{}, fmt.Errorf("no certificate covers %q", spec.Domain)
		}
		sel.Uncovered = uncovered(certByID(list, sel.ID), domains)
		return sel, nil
	}

	if len(domains) == 0 {
		return Selection{}, nil
	}
	if sel, ok := best(domains, list, opts); ok {
		return sel, nil
	}

	// No certificate covers every domain.
	if opts.Partial == PartialNone {
		return Selection{Uncovered: append([]string(nil), domains...)}, nil
	}
	sel, ok := best(domains[:1], list, opts)
	if !ok {
		return Selection{Uncovered: append([]string(nil), domains...)}, nil
	}
	sel.Uncovered = uncovered(certByID(list, sel.ID), domains)
	return sel, nil
}

// best returns the highest ranked certificate that covers every domain.
func best(domains []string, list []npm.Certificate, opts Options) (Selection, bool) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	type candidate struct {
		cert  npm.Certificate
		class Class
	}
	var candidates []candidate
	for _, c := range byID(list) {
		if !usable(c, now) {
			continue
		}
		if class := classify(c, domains); class != ClassNone {
			candidates = append(candidates, candidate{cert: c, class: class})
		}
	}
	if len(candidates) == 0 {
		return Selection{}, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].class != candidates[j].class {
			return candidates[i].class < candidates[j].class
		}
		ei, ej := expiry(candidates[i].cert), expiry(candidates[j].cert)
		if !ei.Equal(ej) {
			return ei.After(ej) // longest remaining validity first
		}
		return candidates[i].cert.ID < candidates[j].cert.ID
	})
	winner := candidates[0]

	// Stability: keep a certificate that is already attached unless the
	// winner matches in a strictly better class.
	if opts.Current > 0 {
		for _, c := range candidates {
			if c.cert.ID != opts.Current {
				continue
			}
			if c.class <= winner.class {
				return Selection{
					ID: c.cert.ID, Class: c.class,
					Pattern: patternFor(c.cert, domains), Kept: true,
				}, true
			}
			break
		}
	}

	return Selection{
		ID: winner.cert.ID, Class: winner.class,
		Pattern: patternFor(winner.cert, domains),
	}, true
}

// usable filters out everything that cannot serve as a server certificate:
// deleted entries, expired ones and NPMplus' client CAs (provider "mtls").
func usable(c npm.Certificate, now time.Time) bool {
	if c.ID <= 0 || bool(c.IsDeleted) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(c.Provider), "mtls") {
		return false
	}
	if exp := expiry(c); !exp.IsZero() && !exp.After(now) {
		return false
	}
	return true
}

// expiry parses `expires_on`, which both flavours render as
// "2026-09-16 10:11:12" in UTC. An unparsable value is treated as "unknown",
// which never disqualifies a certificate but sorts last.
func expiry(c npm.Certificate) time.Time {
	raw := strings.TrimSpace(c.ExpiresOn)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// classify ranks one certificate against a set of host domains.
func classify(c npm.Certificate, domains []string) Class {
	if len(domains) == 0 {
		return ClassNone
	}
	certDomains := canonicalAll(c.DomainNames)
	if len(certDomains) == 0 {
		return ClassNone
	}

	exact, wildcard := 0, 0
	for _, raw := range domains {
		d := canonical(raw)
		if d == "" {
			continue
		}
		switch {
		case containsExact(certDomains, d):
			exact++
		case matchesWildcard(certDomains, d):
			wildcard++
		default:
			return ClassNone
		}
	}
	switch {
	case wildcard == 0 && len(certDomains) == exact:
		return ClassExactOnly
	case wildcard == 0:
		return ClassExactExtra
	case exact > 0:
		return ClassMixed
	default:
		return ClassWildcard
	}
}

// uncovered returns the domains the certificate does not cover.
func uncovered(c npm.Certificate, domains []string) []string {
	certDomains := canonicalAll(c.DomainNames)
	var out []string
	for _, raw := range domains {
		d := canonical(raw)
		if d == "" {
			continue
		}
		if containsExact(certDomains, d) || matchesWildcard(certDomains, d) {
			continue
		}
		out = append(out, raw)
	}
	return out
}

// patternFor returns the certificate entry that matched the first domain,
// which is what the log line quotes.
func patternFor(c npm.Certificate, domains []string) string {
	if len(domains) == 0 {
		return ""
	}
	d := canonical(domains[0])
	for _, raw := range c.DomainNames {
		cd := canonical(raw)
		if cd == d || wildcardMatch(cd, d) {
			return cd
		}
	}
	return ""
}

func containsExact(certDomains []string, domain string) bool {
	for _, cd := range certDomains {
		if cd == domain {
			return true
		}
	}
	return false
}

func matchesWildcard(certDomains []string, domain string) bool {
	for _, cd := range certDomains {
		if wildcardMatch(cd, domain) {
			return true
		}
	}
	return false
}

// wildcardMatch implements RFC 6125: a wildcard covers exactly one label, so
// *.example.com matches a.example.com but neither example.com nor
// a.b.example.com.
func wildcardMatch(pattern, domain string) bool {
	rest, ok := strings.CutPrefix(pattern, "*.")
	if !ok || rest == "" {
		return false
	}
	label, parent, ok := strings.Cut(domain, ".")
	if !ok || label == "" {
		return false
	}
	return parent == rest
}

// canonical lower-cases a domain, drops the trailing dot and converts an
// internationalised name to punycode so comparisons are unambiguous. The
// wildcard label is kept as-is, since it is not valid IDN input.
func canonical(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return ""
	}
	prefix := ""
	if rest, ok := strings.CutPrefix(d, "*."); ok {
		prefix, d = "*.", rest
	}
	if !isASCII(d) {
		if ascii, err := idna.Lookup.ToASCII(d); err == nil {
			d = ascii
		}
	}
	return prefix + d
}

func canonicalAll(domains []string) []string {
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if c := canonical(d); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// byID returns the certificates in a deterministic order.
func byID(list []npm.Certificate) []npm.Certificate {
	out := append([]npm.Certificate(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func certByID(list []npm.Certificate, id int) npm.Certificate {
	for _, c := range list {
		if c.ID == id {
			return c
		}
	}
	return npm.Certificate{}
}

// Hash summarises a certificate list so a change (a new certificate, a
// renewal, a deletion) can trigger a reconcile run.
func Hash(list []npm.Certificate) string {
	parts := make([]string, 0, len(list))
	for _, c := range byID(list) {
		parts = append(parts, fmt.Sprintf("%d:%s:%s:%t:%s",
			c.ID, strings.Join(canonicalAll(c.DomainNames), ","), strings.TrimSpace(c.ExpiresOn), bool(c.IsDeleted), c.Provider))
	}
	return strings.Join(parts, "|")
}
