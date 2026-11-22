package syncer

import (
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/fields"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// A proxy host whose forward authentication has no upstream is not served
// at all: NPMplus renders a configuration nginx refuses, renames it to
// <id>.conf.err and marks the host offline - or, in the releases that render
// inside the write transaction, rejects the write. Either way the labels ask
// for something that cannot work, and auth_request_fallback decides what is
// written instead:
//
//	closed (default)  the whole host is disabled. Disabling only the affected
//	                  location is not an option: its path would then be
//	                  answered by the location above it, possibly without any
//	                  login at all.
//	open              the forward authentication that has no upstream is left
//	                  out, so the host is served without that login.
//
// The decision is taken again on every run from the labels and the upstreams
// NPMplus has, so a configured upstream brings back what the labels say
// without anybody touching the host.

// authDecision is what auth_request_fallback makes of one target.
type authDecision struct {
	// Mode is fields.AuthFallbackClosed or fields.AuthFallbackOpen, "" when
	// every provider the target uses has an upstream.
	Mode string
	// Missing are the providers without an upstream, sorted.
	Missing []string
	// Parts are the parts of the host that use them: "host" and the paths of
	// its locations.
	Parts []string
}

// Applies reports whether the decision changes what is written.
func (d authDecision) Applies() bool { return d.Mode != "" }

// Reason explains the decision in the words the log and /status use.
func (d authDecision) Reason() string {
	reasons := make([]string, 0, len(d.Missing))
	for _, provider := range d.Missing {
		reasons = append(reasons, fmt.Sprintf("no AUTH_REQUEST_%s_UPSTREAM in NPMplus and no override",
			strings.ToUpper(provider)))
	}
	return strings.Join(reasons, "; ")
}

// signature identifies a decision, so it is reported once rather than on every
// run.
func (d authDecision) signature() string {
	return d.Mode + "|" + strings.Join(d.Missing, ",") + "|" + strings.Join(d.Parts, ",")
}

// authProvider returns the provider of a location's auth_request; an empty
// value and "global" inherit the host's.
func authProvider(value, host string) string {
	v := strings.TrimSpace(value)
	if v == "" || v == npm.AuthRequestGlobal {
		return docker.AuthProvider(host)
	}
	return docker.AuthProvider(v)
}

// usesAuthRequest reports whether a proxy target asks for forward
// authentication anywhere.
func usesAuthRequest(t *docker.Target) bool {
	if docker.AuthProvider(t.AuthRequest) != "" {
		return true
	}
	for _, l := range t.Locations {
		if authProvider(l.AuthRequest, t.AuthRequest) != "" {
			return true
		}
	}
	return false
}

// decideAuthFallback works out which providers of a target have no upstream,
// the way NPMplus resolves one for the host it renders (nginx.js, around
// auth_request_<provider>_upstream_resolved):
//
//  1. the override of the host, when the host itself uses the provider;
//  2. otherwise the first enabled location using it that has an override;
//  3. otherwise the global AUTH_REQUEST_<PROVIDER>_UPSTREAM.
//
// A status that is not known never produces a decision: falling back because
// the container could not be read would take hosts down - or open them up -
// for no reason at all.
func decideAuthFallback(t *docker.Target, status docker.AuthStatus) authDecision {
	if t.Kind != npm.KindProxy || status.State != docker.AuthKnown {
		return authDecision{}
	}
	host := docker.AuthProvider(t.AuthRequest)

	present := func(provider string) bool {
		if provider == host {
			if strings.TrimSpace(t.AuthRequestUpstream) != "" {
				return true
			}
			return status.Has(provider)
		}
		for _, l := range t.Locations {
			if l.IsEnabled() && authProvider(l.AuthRequest, t.AuthRequest) == provider &&
				strings.TrimSpace(l.AuthRequestUpstream) != "" {
				return true
			}
		}
		return status.Has(provider)
	}

	missing := map[string]bool{}
	if host != "" && !present(host) {
		missing[host] = true
	}
	for _, l := range t.Locations {
		if !l.IsEnabled() {
			continue // NPMplus renders nothing for it
		}
		if provider := authProvider(l.AuthRequest, t.AuthRequest); provider != "" && !present(provider) {
			missing[provider] = true
		}
	}
	if len(missing) == 0 {
		return authDecision{}
	}

	hostMode := fallbackMode(t.AuthRequestFallback, "")
	d := authDecision{Mode: fields.AuthFallbackOpen}
	if missing[host] {
		d.Parts = append(d.Parts, "host")
		if hostMode == fields.AuthFallbackClosed {
			d.Mode = fields.AuthFallbackClosed
		}
	}
	for _, l := range t.Locations {
		if !l.IsEnabled() || !missing[authProvider(l.AuthRequest, t.AuthRequest)] {
			continue
		}
		d.Parts = append(d.Parts, "location "+l.Path)
		// open applies only where it is set: a location that asks for
		// closed closes the whole host, whatever the host itself says.
		if fallbackMode(l.AuthRequestFallback, hostMode) == fields.AuthFallbackClosed {
			d.Mode = fields.AuthFallbackClosed
		}
	}
	for provider := range missing {
		d.Missing = append(d.Missing, provider)
	}
	sort.Strings(d.Missing)
	return d
}

// fallbackMode is the effective auth_request_fallback of a part of a host:
// its own value, else the inherited one, else the built-in default.
func fallbackMode(own, inherited string) string {
	switch {
	case own != "":
		return own
	case inherited != "":
		return inherited
	default:
		return fields.AuthFallbackClosed
	}
}

// applyAuthFallback returns the target to write: a copy with the decision
// applied, or the target itself when nothing falls back. The copy matters:
// the target may well be what the next run starts from again.
func applyAuthFallback(t *docker.Target, d authDecision) *docker.Target {
	if !d.Applies() {
		return t
	}
	out := *t
	out.Locations = slices.Clone(t.Locations)
	if d.Mode == fields.AuthFallbackClosed {
		out.Enabled = false
		return &out
	}

	missing := func(provider string) bool { return slices.Contains(d.Missing, provider) }
	hostMissing := missing(docker.AuthProvider(t.AuthRequest))
	for i := range out.Locations {
		l := &out.Locations[i]
		if !l.IsEnabled() || !missing(authProvider(l.AuthRequest, t.AuthRequest)) {
			continue
		}
		l.AuthRequest, l.AuthRequestUpstream = "none", ""
	}
	if hostMissing {
		out.AuthRequest, out.AuthRequestUpstream = "none", ""
	}
	return &out
}

// FallbackStatus is a host auth_request_fallback applies to, for /status.
type FallbackStatus struct {
	Key       string   `json:"key"`
	Container string   `json:"container"`
	Mode      string   `json:"mode"`
	Providers []string `json:"providers"`
	Parts     []string `json:"parts"`
	Reason    string   `json:"reason"`
	// Created is false for a host that does not exist yet and, being closed,
	// is not created: see reconcileKind.
	Created bool `json:"created"`
}

// noteFallbacks records the decisions of one run and logs the ones that are
// new or changed: an error for a closed host, a warning for an open one, and
// an info line once a host has its forward authentication back.
func (w *Worker) noteFallbacks(log *slog.Logger, current map[string]FallbackStatus, signatures map[string]string) {
	w.refMu.Lock()
	previous := w.fallbackSigs
	w.fallbacks, w.fallbackSigs = current, signatures
	w.refMu.Unlock()

	for _, key := range sortedKeys(current) {
		f := current[key]
		if previous[key] == signatures[key] {
			continue
		}
		attrs := []any{
			slog.String("key", key), slog.String("container", f.Container),
			slog.String("providers", strings.Join(f.Providers, ",")),
			slog.String("parts", strings.Join(f.Parts, ",")),
			slog.String("reason", f.Reason),
		}
		switch {
		case f.Mode == fields.AuthFallbackClosed && f.Created:
			log.Error("forward authentication has no upstream, disabling the host (auth_request_fallback=closed)", attrs...)
			continue
		case f.Mode == fields.AuthFallbackClosed:
			log.Error("forward authentication has no upstream, not creating the host (auth_request_fallback=closed)", attrs...)
			continue
		}
		log.Warn("forward authentication has no upstream, serving the host without it (auth_request_fallback=open)", attrs...)
	}
	for _, key := range sortedKeys(previous) {
		if _, still := current[key]; !still {
			log.Info("forward authentication has its upstream again, writing it as labelled", slog.String("key", key))
		}
	}
}

// Fallbacks lists the hosts auth_request_fallback currently applies to.
func (w *Worker) Fallbacks() []FallbackStatus {
	w.refMu.RLock()
	defer w.refMu.RUnlock()
	out := make([]FallbackStatus, 0, len(w.fallbacks))
	for _, key := range sortedKeys(w.fallbacks) {
		out = append(out, w.fallbacks[key])
	}
	return out
}
