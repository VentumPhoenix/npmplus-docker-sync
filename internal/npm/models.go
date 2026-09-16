package npm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Meta keys written into every resource this tool owns.
const (
	MetaManagedBy   = "managed_by"
	MetaContainer   = "managed_container"
	MetaIndex       = "managed_index"
	ManagedByValue  = "npmplus-docker-sync"
	CertificateNew  = "new"
	certificateNone = 0
)

// LegacyManagedByValues are ownership markers written by earlier releases.
// They are still recognised so an upgrade adopts (and re-stamps) existing
// resources instead of orphaning them.
var LegacyManagedByValues = []string{"npm-docker-sync"}

// IsManaged reports whether the meta object carries the current ownership
// marker or one of its predecessors.
func IsManaged(m Meta) bool {
	if m.ManagedBy(ManagedByValue) {
		return true
	}
	for _, legacy := range LegacyManagedByValues {
		if m.ManagedBy(legacy) {
			return true
		}
	}
	return false
}

// Meta is the free-form meta object attached to every NPM resource. NPM keeps
// its Let's Encrypt settings here; we add our own ownership markers.
type Meta map[string]any

// ManagedBy reports whether the resource carries our ownership marker.
func (m Meta) ManagedBy(value string) bool {
	if m == nil {
		return false
	}
	v, ok := m[MetaManagedBy].(string)
	return ok && v == value
}

// Container returns the container name recorded in the marker, if any.
func (m Meta) Container() string {
	if m == nil {
		return ""
	}
	v, _ := m[MetaContainer].(string)
	return v
}

// Access list types. NPMplus replaced NPM's single `access_list_id` with a
// list of ids plus an explicit type; "public" means no access list at all and
// "global" (locations only) means "inherit from the proxy host".
const (
	AccessListPublic = "public"
	AccessListCustom = "custom"
	AccessListGlobal = "global"
)

// Location is an optional custom location block of a proxy host.
//
// The npmplus_* fields are mandatory on NPMplus and unknown to upstream NPM;
// the per-flavour payload structs decide which of them are sent.
type Location struct {
	Path           string `json:"path"`
	AdvancedConfig string `json:"advanced_config"`
	ForwardScheme  string `json:"forward_scheme"`
	ForwardHost    string `json:"forward_host"`
	ForwardPort    int    `json:"forward_port"`
	AccessListIDs  []int  `json:"npmplus_access_list_ids,omitempty"`
	AccessListType string `json:"npmplus_access_list_type,omitempty"`
}

// accessListType returns the location's access list type, defaulting to
// "global" (inherit the proxy host's setting).
func (l Location) accessListType() string {
	if l.AccessListType != "" {
		return l.AccessListType
	}
	if len(l.AccessListIDs) > 0 {
		return AccessListCustom
	}
	return AccessListGlobal
}

// Certificate mirrors the `/api/nginx/certificates` resource.
type Certificate struct {
	ID          int      `json:"id"`
	Provider    string   `json:"provider"`
	NiceName    string   `json:"nice_name"`
	DomainNames []string `json:"domain_names"`
	ExpiresOn   string   `json:"expires_on"`
}

// CertificateID models NPM's polymorphic certificate_id field: responses
// return a number, while create/update payloads may carry the string "new"
// to request a fresh Let's Encrypt certificate.
type CertificateID struct {
	ID  int
	New bool
}

// NewCertificate returns a CertificateID requesting a new LE certificate.
func NewCertificate() CertificateID { return CertificateID{New: true} }

// CertificateRef returns a CertificateID pointing at an existing certificate.
func CertificateRef(id int) CertificateID { return CertificateID{ID: id} }

// IsZero reports whether no certificate is attached at all.
func (c CertificateID) IsZero() bool { return !c.New && c.ID == certificateNone }

// String implements fmt.Stringer.
func (c CertificateID) String() string {
	if c.New {
		return CertificateNew
	}
	return strconv.Itoa(c.ID)
}

// MarshalJSON emits either a number or the literal string "new".
func (c CertificateID) MarshalJSON() ([]byte, error) {
	if c.New {
		return json.Marshal(CertificateNew)
	}
	return json.Marshal(c.ID)
}

// UnmarshalJSON accepts numbers, numeric strings, "new", null and "".
func (c *CertificateID) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*c = CertificateID{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "", "0", "none":
			*c = CertificateID{}
		case CertificateNew:
			*c = CertificateID{New: true}
		default:
			id, err := strconv.Atoi(s)
			if err != nil {
				return fmt.Errorf("certificate_id: %q is neither a number nor %q", s, CertificateNew)
			}
			*c = CertificateID{ID: id}
		}
		return nil
	}
	var id int
	if err := json.Unmarshal(data, &id); err != nil {
		return fmt.Errorf("certificate_id: %w", err)
	}
	*c = CertificateID{ID: id}
	return nil
}

// Flag models NPM's "enabled" field which is a number in some versions and a
// boolean in others.
type Flag bool

// MarshalJSON always writes a boolean, which every NPM version accepts.
func (f Flag) MarshalJSON() ([]byte, error) { return json.Marshal(bool(f)) }

// UnmarshalJSON accepts true/false, 1/0 and "1"/"0".
func (f *Flag) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	switch string(data) {
	case "", "null":
		*f = false
		return nil
	case "true", "1", `"1"`, `"true"`:
		*f = true
		return nil
	case "false", "0", `"0"`, `"false"`:
		*f = false
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("enabled: %w", err)
	}
	*f = Flag(b)
	return nil
}

// tokenResponse is the payload returned by POST /api/tokens. Standard NPM
// returns the JWT here; NPMplus sets an httpOnly cookie instead and may
// return an empty body.
type tokenResponse struct {
	Token   string   `json:"token"`
	Expires flexTime `json:"expires"`
}

// flexTime parses the different shapes NPM used for `expires` over the
// years: RFC3339 strings, unix seconds and unix milliseconds.
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" || string(data) == `""` {
		t.Time = time.Time{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, s); err == nil {
				t.Time = parsed
				return nil
			}
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			t.Time = fromUnix(n)
			return nil
		}
		return fmt.Errorf("expires: cannot parse %q", s)
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("expires: %w", err)
	}
	i, err := n.Int64()
	if err != nil {
		return fmt.Errorf("expires: %w", err)
	}
	t.Time = fromUnix(i)
	return nil
}

// fromUnix treats large values as milliseconds and small ones as seconds.
func fromUnix(n int64) time.Time {
	const millisThreshold = 1e11
	if n > millisThreshold {
		return time.UnixMilli(n).UTC()
	}
	return time.Unix(n, 0).UTC()
}

// apiError is the error envelope used by NPM.
type apiError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// NormalizeDomains lowercases, trims and sorts domain names so two resources
// can be compared deterministically.
func NormalizeDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		d = strings.TrimSuffix(d, ".")
		if d == "" {
			continue
		}
		if _, dup := seen[d]; dup {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// fingerprint hashes any canonical shape with SHA-256. Only configuration
// relevant fields must be passed in; server-side fields such as ids and
// timestamps are deliberately excluded by the callers.
func fingerprint(shape any) string {
	payload, err := json.Marshal(shape)
	if err != nil {
		// Cannot happen for the flat shapes used here; degrade safely.
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// ownership extracts the marker fields shared by every fingerprint shape.
func ownership(m Meta) (managedBy, container string, index int) {
	if m == nil {
		return "", "", 0
	}
	managedBy, _ = m[MetaManagedBy].(string)
	container, _ = m[MetaContainer].(string)
	switch v := m[MetaIndex].(type) {
	case int:
		index = v
	case float64: // JSON round-trip
		index = int(v)
	}
	return managedBy, container, index
}

// ---------------------------------------------------------------------------
// Ports
// ---------------------------------------------------------------------------

// Port models the polymorphic port fields of the stream resource. Upstream NPM
// types `incoming_port` and `forwarding_port` as integers, NPMplus types them
// as strings and additionally accepts a range ("8080-8090") for the incoming
// port and "$server_port" for the forwarded one.
//
// The raw text is preserved so a foreign stream that uses one of the NPMplus
// forms still round-trips and keeps a stable identity.
type Port struct{ raw string }

// PortOf returns a Port for a plain port number. The zero value means unset.
func PortOf(n int) Port {
	if n == 0 {
		return Port{}
	}
	return Port{raw: strconv.Itoa(n)}
}

// PortText returns a Port for one of the textual forms NPMplus allows.
func PortText(s string) Port { return Port{raw: strings.TrimSpace(s)} }

// String returns the port in its textual form ("" when unset).
func (p Port) String() string { return p.raw }

// Int returns the numeric value, or 0 for an unset or non-numeric port such as
// a range or "$server_port".
func (p Port) Int() int {
	n, err := strconv.Atoi(p.raw)
	if err != nil {
		return 0
	}
	return n
}

// IsZero reports whether no port is set.
func (p Port) IsZero() bool { return p.raw == "" }

// MarshalJSON emits a number for numeric ports and a string otherwise, which
// matches what both flavours return. Request bodies do not use this: the
// per-flavour payload structs pick the type the server's schema demands.
func (p Port) MarshalJSON() ([]byte, error) {
	if n := p.Int(); n > 0 {
		return json.Marshal(n)
	}
	return json.Marshal(p.raw)
}

// UnmarshalJSON accepts both the integer (NPM) and the string (NPMplus) form.
func (p *Port) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*p = Port{}
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("port: %w", err)
		}
		*p = Port{raw: strings.TrimSpace(s)}
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("port: %w", err)
	}
	*p = Port{raw: n.String()}
	return nil
}
