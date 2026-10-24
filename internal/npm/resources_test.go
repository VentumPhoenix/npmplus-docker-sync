package npm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestKindPaths(t *testing.T) {
	t.Parallel()

	want := map[Kind]string{
		KindProxy:    "/api/nginx/proxy-hosts",
		KindRedirect: "/api/nginx/redirection-hosts",
		KindStream:   "/api/nginx/streams",
		KindDead:     "/api/nginx/dead-hosts",
	}
	for kind, path := range want {
		if got := kind.Path(); got != path {
			t.Errorf("%s.Path() = %q, want %q", kind, got, path)
		}
		if !kind.Valid() {
			t.Errorf("%s.Valid() = false", kind)
		}
	}
	if Kind("nonsense").Valid() {
		t.Error("unknown kind reported as valid")
	}
	if len(Kinds) != len(want) {
		t.Errorf("Kinds = %v, want all four resource types", Kinds)
	}
}

func TestResourceKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource Resource
		want     string
	}{
		{
			name:     "proxy host uses the first domain",
			resource: &ProxyHost{DomainNames: []string{"z.example.com", "a.example.com"}},
			want:     "a.example.com",
		},
		{
			name:     "redirection host uses the first domain",
			resource: &RedirectionHost{DomainNames: []string{"old.example.com"}},
			want:     "old.example.com",
		},
		{
			name:     "stream uses the incoming port",
			resource: &Stream{IncomingPort: PortOf(5432)},
			want:     "5432",
		},
		{
			name:     "dead host uses the first domain",
			resource: &DeadHost{DomainNames: []string{"parked.example.com"}},
			want:     "parked.example.com",
		},
		{
			name:     "resource without domains has no key",
			resource: &ProxyHost{},
			want:     "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.resource.ResourceKey(); got != tc.want {
				t.Errorf("ResourceKey() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResourceIDRoundTrip(t *testing.T) {
	t.Parallel()

	for _, resource := range []Resource{&ProxyHost{}, &RedirectionHost{}, &Stream{}, &DeadHost{}} {
		resource.SetResourceID(42)
		if got := resource.ResourceID(); got != 42 {
			t.Errorf("%T: ResourceID() = %d, want 42", resource, got)
		}
	}
}

func TestFingerprintIgnoresServerFields(t *testing.T) {
	t.Parallel()

	base := &ProxyHost{
		DomainNames: []string{"app.example.com"}, ForwardHost: "app", ForwardPort: 80,
		ForwardScheme: "http", Meta: Meta{MetaManagedBy: ManagedByValue},
	}
	withServerFields := *base
	withServerFields.ID = 17
	withServerFields.CreatedOn = "2026-01-01T00:00:00.000Z"
	withServerFields.ModifiedOn = "2026-02-02T00:00:00.000Z"
	withServerFields.OwnerUserID = 1

	if base.Fingerprint() != withServerFields.Fingerprint() {
		t.Error("ids and timestamps must not influence the fingerprint")
	}
}

func TestFingerprintDetectsChanges(t *testing.T) {
	t.Parallel()

	proxy := func() *ProxyHost {
		return &ProxyHost{
			DomainNames: []string{"app.example.com"}, ForwardHost: "app", ForwardPort: 80,
			ForwardScheme: "http", AllowWebsocketUpgrade: true, BlockExploits: true,
			Meta: Meta{MetaManagedBy: ManagedByValue, MetaContainer: "app", MetaIndex: 0},
		}
	}
	redirect := func() *RedirectionHost {
		return &RedirectionHost{
			DomainNames: []string{"old.example.com"}, ForwardDomainName: "new.example.com",
			ForwardScheme: "auto", ForwardHTTPCode: 301, PreservePath: true,
			Meta: Meta{MetaManagedBy: ManagedByValue},
		}
	}
	stream := func() *Stream {
		return &Stream{
			IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432), TCPForwarding: true,
			Meta: Meta{MetaManagedBy: ManagedByValue},
		}
	}
	dead := func() *DeadHost {
		return &DeadHost{DomainNames: []string{"parked.example.com"}, Meta: Meta{MetaManagedBy: ManagedByValue}}
	}

	tests := []struct {
		name   string
		base   Resource
		mutate func() Resource
	}{
		{"proxy port", proxy(), func() Resource { r := proxy(); r.ForwardPort = 81; return r }},
		{"proxy host", proxy(), func() Resource { r := proxy(); r.ForwardHost = "172.20.0.5"; return r }},
		{"proxy scheme", proxy(), func() Resource { r := proxy(); r.ForwardScheme = "https"; return r }},
		{"proxy domains", proxy(), func() Resource { r := proxy(); r.DomainNames = []string{"other.example.com"}; return r }},
		{"proxy certificate", proxy(), func() Resource { r := proxy(); r.CertificateID = CertificateRef(2); return r }},
		{"proxy websockets", proxy(), func() Resource { r := proxy(); r.AllowWebsocketUpgrade = false; return r }},
		{"proxy access list", proxy(), func() Resource { r := proxy(); r.AccessListIDs = []int{3}; return r }},
		{"proxy advanced config", proxy(), func() Resource { r := proxy(); r.AdvancedConfig = "add_header X-Test 1;"; return r }},
		{"proxy container marker", proxy(), func() Resource { r := proxy(); r.Meta[MetaContainer] = "renamed"; return r }},
		{"proxy label index", proxy(), func() Resource { r := proxy(); r.Meta[MetaIndex] = 2; return r }},
		{"redirect target", redirect(), func() Resource { r := redirect(); r.ForwardDomainName = "other.example.com"; return r }},
		{"redirect code", redirect(), func() Resource { r := redirect(); r.ForwardHTTPCode = 302; return r }},
		{"redirect preserve path", redirect(), func() Resource { r := redirect(); r.PreservePath = false; return r }},
		{"stream incoming port", stream(), func() Resource { r := stream(); r.IncomingPort = PortOf(5433); return r }},
		{"stream upstream", stream(), func() Resource { r := stream(); r.ForwardingHost = "10.0.0.2"; return r }},
		{"stream udp", stream(), func() Resource { r := stream(); r.UDPForwarding = true; return r }},
		{"dead domains", dead(), func() Resource { r := dead(); r.DomainNames = []string{"other.example.com"}; return r }},
		{"dead http2", dead(), func() Resource { r := dead(); r.HTTP2Support = true; return r }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.base.Fingerprint() == tc.mutate().Fingerprint() {
				t.Errorf("changing %s must change the fingerprint", tc.name)
			}
		})
	}
}

func TestFingerprintSeparatesKinds(t *testing.T) {
	t.Parallel()

	proxy := &ProxyHost{DomainNames: []string{"app.example.com"}}
	dead := &DeadHost{DomainNames: []string{"app.example.com"}}
	if proxy.Fingerprint() == dead.Fingerprint() {
		t.Error("resources of different kinds must not share a fingerprint")
	}
}

func TestAdoptServerState(t *testing.T) {
	t.Parallel()

	t.Run("adopts an issued certificate", func(t *testing.T) {
		desired := &ProxyHost{CertificateID: NewCertificate()}
		desired.AdoptServerState(&ProxyHost{CertificateID: CertificateRef(5)})
		if desired.CertificateID != CertificateRef(5) {
			t.Errorf("CertificateID = %v, want the issued certificate", desired.CertificateID)
		}
	})

	t.Run("keeps an explicit certificate", func(t *testing.T) {
		desired := &ProxyHost{CertificateID: CertificateRef(2)}
		desired.AdoptServerState(&ProxyHost{CertificateID: CertificateRef(5)})
		if desired.CertificateID != CertificateRef(2) {
			t.Errorf("CertificateID = %v, want the label value to win", desired.CertificateID)
		}
	})

	t.Run("still requests a certificate while none exists", func(t *testing.T) {
		desired := &Stream{CertificateID: NewCertificate()}
		desired.AdoptServerState(&Stream{})
		if !desired.CertificateID.New {
			t.Error("a pending request must survive until NPM issues the certificate")
		}
	})

	t.Run("ignores a mismatched kind", func(t *testing.T) {
		desired := &ProxyHost{CertificateID: NewCertificate()}
		desired.AdoptServerState(&DeadHost{CertificateID: CertificateRef(5)})
		if !desired.CertificateID.New {
			t.Error("state must only be adopted from the same kind")
		}
	})
}

func TestResourceJSONShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resource Resource
		contains []string
	}{
		{
			name: "proxy host",
			resource: &ProxyHost{
				DomainNames: []string{"app.example.com"}, ForwardHost: "app",
				ForwardPort: 80, ForwardScheme: "http", AllowWebsocketUpgrade: true,
			},
			contains: []string{`"domain_names":["app.example.com"]`, `"forward_port":80`, `"allow_websocket_upgrade":true`, `"certificate_id":0`},
		},
		{
			name: "redirection host",
			resource: &RedirectionHost{
				DomainNames: []string{"old.example.com"}, ForwardDomainName: "new.example.com",
				ForwardHTTPCode: 301, ForwardScheme: "auto", PreservePath: true,
			},
			contains: []string{`"forward_domain_name":"new.example.com"`, `"forward_http_code":301`, `"preserve_path":true`},
		},
		{
			name:     "stream",
			resource: &Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432), TCPForwarding: true},
			contains: []string{`"incoming_port":5432`, `"forwarding_host":"db"`, `"tcp_forwarding":true`, `"udp_forwarding":false`},
		},
		{
			name:     "dead host",
			resource: &DeadHost{DomainNames: []string{"parked.example.com"}, HTTP2Support: true},
			contains: []string{`"domain_names":["parked.example.com"]`, `"http2_support":true`},
		},
		{
			name:     "new certificate is sent as a string",
			resource: &ProxyHost{DomainNames: []string{"a.example.com"}, CertificateID: NewCertificate()},
			contains: []string{`"certificate_id":"new"`},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tc.resource)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			body := string(raw)
			for _, want := range tc.contains {
				if !strings.Contains(body, want) {
					t.Errorf("payload %s\ndoes not contain %s", body, want)
				}
			}
			if strings.Contains(body, `"id":0`) {
				t.Errorf("payload must omit an empty id: %s", body)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		resource Resource
		want     string
	}{
		{&ProxyHost{DomainNames: []string{"app.example.com"}, ForwardScheme: "http", ForwardHost: "app", ForwardPort: 80}, "app.example.com → http://app:80"},
		{&RedirectionHost{DomainNames: []string{"old.example.com"}, ForwardScheme: "auto", ForwardDomainName: "new.example.com", ForwardHTTPCode: 301}, "old.example.com ⇒ auto://new.example.com (301)"},
		{&Stream{IncomingPort: PortOf(5432), ForwardingHost: "db", ForwardingPort: PortOf(5432), TCPForwarding: true, UDPForwarding: true}, ":5432 → db:5432 (tcp+udp)"},
		{&DeadHost{DomainNames: []string{"parked.example.com"}}, "parked.example.com → 404"},
	}
	for _, tc := range tests {
		if got := tc.resource.Describe(); got != tc.want {
			t.Errorf("Describe() = %q, want %q", got, tc.want)
		}
	}
}

// TestAccessListsFromTheRelation: NPMplus moved access lists into a table of
// their own and returns them as an expanded `access_lists` relation instead of
// npmplus_access_list_ids - for the host and for each custom location. Read
// as "no access list", every protected host would be rewritten as public.
func TestAccessListsFromTheRelation(t *testing.T) {
	t.Parallel()

	body := `{"id":1,"domain_names":["a.example.com"],"npmplus_access_list_type":"custom",
		"access_lists":[{"id":5,"name":"VPN"},{"id":3,"name":"Intern"}],
		"locations":[{"path":"/api","npmplus_access_list_type":"custom","access_lists":[{"id":7}]}]}`
	var host ProxyHost
	if err := json.Unmarshal([]byte(body), &host); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if ids, listType := host.accessLists(); !reflect.DeepEqual(ids, []int{3, 5}) || listType != AccessListCustom {
		t.Errorf("accessLists() = %v, %s; want [3 5], custom", ids, listType)
	}
	if got := host.Locations[0].AccessListIDs; !reflect.DeepEqual(got, []int{7}) {
		t.Errorf("location access lists = %v, want [7]", got)
	}
	if host.AccessLists != nil || host.Locations[0].AccessLists != nil {
		t.Error("the expanded relation is kept on the model")
	}
}

// TestLocationsConvergeWithTheirOwnTable: NPMplus keeps locations in a table
// of their own, whose columns fill in what a label left unset. The stored row
// must hash like the desired location, or the host is rewritten on every run.
func TestLocationsConvergeWithTheirOwnTable(t *testing.T) {
	t.Parallel()

	desired := func() *ProxyHost {
		return &ProxyHost{
			DomainNames: []string{"a.example.com"}, ForwardScheme: "http", ForwardHost: "10.0.0.2", ForwardPort: 80,
			Locations: []Location{{Path: "/api", ForwardScheme: "http", ForwardHost: "10.0.0.2", ForwardPort: 80}},
		}
	}
	body := `{"id":1,"domain_names":["a.example.com"],"forward_scheme":"http","forward_host":"10.0.0.2","forward_port":80,
		"locations":[{"id":4,"proxy_host_id":1,"path":"/api","location_type":"","forward_scheme":"http",
		"forward_host":"10.0.0.2","forward_port":80,"npmplus_enabled":true,"npmplus_x_frame_options":"SAMEORIGIN",
		"npmplus_auth_request":"none","npmplus_auth_request_upstream":"","advanced_config":"",
		"npmplus_access_list_type":"global","access_lists":[]}]}`
	var stored ProxyHost
	if err := json.Unmarshal([]byte(body), &stored); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	host := desired()
	host.AdoptServerState(&stored)
	if host.Fingerprint() != stored.Fingerprint() {
		t.Errorf("a location with the server's defaults does not converge:\n%s", Diff(&stored, host))
	}

	explicit := desired()
	explicit.Locations[0].XFrameOptions = "DENY"
	explicit.AdoptServerState(&stored)
	if explicit.Locations[0].XFrameOptions != "DENY" {
		t.Error("an explicit location value must win over the stored one")
	}
}

// TestFingerprintFollowsTheForwardRules: NPMplus clears the switches that do
// nothing for a scheme or a combination before storing a host or a location.
// A label combination the server normalises must not count as a drift.
func TestFingerprintFollowsTheForwardRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		desired, stored Location
	}{
		{"request buffering stays on while AppSec runs",
			Location{ForwardScheme: "http", DisableRequestBuffering: true},
			Location{ForwardScheme: "http"}},
		{"buffering applies to http only",
			Location{ForwardScheme: "grpc", DisableCrowdsecAppsec: true, DisableRequestBuffering: true, DisableResponseBuffering: true},
			Location{ForwardScheme: "grpc", DisableCrowdsecAppsec: true}},
		{"fancyindex applies to path only",
			Location{ForwardScheme: "http", FancyIndex: true},
			Location{ForwardScheme: "http"}},
		{"upstream compression does not apply to path",
			Location{ForwardScheme: "path", UpstreamCompression: true},
			Location{ForwardScheme: "path"}},
		{"an auth upstream needs an auth service",
			Location{ForwardScheme: "http", AuthRequest: "none", AuthRequestUpstream: "http://auth:9091"},
			Location{ForwardScheme: "http", AuthRequest: "none"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			host := func(l Location) *ProxyHost {
				return &ProxyHost{
					DomainNames: []string{"a.example.com"}, ForwardScheme: l.ForwardScheme,
					DisableCrowdsecAppsec: l.DisableCrowdsecAppsec, DisableRequestBuffering: l.DisableRequestBuffering,
					DisableResponseBuffering: l.DisableResponseBuffering, UpstreamCompression: l.UpstreamCompression,
					FancyIndex: l.FancyIndex, AuthRequest: l.AuthRequest, AuthRequestUpstream: l.AuthRequestUpstream,
					Locations: []Location{l},
				}
			}
			if host(tt.desired).Fingerprint() != host(tt.stored).Fingerprint() {
				t.Error("the combination the server normalises counts as a drift")
			}
		})
	}

	kept := &ProxyHost{DomainNames: []string{"a.example.com"}, ForwardScheme: "http",
		DisableCrowdsecAppsec: true, DisableRequestBuffering: true}
	plain := &ProxyHost{DomainNames: []string{"a.example.com"}, ForwardScheme: "http", DisableCrowdsecAppsec: true}
	if kept.Fingerprint() == plain.Fingerprint() {
		t.Error("a combination the server keeps must still count")
	}
}
