package npm

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestCertificateIDUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		json    string
		want    CertificateID
		wantErr bool
	}{
		{name: "number", json: `5`, want: CertificateID{ID: 5}},
		{name: "zero", json: `0`, want: CertificateID{}},
		{name: "null", json: `null`, want: CertificateID{}},
		{name: "new keyword", json: `"new"`, want: CertificateID{New: true}},
		{name: "numeric string", json: `"12"`, want: CertificateID{ID: 12}},
		{name: "empty string", json: `""`, want: CertificateID{}},
		{name: "garbage", json: `"nope"`, wantErr: true},
		{name: "object", json: `{}`, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got CertificateID
			err := json.Unmarshal([]byte(tc.json), &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal(%s) error = nil, want error", tc.json)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.json, err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCertificateIDMarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   CertificateID
		want string
	}{
		{name: "existing", in: CertificateRef(3), want: `3`},
		{name: "new", in: NewCertificate(), want: `"new"`},
		{name: "none", in: CertificateID{}, want: `0`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if string(raw) != tc.want {
				t.Errorf("Marshal() = %s, want %s", raw, tc.want)
			}
		})
	}
}

func TestFlagUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		json string
		want Flag
	}{
		{json: `true`, want: true},
		{json: `1`, want: true},
		{json: `"1"`, want: true},
		{json: `false`, want: false},
		{json: `0`, want: false},
		{json: `null`, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.json, func(t *testing.T) {
			t.Parallel()
			var got Flag
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.json, err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// NPMplus changed npmplus_proxy_protocol_forwarding from an integer enum to a
// boolean, so both shapes have to decode - a reader that only knew integers
// failed every stream listing against 2.15.1.
func TestProxyProtocolLevelUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		json string
		want ProxyProtocolLevel
	}{
		{json: `0`, want: 0},
		{json: `1`, want: 1},
		{json: `2`, want: 2},
		{json: `false`, want: 0},
		{json: `true`, want: 1},
		{json: `"2"`, want: 2},
		{json: `"true"`, want: 1},
		{json: `null`, want: 0},
	}
	for _, tc := range tests {
		t.Run(tc.json, func(t *testing.T) {
			t.Parallel()
			var got ProxyProtocolLevel
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.json, err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Writing stays numeric: the older NPMplus schema demands an integer, and the
// newer one coerces 0 and 1 back to false and true.
func TestProxyProtocolLevelMarshal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		level ProxyProtocolLevel
		want  string
	}{{0, "0"}, {1, "1"}, {2, "2"}} {
		got, err := json.Marshal(tc.level)
		if err != nil {
			t.Fatalf("Marshal(%v) error = %v", tc.level, err)
		}
		if string(got) != tc.want {
			t.Errorf("Marshal(%v) = %s, want %s", tc.level, got, tc.want)
		}
	}
}

func TestFlexTimeUnmarshal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want time.Time
	}{
		{name: "rfc3339", json: `"2030-01-02T03:04:05Z"`, want: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)},
		{name: "npm millis format", json: `"2030-01-02T03:04:05.000Z"`, want: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)},
		{name: "unix seconds", json: `1893553445`, want: time.Unix(1893553445, 0).UTC()},
		{name: "unix millis", json: `1893553445000`, want: time.UnixMilli(1893553445000).UTC()},
		{name: "null", json: `null`, want: time.Time{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got flexTime
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", tc.json, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got.Time, tc.want)
			}
		})
	}
}

func TestNormalizeDomains(t *testing.T) {
	t.Parallel()

	got := NormalizeDomains([]string{"B.example.com ", "a.example.com", "a.example.com", "", "c.example.com."})
	want := []string{"a.example.com", "b.example.com", "c.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NormalizeDomains() = %v, want %v", got, want)
	}
}

func TestMetaManagedBy(t *testing.T) {
	t.Parallel()

	if (Meta{MetaManagedBy: ManagedByValue}).ManagedBy(ManagedByValue) != true {
		t.Error("marked host should report as managed")
	}
	if (Meta{}).ManagedBy(ManagedByValue) {
		t.Error("unmarked host must not report as managed")
	}
	if Meta(nil).ManagedBy(ManagedByValue) {
		t.Error("nil meta must not report as managed")
	}
	if (Meta{MetaManagedBy: 5}).ManagedBy(ManagedByValue) {
		t.Error("non-string marker must not report as managed")
	}
}
