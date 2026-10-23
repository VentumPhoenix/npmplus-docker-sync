package npm

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func sampleMarker() marker {
	return marker{
		Key: "app.example.com", Container: "whoami", ContainerID: "3f2a9c",
		Index: 2, Instance: "homelab a;{b}", Prefix: "npm",
	}
}

func TestMarkerRoundTrip(t *testing.T) {
	t.Parallel()

	m := sampleMarker()
	line := m.encode()
	if strings.ContainsAny(line, "\n;{") {
		t.Errorf("marker line %q contains a newline, ';' or '{'", line)
	}
	parsed, ok := parseMarker(line)
	if !ok {
		t.Fatalf("parseMarker(%q) failed", line)
	}
	if parsed != m {
		t.Errorf("parseMarker() = %+v, want %+v", parsed, m)
	}
}

// TestMarkerDoesNotLookLikeADefaultLocation: upstream NPM scans the advanced
// configuration with this expression (internal/nginx.js) to decide whether it
// still emits its own `location /` block. The marker must never match it.
func TestMarkerDoesNotLookLikeADefaultLocation(t *testing.T) {
	t.Parallel()

	npmDefaultLocation := regexp.MustCompile(`(?im)^(?:.*;)?\s*?location\s*?/\s*?{`)
	m := sampleMarker()
	m.Key = "location / {"
	m.Container = "location /{"
	if config := withMarker("", m); npmDefaultLocation.MatchString(config) {
		t.Errorf("marker %q is read as a default location by upstream NPM", config)
	}
}

func TestSplitMarker(t *testing.T) {
	t.Parallel()

	m := sampleMarker()
	line := m.encode()
	other := m
	other.Index = 3

	tests := []struct {
		name      string
		config    string
		wantUser  string
		wantFound bool
		wantValid bool
	}{
		{"no marker", "client_max_body_size 0;", "client_max_body_size 0;", false, false},
		{"marker only", line, "", true, true},
		{"marker and config", line + "\nclient_max_body_size 0;\nproxy_read_timeout 300;", "client_max_body_size 0;\nproxy_read_timeout 300;", true, true},
		{"crlf line endings", line + "\r\nclient_max_body_size 0;\r\n", "client_max_body_size 0;\r\n", true, true},
		{"marker moved below the config", "client_max_body_size 0;\n" + line, "client_max_body_size 0;", true, true},
		{"indented marker", "  " + line + "\nreturn 404;", "return 404;", true, true},
		{"the same marker twice", line + "\n" + line, "", true, true},
		{"two different markers", line + "\n" + other.encode(), "", true, false},
		{"unknown marker version", strings.Replace(line, "v=1", "v=9", 1), "", true, false},
		{"mangled marker", markerPrefix + " hand edited", "", true, false},
		{"user comment is kept", "# my own comment\n" + line, "# my own comment", true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user, got, found, valid := splitMarker(tc.config)
			if user != tc.wantUser || found != tc.wantFound || valid != tc.wantValid {
				t.Errorf("splitMarker() = %q, found %t, valid %t; want %q, %t, %t",
					user, found, valid, tc.wantUser, tc.wantFound, tc.wantValid)
			}
			if tc.wantValid && got != m {
				t.Errorf("splitMarker() marker = %+v, want %+v", got, m)
			}
		})
	}
}

// TestWithMarkerKeepsTheUserConfigVerbatim: what the labels configured must
// come back byte for byte, or every run would see a difference.
func TestWithMarkerKeepsTheUserConfigVerbatim(t *testing.T) {
	t.Parallel()

	for _, user := range []string{"", "return 404;", "a;\n\nb;\n", "  indented;\r\n"} {
		got, _, _, valid := splitMarker(withMarker(user, sampleMarker()))
		if !valid || got != user {
			t.Errorf("round trip of %q = %q (valid %t)", user, got, valid)
		}
	}
}

func TestLiftOwnership(t *testing.T) {
	t.Parallel()

	m := sampleMarker()
	ours := func() Meta {
		return Meta{MetaManagedBy: ManagedByValue, MetaContainer: "old", MetaIndex: 0, "nginx_online": true}
	}

	t.Run("marker without meta", func(t *testing.T) {
		t.Parallel()
		user, meta, marked := liftOwnership(withMarker("return 404;", m), nil, m.Key)
		if !marked || user != "return 404;" {
			t.Fatalf("liftOwnership() = %q, marked %t", user, marked)
		}
		if !IsManaged(meta) || meta.Container() != "whoami" || meta.ContainerID() != "3f2a9c" ||
			meta.Instance() != m.Instance || meta.Prefix() != "npm" {
			t.Errorf("meta = %v, want the marker's ownership", meta)
		}
		if _, _, index, _ := ownership(meta); index != 2 {
			t.Errorf("index = %d, want 2", index)
		}
	})

	t.Run("marker wins over meta", func(t *testing.T) {
		t.Parallel()
		_, meta, marked := liftOwnership(withMarker("", m), ours(), m.Key)
		if !marked || meta.Container() != "whoami" || meta["nginx_online"] != true {
			t.Errorf("meta = %v, marked %t; want the marker's container and the other keys kept", meta, marked)
		}
	})

	t.Run("meta without marker", func(t *testing.T) {
		t.Parallel()
		user, meta, marked := liftOwnership("return 404;", ours(), m.Key)
		if marked || user != "return 404;" || !IsManaged(meta) {
			t.Errorf("liftOwnership() = %q, %v, marked %t; want meta untouched", user, meta, marked)
		}
	})

	// A marker that was copied to another host, or whose host had its first
	// domain changed by hand, must not make that host ours - and neither may
	// the meta object that came along with it.
	t.Run("marker for another key", func(t *testing.T) {
		t.Parallel()
		user, meta, marked := liftOwnership(withMarker("return 404;", m), ours(), "copy.example.com")
		if marked || IsManaged(meta) {
			t.Errorf("meta = %v, marked %t; want the resource to be nobody's", meta, marked)
		}
		if user != "return 404;" || meta["nginx_online"] != true {
			t.Errorf("liftOwnership() = %q, %v; want the config and the server keys kept", user, meta)
		}
	})

	t.Run("broken marker", func(t *testing.T) {
		t.Parallel()
		_, meta, marked := liftOwnership(markerPrefix+" edited", ours(), m.Key)
		if marked || IsManaged(meta) {
			t.Errorf("meta = %v, marked %t; want the resource to be nobody's", meta, marked)
		}
	})

	t.Run("foreign meta is never touched", func(t *testing.T) {
		t.Parallel()
		redth := Meta{MetaManagedBy: RedthManagedByValue}
		_, meta, _ := liftOwnership(markerPrefix+" edited", redth, m.Key)
		if !IsRedth(meta) {
			t.Errorf("meta = %v, want Redth's marker kept", meta)
		}
	})
}

// TestDecodingLiftsTheOwnership covers the decoding path every API response
// takes: what NPMplus returns after it emptied the meta object still reads as
// ours, with the group from its new column.
func TestDecodingLiftsTheOwnership(t *testing.T) {
	t.Parallel()

	m := marker{Key: "app.example.com", Container: "web", ContainerID: "web-id", Index: 0, Prefix: "npm"}
	body, err := json.Marshal(map[string]any{
		"id":                1,
		"domain_names":      []string{"app.example.com"},
		"advanced_config":   withMarker("client_max_body_size 0;", m),
		"npmplus_directory": "Production",
	})
	if err != nil {
		t.Fatal(err)
	}

	var host ProxyHost
	if err := json.Unmarshal(body, &host); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !host.OwnerMarked() || !IsManaged(host.Meta) || host.Meta.Container() != "web" {
		t.Errorf("host = %+v, want it to be ours", host)
	}
	if host.AdvancedConfig != "client_max_body_size 0;" {
		t.Errorf("AdvancedConfig = %q, want the marker removed", host.AdvancedConfig)
	}
	if host.Meta.Directory() != "Production" || host.Directory != "" {
		t.Errorf("group = %q (column %q), want it moved into the meta object", host.Meta.Directory(), host.Directory)
	}

	// The same for the three other kinds.
	streamBody := `{"id":2,"incoming_port":"5432","npmplus_advanced_config":` +
		jsonString(withMarker("", marker{Key: "5432", Container: "db"})) + `}`
	var stream Stream
	if err := json.Unmarshal([]byte(streamBody), &stream); err != nil || !stream.OwnerMarked() || stream.Meta.Container() != "db" {
		t.Errorf("stream = %+v, %v; want it to be ours", stream, err)
	}
	redirectBody := `{"id":3,"domain_names":["old.example.com"],"advanced_config":` +
		jsonString(withMarker("", marker{Key: "old.example.com", Container: "web"})) + `}`
	var redirect RedirectionHost
	if err := json.Unmarshal([]byte(redirectBody), &redirect); err != nil || !redirect.OwnerMarked() {
		t.Errorf("redirect = %+v, %v; want it to be ours", redirect, err)
	}
	deadBody := `{"id":4,"domain_names":["parked.example.com"],"advanced_config":` +
		jsonString(withMarker("", marker{Key: "parked.example.com", Container: "web"})) + `}`
	var dead DeadHost
	if err := json.Unmarshal([]byte(deadBody), &dead); err != nil || !dead.OwnerMarked() {
		t.Errorf("404 host = %+v, %v; want it to be ours", dead, err)
	}
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
