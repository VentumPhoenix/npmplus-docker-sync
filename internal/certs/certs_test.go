package certs

import (
	"reflect"
	"testing"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

var now = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

// cert is a small fixture helper.
func cert(id int, expires string, domains ...string) npm.Certificate {
	return npm.Certificate{
		ID: id, Provider: "letsencrypt", NiceName: domains[0],
		DomainNames: domains, ExpiresOn: expires,
	}
}

const (
	future  = "2026-12-01 00:00:00"
	further = "2027-06-01 00:00:00"
	past    = "2026-01-01 00:00:00"
)

func TestParseSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw     string
		want    Spec
		wantErr bool
	}{
		{raw: "", want: Spec{Mode: ModeAuto}},
		{raw: "auto", want: Spec{Mode: ModeAuto, Raw: "auto"}},
		{raw: "AUTO", want: Spec{Mode: ModeAuto, Raw: "AUTO"}},
		{raw: "123", want: Spec{Mode: ModeID, ID: 123, Raw: "123"}},
		{raw: "0", want: Spec{Mode: ModeNone, Raw: "0"}},
		{raw: "none", want: Spec{Mode: ModeNone, Raw: "none"}},
		{raw: "off", want: Spec{Mode: ModeNone, Raw: "off"}},
		{raw: "new", want: Spec{Mode: ModeNew, Raw: "new"}},
		{raw: "letsencrypt", want: Spec{Mode: ModeNew, Raw: "letsencrypt"}},
		{raw: "*.home.example.com", want: Spec{Mode: ModeDomain, Domain: "*.home.example.com", Raw: "*.home.example.com"}},
		{raw: "App.Example.com", want: Spec{Mode: ModeDomain, Domain: "app.example.com", Raw: "App.Example.com"}},
		{raw: "name:My Wildcard", want: Spec{Mode: ModeName, Name: "My Wildcard", Raw: "name:My Wildcard"}},
		{raw: "NAME:My Wildcard", want: Spec{Mode: ModeName, Name: "My Wildcard", Raw: "NAME:My Wildcard"}},
		{raw: "-1", wantErr: true},
		{raw: "name:", wantErr: true},
		{raw: "nonsense", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			got, err := ParseSpec(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSpec(%q) = %+v, want an error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSpec(%q) error = %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("ParseSpec(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

// TestWildcardMatchesOneLabel is RFC 6125: a wildcard covers exactly one
// label, neither the apex nor a deeper name.
func TestWildcardMatchesOneLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, domain string
		want            bool
	}{
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", false},
		{"*.example.com", "a.b.example.com", false},
		{"*.example.com", "a.example.org", false},
		{"*.b.example.com", "a.b.example.com", true},
		{"example.com", "example.com", false}, // not a wildcard
	}
	for _, tc := range tests {
		if got := wildcardMatch(tc.pattern, tc.domain); got != tc.want {
			t.Errorf("wildcardMatch(%q, %q) = %t, want %t", tc.pattern, tc.domain, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cert    npm.Certificate
		domains []string
		want    Class
	}{
		{
			name: "exact and nothing else", cert: cert(1, future, "app.example.com"),
			domains: []string{"app.example.com"}, want: ClassExactOnly,
		},
		{
			name: "exact with extra sans", cert: cert(1, future, "app.example.com", "www.example.com"),
			domains: []string{"app.example.com"}, want: ClassExactExtra,
		},
		{
			name: "wildcard only", cert: cert(1, future, "*.example.com"),
			domains: []string{"app.example.com"}, want: ClassWildcard,
		},
		{
			name: "mixed", cert: cert(1, future, "example.com", "*.example.com"),
			domains: []string{"example.com", "app.example.com"}, want: ClassMixed,
		},
		{
			name: "apex is not covered by a wildcard", cert: cert(1, future, "*.example.com"),
			domains: []string{"example.com"}, want: ClassNone,
		},
		{
			name: "two levels are not covered", cert: cert(1, future, "*.example.com"),
			domains: []string{"a.b.example.com"}, want: ClassNone,
		},
		{
			name: "case insensitive", cert: cert(1, future, "App.Example.COM"),
			domains: []string{"app.example.com"}, want: ClassExactOnly,
		},
		{
			name: "idn is compared as punycode", cert: cert(1, future, "xn--mnchen-3ya.example.com"),
			domains: []string{"münchen.example.com"}, want: ClassExactOnly,
		},
		{
			name: "one uncovered domain disqualifies", cert: cert(1, future, "*.example.com"),
			domains: []string{"app.example.com", "other.org"}, want: ClassNone,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := classify(tc.cert, tc.domains); got != tc.want {
				t.Errorf("classify() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestResolveAuto(t *testing.T) {
	t.Parallel()

	wildcard := cert(1, future, "*.home.example.com")
	exact := cert(2, future, "app.home.example.com")
	expired := cert(3, past, "app.home.example.com")
	mtls := npm.Certificate{ID: 4, Provider: "mtls", DomainNames: []string{"app.home.example.com"}, ExpiresOn: further}
	deleted := npm.Certificate{ID: 5, Provider: "letsencrypt", DomainNames: []string{"app.home.example.com"}, ExpiresOn: further, IsDeleted: npm.Flag(true)}

	tests := []struct {
		name      string
		list      []npm.Certificate
		domains   []string
		current   int
		partial   Partial
		wantID    int
		wantClass Class
		wantUncov []string
	}{
		{
			name: "wildcard when nothing else fits", list: []npm.Certificate{wildcard},
			domains: []string{"app.home.example.com"}, wantID: 1, wantClass: ClassWildcard,
		},
		{
			name: "exact beats wildcard", list: []npm.Certificate{wildcard, exact},
			domains: []string{"app.home.example.com"}, wantID: 2, wantClass: ClassExactOnly,
		},
		{
			name: "expired is ignored", list: []npm.Certificate{wildcard, expired},
			domains: []string{"app.home.example.com"}, wantID: 1, wantClass: ClassWildcard,
		},
		{
			name: "client CAs are ignored", list: []npm.Certificate{wildcard, mtls},
			domains: []string{"app.home.example.com"}, wantID: 1, wantClass: ClassWildcard,
		},
		{
			name: "deleted certificates are ignored", list: []npm.Certificate{wildcard, deleted},
			domains: []string{"app.home.example.com"}, wantID: 1, wantClass: ClassWildcard,
		},
		{
			name:    "longest validity wins a tie",
			list:    []npm.Certificate{cert(7, future, "*.home.example.com"), cert(8, further, "*.home.example.com")},
			domains: []string{"app.home.example.com"}, wantID: 8, wantClass: ClassWildcard,
		},
		{
			name:    "lowest id breaks a full tie",
			list:    []npm.Certificate{cert(9, future, "*.home.example.com"), cert(6, future, "*.home.example.com")},
			domains: []string{"app.home.example.com"}, wantID: 6, wantClass: ClassWildcard,
		},
		{
			name: "nothing matches", list: []npm.Certificate{wildcard},
			domains: []string{"other.org"}, wantID: 0, wantUncov: []string{"other.org"},
		},
		{
			name: "partial coverage keeps the primary domain", list: []npm.Certificate{wildcard},
			domains: []string{"app.home.example.com", "other.org"},
			wantID:  1, wantClass: ClassWildcard, wantUncov: []string{"other.org"},
		},
		{
			name: "partial=none attaches nothing", list: []npm.Certificate{wildcard},
			domains: []string{"app.home.example.com", "other.org"}, partial: PartialNone,
			wantID: 0, wantUncov: []string{"app.home.example.com", "other.org"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			partial := tc.partial
			if partial == "" {
				partial = PartialPrimary
			}
			got, err := Resolve(AutoSpec, tc.domains, tc.list, Options{Current: tc.current, Partial: partial, Now: now})
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.ID != tc.wantID {
				t.Errorf("id = %d, want %d", got.ID, tc.wantID)
			}
			if tc.wantID != 0 && got.Class != tc.wantClass {
				t.Errorf("class = %s, want %s", got.Class, tc.wantClass)
			}
			if len(tc.wantUncov) > 0 && !reflect.DeepEqual(got.Uncovered, tc.wantUncov) {
				t.Errorf("uncovered = %v, want %v", got.Uncovered, tc.wantUncov)
			}
		})
	}
}

// TestResolveIsStable covers section 2.3: an equally good certificate must not
// move a host around, a better one must.
func TestResolveIsStable(t *testing.T) {
	t.Parallel()

	attached := cert(1, future, "*.home.example.com")
	sameClass := cert(2, further, "*.home.example.com")
	better := cert(3, future, "app.home.example.com")
	domains := []string{"app.home.example.com"}

	got, err := Resolve(AutoSpec, domains, []npm.Certificate{attached, sameClass},
		Options{Current: 1, Partial: PartialPrimary, Now: now})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != 1 || !got.Kept {
		t.Errorf("a new certificate of the same class must not steal the host, got %+v", got)
	}

	got, err = Resolve(AutoSpec, domains, []npm.Certificate{attached, better},
		Options{Current: 1, Partial: PartialPrimary, Now: now})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != 3 || got.Class != ClassExactOnly {
		t.Errorf("an exact certificate must replace the wildcard, got %+v", got)
	}

	// An attached certificate that expired is replaced.
	got, err = Resolve(AutoSpec, domains, []npm.Certificate{cert(1, past, "*.home.example.com"), attachedCopy()},
		Options{Current: 1, Partial: PartialPrimary, Now: now})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.ID != 9 {
		t.Errorf("an expired certificate must be replaced, got %+v", got)
	}
}

func attachedCopy() npm.Certificate { return cert(9, future, "*.home.example.com") }

func TestResolveExplicitModes(t *testing.T) {
	t.Parallel()

	list := []npm.Certificate{
		cert(1, future, "*.home.example.com"),
		{ID: 2, Provider: "letsencrypt", NiceName: "My Wildcard", DomainNames: []string{"*.other.example.com"}, ExpiresOn: future},
	}

	cases := []struct {
		raw     string
		wantID  int
		wantNew bool
		wantErr bool
	}{
		{raw: "1", wantID: 1},
		{raw: "none", wantID: 0},
		{raw: "new", wantNew: true},
		{raw: "name:My Wildcard", wantID: 2},
		{raw: "name:nope", wantErr: true},
		{raw: "app.home.example.com", wantID: 1},
		{raw: "nothing.example.org", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			spec, err := ParseSpec(tc.raw)
			if err != nil {
				t.Fatalf("ParseSpec: %v", err)
			}
			got, err := Resolve(spec, []string{"app.home.example.com"}, list, Options{Partial: PartialPrimary, Now: now})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.ID != tc.wantID || got.New != tc.wantNew {
				t.Errorf("Resolve() = %+v, want id %d new %t", got, tc.wantID, tc.wantNew)
			}
		})
	}
}

func TestHashDetectsChanges(t *testing.T) {
	t.Parallel()

	base := []npm.Certificate{cert(1, future, "*.example.com")}
	if Hash(base) != Hash([]npm.Certificate{cert(1, future, "*.EXAMPLE.com")}) {
		t.Error("the hash must not depend on the spelling of a domain")
	}
	if Hash(base) == Hash(append(base, cert(2, future, "app.example.com"))) {
		t.Error("a new certificate must change the hash")
	}
	if Hash(base) == Hash([]npm.Certificate{cert(1, further, "*.example.com")}) {
		t.Error("a renewal must change the hash")
	}
}
