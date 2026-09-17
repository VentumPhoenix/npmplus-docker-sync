package npm

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tokenHandler answers the login of every test server in this file.
func tokenHandler(mux *http.ServeMux) *http.ServeMux {
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token": "test-token", "expires": time.Now().Add(time.Hour).Unix(),
		})
	})
	return mux
}

// TestSecretsNeverReachTheLog is the test for the redaction helpers: the debug
// log of a request body must not carry the DNS provider credentials a
// certificate request puts into `meta`.
func TestSecretsNeverReachTheLog(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mux := tokenHandler(http.NewServeMux())
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv, WithLogger(logger), WithPayloadLogging(true))
	host := &ProxyHost{
		DomainNames: []string{"app.example.com"}, ForwardScheme: "http",
		ForwardHost: "app", ForwardPort: 80,
		Meta: Meta{
			"dns_provider_credentials": "dns_cloudflare_api_token = super-secret-value",
			"letsencrypt_email":        "admin@example.com",
			"api_key":                  "another-secret",
			"nested":                   map[string]any{"password": "hunter2"},
		},
	}
	if _, err := client.Create(context.Background(), host); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	logged := buffer.String()
	for _, secret := range []string{"super-secret-value", "another-secret", "hunter2"} {
		if strings.Contains(logged, secret) {
			t.Errorf("the log leaked %q:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, "***") {
		t.Errorf("the redacted body should be logged, got:\n%s", logged)
	}
	if !strings.Contains(logged, "admin@example.com") {
		t.Error("harmless fields should survive redaction, so a 400 can still be debugged")
	}
}

func TestSecretish(t *testing.T) {
	t.Parallel()

	for key, want := range map[string]bool{
		"dns_provider_credentials": true,
		"letsencrypt_credential":   true,
		"API_KEY":                  true,
		"apikey":                   true,
		"Password":                 true,
		"token":                    true,
		"secret":                   true,
		"letsencrypt_email":        false,
		"domain_names":             false,
		"forward_host":             false,
	} {
		if got := secretish(key); got != want {
			t.Errorf("secretish(%q) = %t, want %t", key, got, want)
		}
	}
}

func TestRedactPayloadHandlesBrokenBodies(t *testing.T) {
	t.Parallel()

	if got := string(redactPayload([]byte("not json"))); got != `"<unparseable>"` {
		t.Errorf("redactPayload(broken) = %s", got)
	}
	got := string(redactPayload([]byte(`{"a":[{"token":"x"},{"b":1}]}`)))
	if strings.Contains(got, `"x"`) {
		t.Errorf("a secret inside an array survived redaction: %s", got)
	}
}

// The typed helpers are the API most callers use; they must speak to the right
// paths and decode the answers.
func TestTypedClientMethods(t *testing.T) {
	t.Parallel()

	var seen []string
	mux := tokenHandler(http.NewServeMux())
	record := func(pattern string, body string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, r.Method+" "+r.URL.Path)
			_, _ = w.Write([]byte(body))
		})
	}
	record("/api/nginx/redirection-hosts", `{"id":11}`)
	record("/api/nginx/redirection-hosts/11", `{"id":11}`)
	record("/api/nginx/streams", `{"id":12}`)
	record("/api/nginx/streams/12", `{"id":12}`)
	record("/api/nginx/dead-hosts", `{"id":13}`)
	record("/api/nginx/dead-hosts/13", `{"id":13}`)
	record("/api/nginx/certificates", `[{"id":5,"provider":"letsencrypt","nice_name":"wildcard","domain_names":["*.example.com"],"expires_on":"2030-01-01 00:00:00","is_deleted":0}]`)
	record("/api/nginx/access-lists", `[{"id":4,"name":"Intern"}]`)
	record("/api/", `{"status":"OK","version":{"major":2,"minor":12,"revision":6}}`)

	srv := httptest.NewServer(mux)
	defer srv.Close()
	client := newTestClient(t, srv)
	ctx := context.Background()

	if _, err := client.CreateRedirectionHost(ctx, &RedirectionHost{DomainNames: []string{"a.example.com"}}); err != nil {
		t.Fatalf("CreateRedirectionHost: %v", err)
	}
	if _, err := client.UpdateRedirectionHost(ctx, 11, &RedirectionHost{DomainNames: []string{"a.example.com"}}); err != nil {
		t.Fatalf("UpdateRedirectionHost: %v", err)
	}
	if err := client.DeleteRedirectionHost(ctx, 11); err != nil {
		t.Fatalf("DeleteRedirectionHost: %v", err)
	}
	if _, err := client.CreateStream(ctx, &Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432)}); err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if _, err := client.UpdateStream(ctx, 12, &Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432)}); err != nil {
		t.Fatalf("UpdateStream: %v", err)
	}
	if err := client.DeleteStream(ctx, 12); err != nil {
		t.Fatalf("DeleteStream: %v", err)
	}
	if _, err := client.CreateDeadHost(ctx, &DeadHost{DomainNames: []string{"parked.example.com"}}); err != nil {
		t.Fatalf("CreateDeadHost: %v", err)
	}
	if _, err := client.UpdateDeadHost(ctx, 13, &DeadHost{DomainNames: []string{"parked.example.com"}}); err != nil {
		t.Fatalf("UpdateDeadHost: %v", err)
	}
	if err := client.DeleteDeadHost(ctx, 13); err != nil {
		t.Fatalf("DeleteDeadHost: %v", err)
	}

	certs, err := client.ListCertificates(ctx)
	if err != nil || len(certs) != 1 || certs[0].ID != 5 {
		t.Fatalf("ListCertificates() = %+v, %v", certs, err)
	}
	if certs[0].DomainNames[0] != "*.example.com" || bool(certs[0].IsDeleted) {
		t.Errorf("certificate decoded wrong: %+v", certs[0])
	}
	lists, err := client.ListAccessLists(ctx)
	if err != nil || len(lists) != 1 || lists[0].Name != "Intern" {
		t.Fatalf("ListAccessLists() = %+v, %v", lists, err)
	}
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}

	for _, want := range []string{
		"POST /api/nginx/redirection-hosts", "PUT /api/nginx/redirection-hosts/11",
		"DELETE /api/nginx/redirection-hosts/11", "POST /api/nginx/streams",
		"PUT /api/nginx/streams/12", "DELETE /api/nginx/streams/12",
		"POST /api/nginx/dead-hosts", "PUT /api/nginx/dead-hosts/13",
		"DELETE /api/nginx/dead-hosts/13", "GET /api/nginx/certificates",
		"GET /api/nginx/access-lists", "GET /api/",
	} {
		if !containsString(seen, want) {
			t.Errorf("%q was never requested; calls: %v", want, seen)
		}
	}
}

// A schema rejection means the server may no longer be the flavour it was at
// startup. The dialect is probed again, so the next reconcile writes the right
// body instead of failing forever.
func TestSchemaRejectionReprobesTheFlavour(t *testing.T) {
	t.Parallel()

	flavour := "npm"
	mux := tokenHandler(http.NewServeMux())
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// The probe: the collection now looks like NPMplus.
			if flavour == "npmplus" {
				_, _ = w.Write([]byte(`[{"id":1,"npmplus_access_list_ids":[]}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"id":1,"access_list_id":0}]`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"data must NOT have additional properties"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv)
	if got, err := client.DetectFlavour(context.Background()); err != nil || got != FlavourNPM {
		t.Fatalf("DetectFlavour() = %s, %v; want npm", got, err)
	}

	// The server was upgraded to NPMplus in the meantime.
	flavour = "npmplus"
	_, err := client.Create(context.Background(), &ProxyHost{
		DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80,
	})
	if err == nil {
		t.Fatal("Create() error = nil, want the rejection to be reported")
	}
	if got := client.Flavour(); got != FlavourNPMplus {
		t.Errorf("Flavour() = %s, want the dialect to be re-probed", got)
	}
}

// A pinned flavour is never re-probed: the operator's choice wins over any
// heuristic.
func TestPinnedFlavourSurvivesARejection(t *testing.T) {
	t.Parallel()

	mux := tokenHandler(http.NewServeMux())
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":1,"npmplus_access_list_ids":[]}]`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"data must NOT have additional properties"}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv, WithFlavour(FlavourNPM))
	_, _ = client.Create(context.Background(), &ProxyHost{
		DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "a", ForwardPort: 80,
	})
	if got := client.Flavour(); got != FlavourNPM {
		t.Errorf("Flavour() = %s, want the pinned dialect", got)
	}
}

// Recheck notices a server upgrade between two resyncs.
func TestRecheckNoticesAVersionChange(t *testing.T) {
	t.Parallel()

	version := `{"major":2,"minor":12,"revision":6}`
	mux := tokenHandler(http.NewServeMux())
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"OK","version":` + version + `}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newTestClient(t, srv)
	if err := client.Recheck(context.Background()); err != nil {
		t.Fatalf("Recheck() error = %v", err)
	}
	first := client.ServerVersion()
	if first == "" {
		t.Fatal("ServerVersion() is empty after the first check")
	}

	version = `{"major":2,"minor":13,"revision":0}`
	if err := client.Recheck(context.Background()); err != nil {
		t.Fatalf("Recheck() error = %v", err)
	}
	if client.ServerVersion() == first {
		t.Error("ServerVersion() did not follow the upgrade")
	}
}

func TestDiffNamesTheChangedFields(t *testing.T) {
	t.Parallel()

	live := &ProxyHost{
		DomainNames: []string{"app.example.com"}, ForwardScheme: "http",
		ForwardHost: "app", ForwardPort: 80, CertificateID: CertificateRef(0),
	}
	desired := &ProxyHost{
		DomainNames: []string{"app.example.com"}, ForwardScheme: "http",
		ForwardHost: "app", ForwardPort: 8080, CertificateID: CertificateRef(12), SSLForced: true,
	}

	diff := Diff(live, desired)
	for _, want := range []string{"forward_port: 80 → 8080", "certificate_id: 0 → 12", "ssl_forced: false → true"} {
		if !strings.Contains(diff, want) {
			t.Errorf("Diff() = %q, want it to contain %q", diff, want)
		}
	}
	if strings.Contains(diff, "domain_names") {
		t.Errorf("Diff() should not report unchanged fields: %q", diff)
	}
	if got := Diff(live, live); got != "" {
		t.Errorf("Diff(x, x) = %q, want empty", got)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
