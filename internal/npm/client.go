// Package npm implements a minimal, dependency-free API client for
// Nginx Proxy Manager and NPMplus.
//
// The two flavours authenticate differently:
//
//	NPM      POST /api/tokens -> {"token":"<jwt>"} -> Authorization: Bearer <jwt>
//	NPMplus  POST /api/tokens -> Set-Cookie: npm_token=...; HttpOnly
//
// The client detects the mode from the login response and transparently
// re-authenticates when the session expires or the API answers 401.
package npm

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// AuthMode describes how the client authenticates against the API.
type AuthMode int

const (
	// AuthUnknown means no successful login has happened yet.
	AuthUnknown AuthMode = iota
	// AuthCookie is used by NPMplus (httpOnly session cookies).
	AuthCookie
	// AuthBearer is used by upstream NPM (JWT in the Authorization header).
	AuthBearer
)

// String implements fmt.Stringer.
func (m AuthMode) String() string {
	switch m {
	case AuthCookie:
		return "cookie"
	case AuthBearer:
		return "bearer"
	default:
		return "unknown"
	}
}

// refreshWindow is how long before the reported expiry the client renews
// its session proactively.
const refreshWindow = 5 * time.Minute

// maxErrorBody caps how much of an error response body is kept for logging.
const maxErrorBody = 4 << 10

// drainLimit caps how much of a response body is read before closing it, so
// the connection can be reused without an unbounded read.
const drainLimit = 64 << 10

// Client talks to the NPM/NPMplus REST API. It is safe for concurrent use.
type Client struct {
	baseURL   *url.URL
	hc        *http.Client
	identity  string
	secret    string
	userAgent string
	log       *slog.Logger
	now       func() time.Time

	// logPayloads mirrors full request bodies into the debug log. Off by
	// default: the meta object can carry DNS provider credentials.
	logPayloads bool

	mu      sync.RWMutex
	mode    AuthMode
	token   string
	expires time.Time
	flavour Flavour

	loginMu sync.Mutex // serialises (re-)logins
}

// Option customises the client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client. The client must have a
// cookie jar for NPMplus support; New installs one if it is missing.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.hc = hc }
}

// WithTimeout sets the per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.hc.Timeout = d }
}

// WithInsecureSkipVerify disables TLS verification. Only use it for NPM
// instances behind a self-signed certificate on a trusted network.
func WithInsecureSkipVerify(skip bool) Option {
	return func(c *Client) {
		if !skip {
			return
		}
		transport, ok := c.hc.Transport.(*http.Transport)
		if !ok || transport == nil {
			transport = http.DefaultTransport.(*http.Transport).Clone()
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{} //nolint:gosec // opt-in only
		}
		transport.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // opt-in only
		c.hc.Transport = transport
	}
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithPayloadLogging mirrors complete request bodies into the debug log.
// Values of credential-looking meta keys are redacted, but the bodies still
// contain host names and advanced nginx config, so this stays opt-in.
func WithPayloadLogging(enabled bool) Option {
	return func(c *Client) { c.logPayloads = enabled }
}

// WithClock injects a time source (tests).
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}

// New creates a client for the given base URL (e.g. http://npm:81).
func New(baseURL, identity, secret string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("npm: base URL must not be empty")
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("npm: parse base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("npm: base URL %q must be absolute", baseURL)
	}

	c := &Client{
		baseURL:   u,
		hc:        &http.Client{Timeout: 30 * time.Second},
		identity:  identity,
		secret:    secret,
		userAgent: "npm-docker-sync",
		log:       slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})),
		now:       time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if c.hc.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("npm: create cookie jar: %w", err)
		}
		c.hc.Jar = jar
	}
	return c, nil
}

// AuthMode reports the detected authentication mode.
func (c *Client) AuthMode() AuthMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mode
}

// BaseURL returns the configured API base URL.
func (c *Client) BaseURL() string { return c.baseURL.String() }

// Login authenticates against POST /api/tokens and detects whether the
// server uses cookies (NPMplus) or a bearer token (NPM).
func (c *Client) Login(ctx context.Context) error {
	c.loginMu.Lock()
	defer c.loginMu.Unlock()
	return c.login(ctx)
}

// login performs the actual request. Callers must hold loginMu.
func (c *Client) login(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{
		"identity": c.identity,
		"secret":   c.secret,
	})
	if err != nil {
		return fmt.Errorf("npm: encode credentials: %w", err)
	}

	req, err := c.newRequest(ctx, http.MethodPost, "/api/tokens", bytes.NewReader(payload))
	if err != nil {
		return err
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("npm: login request: %w", err)
	}
	defer func() {
		// Draining before closing lets the connection go back to the pool.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.statusError(resp, http.MethodPost, "/api/tokens")
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("npm: read login response: %w", err)
	}

	var tok tokenResponse
	if len(bytes.TrimSpace(body)) > 0 {
		// A missing/invalid JSON body is not fatal as long as a cookie was set.
		if err := json.Unmarshal(body, &tok); err != nil && !hasSessionCookie(resp) {
			return fmt.Errorf("npm: decode login response: %w", err)
		}
	}

	mode := AuthUnknown
	switch {
	case hasSessionCookie(resp):
		// NPMplus: rely purely on the cookie jar from here on.
		mode = AuthCookie
	case strings.TrimSpace(tok.Token) != "":
		mode = AuthBearer
	default:
		return errors.New("npm: login succeeded but neither a session cookie nor a token was returned")
	}

	c.mu.Lock()
	c.mode = mode
	c.token = strings.TrimSpace(tok.Token)
	c.expires = tok.Expires.Time
	c.mu.Unlock()

	c.log.Info("authenticated against npm api",
		slog.String("mode", mode.String()),
		slog.String("url", c.baseURL.String()),
		slog.Time("expires", tok.Expires.Time))
	return nil
}

// hasSessionCookie reports whether the response sets a usable cookie.
func hasSessionCookie(resp *http.Response) bool {
	for _, cookie := range resp.Cookies() {
		if cookie == nil || cookie.Value == "" {
			continue
		}
		// An expired cookie is a logout, not a session.
		if cookie.MaxAge < 0 {
			continue
		}
		return true
	}
	return false
}

// ensureAuth logs in when no session exists yet or when it is about to expire.
func (c *Client) ensureAuth(ctx context.Context) error {
	c.mu.RLock()
	mode, expires := c.mode, c.expires
	c.mu.RUnlock()

	if mode != AuthUnknown && (expires.IsZero() || c.now().Add(refreshWindow).Before(expires)) {
		return nil
	}

	c.loginMu.Lock()
	defer c.loginMu.Unlock()

	// Another goroutine may have refreshed while we waited for the lock.
	c.mu.RLock()
	mode, expires = c.mode, c.expires
	c.mu.RUnlock()
	if mode != AuthUnknown && (expires.IsZero() || c.now().Add(refreshWindow).Before(expires)) {
		return nil
	}
	return c.login(ctx)
}

// ---------------------------------------------------------------------------
// Resource access
//
// Every collection is available in two shapes: a typed accessor (handy for
// library users) and a Kind-dispatching one used by the reconcile worker so
// it can treat all four resource types uniformly.
// ---------------------------------------------------------------------------

// ListProxyHosts returns every proxy host known to NPM.
func (c *Client) ListProxyHosts(ctx context.Context) ([]ProxyHost, error) {
	return listCollection[ProxyHost](ctx, c, KindProxy)
}

// ListRedirectionHosts returns every redirection host known to NPM.
func (c *Client) ListRedirectionHosts(ctx context.Context) ([]RedirectionHost, error) {
	return listCollection[RedirectionHost](ctx, c, KindRedirect)
}

// ListStreams returns every TCP/UDP stream known to NPM.
func (c *Client) ListStreams(ctx context.Context) ([]Stream, error) {
	return listCollection[Stream](ctx, c, KindStream)
}

// ListDeadHosts returns every 404 host known to NPM.
func (c *Client) ListDeadHosts(ctx context.Context) ([]DeadHost, error) {
	return listCollection[DeadHost](ctx, c, KindDead)
}

// CreateProxyHost creates a new proxy host.
func (c *Client) CreateProxyHost(ctx context.Context, host *ProxyHost) (*ProxyHost, error) {
	return createTyped(ctx, c, host)
}

// UpdateProxyHost updates an existing proxy host.
func (c *Client) UpdateProxyHost(ctx context.Context, id int, host *ProxyHost) (*ProxyHost, error) {
	return updateTyped(ctx, c, id, host)
}

// DeleteProxyHost removes a proxy host.
func (c *Client) DeleteProxyHost(ctx context.Context, id int) error {
	return c.Delete(ctx, KindProxy, id)
}

// CreateRedirectionHost creates a new redirection host.
func (c *Client) CreateRedirectionHost(ctx context.Context, host *RedirectionHost) (*RedirectionHost, error) {
	return createTyped(ctx, c, host)
}

// UpdateRedirectionHost updates an existing redirection host.
func (c *Client) UpdateRedirectionHost(ctx context.Context, id int, host *RedirectionHost) (*RedirectionHost, error) {
	return updateTyped(ctx, c, id, host)
}

// DeleteRedirectionHost removes a redirection host.
func (c *Client) DeleteRedirectionHost(ctx context.Context, id int) error {
	return c.Delete(ctx, KindRedirect, id)
}

// CreateStream creates a new stream.
func (c *Client) CreateStream(ctx context.Context, stream *Stream) (*Stream, error) {
	return createTyped(ctx, c, stream)
}

// UpdateStream updates an existing stream.
func (c *Client) UpdateStream(ctx context.Context, id int, stream *Stream) (*Stream, error) {
	return updateTyped(ctx, c, id, stream)
}

// DeleteStream removes a stream.
func (c *Client) DeleteStream(ctx context.Context, id int) error {
	return c.Delete(ctx, KindStream, id)
}

// CreateDeadHost creates a new 404 host.
func (c *Client) CreateDeadHost(ctx context.Context, host *DeadHost) (*DeadHost, error) {
	return createTyped(ctx, c, host)
}

// UpdateDeadHost updates an existing 404 host.
func (c *Client) UpdateDeadHost(ctx context.Context, id int, host *DeadHost) (*DeadHost, error) {
	return updateTyped(ctx, c, id, host)
}

// DeleteDeadHost removes a 404 host.
func (c *Client) DeleteDeadHost(ctx context.Context, id int) error {
	return c.Delete(ctx, KindDead, id)
}

// List returns every resource of the given kind.
func (c *Client) List(ctx context.Context, kind Kind) ([]Resource, error) {
	switch kind {
	case KindProxy:
		return collect[ProxyHost](c.ListProxyHosts(ctx))
	case KindRedirect:
		return collect[RedirectionHost](c.ListRedirectionHosts(ctx))
	case KindStream:
		return collect[Stream](c.ListStreams(ctx))
	case KindDead:
		return collect[DeadHost](c.ListDeadHosts(ctx))
	default:
		return nil, fmt.Errorf("npm: unknown resource kind %q", kind)
	}
}

// Create adds a resource and returns the version NPM stored.
//
// Only the flavour specific request payload is sent, never the response model:
// both APIs reject unknown properties outright.
func (c *Client) Create(ctx context.Context, resource Resource) (Resource, error) {
	kind := resource.Kind()
	created := newResource(kind)
	if created == nil {
		return nil, fmt.Errorf("npm: unknown resource kind %q", kind)
	}
	payload, err := c.payloadFor(resource)
	if err != nil {
		return nil, err
	}
	if err := c.do(ctx, http.MethodPost, kind.Path(), payload, created); err != nil {
		return nil, err
	}
	return created, nil
}

// Update replaces an existing resource.
func (c *Client) Update(ctx context.Context, id int, resource Resource) (Resource, error) {
	kind := resource.Kind()
	updated := newResource(kind)
	if updated == nil {
		return nil, fmt.Errorf("npm: unknown resource kind %q", kind)
	}
	payload, err := c.payloadFor(resource)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("%s/%d", kind.Path(), id)
	if err := c.do(ctx, http.MethodPut, path, payload, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

// payloadFor renders the request body for the flavour in use, defaulting to
// NPMplus when detection has not run.
func (c *Client) payloadFor(resource Resource) (any, error) {
	flavour := c.Flavour()
	if flavour == FlavourAuto {
		flavour = FlavourNPMplus
	}
	payload, err := resource.Payload(flavour)
	if err != nil {
		return nil, fmt.Errorf("npm: build %s payload for %s: %w", resource.Kind().Label(), flavour, err)
	}
	return payload, nil
}

// SetEnabled switches a resource on or off. Both flavours refuse `enabled` in
// a create/update body and expose dedicated endpoints instead, which answer
// 400 "Host is already enabled" when the state already matches - that is
// treated as success.
func (c *Client) SetEnabled(ctx context.Context, kind Kind, id int, enabled bool) error {
	if !kind.Valid() {
		return fmt.Errorf("npm: unknown resource kind %q", kind)
	}
	action := "disable"
	if enabled {
		action = "enable"
	}
	path := fmt.Sprintf("%s/%d/%s", kind.Path(), id, action)
	err := c.do(ctx, http.MethodPost, path, nil, nil)
	if err != nil && isAlreadyInState(err) {
		return nil
	}
	return err
}

// isAlreadyInState recognises the 400 both flavours answer when the resource
// is already in the requested state.
func isAlreadyInState(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	msg := strings.ToLower(apiErr.Message)
	return strings.Contains(msg, "already enabled") || strings.Contains(msg, "already disabled")
}

// Delete removes a resource of the given kind.
func (c *Client) Delete(ctx context.Context, kind Kind, id int) error {
	if !kind.Valid() {
		return fmt.Errorf("npm: unknown resource kind %q", kind)
	}
	path := fmt.Sprintf("%s/%d", kind.Path(), id)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ListCertificates returns the certificates stored in NPM.
func (c *Client) ListCertificates(ctx context.Context) ([]Certificate, error) {
	var certs []Certificate
	if err := c.do(ctx, http.MethodGet, "/api/nginx/certificates", nil, &certs); err != nil {
		return nil, err
	}
	return certs, nil
}

// ListAccessLists returns the access lists stored in NPM, so labels can name
// them instead of pinning an id that differs between instances.
func (c *Client) ListAccessLists(ctx context.Context) ([]AccessList, error) {
	var lists []AccessList
	if err := c.do(ctx, http.MethodGet, "/api/nginx/access-lists", nil, &lists); err != nil {
		return nil, err
	}
	return lists, nil
}

// listCollection fetches a typed collection.
func listCollection[T any](ctx context.Context, c *Client, kind Kind) ([]T, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("npm: unknown resource kind %q", kind)
	}
	var out []T
	if err := c.do(ctx, http.MethodGet, kind.Path(), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// createTyped is the typed wrapper around Create.
func createTyped[T any, P interface {
	*T
	Resource
}](ctx context.Context, c *Client, resource P) (P, error) {
	created, err := c.Create(ctx, resource)
	if err != nil {
		return nil, err
	}
	typed, ok := created.(P)
	if !ok {
		return nil, fmt.Errorf("npm: unexpected response type %T for %s", created, resource.Kind())
	}
	return typed, nil
}

// updateTyped is the typed wrapper around Update.
func updateTyped[T any, P interface {
	*T
	Resource
}](ctx context.Context, c *Client, id int, resource P) (P, error) {
	updated, err := c.Update(ctx, id, resource)
	if err != nil {
		return nil, err
	}
	typed, ok := updated.(P)
	if !ok {
		return nil, fmt.Errorf("npm: unexpected response type %T for %s", updated, resource.Kind())
	}
	return typed, nil
}

// collect converts a typed collection into []Resource without copying the
// elements twice.
func collect[T any, P interface {
	*T
	Resource
}](items []T, err error) ([]Resource, error) {
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0, len(items))
	for i := range items {
		out = append(out, P(&items[i]))
	}
	return out, nil
}

// newResource returns an empty resource of the given kind.
func newResource(kind Kind) Resource {
	switch kind {
	case KindProxy:
		return &ProxyHost{}
	case KindRedirect:
		return &RedirectionHost{}
	case KindStream:
		return &Stream{}
	case KindDead:
		return &DeadHost{}
	default:
		return nil
	}
}

// Ping performs a cheap authenticated request; used by the readiness probe.
func (c *Client) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/api/", nil, nil)
}

// do executes an authenticated API call, retrying once after a 401/403 with a
// fresh session (covers server restarts and revoked tokens).
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if err := c.ensureAuth(ctx); err != nil {
		return err
	}

	var payload []byte
	if in != nil {
		var err error
		if payload, err = json.Marshal(in); err != nil {
			return fmt.Errorf("npm: encode request body: %w", err)
		}
	}
	c.logRequest(method, path, payload)

	for attempt := 0; attempt < 2; attempt++ {
		staleSession, err := c.attempt(ctx, method, path, payload, out)
		if !staleSession {
			return err
		}
		if attempt == 1 {
			return err
		}
		// The session expired or was revoked (NPM restarts invalidate them):
		// log in once more and replay the request.
		c.invalidate()
		c.loginMu.Lock()
		loginErr := c.login(ctx)
		c.loginMu.Unlock()
		if loginErr != nil {
			return loginErr
		}
	}
	return fmt.Errorf("npm: %s %s: authentication retry exhausted", method, path)
}

// attempt performs exactly one request and owns its response body. It reports
// whether the failure was a stale session, which is the only case do() retries.
func (c *Client) attempt(ctx context.Context, method, path string, payload []byte, out any) (staleSession bool, err error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return false, err
	}
	c.applyAuth(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("npm: %s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, drainLimit))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return true, &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: "unauthorized"}

	case resp.StatusCode < 200 || resp.StatusCode > 299:
		apiErr := c.statusError(resp, method, path)
		// A schema rejection names neither the offending property nor the body
		// that caused it, so pair the two up while we still have both.
		c.log.Debug("npm api rejected a request",
			slog.String("method", method),
			slog.String("path", path),
			slog.Int("status", resp.StatusCode),
			slog.String("flavour", c.Flavour().String()),
			slog.String("request_fields", strings.Join(payloadKeys(payload), ",")),
			slog.String("error", apiErr.Error()))
		return false, apiErr
	}

	if out == nil {
		return false, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("npm: decode %s %s response: %w", method, path, err)
	}
	return false, nil
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL.String()+path, body)
	if err != nil {
		return nil, fmt.Errorf("npm: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// applyAuth injects the bearer token when the server is token based. In
// cookie mode nothing is added: the cookie jar handles it.
func (c *Client) applyAuth(req *http.Request) {
	c.mu.RLock()
	mode, token := c.mode, c.token
	c.mu.RUnlock()
	if mode == AuthBearer && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

func (c *Client) invalidate() {
	c.mu.Lock()
	c.mode = AuthUnknown
	c.token = ""
	c.expires = time.Time{}
	c.mu.Unlock()
}

func (c *Client) statusError(resp *http.Response, method, path string) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	body := strings.TrimSpace(string(raw))
	msg := body
	var envelope apiError
	if json.Unmarshal(raw, &envelope) == nil && envelope.Error.Message != "" {
		msg = envelope.Error.Message
	}
	return &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: msg, Body: body}
}

// logRequest records what is about to be sent. The field names alone are
// enough to spot a schema mismatch, and unlike the values they cannot leak a
// credential, so they are logged unconditionally at debug level.
func (c *Client) logRequest(method, path string, payload []byte) {
	if len(payload) == 0 || !c.log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	attrs := []any{
		slog.String("method", method),
		slog.String("path", path),
		slog.String("flavour", c.Flavour().String()),
		slog.Int("bytes", len(payload)),
		slog.String("fields", strings.Join(payloadKeys(payload), ",")),
	}
	if c.logPayloads {
		attrs = append(attrs, slog.String("body", string(redactPayload(payload))))
	}
	c.log.Debug("npm api request", attrs...)
}

// payloadKeys lists the top level properties of a JSON request body, sorted.
// This is exactly the information an `additionalProperties: false` rejection
// withholds.
func payloadKeys(payload []byte) []string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// secretish matches meta keys whose value must never reach a log sink; the
// Let's Encrypt DNS challenge stores provider API tokens in `meta`.
func secretish(key string) bool {
	key = strings.ToLower(key)
	for _, needle := range []string{"credential", "secret", "password", "token", "api_key", "apikey"} {
		if strings.Contains(key, needle) {
			return true
		}
	}
	return false
}

// redactPayload replaces credential-looking values anywhere in the body.
func redactPayload(payload []byte) []byte {
	var decoded any
	if json.Unmarshal(payload, &decoded) != nil {
		return []byte(`"<unparseable>"`)
	}
	redacted, err := json.Marshal(redactValue(decoded))
	if err != nil {
		return []byte(`"<unmarshalable>"`)
	}
	return redacted
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			if secretish(key) {
				out[key] = "***"
				continue
			}
			out[key] = redactValue(nested)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, nested := range typed {
			out = append(out, redactValue(nested))
		}
		return out
	default:
		return value
	}
}

// APIError is returned for non-2xx API responses.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
	// Body is the raw response body (truncated), kept for diagnostics when
	// the API returns something other than its usual error envelope.
	Body string
}

// Error implements error.
func (e *APIError) Error() string {
	target := strings.TrimSpace(e.Method + " " + e.Path)
	if e.Message == "" {
		return fmt.Sprintf("npm: %s failed with status %d", target, e.Status)
	}
	return fmt.Sprintf("npm: %s failed with status %d: %s", target, e.Status, e.Message)
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}
