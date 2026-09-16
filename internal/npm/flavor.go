package npm

import (
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
//  2. GET /api/: upstream NPM reports a `version` object, NPMplus does not.
//
// When neither is conclusive the detection falls back to NPMplus, which is
// what this tool is named after, and says so in the log.
func (c *Client) DetectFlavour(ctx context.Context) (Flavour, error) {
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
	if healthErr := c.do(ctx, http.MethodGet, "/api/", nil, &health); healthErr == nil && len(health.Version) > 0 {
		return FlavourNPM, "GET /api/ version object", nil
	}

	return FlavourNPMplus, "default (no proxy host to inspect)", nil
}
