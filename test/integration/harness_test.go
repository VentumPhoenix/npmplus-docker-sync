//go:build integration

// Package integration drives the real thing: a running NPM (or NPMplus), a
// real Docker daemon and the binary this repository builds.
//
// Unit and contract tests prove that a payload matches the API schema. They
// cannot prove that the API accepts it, that /enable works, that the login and
// the token refresh behave, that the flavour detection is right, or that nginx
// actually serves the host afterwards. That is what these tests are for.
//
// Run them with:
//
//	make integration                       # NPM, the default
//	NPM_IMAGE=ghcr.io/zoeyvid/npmplus:... make integration
package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// The harness is configured through the same environment the compose file
// reads, so `docker compose` and `go test` always agree.
var (
	npmImage    = env("NPM_IMAGE", "jc21/nginx-proxy-manager:latest")
	npmIdentity = env("NPM_IDENTITY", "admin@example.com")
	npmSecret   = env("NPM_SECRET", "integration-secret")
	apiPort     = env("NPM_API_PORT", "8181")
	httpPort    = env("NPM_HTTP_PORT", "8180")
	streamPort  = env("NPM_STREAM_PORT", "8543")
	network     = "npmsync-it"
	project     = "npmsync-it"
)

// defaultCredentials are what both projects ship with; the harness rotates
// them when the image does not seed a user from the environment.
const (
	defaultIdentity = "admin@example.com"
	defaultSecret   = "changeme"
)

// binary is the freshly built sync binary, set up by TestMain.
var binary string

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func apiURL() string  { return "http://127.0.0.1:" + apiPort }
func httpURL() string { return "http://127.0.0.1:" + httpPort }

// TestMain builds the binary, starts the stack and tears it down again.
func TestMain(m *testing.M) {
	code, err := runSuite(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration harness: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func runSuite(m *testing.M) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	dir, err := os.MkdirTemp("", "npmsync-it")
	if err != nil {
		return 1, err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	binary = filepath.Join(dir, "npmplus-docker-sync")
	if out, err := run(ctx, "", "go", "build", "-o", binary, "../.."); err != nil {
		return 1, fmt.Errorf("build the binary: %w\n%s", err, out)
	}

	if keep := os.Getenv("KEEP_STACK"); keep != "1" {
		defer composeDown(context.Background())
	}
	if err := composeUp(ctx); err != nil {
		logs, _ := compose(context.Background(), "logs", "--no-color", "--tail", "80")
		return 1, fmt.Errorf("start the stack: %w\n%s", err, logs)
	}
	if err := waitForAPI(ctx); err != nil {
		logs, _ := compose(context.Background(), "logs", "--no-color", "--tail", "80")
		return 1, fmt.Errorf("wait for the npm api: %w\n%s", err, logs)
	}
	if err := ensureAdmin(ctx); err != nil {
		return 1, fmt.Errorf("prepare the admin account: %w", err)
	}
	return m.Run(), nil
}

// ---------------------------------------------------------------------------
// compose and docker
// ---------------------------------------------------------------------------

func compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-p", project, "-f", "docker-compose.yml"}, args...)
	return run(ctx, "", "docker", full...)
}

func composeUp(ctx context.Context) error {
	out, err := compose(ctx, "up", "-d", "--wait", "--wait-timeout", "300")
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

func composeDown(ctx context.Context) {
	if out, err := compose(ctx, "down", "-v", "--remove-orphans"); err != nil {
		fmt.Fprintf(os.Stderr, "compose down: %v\n%s", err, out)
	}
}

// run executes a command with a deadline and returns its combined output.
func run(ctx context.Context, stdin, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed test commands
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Env = append(os.Environ(),
		"NPM_IMAGE="+npmImage,
		"NPM_IDENTITY="+npmIdentity,
		"NPM_SECRET="+npmSecret,
		"NPM_API_PORT="+apiPort,
		"NPM_HTTP_PORT="+httpPort,
		"NPM_STREAM_PORT="+streamPort,
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// docker runs a docker command and fails the test when it does.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	out, err := run(ctx, "", "docker", args...)
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(out)
}

// startContainer runs a labelled whoami and removes it when the test ends.
func startContainer(t *testing.T, name string, labels map[string]string, extra ...string) string {
	t.Helper()

	args := []string{"run", "-d", "--name", name, "--network", network, "--label", "npmsync-it=1"}
	for key, value := range labels {
		args = append(args, "--label", key+"="+value)
	}
	args = append(args, extra...)
	args = append(args, "traefik/whoami:latest")

	id := docker(t, args...)
	t.Cleanup(func() { removeContainer(name) })
	return id
}

// removeContainer deletes a container, ignoring "no such container".
func removeContainer(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = run(ctx, "", "docker", "rm", "-f", name)
}

// ---------------------------------------------------------------------------
// the sync binary
// ---------------------------------------------------------------------------

// syncRun is one invocation of the tool.
type syncRun struct {
	Output string
	Err    error
}

// Failed reports whether the run exited non-zero.
func (r syncRun) Failed() bool { return r.Err != nil }

// sync runs `npmplus-docker-sync sync` once with the given extra environment.
// A single deterministic reconcile beats waiting for a debounce window.
func syncOnce(t *testing.T, extra ...string) syncRun {
	t.Helper()
	return runBinary(t, []string{"sync"}, extra...)
}

// validate runs the `validate` subcommand, which never touches NPM.
func validate(t *testing.T, extra ...string) syncRun {
	t.Helper()
	return runBinary(t, []string{"validate"}, extra...)
}

func runBinary(t *testing.T, args []string, extra ...string) syncRun {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // the binary this repo builds
	cmd.Env = append(os.Environ(),
		"NPM_URL="+apiURL(),
		"NPM_IDENTITY="+npmIdentity,
		"NPM_SECRET="+npmSecret,
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"NPM_NETWORK="+network,
		"LOG_LEVEL=debug",
		"LOG_FORMAT=text",
		"RESYNC_INTERVAL=0",
	)
	cmd.Env = append(cmd.Env, extra...)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	out := buf.String()
	if testing.Verbose() {
		t.Logf("%s %s\n%s", filepath.Base(binary), strings.Join(args, " "), out)
	}
	return syncRun{Output: out, Err: err}
}

// mustSync runs one reconcile and fails the test when it does not succeed.
func mustSync(t *testing.T, extra ...string) syncRun {
	t.Helper()
	run := syncOnce(t, extra...)
	if run.Failed() {
		t.Fatalf("sync failed: %v\n%s", run.Err, run.Output)
	}
	return run
}

// startDaemon runs the tool the way it runs in production - watching the event
// stream - and returns a function that stops it again.
func startDaemon(t *testing.T, extra ...string) func() {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary) //nolint:gosec // the binary this repo builds
	cmd.Env = append(os.Environ(),
		"NPM_URL="+apiURL(),
		"NPM_IDENTITY="+npmIdentity,
		"NPM_SECRET="+npmSecret,
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"NPM_NETWORK="+network,
		"LOG_LEVEL=debug",
		"RESYNC_INTERVAL=30s",
	)
	cmd.Env = append(cmd.Env, extra...)

	var buf syncBuffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatalf("start the daemon: %v", err)
	}

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		// SIGTERM first: the shutdown flush is part of what is being tested.
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			cancel()
		}
		cancel()
		if testing.Verbose() {
			t.Logf("daemon log:\n%s", buf.String())
		}
	}
	t.Cleanup(stop)
	return stop
}

// syncBuffer is a mutex-guarded buffer: the daemon writes from its own
// goroutine while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// the npm api
// ---------------------------------------------------------------------------

// client returns an API client for the stack, using the real client of this
// repository - which is also how the flavour detection gets exercised.
func client(t *testing.T) *npm.Client {
	t.Helper()

	c, err := npm.New(apiURL(), npmIdentity, npmSecret, npm.WithTimeout(30*time.Second))
	if err != nil {
		t.Fatalf("npm.New: %v", err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.DetectFlavour(context.Background()); err != nil {
		t.Fatalf("detect flavour: %v", err)
	}
	return c
}

// waitForAPI blocks until the API answers at all.
func waitForAPI(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL()+"/api/", nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New("timed out")
}

// ensureAdmin makes the configured credentials work.
//
// Recent NPM and NPMplus images seed the first user from the environment;
// older ones ship admin@example.com/changeme and expect the password to be
// changed on first login. Both are handled, so the suite does not depend on
// which of the two an image does.
func ensureAdmin(ctx context.Context) error {
	if err := login(ctx, npmIdentity, npmSecret); err == nil {
		return nil
	}
	if err := login(ctx, defaultIdentity, defaultSecret); err != nil {
		return fmt.Errorf("neither the configured nor the default credentials work: %w", err)
	}
	// Rotate the default account into the configured one.
	c, err := npm.New(apiURL(), defaultIdentity, defaultSecret, npm.WithTimeout(30*time.Second))
	if err != nil {
		return err
	}
	if err := c.Login(ctx); err != nil {
		return err
	}
	if err := c.UpdateOwnCredentials(ctx, npmIdentity, defaultSecret, npmSecret); err != nil {
		return fmt.Errorf("rotate the default credentials: %w", err)
	}
	return login(ctx, npmIdentity, npmSecret)
}

func login(ctx context.Context, identity, secret string) error {
	c, err := npm.New(apiURL(), identity, secret, npm.WithTimeout(30*time.Second))
	if err != nil {
		return err
	}
	return c.Login(ctx)
}

// ---------------------------------------------------------------------------
// assertions
// ---------------------------------------------------------------------------

// waitFor polls until the condition holds or the timeout expires.
func waitFor(t *testing.T, what string, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// proxyHosts returns the live proxy hosts.
func resources(t *testing.T, c *npm.Client, kind npm.Kind) []npm.Resource {
	t.Helper()
	list, err := c.List(context.Background(), kind)
	if err != nil {
		t.Fatalf("list %s: %v", kind, err)
	}
	return list
}

// findByKey returns the resource with that identity, or nil.
func findByKey(list []npm.Resource, key string) npm.Resource {
	for _, r := range list {
		if r.ResourceKey() == key {
			return r
		}
	}
	return nil
}

// requireHost fails unless a host with that domain exists.
func requireHost(t *testing.T, c *npm.Client, kind npm.Kind, key string) npm.Resource {
	t.Helper()
	found := findByKey(resources(t, c, kind), key)
	if found == nil {
		t.Fatalf("no %s for %q", kind, key)
	}
	return found
}

// requireNoHost fails when a host with that domain exists.
func requireNoHost(t *testing.T, c *npm.Client, kind npm.Kind, key string) {
	t.Helper()
	if found := findByKey(resources(t, c, kind), key); found != nil {
		t.Fatalf("%s %q still exists (id %d)", kind, key, found.ResourceID())
	}
}

// getThroughNPM asks nginx itself, which is the only proof that a host does
// what it was created for.
func getThroughNPM(t *testing.T, host string) (int, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpURL()+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Host = host

	// Redirects would take us to https on a port this test does not publish.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s through npm: %v", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	return resp.StatusCode, string(body)
}

// requireServed asserts that nginx answers for a host with the whoami backend.
func requireServed(t *testing.T, host string) {
	t.Helper()
	waitFor(t, "nginx to serve "+host, 30*time.Second, func() bool {
		status, body := getThroughNPM(t, host)
		return status == http.StatusOK && strings.Contains(body, "Hostname:")
	})
}
