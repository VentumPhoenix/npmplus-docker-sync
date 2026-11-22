package npm

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeProxy(t *testing.T, raw string) *ProxyHost {
	t.Helper()
	host := &ProxyHost{}
	if err := json.Unmarshal([]byte(raw), host); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return host
}

func TestNginxStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		raw      string
		online   bool
		message  string
		reported bool
	}{
		{"not reported", `{"id":1,"domain_names":["a.example.com"]}`, true, "", false},
		{"column online", `{"id":1,"npmplus_nginx_online":true,"npmplus_nginx_err":""}`, true, "", true},
		{
			"column offline",
			`{"id":1,"npmplus_nginx_online":false,"npmplus_nginx_err":"nginx: [emerg] invalid URL prefix\nnginx: configuration file test failed"}`,
			false, "nginx: [emerg] invalid URL prefix", true,
		},
		{"column as integer", `{"id":1,"npmplus_nginx_online":0}`, false, "", true},
		{"meta offline", `{"id":1,"meta":{"nginx_online":false,"nginx_err":"broken"}}`, false, "broken", true},
		{"meta online", `{"id":1,"meta":{"nginx_online":true}}`, true, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			online, message, reported := NginxStatus(decodeProxy(t, tt.raw))
			if online != tt.online || message != tt.message || reported != tt.reported {
				t.Errorf("NginxStatus() = %t, %q, %t; want %t, %q, %t",
					online, message, reported, tt.online, tt.message, tt.reported)
			}
		})
	}

	long := strings.Repeat("x", 2*maxNginxError)
	if _, message, _ := NginxStatus(decodeProxy(t, `{"npmplus_nginx_online":false,"npmplus_nginx_err":"`+long+`"}`)); len(message) > maxNginxError+len("…") {
		t.Errorf("message of %d bytes is not shortened", len(message))
	}
	for _, r := range []Resource{&RedirectionHost{}, &Stream{}, &DeadHost{}} {
		if _, _, reported := NginxStatus(r); reported {
			t.Errorf("%T reports a status it was never given", r)
		}
	}
}

// TestNginxStatusIsNeverWritten: the status is the server's, so it takes part
// in neither the request, the fingerprint nor the diff.
func TestNginxStatusIsNeverWritten(t *testing.T) {
	t.Parallel()

	live := decodeProxy(t, `{"id":1,"domain_names":["a.example.com"],"forward_host":"a","forward_port":80,"forward_scheme":"http","npmplus_nginx_online":false,"npmplus_nginx_err":"broken"}`)
	desired := decodeProxy(t, `{"id":1,"domain_names":["a.example.com"],"forward_host":"a","forward_port":80,"forward_scheme":"http"}`)
	if live.Fingerprint() != desired.Fingerprint() {
		t.Error("the nginx status changes the fingerprint")
	}
	if diff := Diff(live, desired); diff != "" {
		t.Errorf("Diff() = %q, want the nginx status ignored", diff)
	}
	for _, flavour := range []Flavour{FlavourNPM, FlavourNPMplus} {
		payload, err := live.Payload(Dialect{Flavour: flavour})
		if err != nil {
			t.Fatalf("Payload(%s) error = %v", flavour, err)
		}
		raw, _ := json.Marshal(payload)
		if strings.Contains(string(raw), "nginx_online") || strings.Contains(string(raw), "nginx_err") {
			t.Errorf("%s payload carries the nginx status: %s", flavour, raw)
		}
	}
}
