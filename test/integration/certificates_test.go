//go:build integration

package integration

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// The automatic certificate selection is the feature with the most rules, and
// none of them can be proven against a mock: NPM decides what it stores.
func TestCertificateSelection(t *testing.T) {
	c := client(t)

	wildcard := uploadCertificate(t, "wildcard", "*.certs.test")
	t.Cleanup(func() { apiDelete(t, "/api/nginx/certificates/%d", wildcard) })

	startContainer(t, "it-cert", map[string]string{
		"npm.proxy.domains": "app.certs.test",
		"npm.proxy.port":    "80",
	})
	mustSync(t)

	host := requireHost(t, c, npm.KindProxy, "app.certs.test")
	proxy, ok := host.(*npm.ProxyHost)
	if !ok {
		t.Fatalf("unexpected resource %T", host)
	}
	if proxy.CertificateID.ID != wildcard {
		t.Fatalf("certificate = %s, want the wildcard (%d)", proxy.CertificateID, wildcard)
	}
	if !proxy.SSLForced {
		t.Error("ssl_forced should follow the attached certificate")
	}

	// An exact certificate must win over the wildcard on the next run.
	exact := uploadCertificate(t, "exact", "app.certs.test")
	t.Cleanup(func() { apiDelete(t, "/api/nginx/certificates/%d", exact) })

	mustSync(t)
	updated := requireHost(t, c, npm.KindProxy, "app.certs.test")
	if proxy, ok := updated.(*npm.ProxyHost); ok && proxy.CertificateID.ID != exact {
		t.Errorf("certificate = %s, want the exact one (%d)", proxy.CertificateID, exact)
	}

	// And an explicit "none" turns it off again.
	removeContainer("it-cert")
	startContainer(t, "it-cert", map[string]string{
		"npm.proxy.domains":     "app.certs.test",
		"npm.proxy.port":        "80",
		"npm.proxy.certificate": "none",
	})
	mustSync(t)
	plain := requireHost(t, c, npm.KindProxy, "app.certs.test")
	if proxy, ok := plain.(*npm.ProxyHost); ok && !proxy.CertificateID.IsZero() {
		t.Errorf("certificate = %s, want none", proxy.CertificateID)
	}
}

// A certificate created after the tool started has to reach its hosts without
// a restart: that is what the certificate poll is for.
func TestCertificatePollPicksUpANewCertificate(t *testing.T) {
	c := client(t)

	stop := startDaemon(t, "CERTIFICATE_POLL_INTERVAL=5s", "DEBOUNCE_INTERVAL=1s")
	defer stop()

	startContainer(t, "it-certpoll", map[string]string{
		"npm.proxy.domains": "poll.certs.test",
		"npm.proxy.port":    "80",
	})
	waitFor(t, "the host to appear", 60*time.Second, func() bool {
		return findByKey(resources(t, c, npm.KindProxy), "poll.certs.test") != nil
	})

	id := uploadCertificate(t, "poll", "*.certs.test")
	t.Cleanup(func() { apiDelete(t, "/api/nginx/certificates/%d", id) })

	waitFor(t, "the certificate to be attached", 90*time.Second, func() bool {
		host := findByKey(resources(t, c, npm.KindProxy), "poll.certs.test")
		proxy, ok := host.(*npm.ProxyHost)
		return ok && proxy.CertificateID.ID == id
	})
}

// Access lists referenced by name have to resolve - and a name that does not
// exist must never produce a public host.
func TestAccessListByName(t *testing.T) {
	c := client(t)

	id := createAccessList(t, "Integration")
	t.Cleanup(func() { apiDelete(t, "/api/nginx/access-lists/%d", id) })

	startContainer(t, "it-acl", map[string]string{
		"npm.proxy.domains":     "acl.test",
		"npm.proxy.port":        "80",
		"npm.proxy.access_list": "Integration",
	})
	mustSync(t)

	host := requireHost(t, c, npm.KindProxy, "acl.test")
	proxy, ok := host.(*npm.ProxyHost)
	if !ok {
		t.Fatalf("unexpected resource %T", host)
	}
	if !usesAccessList(proxy, id) {
		t.Errorf("access list = %v/%d, want %d", proxy.AccessListIDs, proxy.AccessListID, id)
	}

	removeContainer("it-acl")
	startContainer(t, "it-acl-typo", map[string]string{
		"npm.proxy.domains":     "acl-typo.test",
		"npm.proxy.port":        "80",
		"npm.proxy.access_list": "Integratino",
	})
	run := syncOnce(t)
	requireNoHost(t, c, npm.KindProxy, "acl-typo.test")
	if !strings.Contains(run.Output, "access list") {
		t.Errorf("the run should explain the unresolved access list:\n%s", run.Output)
	}
}

func usesAccessList(h *npm.ProxyHost, id int) bool {
	if h.AccessListID == id {
		return true
	}
	for _, candidate := range h.AccessListIDs {
		if candidate == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// API helpers that the client does not need in production
// ---------------------------------------------------------------------------

// uploadCertificate creates a self-signed certificate for the given domains
// and uploads it, returning its id.
func uploadCertificate(t *testing.T, name string, domains ...string) int {
	t.Helper()

	certPEM, keyPEM := selfSigned(t, domains...)
	token := apiToken(t)

	created := apiPost(t, token, "/api/nginx/certificates", map[string]any{
		"provider":     "other",
		"nice_name":    name,
		"domain_names": domains,
		"meta":         map[string]any{},
	})
	id, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("certificate response without an id: %v", created)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for field, content := range map[string][]byte{
		"certificate":     certPEM,
		"certificate_key": keyPEM,
	} {
		part, err := writer.CreateFormFile(field, field+".pem")
		if err != nil {
			t.Fatalf("multipart: %v", err)
		}
		if _, err := part.Write(content); err != nil {
			t.Fatalf("multipart write: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}

	path := fmt.Sprintf("/api/nginx/certificates/%d/upload", int(id))
	req, err := http.NewRequest(http.MethodPost, apiURL()+path, &body) //nolint:noctx // test helper
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload the certificate: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Fatalf("upload the certificate: %s %s", resp.Status, payload)
	}
	return int(id)
}

// createAccessList adds an access list and returns its id.
func createAccessList(t *testing.T, name string) int {
	t.Helper()

	created := apiPost(t, apiToken(t), "/api/nginx/access-lists", map[string]any{
		"name":        name,
		"satisfy_any": true,
		"pass_auth":   false,
		"items":       []any{map[string]any{"username": "user", "password": "secret"}},
		"clients":     []any{},
		"meta":        map[string]any{},
	})
	id, ok := created["id"].(float64)
	if !ok {
		t.Fatalf("access list response without an id: %v", created)
	}
	return int(id)
}

// apiToken logs in and returns a bearer token.
func apiToken(t *testing.T) string {
	t.Helper()

	body, err := json.Marshal(map[string]string{"identity": npmIdentity, "secret": npmSecret})
	if err != nil {
		t.Fatalf("encode credentials: %v", err)
	}
	resp, err := http.Post(apiURL()+"/api/tokens", "application/json", bytes.NewReader(body)) //nolint:noctx // test helper
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode the token: %v", err)
	}
	return decoded.Token
}

// apiDelete removes a resource the client has no typed method for.
func apiDelete(t *testing.T, format string, id int) {
	t.Helper()

	req, err := http.NewRequest(http.MethodDelete, apiURL()+fmt.Sprintf(format, id), nil) //nolint:noctx // test helper
	if err != nil {
		t.Logf("build the delete request: %v", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("delete %s: %v", req.URL.Path, err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// apiPost sends a JSON body and returns the decoded answer.
func apiPost(t *testing.T, token, path string, payload any) map[string]any {
	t.Helper()

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
	req, err := http.NewRequest(http.MethodPost, apiURL()+path, bytes.NewReader(body)) //nolint:noctx // test helper
	if err != nil {
		t.Fatalf("build %s: %v", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		t.Fatalf("POST %s: %s %s", path, resp.Status, raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v (%s)", path, err, raw)
	}
	return decoded
}

// selfSigned builds a certificate the tests can upload. Let's Encrypt is not
// part of the automated suite: its rate limits and DNS requirements belong in
// a manual run against the staging endpoint.
func selfSigned(t *testing.T, domains ...string) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate a key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: domains[0], Organization: []string{"integration"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              domains,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create the certificate: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return certPEM, keyPEM
}
