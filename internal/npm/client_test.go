package npm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient wires a client against a test server.
func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	c, err := New(srv.URL, "admin@example.com", "changeme", opts...)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestLoginDetectsAuthMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		wantMode      AuthMode
		wantAuthzHdr  string
		wantCookie    string
		wantLoginErr  bool
		wantExpiresIn time.Duration
	}{
		{
			name: "npm returns jwt in json body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token":"jwt-123","expires":"2099-01-01T00:00:00.000Z"}`))
			},
			wantMode:     AuthBearer,
			wantAuthzHdr: "Bearer jwt-123",
		},
		{
			name: "npmplus sets httponly cookie",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "cookie-abc", Path: "/", HttpOnly: true})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"expires":"2099-01-01T00:00:00.000Z"}`))
			},
			wantMode:   AuthCookie,
			wantCookie: "npm_token=cookie-abc",
		},
		{
			name: "cookie wins when both are present",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "cookie-xyz", Path: "/"})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"token":"jwt-should-be-ignored"}`))
			},
			wantMode:     AuthCookie,
			wantCookie:   "npm_token=cookie-xyz",
			wantAuthzHdr: "",
		},
		{
			name: "cookie without json body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "only-cookie", Path: "/"})
				w.WriteHeader(http.StatusOK)
			},
			wantMode:   AuthCookie,
			wantCookie: "npm_token=only-cookie",
		},
		{
			name: "empty response is an error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			},
			wantLoginErr: true,
		},
		{
			name: "expired cookie is not a session",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "gone", Path: "/", MaxAge: -1})
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			},
			wantLoginErr: true,
		},
		{
			name: "invalid credentials",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Invalid email or password"}}`))
			},
			wantLoginErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				gotAuthz  atomic.Value
				gotCookie atomic.Value
			)
			gotAuthz.Store("")
			gotCookie.Store("")

			mux := http.NewServeMux()
			mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
				var creds map[string]string
				if err := json.NewDecoder(r.Body).Decode(&creds); err != nil && r.ContentLength > 0 {
					t.Errorf("login payload is not JSON: %v", err)
				}
				if creds["identity"] != "admin@example.com" || creds["secret"] != "changeme" {
					t.Errorf("unexpected credentials %v", creds)
				}
				tc.handler(w, r)
			})
			mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
				gotAuthz.Store(r.Header.Get("Authorization"))
				gotCookie.Store(r.Header.Get("Cookie"))
				_, _ = w.Write([]byte(`[]`))
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()

			client := newTestClient(t, srv)
			err := client.Login(context.Background())
			if tc.wantLoginErr {
				if err == nil {
					t.Fatalf("Login() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Login() error = %v", err)
			}
			if got := client.AuthMode(); got != tc.wantMode {
				t.Fatalf("AuthMode() = %v, want %v", got, tc.wantMode)
			}

			if _, err := client.ListProxyHosts(context.Background()); err != nil {
				t.Fatalf("ListProxyHosts() error = %v", err)
			}
			if got := gotAuthz.Load().(string); got != tc.wantAuthzHdr {
				t.Errorf("Authorization header = %q, want %q", got, tc.wantAuthzHdr)
			}
			if tc.wantCookie != "" {
				if got := gotCookie.Load().(string); !strings.Contains(got, tc.wantCookie) {
					t.Errorf("Cookie header = %q, want it to contain %q", got, tc.wantCookie)
				}
			}
		})
	}
}

func TestClientReauthenticatesOn401(t *testing.T) {
	t.Parallel()

	var logins, listCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		n := logins.Add(1)
		_, _ = w.Write([]byte(`{"token":"jwt-` + string(rune('0'+n)) + `"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		// The session is revoked exactly once, mimicking an NPM restart.
		if listCalls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-2" {
			t.Errorf("retry used stale token %q", got)
		}
		_, _ = w.Write([]byte(`[{"id":7,"domain_names":["a.example.com"]}]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv)
	hosts, err := client.ListProxyHosts(context.Background())
	if err != nil {
		t.Fatalf("ListProxyHosts() error = %v", err)
	}
	if len(hosts) != 1 || hosts[0].ID != 7 {
		t.Fatalf("hosts = %+v, want one host with id 7", hosts)
	}
	if got := logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (initial + retry)", got)
	}
}

func TestClientRefreshesBeforeExpiry(t *testing.T) {
	t.Parallel()

	var logins atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		logins.Add(1)
		expires := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
		_, _ = w.Write([]byte(`{"token":"jwt","expires":"` + expires + `"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv)
	for i := 0; i < 3; i++ {
		if _, err := client.ListProxyHosts(context.Background()); err != nil {
			t.Fatalf("ListProxyHosts() error = %v", err)
		}
	}
	// The token expires within the refresh window, so every call re-logs in.
	if got := logins.Load(); got != 3 {
		t.Errorf("logins = %d, want 3 (token always inside refresh window)", got)
	}
}

func TestProxyHostCRUD(t *testing.T) {
	t.Parallel()

	var deleted atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"jwt"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		var got ProxyHost
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode create payload: %v", err)
		}
		if got.ForwardPort != 8080 || got.ForwardHost != "whoami" {
			t.Errorf("payload = %+v, want forward whoami:8080", got)
		}
		got.ID = 42
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(got)
	})
	mux.HandleFunc("/api/nginx/proxy-hosts/42", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPut:
			_, _ = w.Write([]byte(`{"id":42,"domain_names":["a.example.com"]}`))
		case http.MethodDelete:
			deleted.Add(1)
			_, _ = w.Write([]byte(`true`))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/nginx/proxy-hosts/99", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not found"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()

	created, err := client.CreateProxyHost(ctx, &ProxyHost{
		DomainNames: []string{"a.example.com"},
		ForwardHost: "whoami",
		ForwardPort: 8080,
	})
	if err != nil {
		t.Fatalf("CreateProxyHost() error = %v", err)
	}
	if created.ID != 42 {
		t.Fatalf("created.ID = %d, want 42", created.ID)
	}
	if _, err := client.UpdateProxyHost(ctx, 42, created); err != nil {
		t.Fatalf("UpdateProxyHost() error = %v", err)
	}
	if err := client.DeleteProxyHost(ctx, 42); err != nil {
		t.Fatalf("DeleteProxyHost() error = %v", err)
	}
	if got := deleted.Load(); got != 1 {
		t.Errorf("delete calls = %d, want 1", got)
	}

	err = client.DeleteProxyHost(ctx, 99)
	if !IsNotFound(err) {
		t.Fatalf("DeleteProxyHost(99) error = %v, want a 404 APIError", err)
	}
}

func TestNewValidatesBaseURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "npm:81", "://broken"} {
		if _, err := New(raw, "a", "b"); err == nil {
			t.Errorf("New(%q) error = nil, want error", raw)
		}
	}
	c, err := New("http://npm:81/", "a", "b")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := c.BaseURL(); got != "http://npm:81" {
		t.Errorf("BaseURL() = %q, want trailing slash removed", got)
	}
}

// collectionServer serves all four NPM collections from memory.
type collectionServer struct {
	mu       sync.Mutex
	payloads map[string]string
	requests []string
	notFound map[string]bool
	lastBody map[string]string
	nextID   int
}

func newCollectionServer() *collectionServer {
	return &collectionServer{
		payloads: map[string]string{
			"/api/nginx/proxy-hosts":       `[{"id":1,"domain_names":["app.example.com"],"forward_host":"app","forward_port":80,"forward_scheme":"http","certificate_id":0,"meta":{"managed_by":"npmplus-docker-sync"}}]`,
			"/api/nginx/redirection-hosts": `[{"id":2,"domain_names":["old.example.com"],"forward_domain_name":"app.example.com","forward_http_code":301,"forward_scheme":"auto","preserve_path":true,"certificate_id":"3"}]`,
			"/api/nginx/streams":           `[{"id":3,"incoming_port":5432,"forwarding_host":"db","forwarding_port":5432,"tcp_forwarding":true,"udp_forwarding":false,"certificate_id":null}]`,
			"/api/nginx/dead-hosts":        `[{"id":4,"domain_names":["parked.example.com"],"certificate_id":0,"enabled":1}]`,
		},
		notFound: map[string]bool{},
		lastBody: map[string]string{},
		nextID:   100,
	}
}

func (s *collectionServer) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"token":"jwt"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)

		collection := r.URL.Path
		if idx := strings.LastIndex(collection, "/"); idx > 0 {
			if _, err := strconv.Atoi(collection[idx+1:]); err == nil {
				collection = collection[:idx]
			}
		}
		if s.notFound[collection] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":404,"message":"Not found"}}`))
			return
		}

		switch r.Method {
		case http.MethodGet:
			payload, ok := s.payloads[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(payload))
		case http.MethodPost, http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			s.lastBody[collection] = string(body)
			var generic map[string]any
			if err := json.Unmarshal(body, &generic); err != nil {
				t.Errorf("%s %s: invalid JSON payload: %v", r.Method, r.URL.Path, err)
			}
			if generic["id"] != nil {
				t.Errorf("%s %s: payload must not carry an id: %s", r.Method, r.URL.Path, body)
			}
			generic["id"] = s.nextID
			s.nextID++
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(generic)
		case http.MethodDelete:
			_, _ = w.Write([]byte(`true`))
		}
	})
	return mux
}

func TestClientListsEveryCollection(t *testing.T) {
	t.Parallel()

	backend := newCollectionServer()
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()

	for _, kind := range npmKinds() {
		resources, err := client.List(ctx, kind)
		if err != nil {
			t.Fatalf("List(%s) error = %v", kind, err)
		}
		if len(resources) != 1 {
			t.Fatalf("List(%s) = %d resources, want 1", kind, len(resources))
		}
		if resources[0].Kind() != kind {
			t.Errorf("List(%s) returned kind %s", kind, resources[0].Kind())
		}
		if resources[0].ResourceID() == 0 {
			t.Errorf("List(%s) lost the resource id", kind)
		}
	}

	// The polymorphic certificate_id shapes must all decode.
	redirects, err := client.ListRedirectionHosts(ctx)
	if err != nil {
		t.Fatalf("ListRedirectionHosts() error = %v", err)
	}
	if redirects[0].CertificateID != CertificateRef(3) {
		t.Errorf("certificate_id = %v, want the numeric string to decode", redirects[0].CertificateID)
	}
	streams, err := client.ListStreams(ctx)
	if err != nil {
		t.Fatalf("ListStreams() error = %v", err)
	}
	if !streams[0].CertificateID.IsZero() || streams[0].IncomingPort.Int() != 5432 {
		t.Errorf("stream = %+v", streams[0])
	}
	deadHosts, err := client.ListDeadHosts(ctx)
	if err != nil {
		t.Fatalf("ListDeadHosts() error = %v", err)
	}
	if !bool(deadHosts[0].Enabled) {
		t.Error("numeric enabled flag did not decode to true")
	}
}

func TestClientCreateUpdateDeleteAllKinds(t *testing.T) {
	t.Parallel()

	backend := newCollectionServer()
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	client := newTestClient(t, srv)
	ctx := context.Background()

	resources := []Resource{
		&ProxyHost{DomainNames: []string{"new.example.com"}, ForwardHost: "app", ForwardPort: 80, ForwardScheme: "http"},
		&RedirectionHost{DomainNames: []string{"old.example.com"}, ForwardDomainName: "new.example.com", ForwardHTTPCode: 301, ForwardScheme: "auto"},
		&Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432), TCPForwarding: true},
		&DeadHost{DomainNames: []string{"parked.example.com"}},
	}

	for _, resource := range resources {
		created, err := client.Create(ctx, resource)
		if err != nil {
			t.Fatalf("Create(%s) error = %v", resource.Kind(), err)
		}
		if created.ResourceID() == 0 {
			t.Errorf("Create(%s) did not return an id", resource.Kind())
		}
		if created.Kind() != resource.Kind() {
			t.Errorf("Create(%s) returned kind %s", resource.Kind(), created.Kind())
		}

		if _, err := client.Update(ctx, created.ResourceID(), resource); err != nil {
			t.Fatalf("Update(%s) error = %v", resource.Kind(), err)
		}
		if err := client.Delete(ctx, resource.Kind(), created.ResourceID()); err != nil {
			t.Fatalf("Delete(%s) error = %v", resource.Kind(), err)
		}
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, kind := range npmKinds() {
		for _, method := range []string{"POST", "PUT", "DELETE"} {
			want := method + " " + kind.Path()
			found := false
			for _, req := range backend.requests {
				if strings.HasPrefix(req, want) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("missing request %q; got %v", want, backend.requests)
			}
		}
	}
}

func TestClientMissingCollectionIsNotFound(t *testing.T) {
	t.Parallel()

	backend := newCollectionServer()
	backend.notFound["/api/nginx/streams"] = true
	srv := httptest.NewServer(backend.handler(t))
	defer srv.Close()

	client := newTestClient(t, srv)
	_, err := client.List(context.Background(), KindStream)
	if !IsNotFound(err) {
		t.Fatalf("List(stream) error = %v, want a 404 APIError", err)
	}
}

func TestClientRejectsUnknownKind(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(newCollectionServer().handler(t))
	defer srv.Close()

	client := newTestClient(t, srv)
	if _, err := client.List(context.Background(), Kind("nonsense")); err == nil {
		t.Error("List() error = nil for an unknown kind")
	}
	if err := client.Delete(context.Background(), Kind("nonsense"), 1); err == nil {
		t.Error("Delete() error = nil for an unknown kind")
	}
}

// npmKinds returns the package level kind list (kept local so the test reads
// independently of the exported slice being mutated elsewhere).
func npmKinds() []Kind { return []Kind{KindProxy, KindRedirect, KindStream, KindDead} }
