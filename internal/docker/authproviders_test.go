package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

// forbidden is what the Docker client returns for a 403 from a socket proxy.
type forbidden struct{ error }

func (forbidden) Forbidden() {}

// secretValue is the admin password the NPMplus fixture carries. It must never
// leave the inspect response.
const secretValue = "s3cret-admin-password-1234"

// upstreamValue is the authentik upstream of the fixture; the address is just
// as private as the password.
const upstreamValue = "http://authentik-server.internal:9000"

func npmplusInspect(id string, env ...string) container.InspectResponse {
	return container.InspectResponse{
		ContainerJSONBase: &container.ContainerJSONBase{
			ID:    id,
			State: &container.State{StartedAt: "2026-11-22T10:00:00Z"},
		},
		Config: &container.Config{Env: env},
	}
}

// npmplusEnv is the environment of a typical NPMplus container.
func npmplusEnv() []string {
	return []string{
		"TZ=Europe/Berlin",
		"INITIAL_ADMIN_EMAIL=admin@example.com",
		"INITIAL_ADMIN_PASSWORD=" + secretValue,
		"OIDC_CLIENT_SECRET=" + secretValue,
		"AUTH_REQUEST_AUTHENTIK_UPSTREAM=" + upstreamValue,
		"AUTH_REQUEST_AUTHELIA_UPSTREAM=",
	}
}

func TestParseAuthUpstreams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{"none set", []string{"TZ=UTC", "ACME_EMAIL=a@example.com"}, nil},
		{"empty value is not set", []string{"AUTH_REQUEST_AUTHENTIK_UPSTREAM=", "AUTH_REQUEST_ANUBIS_UPSTREAM=  "}, nil},
		{"variable without a value", []string{"AUTH_REQUEST_AUTHENTIK_UPSTREAM"}, nil},
		{"host and port", []string{"AUTH_REQUEST_AUTHENTIK_UPSTREAM=http://authentik:9000"}, []string{"authentik"}},
		{"cu_ upstream block", []string{"AUTH_REQUEST_AUTHELIA_UPSTREAM=https://cu_authelia"}, []string{"authelia"}},
		{
			"several, sorted",
			[]string{"AUTH_REQUEST_TINYAUTH_UPSTREAM=http://tinyauth:3000", "AUTH_REQUEST_ANUBIS_UPSTREAM=http://anubis:8923"},
			[]string{"anubis", "tinyauth"},
		},
		{"unknown provider", []string{"AUTH_REQUEST_KEYCLOAK_UPSTREAM=http://kc:8080"}, nil},
		{"lower case name is a different variable", []string{"auth_request_authentik_upstream=http://a:9000"}, nil},
		{"value containing =", []string{"AUTH_REQUEST_OAUTH2PROXY_UPSTREAM=http://o2p:4180?a=b"}, []string{"oauth2proxy"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseAuthUpstreams(tt.env); !slices.Equal(got, tt.want) {
				t.Errorf("ParseAuthUpstreams() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAuthProvider(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ value, want string }{
		{"authentik", "authentik"},
		{"authentik-send-basic-auth", "authentik"},
		{" Authelia ", "authelia"},
		{"none", ""},
		{"global", ""},
		{"", ""},
		{"keycloak", ""},
	} {
		if got := AuthProvider(tt.value); got != tt.want {
			t.Errorf("AuthProvider(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestParseAuthProviders(t *testing.T) {
	t.Parallel()

	got, err := ParseAuthProviders("authentik-send-basic-auth, authelia;tinyauth authentik")
	if err != nil || !slices.Equal(got, []string{"authelia", "authentik", "tinyauth"}) {
		t.Errorf("ParseAuthProviders() = %v, %v", got, err)
	}
	if got, err := ParseAuthProviders("none"); err != nil || len(got) != 0 {
		t.Errorf("ParseAuthProviders(none) = %v, %v; want an empty list", got, err)
	}
	if _, err := ParseAuthProviders("authentik,keycloak"); err == nil || !strings.Contains(err.Error(), "keycloak") {
		t.Errorf("ParseAuthProviders(keycloak) error = %v, want it named", err)
	}
}

func TestLocateNPM(t *testing.T) {
	t.Parallel()

	containers := []Container{
		{
			ID: "aaaa1111", Name: "npmplus",
			Labels:   map[string]string{composeService: "npm"},
			Networks: []Network{{Name: "proxy", IPv4: "172.20.0.2", Aliases: []string{"npmplus", "proxy-manager"}}},
		},
		{
			ID: "bbbb2222", Name: "whoami",
			Networks: []Network{{Name: "proxy", IPv4: "172.20.0.3"}, {Name: "other", IPv4: "172.30.0.2"}},
		},
		{
			ID: "cccc3333", Name: "twin",
			Networks: []Network{{Name: "other", IPv4: "172.20.0.2"}},
		},
		{ID: "self0000", Name: "npmplus-docker-sync", Networks: []Network{{Name: "proxy", IPv4: "172.20.0.9"}}},
	}

	tests := []struct {
		name   string
		d      AuthDetection
		want   string // container name, "" for none
		reason string // part of the reason when there is none
	}{
		{name: "NPM_CONTAINER by name", d: AuthDetection{Container: "npmplus"}, want: "npmplus"},
		{name: "NPM_CONTAINER by id prefix", d: AuthDetection{Container: "bbbb"}, want: "whoami"},
		{name: "NPM_CONTAINER wins over NPM_URL", d: AuthDetection{Container: "whoami", URLHost: "npmplus"}, want: "whoami"},
		{name: "NPM_CONTAINER not found", d: AuthDetection{Container: "nope"}, reason: `NPM_CONTAINER "nope"`},
		{name: "by container name", d: AuthDetection{URLHost: "npmplus"}, want: "npmplus"},
		{name: "by network alias", d: AuthDetection{URLHost: "proxy-manager"}, want: "npmplus"},
		{name: "by compose service", d: AuthDetection{URLHost: "npm"}, want: "npmplus"},
		{name: "by ip", d: AuthDetection{URLHost: "172.20.0.3"}, want: "whoami"},
		{name: "ip on two networks, NPM_NETWORK decides", d: AuthDetection{URLHost: "172.20.0.2", Network: "proxy"}, want: "npmplus"},
		{name: "ip on two networks without NPM_NETWORK", d: AuthDetection{URLHost: "172.20.0.2"}, reason: "several containers"},
		{name: "own container is never npm", d: AuthDetection{URLHost: "172.20.0.9"}, reason: "no container answers"},
		{name: "loopback", d: AuthDetection{URLHost: "127.0.0.1"}, reason: "not a container address"},
		{name: "localhost", d: AuthDetection{URLHost: "localhost"}, reason: "not a container address"},
		{name: "unknown name", d: AuthDetection{URLHost: "npm.example.com"}, reason: "no container answers"},
		{name: "no host", d: AuthDetection{}, reason: "NPM_URL names no host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := locateNPM(containers, tt.d, "self0000")
			if tt.want != "" {
				if reason != "" || got.Name != tt.want {
					t.Errorf("locateNPM() = %q (%s), want %q", got.Name, reason, tt.want)
				}
				return
			}
			if reason == "" || !strings.Contains(reason, tt.reason) {
				t.Errorf("locateNPM() = %q, reason %q; want a reason containing %q", got.Name, reason, tt.reason)
			}
		})
	}
}

// authFixture is a Docker host with an NPMplus container and an app protected
// by authentik.
func authFixture() *mockAPI {
	return &mockAPI{
		containers: []container.Summary{
			{
				ID: "npmplus-id", Names: []string{"/npmplus"},
				NetworkSettings: &container.NetworkSettingsSummary{Networks: map[string]*network.EndpointSettings{
					"proxy": {IPAddress: "172.20.0.2"},
				}},
			},
			{
				ID: "app-id", Names: []string{"/app"},
				Labels: map[string]string{
					"npm.proxy.domains": "app.example.com", "npm.proxy.port": "80",
					"npm.proxy.auth_request": "authentik",
				},
			},
		},
		inspect: map[string]container.InspectResponse{"npmplus-id": npmplusInspect("npmplus-id", npmplusEnv()...)},
	}
}

func TestSnapshotReadsTheAuthUpstreams(t *testing.T) {
	t.Parallel()

	api := authFixture()
	l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, nil)
	l.SetAuthDetection(AuthDetection{URLHost: "172.20.0.2"})

	snapshot, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	got := snapshot.Auth
	if got.State != AuthKnown || !slices.Equal(got.Providers, []string{"authentik"}) {
		t.Errorf("Auth = %+v, want authentik known", got)
	}
	if got.ContainerID != "npmplus-id" || got.Started == "" || got.Source != "container npmplus" {
		t.Errorf("Auth = %+v, want the container and its start recorded", got)
	}
	if !slices.Equal(api.inspected, []string{"npmplus-id"}) {
		t.Errorf("inspected %v, want the npmplus container", api.inspected)
	}
}

func TestSnapshotSkipsTheAuthUpstreamsWhenNobodyNeedsThem(t *testing.T) {
	t.Parallel()

	api := authFixture()
	delete(api.containers[1].Labels, "npm.proxy.auth_request")
	l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, nil)
	l.SetAuthDetection(AuthDetection{URLHost: "172.20.0.2"})

	snapshot, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Auth.State != AuthUnchecked || len(api.inspected) != 0 {
		t.Errorf("Auth = %+v after inspecting %v; want nothing looked at", snapshot.Auth, api.inspected)
	}
}

func TestSnapshotTakesTheProvidersFromTheEnvironment(t *testing.T) {
	t.Parallel()

	api := authFixture()
	l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, nil)
	l.SetAuthDetection(AuthDetection{URLHost: "172.20.0.2", Fixed: true, Providers: []string{"authelia"}})

	snapshot, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if got := snapshot.Auth; got.State != AuthKnown || !slices.Equal(got.Providers, []string{"authelia"}) ||
		got.Source != "NPM_AUTH_REQUEST_PROVIDERS" {
		t.Errorf("Auth = %+v, want NPM_AUTH_REQUEST_PROVIDERS to replace the detection", got)
	}
	if len(api.inspected) != 0 {
		t.Errorf("inspected %v although NPM_AUTH_REQUEST_PROVIDERS is set", api.inspected)
	}
}

// TestRefusedInspectIsUnknownAndWarnsOnce: a socket proxy without
// CONTAINERS=1 answers 403. That is "unknown", which never falls back, and the
// log says so once - not on every run.
func TestRefusedInspectIsUnknownAndWarnsOnce(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"forbidden": forbidden{errors.New("Error response from daemon: forbidden")},
		"other":     errors.New("connection reset by peer"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			api := authFixture()
			api.inspectErr = err
			l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, log)
			l.SetAuthDetection(AuthDetection{Container: "npmplus"})

			var snapshot Snapshot
			for range 3 {
				if snapshot, err = l.Snapshot(context.Background()); err != nil {
					t.Fatalf("Snapshot() error = %v", err)
				}
			}
			if snapshot.Auth.State != AuthUnknown || snapshot.Auth.Reason == "" {
				t.Errorf("Auth = %+v, want unknown with a reason", snapshot.Auth)
			}
			if name == "forbidden" && !strings.Contains(snapshot.Auth.Reason, "CONTAINERS=1") {
				t.Errorf("reason %q does not say which permission is missing", snapshot.Auth.Reason)
			}
			if n := strings.Count(buf.String(), "level=WARN"); n != 1 {
				t.Errorf("logged %d warnings, want 1:\n%s", n, buf.String())
			}
		})
	}
}

func TestUnknownContainerIsUnknown(t *testing.T) {
	t.Parallel()

	api := authFixture()
	l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, nil)
	l.SetAuthDetection(AuthDetection{URLHost: "127.0.0.1"})
	snapshot, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Auth.State != AuthUnknown || !strings.Contains(snapshot.Auth.Reason, "NPM_CONTAINER") {
		t.Errorf("Auth = %+v, want unknown, pointing at NPM_CONTAINER", snapshot.Auth)
	}
}

// TestNoEnvironmentValueLeaks: the NPMplus environment carries the admin
// password, OIDC secrets and the upstream addresses. None of it may reach a
// log line - at debug level, in every state - or the status that /status
// serves.
func TestNoEnvironmentValueLeaks(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	api := authFixture()
	l := NewListener(api, ParseOptions{Prefix: "npm", ExposedByDefault: true}, log)

	var statuses []AuthStatus
	for _, d := range []AuthDetection{
		{Container: "npmplus"},
		{URLHost: "172.20.0.2"},
		{Container: "missing"},
	} {
		l.SetAuthDetection(d)
		snapshot, err := l.Snapshot(context.Background())
		if err != nil {
			t.Fatalf("Snapshot() error = %v", err)
		}
		statuses = append(statuses, snapshot.Auth)
	}
	api.inspectErr = errors.New("inspect failed")
	l.SetAuthDetection(AuthDetection{Container: "npmplus"})
	snapshot, err := l.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	statuses = append(statuses, snapshot.Auth)

	rendered := buf.String() + fmt.Sprintf("%+v %#v", statuses, statuses)
	for _, secret := range []string{secretValue, upstreamValue, "authentik-server.internal"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("%q leaked into the log or the status:\n%s", secret, rendered)
		}
	}
	if !strings.Contains(buf.String(), "auth_request upstreams configured in npmplus") {
		t.Errorf("the known status was not logged at all:\n%s", buf.String())
	}
}
