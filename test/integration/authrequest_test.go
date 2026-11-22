//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// npmContainer is the NPMplus container of the stack. NPM_URL points at the
// published port on 127.0.0.1, which is no container address, so the tool is
// told where to read the auth_request upstreams from.
const npmContainer = "npmsync-it-npm"

// authUpstream is a valid AUTH_REQUEST_<PROVIDER>_UPSTREAM: scheme, host and
// port, nothing else. Whoami answers every path with 200, which is all the
// auth_request subrequest needs to let a request through.
const authUpstream = "http://npmsync-it-whoami:80"

// TestAuthRequestFallback runs the three cases against an NPMplus without
// AUTH_REQUEST_AUTHENTIK_UPSTREAM: open serves the host without the login,
// closed disables an existing host (and does not create a new one), and
// setting the variable plus restarting NPMplus brings the forward
// authentication back without restarting the tool.
func TestAuthRequestFallback(t *testing.T) {
	c := client(t)
	if c.Flavour() != npm.FlavourNPMplus {
		t.Skip("auth_request is an NPMplus feature")
	}
	detect := "NPM_CONTAINER=" + npmContainer

	labels := func(domain string, extra map[string]string) map[string]string {
		out := map[string]string{
			"npm.proxy.domains":      domain,
			"npm.proxy.port":         "80",
			"npm.proxy.auth_request": "authentik",
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	startContainer(t, "it-auth-open", labels("auth-open.test", map[string]string{"npm.proxy.auth_request_fallback": "open"}))
	// Created while its own override supplies the upstream, so it exists
	// and is online before the override goes away.
	startContainer(t, "it-auth-closed", labels("auth-closed.test", map[string]string{"npm.proxy.auth_request_upstream": authUpstream}))
	startContainer(t, "it-auth-new", labels("auth-new.test", nil))

	first := mustSync(t, detect)
	if !strings.Contains(first.Output, "auth_request upstreams configured in npmplus") {
		t.Fatalf("the upstreams were not read from %s:\n%s", npmContainer, first.Output)
	}

	t.Run("open serves the host without the login", func(t *testing.T) {
		host := proxyHost(t, c, "auth-open.test")
		if !host.IsEnabled() || host.AuthRequest != "none" {
			t.Fatalf("enabled=%t auth_request=%q, want an enabled host without forward authentication", host.IsEnabled(), host.AuthRequest)
		}
		requireServed(t, "auth-open.test")
		if !strings.Contains(first.Output, "serving the host without it") {
			t.Errorf("no warning about the open fallback:\n%s", first.Output)
		}
	})

	t.Run("closed does not create a new host", func(t *testing.T) {
		requireNoHost(t, c, npm.KindProxy, "auth-new.test")
		if !strings.Contains(first.Output, "not creating the host") {
			t.Errorf("no error about the closed fallback:\n%s", first.Output)
		}
	})

	t.Run("closed disables an existing host", func(t *testing.T) {
		host := proxyHost(t, c, "auth-closed.test")
		if !host.IsEnabled() || host.AuthRequest != "authentik" {
			t.Fatalf("with its override: enabled=%t auth_request=%q, want enabled with authentik", host.IsEnabled(), host.AuthRequest)
		}
		if online, message, reported := npm.NginxStatus(host); reported && !online {
			t.Fatalf("the host with an override is offline: %s", message)
		}

		removeContainer("it-auth-closed")
		startContainer(t, "it-auth-closed", labels("auth-closed.test", nil))
		run := mustSync(t, detect)

		host = proxyHost(t, c, "auth-closed.test")
		if host.IsEnabled() {
			t.Fatalf("the host is still enabled without an upstream:\n%s", run.Output)
		}
		if host.AuthRequest != "authentik" {
			t.Errorf("the closed host lost its forward authentication: %q", host.AuthRequest)
		}
		if !strings.Contains(run.Output, "disabling the host") {
			t.Errorf("no error about the closed fallback:\n%s", run.Output)
		}
		requireNoErrFile(t, host.ResourceID())
	})

	t.Run("the upstream brings the forward authentication back", func(t *testing.T) {
		stop := startDaemon(t, detect)
		defer stop()

		recreateNPM(t, "AUTH_REQUEST_AUTHENTIK_UPSTREAM="+authUpstream)
		t.Cleanup(func() { recreateNPM(t) })

		// No sync is run by hand: the start event of NPMplus, and the retry
		// while its API is still coming up, are what has to apply it.
		var last string
		waitFor(t, "the forward authentication to come back", 4*time.Minute, func() bool {
			state, err := authState()
			last = fmt.Sprint(state, err)
			return err == nil &&
				state["auth-open.test"] == "authentik/enabled" &&
				state["auth-closed.test"] == "authentik/enabled" &&
				state["auth-new.test"] == "authentik/enabled"
		})
		t.Logf("recovered: %s", last)
	})
}

// proxyHost returns the live proxy host for a domain.
func proxyHost(t *testing.T, c *npm.Client, domain string) *npm.ProxyHost {
	t.Helper()
	host, ok := requireHost(t, c, npm.KindProxy, domain).(*npm.ProxyHost)
	if !ok {
		t.Fatalf("%s is not a proxy host", domain)
	}
	return host
}

// authState reads auth_request and the enabled flag of every proxy host with
// a fresh session, since NPMplus may just have restarted. It returns an error
// instead of failing while the API is not back yet.
func authState() (map[string]string, error) {
	c, err := client0(npmIdentity, npmSecret)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Login(ctx); err != nil {
		return nil, err
	}
	if _, err := c.DetectFlavour(ctx); err != nil {
		return nil, err
	}
	list, err := c.List(ctx, npm.KindProxy)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, r := range list {
		host, ok := r.(*npm.ProxyHost)
		if !ok {
			continue
		}
		state := "disabled"
		if host.IsEnabled() {
			state = "enabled"
		}
		out[host.ResourceKey()] = host.AuthRequest + "/" + state
	}
	return out, nil
}

// requireNoErrFile asserts that NPMplus kept no rejected configuration for a
// host: a disabled host has no configuration at all.
func requireNoErrFile(t *testing.T, id int) {
	t.Helper()
	path := fmt.Sprintf("/data/nginx/proxy_host/%d.conf.err", id)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := run(ctx, "", "docker", "exec", npmContainer, "sh", "-c", "test ! -e "+path)
	if err != nil {
		t.Errorf("%s exists: %v\n%s", path, err, out)
	}
}

// recreateNPM recreates the NPM container with extra environment, keeping its
// data volume, and waits until its API answers again.
func recreateNPM(t *testing.T, env ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	args := []string{"compose", "-p", project, "-f", "docker-compose.yml", "up", "-d", "--wait", "--wait-timeout", "420", "npm"}
	cmd := exec.CommandContext(ctx, "docker", args...) //nolint:gosec // fixed test command
	cmd.Env = append(os.Environ(),
		"NPM_IMAGE="+npmImage,
		"NPM_IDENTITY="+npmIdentity,
		"NPM_SECRET="+npmSecret,
		"NPM_API_PORT="+apiPort,
		"NPM_HTTP_PORT="+httpPort,
		"NPM_STREAM_PORT="+streamPort,
		"NPM_LETSENCRYPT_TARGET="+letsEncryptTarget,
		// Unset unless the caller sets it: the last occurrence wins.
		"AUTH_REQUEST_AUTHENTIK_UPSTREAM=",
	)
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recreate npm: %v\n%s", err, out)
	}
	if err := waitForAPI(ctx); err != nil {
		t.Fatalf("npm did not come back: %v\n%s", err, npmLogs())
	}
}
