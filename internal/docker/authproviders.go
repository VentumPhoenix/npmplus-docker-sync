package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// AuthRequestProviders are the forward-auth integrations NPMplus can point at
// a global upstream, one AUTH_REQUEST_<PROVIDER>_UPSTREAM variable each
// (backend/internal/nginx.js, authRequestProviders).
var AuthRequestProviders = []string{"anubis", "tinyauth", "oauth2proxy", "voidauth", "authelia", "authentik"}

// AuthProvider returns the provider whose upstream an auth_request value
// uses, or "" for none. authentik-send-basic-auth is authentik with a
// different header set and shares its upstream.
func AuthProvider(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "authentik-send-basic-auth" {
		return "authentik"
	}
	if slices.Contains(AuthRequestProviders, v) {
		return v
	}
	return ""
}

// AuthState says how much is known about the upstreams NPMplus has.
type AuthState int

// The states of an AuthStatus.
const (
	// AuthUnchecked means no container asked for forward authentication,
	// so nothing was looked at.
	AuthUnchecked AuthState = iota
	// AuthUnknown means the NPMplus container could not be found or read.
	// Nothing may fall back because of it.
	AuthUnknown
	// AuthKnown means Providers is the complete list.
	AuthKnown
)

// String implements fmt.Stringer.
func (s AuthState) String() string {
	switch s {
	case AuthUnknown:
		return "unknown"
	case AuthKnown:
		return "known"
	default:
		return "unchecked"
	}
}

// AuthStatus is which auth_request providers NPMplus has a global upstream
// for. It deliberately holds nothing but the provider names: the environment
// it is read from also carries the admin password and OIDC secrets.
type AuthStatus struct {
	State AuthState
	// Providers are the providers with a non-empty global upstream, sorted.
	Providers []string
	// Source is where the list came from: the container it was read from or
	// NPM_AUTH_REQUEST_PROVIDERS.
	Source string
	// Reason explains an AuthUnknown state.
	Reason string
	// ContainerID and Started identify the NPMplus container and its current
	// start. A new start is what can change the upstreams.
	ContainerID string
	Started     string
}

// Has reports whether NPMplus has a global upstream for a provider.
func (s AuthStatus) Has(provider string) bool { return slices.Contains(s.Providers, provider) }

// Fingerprint identifies the status, including the start of the NPMplus
// container it was read from.
func (s AuthStatus) Fingerprint() string {
	return fmt.Sprintf("%s|%s|%s|%s", s.State, strings.Join(s.Providers, ","), s.ContainerID, s.Started)
}

// describe renders the status for the log, which is also how a change of it
// is noticed.
func (s AuthStatus) describe() string {
	switch s.State {
	case AuthKnown:
		return fmt.Sprintf("known from %s: %s", s.Source, providerList(s.Providers))
	case AuthUnknown:
		return "unknown: " + s.Reason
	default:
		return ""
	}
}

func providerList(providers []string) string {
	if len(providers) == 0 {
		return "none"
	}
	return strings.Join(providers, ",")
}

// AuthDetection says how the NPMplus container is found.
type AuthDetection struct {
	// Container is NPM_CONTAINER: a name or an id prefix.
	Container string
	// URLHost is the host part of NPM_URL, matched against the addresses,
	// names and aliases of the containers when Container is empty.
	URLHost string
	// Network is NPM_NETWORK, preferred when several containers match.
	Network string
	// Providers is NPM_AUTH_REQUEST_PROVIDERS. When Fixed is set it replaces
	// the detection entirely, for an NPMplus on another Docker host.
	Providers []string
	Fixed     bool
}

// SetAuthDetection configures how the auth_request upstreams of NPMplus are
// found.
func (l *Listener) SetAuthDetection(d AuthDetection) {
	l.authMu.Lock()
	defer l.authMu.Unlock()
	l.auth = d
}

// authStatus reads which auth_request providers NPMplus has an upstream for.
// It is only called when a target uses forward authentication, so a setup
// without it needs neither the container nor the permission to inspect it.
func (l *Listener) authStatus(ctx context.Context, containers []Container) AuthStatus {
	l.authMu.Lock()
	detection := l.auth
	l.authMu.Unlock()

	status := detectAuth(ctx, l.api, containers, detection, l.selfID)
	l.reportAuth(status)
	return status
}

// detectAuth is authStatus without the logging.
func detectAuth(ctx context.Context, api APIClient, containers []Container, d AuthDetection, selfID string) AuthStatus {
	if d.Fixed {
		providers := append([]string(nil), d.Providers...)
		sort.Strings(providers)
		return AuthStatus{State: AuthKnown, Providers: providers, Source: "NPM_AUTH_REQUEST_PROVIDERS"}
	}
	target, reason := locateNPM(containers, d, selfID)
	if reason != "" {
		return AuthStatus{State: AuthUnknown, Reason: reason}
	}
	inspect, err := api.ContainerInspect(ctx, target.ID)
	if err != nil {
		reason := fmt.Sprintf("cannot inspect the npm container %s: %v", target.Name, err)
		if cerrdefs.IsPermissionDenied(err) || strings.Contains(err.Error(), "403") {
			reason = fmt.Sprintf("docker refused to inspect the npm container %s "+
				"(a socket proxy needs CONTAINERS=1 for GET /containers/{id}/json)", target.Name)
		}
		return AuthStatus{State: AuthUnknown, Reason: reason}
	}
	status := AuthStatus{State: AuthKnown, Source: "container " + target.Name, ContainerID: inspect.ID}
	if inspect.State != nil {
		status.Started = inspect.State.StartedAt
	}
	if inspect.Config != nil {
		status.Providers = ParseAuthUpstreams(inspect.Config.Env)
	}
	return status
}

// reportAuth logs the status when it changed: a warning once for every new
// reason it is unknown, an info line when it is known.
func (l *Listener) reportAuth(status AuthStatus) {
	described := status.describe()
	l.authMu.Lock()
	changed := described != l.authReported
	l.authReported = described
	l.authMu.Unlock()
	if !changed {
		return
	}
	switch status.State {
	case AuthUnknown:
		l.log.Warn("cannot tell which auth_request upstreams npmplus has; "+
			"forward authentication is written as labelled and no auth_request_fallback applies",
			slog.String("reason", status.Reason))
	case AuthKnown:
		l.log.Info("auth_request upstreams configured in npmplus",
			slog.String("providers", providerList(status.Providers)),
			slog.String("source", status.Source))
	}
}

// ParseAuthUpstreams returns the providers an environment sets a non-empty
// AUTH_REQUEST_<PROVIDER>_UPSTREAM for, sorted. Only those names are looked
// at, and no value is kept or returned: the same environment holds the admin
// password and OIDC secrets. NPMplus refuses to start with a malformed value,
// so set or not set is all that matters.
func ParseAuthUpstreams(env []string) []string {
	var out []string
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		provider, ok := strings.CutPrefix(name, "AUTH_REQUEST_")
		if !ok {
			continue
		}
		provider, ok = strings.CutSuffix(provider, "_UPSTREAM")
		if !ok {
			continue
		}
		provider = strings.ToLower(provider)
		if slices.Contains(AuthRequestProviders, provider) && !slices.Contains(out, provider) {
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out
}

// ParseAuthProviders parses NPM_AUTH_REQUEST_PROVIDERS: a comma separated list
// of providers, or "none". authentik-send-basic-auth counts as authentik.
func ParseAuthProviders(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if strings.EqualFold(part, "none") {
			continue
		}
		provider := AuthProvider(part)
		if provider == "" {
			return nil, fmt.Errorf("%q is not an auth_request provider (want %s or none)",
				part, strings.Join(AuthRequestProviders, ", "))
		}
		if !slices.Contains(out, provider) {
			out = append(out, provider)
		}
	}
	sort.Strings(out)
	return out, nil
}

// composeService is the label Docker Compose puts the service name in; the
// service name is what another container of the stack calls NPM by.
const composeService = "com.docker.compose.service"

// locateNPM finds the NPMplus container: by NPM_CONTAINER, or by the host of
// NPM_URL. It returns a reason instead when there is no single answer.
func locateNPM(containers []Container, d AuthDetection, selfID string) (Container, string) {
	if name := strings.TrimSpace(d.Container); name != "" {
		for _, c := range containers {
			if c.Name == name || strings.HasPrefix(c.ID, name) {
				return c, ""
			}
		}
		return Container{}, fmt.Sprintf("NPM_CONTAINER %q matches no container", name)
	}

	host := strings.Trim(strings.TrimSpace(d.URLHost), "[]")
	if host == "" {
		return Container{}, "NPM_URL names no host; set NPM_CONTAINER"
	}
	ip := net.ParseIP(host)
	if strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback()) {
		return Container{}, fmt.Sprintf("NPM_URL points at %s, which is not a container address; set NPM_CONTAINER", host)
	}

	// Every match is scored: a match on NPM_NETWORK beats one elsewhere, so a
	// stack that reuses a name on two networks still has one answer.
	type candidate struct {
		c         Container
		preferred bool
	}
	var matches []candidate
	for _, c := range containers {
		if selfID != "" && c.ID == selfID {
			continue
		}
		matched, preferred := false, false
		for _, n := range c.Networks {
			if !answersTo(c, n, host, ip) {
				continue
			}
			matched = true
			if d.Network != "" && n.Name == d.Network {
				preferred = true
			}
		}
		if !matched && ip == nil && (c.Name == host || c.Labels[composeService] == host) {
			matched = true
		}
		if matched {
			matches = append(matches, candidate{c: c, preferred: preferred})
		}
	}
	if len(matches) > 1 {
		var preferred []candidate
		for _, m := range matches {
			if m.preferred {
				preferred = append(preferred, m)
			}
		}
		if len(preferred) > 0 {
			matches = preferred
		}
	}
	switch len(matches) {
	case 0:
		return Container{}, fmt.Sprintf("no container answers to %q from NPM_URL; set NPM_CONTAINER", host)
	case 1:
		return matches[0].c, ""
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m.c.Name)
	}
	sort.Strings(names)
	return Container{}, fmt.Sprintf("several containers answer to %q from NPM_URL (%s); set NPM_CONTAINER",
		host, strings.Join(names, ", "))
}

// answersTo reports whether a container is reachable as host on network n:
// by its address there, or by its name, an alias or its compose service name.
func answersTo(c Container, n Network, host string, ip net.IP) bool {
	if ip != nil {
		for _, own := range []string{n.IPv4, n.IPv6} {
			if parsed := net.ParseIP(own); parsed != nil && parsed.Equal(ip) {
				return true
			}
		}
		return false
	}
	return c.Name == host || slices.Contains(n.Aliases, host) || c.Labels[composeService] == host
}

// usesAuthRequest reports whether any proxy target asks for forward
// authentication, on the host or one of its locations.
func usesAuthRequest(targets []*Target) bool {
	for _, t := range targets {
		if t.Kind != npm.KindProxy {
			continue
		}
		if AuthProvider(t.AuthRequest) != "" {
			return true
		}
		for _, l := range t.Locations {
			if AuthProvider(l.AuthRequest) != "" {
				return true
			}
		}
	}
	return false
}
