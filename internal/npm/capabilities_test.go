package npm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// capabilityServer serves a login, a proxy host listing and - when schema is
// not empty - GET /api/schema. Both can be swapped while the test runs, which
// is what an upgrade of the server looks like from the client.
type capabilityServer struct {
	mu         sync.Mutex
	schema     []byte
	proxyHosts string
}

func (s *capabilityServer) set(schema []byte, proxyHosts string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schema, s.proxyHosts = schema, proxyHosts
}

func (s *capabilityServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := tokenHandler(http.NewServeMux())
	mux.HandleFunc("/api/schema", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if len(s.schema) == 0 {
			http.NotFound(w, nil)
			return
		}
		_, _ = w.Write(s.schema)
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":400,"message":"data must NOT have additional properties"}}`))
			return
		}
		_, _ = w.Write([]byte(s.proxyHosts))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK","version":"2026-10-22-r1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCapabilitiesComeFromTheSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		flavour, ref  string
		wantMeta      bool
		wantDirectory bool
	}{
		{"npmplus", "2026-10-22-r1", true, false},
		{"npmplus", "develop", false, true},
		{"npm", "v2.16.0", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.flavour+"/"+tc.ref, func(t *testing.T) {
			t.Parallel()
			server := &capabilityServer{}
			server.set(vendoredSchemaDocument(t, tc.flavour, tc.ref), `[]`)
			client := newTestClient(t, server.start(t), WithFlavour(Flavour(tc.flavour)))
			if _, err := client.DetectFlavour(context.Background()); err != nil {
				t.Fatalf("DetectFlavour() error = %v", err)
			}
			dialect := client.Dialect()
			if dialect.Schema == nil {
				t.Fatal("the schema was not read")
			}
			if got := dialect.Accepts(KindProxy, "meta"); got != tc.wantMeta {
				t.Errorf("Accepts(meta) = %t, want %t", got, tc.wantMeta)
			}
			if got := dialect.Accepts(KindProxy, "npmplus_directory"); got != tc.wantDirectory {
				t.Errorf("Accepts(npmplus_directory) = %t, want %t", got, tc.wantDirectory)
			}
			for _, kind := range Kinds {
				if !dialect.KeepsOwnership(kind) {
					t.Errorf("%s: the server could not keep the owner", kind)
				}
			}
		})
	}
}

// TestCapabilitiesFallBackToTheHostShape covers a server without a schema
// endpoint: the hosts it returns tell an NPMplus that retired the meta object
// from one that did not.
func TestCapabilitiesFallBackToTheHostShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		proxyHosts  string
		wantRetired bool
	}{
		{"meta object", `[{"id":1,"npmplus_access_list_ids":[],"meta":{}}]`, false},
		{"group column", `[{"id":1,"npmplus_access_list_ids":[],"npmplus_directory":""}]`, true},
		{"no host to look at", `[]`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := &capabilityServer{}
			server.set(nil, tc.proxyHosts)
			client := newTestClient(t, server.start(t))
			if _, err := client.DetectFlavour(context.Background()); err != nil {
				t.Fatalf("DetectFlavour() error = %v", err)
			}
			dialect := client.Dialect()
			if dialect.Schema != nil || dialect.MetaRetired != tc.wantRetired {
				t.Errorf("Dialect() = schema %v, meta retired %t; want no schema, %t",
					dialect.Schema != nil, dialect.MetaRetired, tc.wantRetired)
			}
		})
	}
}

// TestARejectionRereadsTheCapabilities: NPMplus is upgraded underneath a
// running process. The first write after it is rejected; the capabilities are
// read again - also with a pinned flavour, which names the fork, not the
// release - and the next run writes the new dialect.
func TestARejectionRereadsTheCapabilities(t *testing.T) {
	t.Parallel()

	server := &capabilityServer{}
	server.set(vendoredSchemaDocument(t, "npmplus", "2026-10-22-r1"), `[]`)
	client := newTestClient(t, server.start(t), WithFlavour(FlavourNPMplus))
	if _, err := client.DetectFlavour(context.Background()); err != nil {
		t.Fatalf("DetectFlavour() error = %v", err)
	}
	if !client.Dialect().Accepts(KindProxy, "meta") {
		t.Fatal("the released NPMplus accepts the meta object")
	}

	server.set(vendoredSchemaDocument(t, "npmplus", "develop"), `[]`)
	_, err := client.Create(context.Background(), &ProxyHost{
		DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80,
	})
	if err == nil {
		t.Fatal("Create() error = nil, want the rejection reported")
	}
	if client.Dialect().Accepts(KindProxy, "meta") {
		t.Error("the capabilities were not read again after the rejection")
	}
	if client.Flavour() != FlavourNPMplus {
		t.Errorf("Flavour() = %s, want the pinned flavour kept", client.Flavour())
	}
}
