package npm

import "strings"

// The columns in which NPMplus reports whether the configuration it generated
// for a host could be loaded. Upstream NPM and NPMplus releases before the
// columns keep the same two values in the meta object (serverMetaKeys).
const (
	fieldNginxOnline = "npmplus_nginx_online"
	fieldNginxError  = "npmplus_nginx_err"
)

// maxNginxError caps how much of the nginx error message is kept: it names
// the offending line, and that is all a log line or /status needs.
const maxNginxError = 300

// nginxState is what the server reports about the configuration it rendered
// for a resource. It is only ever read: requests are built from payload
// structs of their own, the fingerprint leaves it out and Diff treats it as
// server owned.
type nginxState struct {
	NginxOnline *Flag  `json:"npmplus_nginx_online,omitempty"`
	NginxError  string `json:"npmplus_nginx_err,omitempty"`
}

func (s *nginxState) nginx() *nginxState { return s }

// NginxStatus reports whether the server could load the nginx configuration of
// a resource it returned. reported is false when the server said nothing about
// it - a resource built from labels, or a release that does not track it - and
// a resource the server did not report on counts as online.
//
// A configuration nginx refuses is renamed to <id>.conf.err and the host is
// not served at all, which the API shows nowhere but here.
func NginxStatus(r Resource) (online bool, message string, reported bool) {
	if s, ok := r.(interface{ nginx() *nginxState }); ok {
		if state := s.nginx(); state.NginxOnline != nil {
			return bool(*state.NginxOnline), shortNginxError(state.NginxError), true
		}
	}
	meta := r.ResourceMeta()
	value, ok := meta["nginx_online"]
	if !ok {
		return true, "", false
	}
	switch v := value.(type) {
	case bool:
		online = v
	case float64:
		online = v != 0
	default:
		return true, "", false
	}
	message, _ = meta["nginx_err"].(string)
	return online, shortNginxError(message), true
}

// shortNginxError trims an nginx error message to its first line, at most
// maxNginxError bytes.
func shortNginxError(message string) string {
	message = strings.TrimSpace(message)
	if i := strings.IndexByte(message, '\n'); i >= 0 {
		message = strings.TrimSpace(message[:i])
	}
	if len(message) > maxNginxError {
		message = strings.TrimSpace(message[:maxNginxError]) + "…"
	}
	return message
}
