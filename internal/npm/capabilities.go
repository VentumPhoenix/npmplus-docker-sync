package npm

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// WriteSchema lists, per resource kind, the properties a create or update body
// may carry. It is read from GET /api/schema, which serves the very document
// both servers validate request bodies against, so it is the authoritative
// answer to "may this payload contain field X" - no version table needed.
//
// Properties of a proxy host's custom locations are listed as
// "locations/<name>".
type WriteSchema map[Kind]map[string]Property

// Property is what a schema says about one request property.
type Property struct {
	// Type is the JSON type, "" when the schema allows several.
	Type string
	// Enum lists the allowed values, nil when any value of the type is.
	// Strings are kept as they are, other values as their JSON text.
	Enum []string
}

// legacyProxySwitches are the proxy host switches NPMplus retired together
// with the meta object. Websockets and HTTP/2 are always on there, caching and
// "block exploits" had already been reduced to no-ops, and X-Forwarded-Proto
// is always trusted.
var legacyProxySwitches = []string{
	"allow_websocket_upgrade", "http2_support", "block_exploits", "caching_enabled", "trust_forwarded_proto",
}

// columnFields are the properties NPMplus introduced when it moved the meta
// object into columns.
var columnFields = []string{
	"npmplus_directory", "npmplus_dns_challenge", "npmplus_dns_provider",
	"npmplus_dns_provider_credentials", "npmplus_propagation_seconds",
}

// Accepts reports whether a create/update body of the kind may carry the
// field. The schema read from the server decides when there is one; without it
// the answer comes from what is known about the flavour and its releases.
func (d Dialect) Accepts(kind Kind, field string) bool {
	if fields, ok := d.Schema[kind]; ok {
		_, accepted := fields[field]
		return accepted
	}
	return d.assumes(kind, field)
}

// TypeOf reports the JSON type the server expects for a field, "" when it is
// unknown or not a single type.
func (d Dialect) TypeOf(kind Kind, field string) string {
	if fields, ok := d.Schema[kind]; ok {
		return fields[field].Type
	}
	// NPMplus typed the proxy protocol of a stream as a boolean in the
	// releases that still have the meta object, and went back to the levels
	// 0, 1 and 2 with the ones after it.
	if d.Flavour == FlavourNPMplus && kind == KindStream && field == fieldProxyProtocol {
		if d.MetaRetired {
			return "integer"
		}
		return "boolean"
	}
	return ""
}

// AcceptsValue reports whether the server allows the value for a field that
// is restricted to a list of values.
func (d Dialect) AcceptsValue(kind Kind, field, value string) bool {
	if fields, ok := d.Schema[kind]; ok {
		property, accepted := fields[field]
		return accepted && (property.Enum == nil || slices.Contains(property.Enum, value))
	}
	// The scheme of a redirection that keeps the request's own: upstream NPM
	// calls it "auto" and translates it, NPMplus writes nginx' "$scheme"
	// verbatim into the config and dropped "auto" with the meta object.
	if kind == KindRedirect && field == "forward_scheme" {
		switch {
		case d.Flavour == FlavourNPM:
			return value != SchemeKeep
		case d.MetaRetired:
			return value != SchemeAuto
		}
	}
	return true
}

// Redirection schemes that keep the scheme of the request.
const (
	SchemeAuto = "auto"
	SchemeKeep = "$scheme"
)

// fieldProxyProtocol is the stream's proxy protocol property.
const fieldProxyProtocol = "npmplus_proxy_protocol_forwarding"

// assumes is the built-in knowledge used when the server serves no schema.
func (d Dialect) assumes(kind Kind, field string) bool {
	if d.Flavour == FlavourNPM {
		switch {
		case strings.HasPrefix(field, "npmplus_"), strings.HasPrefix(field, "locations/npmplus_"):
			return false
		case field == "trust_forwarded_proto":
			return d.Version.AtLeast(2, 12)
		case kind == KindStream && field == "certificate_id":
			return d.Version.AtLeast(2, 12)
		}
		return true
	}

	// NPMplus, before or after it retired the meta object.
	retired := d.MetaRetired
	switch {
	case field == "meta", field == "locations/npmplus_location_config":
		return !retired
	case slices.Contains(columnFields, field):
		return retired
	case kind == KindProxy && slices.Contains(legacyProxySwitches, field):
		return !retired
	case kind == KindRedirect && (field == "block_exploits" || field == "http2_support"):
		return !retired
	case kind == KindDead && field == "http2_support":
		return !retired
	}
	return true
}

// advancedField is the property holding a kind's advanced configuration.
func advancedField(kind Kind) string {
	if kind == KindStream {
		return "npmplus_advanced_config"
	}
	return "advanced_config"
}

// CarriesMarker reports whether resources of the kind can hold the ownership
// marker on this server. Every kind can, except upstream NPM's streams, which
// have no advanced configuration; those keep their owner in the meta object.
func (d Dialect) CarriesMarker(kind Kind) bool {
	return d.Accepts(kind, advancedField(kind))
}

// KeepsOwnership reports whether the server can store who owns a resource of
// the kind at all. Without it nothing could ever be recognised as ours again.
func (d Dialect) KeepsOwnership(kind Kind) bool {
	return d.CarriesMarker(kind) || d.Accepts(kind, "meta")
}

// parseWriteSchema extracts the write properties from the document served at
// GET /api/schema - an OpenAPI document with every $ref already resolved.
//
// For each kind the POST of the collection and the PUT of an item are read;
// when both exist only the properties both accept are kept, because a payload
// is built once for either.
func parseWriteSchema(raw []byte) (WriteSchema, error) {
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode the api schema: %w", err)
	}
	if len(document.Paths) == 0 {
		return nil, fmt.Errorf("the api schema lists no paths")
	}

	out := WriteSchema{}
	for _, kind := range Kinds {
		base := strings.TrimPrefix(kind.Path(), "/api")
		var sets []map[string]Property
		if fields, ok := requestFields(document.Paths[base]["post"]); ok {
			sets = append(sets, fields)
		}
		for path, methods := range document.Paths {
			if !isItemPath(base, path) {
				continue
			}
			if fields, ok := requestFields(methods["put"]); ok {
				sets = append(sets, fields)
			}
		}
		if len(sets) == 0 {
			continue
		}
		merged := sets[0]
		for _, other := range sets[1:] {
			for field := range merged {
				if _, ok := other[field]; !ok {
					delete(merged, field)
				}
			}
		}
		out[kind] = merged
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the api schema describes none of the host collections")
	}
	return out, nil
}

// isItemPath matches "<base>/{id}" and nothing deeper.
func isItemPath(base, path string) bool {
	rest, ok := strings.CutPrefix(path, base+"/")
	return ok && strings.HasPrefix(rest, "{") && strings.HasSuffix(rest, "}") && !strings.Contains(rest, "/")
}

// schemaProperty is the part of a JSON schema property this tool reads.
type schemaProperty struct {
	Type  json.RawMessage   `json:"type"`
	Enum  []json.RawMessage `json:"enum"`
	Items struct {
		Properties map[string]schemaProperty `json:"properties"`
	} `json:"items"`
}

func (p schemaProperty) property() Property {
	var out Property
	_ = json.Unmarshal(p.Type, &out.Type) // a list of types stays ""
	for _, raw := range p.Enum {
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = string(raw)
		}
		out.Enum = append(out.Enum, text)
	}
	return out
}

// requestFields reads the top level properties of an operation's JSON request
// body, plus those of the custom locations.
func requestFields(operation json.RawMessage) (map[string]Property, bool) {
	if len(operation) == 0 {
		return nil, false
	}
	var op struct {
		RequestBody struct {
			Content map[string]struct {
				Schema struct {
					Properties map[string]schemaProperty `json:"properties"`
				} `json:"schema"`
			} `json:"content"`
		} `json:"requestBody"`
	}
	if err := json.Unmarshal(operation, &op); err != nil {
		return nil, false
	}
	body, ok := op.RequestBody.Content["application/json"]
	if !ok || len(body.Schema.Properties) == 0 {
		return nil, false
	}
	fields := make(map[string]Property, len(body.Schema.Properties))
	for name, property := range body.Schema.Properties {
		fields[name] = property.property()
		if name != "locations" {
			continue
		}
		for nested, nestedProperty := range property.Items.Properties {
			fields["locations/"+nested] = nestedProperty.property()
		}
	}
	return fields, true
}

// metaRetiredFrom reads the shape of a host as the server returns it: NPMplus
// releases that retired the meta object report the new `npmplus_*` columns
// instead. It is the fallback for a server that serves no schema.
func metaRetiredFrom(host map[string]json.RawMessage) (retired, conclusive bool) {
	if _, ok := host["npmplus_directory"]; ok {
		return true, true
	}
	if _, ok := host["npmplus_nginx_online"]; ok {
		return true, true
	}
	if _, ok := host["meta"]; ok {
		return false, true
	}
	return false, false
}
