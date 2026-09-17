package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Flavour is the API dialect of the server this client talks to.
//
// NPM and NPMplus share the same endpoints but validate request bodies against
// JSON schemas with `additionalProperties: false`, and those schemas have
// drifted apart: NPMplus dropped `enabled` and `access_list_id` from every
// write payload, replaced the latter with `npmplus_access_list_ids` plus
// `npmplus_access_list_type`, and types the stream ports as strings. Sending
// the wrong dialect fails with
//
//	400 data must NOT have additional properties
//
// so the dialect has to be known before the first write.
type Flavour string

// The supported dialects. FlavourAuto asks the client to probe the server.
const (
	FlavourAuto    Flavour = "auto"
	FlavourNPMplus Flavour = "npmplus"
	FlavourNPM     Flavour = "npm"
)

// String implements fmt.Stringer.
func (f Flavour) String() string {
	if f == "" {
		return string(FlavourAuto)
	}
	return string(f)
}

// Valid reports whether f is one of the known values.
func (f Flavour) Valid() bool {
	switch f {
	case FlavourAuto, FlavourNPMplus, FlavourNPM, "":
		return true
	default:
		return false
	}
}

// ParseFlavour resolves the spellings accepted in configuration.
func ParseFlavour(raw string) (Flavour, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "detect":
		return FlavourAuto, nil
	case "npmplus", "npm-plus", "plus":
		return FlavourNPMplus, nil
	case "npm", "nginx-proxy-manager", "upstream":
		return FlavourNPM, nil
	default:
		return FlavourAuto, fmt.Errorf("unknown api flavour %q (want auto, npmplus or npm)", raw)
	}
}

// Flavour reports the dialect in use. It is FlavourAuto until either
// WithFlavour pinned one or DetectFlavour resolved one.
func (c *Client) Flavour() Flavour {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.flavour == "" {
		return FlavourAuto
	}
	return c.flavour
}

// WithFlavour pins the dialect instead of probing for it. Useful when the API
// sits behind a proxy that rewrites responses, or to keep a known-good
// configuration stable across upgrades.
func WithFlavour(f Flavour) Option {
	return func(c *Client) {
		if f != FlavourAuto {
			c.flavour = f
			c.flavourPinned = true
		}
	}
}

// DetectFlavour probes the server and remembers the result. A pinned flavour
// is returned unchanged without any request.
//
// Two signals are used, strongest first:
//
//  1. An existing proxy host: NPMplus serialises `npmplus_access_list_ids`,
//     upstream NPM serialises `access_list_id`. This is authoritative because
//     it is the very field the write payloads disagree about.
//  2. GET /api/: both report a `version`, but in different shapes - upstream
//     NPM an object ({major, minor, revision}), NPMplus a string
//     ("2026-07-24-r1-a30a954-2.15.1"). Older NPMplus versions reported none
//     at all, which stays inconclusive and lands on the default below.
//
// When neither is conclusive the detection falls back to NPMplus, which is
// what this tool is named after, and says so in the log.
func (c *Client) DetectFlavour(ctx context.Context) (Flavour, error) {
	// Unconditionally, and before the pinned shortcut: the version decides
	// which fields a payload may carry, and a pinned flavour says nothing
	// about the release.
	c.readVersion(ctx)

	if f := c.Flavour(); f != FlavourAuto {
		return f, nil
	}

	flavour, how, err := c.probeFlavour(ctx)
	if err != nil {
		return FlavourAuto, err
	}

	c.mu.Lock()
	c.flavour = flavour
	c.mu.Unlock()

	c.log.Info("detected npm api flavour",
		slog.String("flavour", string(flavour)),
		slog.String("detected_from", how))
	return flavour, nil
}

// probeFlavour runs the detection without touching the client state.
func (c *Client) probeFlavour(ctx context.Context) (flavour Flavour, how string, err error) {
	var hosts []map[string]json.RawMessage
	if listErr := c.do(ctx, http.MethodGet, KindProxy.Path(), nil, &hosts); listErr != nil && !IsNotFound(listErr) {
		return FlavourAuto, "", fmt.Errorf("npm: probe api flavour: %w", listErr)
	}
	for _, host := range hosts {
		if _, ok := host["npmplus_access_list_ids"]; ok {
			return FlavourNPMplus, "proxy host schema", nil
		}
		if _, ok := host["access_list_id"]; ok {
			return FlavourNPM, "proxy host schema", nil
		}
	}

	var health struct {
		Status  string          `json:"status"`
		Version json.RawMessage `json:"version"`
	}
	if healthErr := c.do(ctx, http.MethodGet, "/api/", nil, &health); healthErr == nil {
		switch versionShape(health.Version) {
		case '{':
			return FlavourNPM, "GET /api/ version object", nil
		case '"':
			return FlavourNPMplus, "GET /api/ version string", nil
		}
	}

	return FlavourNPMplus, "default (no proxy host to inspect)", nil
}

// versionShape reports the first meaningful byte of the raw `version` value:
// '{' for an object, '"' for a string, 0 when it is absent or something else.
// The shape is the signal - checking only that the field is non-empty would
// read NPMplus' string version as upstream NPM's object.
func versionShape(raw json.RawMessage) byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return 0
	}
	switch trimmed[0] {
	case '{', '"':
		return trimmed[0]
	default:
		return 0
	}
}

// Version is what upstream NPM reports under `version` in GET /api/:
// {"major":2,"minor":11,"revision":3}. NPMplus reports a string there, which
// parses to the zero Version - "no version I can compare against".
type Version struct {
	Major    int `json:"major"`
	Minor    int `json:"minor"`
	Revision int `json:"revision"`
}

// IsZero reports whether the server gave no comparable version.
func (v Version) IsZero() bool { return v == Version{} }

// String implements fmt.Stringer.
func (v Version) String() string {
	if v.IsZero() {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Revision)
}

// AtLeast reports whether the version is at least major.minor.
//
// An unknown version counts as new enough. A server that reports none is
// either NPMplus or something this tool has not seen, and assuming "old"
// there would silently drop settings the user asked for; assuming "new" at
// worst produces the server's own error message, which names the field.
func (v Version) AtLeast(major, minor int) bool {
	if v.IsZero() {
		return true
	}
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

// parseVersion reads the object form and ignores everything else.
func parseVersion(raw json.RawMessage) Version {
	if versionShape(raw) != '{' {
		return Version{}
	}
	var v Version
	if err := json.Unmarshal(raw, &v); err != nil {
		return Version{}
	}
	return v
}

// Dialect is everything a request body has to be written for: the flavour,
// and - for upstream NPM - the version. Its request schemas set
// `additionalProperties: false` and gained fields over releases, so a field
// that 2.12 expects makes 2.11 reject the whole payload with
// "data should NOT have additional properties".
type Dialect struct {
	Flavour Flavour
	Version Version
}

// SupportsTrustForwardedProto reports whether proxy hosts accept
// `trust_forwarded_proto`. Upstream NPM added it in 2.12; NPMplus has always
// had it.
func (d Dialect) SupportsTrustForwardedProto() bool {
	return d.Flavour != FlavourNPM || d.Version.AtLeast(2, 12)
}

// SupportsStreamCertificate reports whether streams accept `certificate_id`.
// Upstream NPM added stream certificates in 2.12; NPMplus has always had them.
func (d Dialect) SupportsStreamCertificate() bool {
	return d.Flavour != FlavourNPM || d.Version.AtLeast(2, 12)
}
