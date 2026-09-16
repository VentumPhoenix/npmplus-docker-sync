package npm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// flavourServer serves a login plus the two endpoints detection looks at.
func flavourServer(t *testing.T, proxyHosts string, health string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "session", Path: "/"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(proxyHosts))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(health))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestDetectFlavour(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxyHosts  string
		health      string
		want        Flavour
		pinned      Flavour
		wantNoProbe bool
	}{
		{
			name:       "npmplus proxy host",
			proxyHosts: `[{"id":1,"domain_names":["a.example.com"],"npmplus_access_list_ids":[],"npmplus_access_list_type":"public"}]`,
			health:     `{"status":"OK","setup":true}`,
			want:       FlavourNPMplus,
		},
		{
			name:       "upstream npm proxy host",
			proxyHosts: `[{"id":1,"domain_names":["a.example.com"],"access_list_id":0}]`,
			health:     `{"status":"OK","setup":true,"version":{"major":2,"minor":12,"revision":6}}`,
			want:       FlavourNPM,
		},
		{
			name:       "empty collection falls back to the health object",
			proxyHosts: `[]`,
			health:     `{"status":"OK","setup":true,"version":{"major":2,"minor":12,"revision":6}}`,
			want:       FlavourNPM,
		},
		{
			name:       "nothing to go on defaults to npmplus",
			proxyHosts: `[]`,
			health:     `{"status":"OK","setup":true}`,
			want:       FlavourNPMplus,
		},
		{
			name:        "a pinned flavour is never probed",
			proxyHosts:  `[{"id":1,"access_list_id":0}]`,
			health:      `{"status":"OK","setup":true,"version":{"major":2}}`,
			pinned:      FlavourNPMplus,
			want:        FlavourNPMplus,
			wantNoProbe: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := flavourServer(t, tt.proxyHosts, tt.health)

			opts := []Option{}
			if tt.pinned != "" {
				opts = append(opts, WithFlavour(tt.pinned))
			}
			client, err := New(server.URL, "admin@example.com", "secret", opts...)
			if err != nil {
				t.Fatalf("New() error: %v", err)
			}
			got, err := client.DetectFlavour(context.Background())
			if err != nil {
				t.Fatalf("DetectFlavour() error: %v", err)
			}
			if got != tt.want {
				t.Errorf("DetectFlavour() = %q, want %q", got, tt.want)
			}
			if client.Flavour() != tt.want {
				t.Errorf("Flavour() = %q, want %q", client.Flavour(), tt.want)
			}
		})
	}
}

func TestParseFlavour(t *testing.T) {
	t.Parallel()

	tests := map[string]Flavour{
		"":         FlavourAuto,
		"auto":     FlavourAuto,
		"NPMplus":  FlavourNPMplus,
		"npm-plus": FlavourNPMplus,
		"npm":      FlavourNPM,
		"upstream": FlavourNPM,
	}
	for raw, want := range tests {
		got, err := ParseFlavour(raw)
		if err != nil || got != want {
			t.Errorf("ParseFlavour(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if _, err := ParseFlavour("traefik"); err == nil {
		t.Error("ParseFlavour(\"traefik\") = nil error, want a rejection")
	}
}

// TestCreateSendsOnlyTheFlavourPayload is the end-to-end guard for the bug
// that made v1.0.0-beta.1 unable to write anything: the response model must
// never reach the wire.
func TestCreateSendsOnlyTheFlavourPayload(t *testing.T) {
	t.Parallel()

	var received map[string]json.RawMessage
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "session", Path: "/"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"domain_names":["a.example.com"],"enabled":true}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := New(server.URL, "admin@example.com", "secret", WithFlavour(FlavourNPMplus))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	host := &ProxyHost{
		ID:          99, // a stale id must not travel
		CreatedOn:   "2026-01-01T00:00:00.000Z",
		DomainNames: []string{"a.example.com"},
		ForwardHost: "10.0.0.2", ForwardPort: 80, ForwardScheme: "http",
		Enabled: true,
		Meta:    Meta{MetaManagedBy: ManagedByValue},
	}
	created, err := client.CreateProxyHost(context.Background(), host)
	if err != nil {
		t.Fatalf("CreateProxyHost() error: %v", err)
	}
	if created.ID != 42 {
		t.Errorf("created id = %d, want 42", created.ID)
	}

	for _, forbidden := range []string{"id", "created_on", "modified_on", "enabled", "access_list_id"} {
		if _, present := received[forbidden]; present {
			t.Errorf("request body contains %q; NPMplus rejects the whole request for it", forbidden)
		}
	}
	for _, required := range []string{"domain_names", "forward_host", "forward_port", "forward_scheme", "npmplus_access_list_ids"} {
		if _, present := received[required]; !present {
			t.Errorf("request body is missing %q", required)
		}
	}
}

// TestSetEnabledUsesTheDedicatedEndpoints and tolerates the 400 both flavours
// answer when the state already matches.
func TestSetEnabledUsesTheDedicatedEndpoints(t *testing.T) {
	t.Parallel()

	var paths []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "npm_token", Value: "session", Path: "/"})
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/nginx/proxy-hosts/7/enable", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"Host is already enabled"}}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts/7/disable", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		_, _ = w.Write([]byte(`true`))
	})
	mux.HandleFunc("/api/nginx/streams/3/disable", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":500,"message":"nope"}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := New(server.URL, "admin@example.com", "secret")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	ctx := context.Background()

	if err := client.SetEnabled(ctx, KindProxy, 7, false); err != nil {
		t.Fatalf("SetEnabled(false) error: %v", err)
	}
	if err := client.SetEnabled(ctx, KindProxy, 7, true); err != nil {
		t.Fatalf("SetEnabled(true) on an already enabled host = %v, want it treated as success", err)
	}
	if err := client.SetEnabled(ctx, KindStream, 3, false); err == nil {
		t.Fatal("SetEnabled() = nil error for a 500, want it reported")
	}

	want := []string{"POST /api/nginx/proxy-hosts/7/disable", "POST /api/nginx/proxy-hosts/7/enable"}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", paths, want)
	}
}
