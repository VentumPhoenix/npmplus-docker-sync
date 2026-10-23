package npm

import (
	"net/url"
	"strconv"
	"strings"
)

// The ownership marker is a comment line at the top of a resource's advanced
// configuration:
//
//	# npmplus-docker-sync: managed from Docker labels, do not edit or remove this line | v=1 key=app.example.com container=whoami index=0
//
// It exists because the meta object cannot be relied on. NPMplus moves its
// own meta keys into columns and empties `meta` on every host while doing so
// (migration 20260926160524_meta_to_columns), and its write schemas no longer
// accept the property at all. The advanced configuration survives that
// migration untouched, exists on proxy, redirection and 404 hosts of both
// flavours and on NPMplus streams, is stored verbatim, and a comment means
// nothing to nginx.
//
// The marker is written next to the meta keys wherever a server still accepts
// them, so a host keeps its owner no matter which of the two survives. When
// they disagree, the marker wins: it is what this tool reads first.
//
// The values are query encoded. That keeps the line free of `;` and `{`,
// which upstream NPM scans the advanced configuration for when it decides
// whether to emit its default `location /` block.
const (
	markerPrefix  = "# npmplus-docker-sync:"
	markerNotice  = "managed from Docker labels, do not edit or remove this line"
	markerVersion = "1"
)

// marker is the ownership a resource carries in its advanced configuration.
type marker struct {
	// Key is the identity the resource had when the marker was written. A
	// marker whose key no longer matches belongs to another resource - a copy
	// made in the UI, or a host whose domains were edited by hand - and is
	// not trusted.
	Key         string
	Container   string
	ContainerID string
	Index       int
	Instance    string
	Prefix      string
}

// encode renders the marker line: space separated key=value pairs in a fixed
// order, each value query escaped.
func (m marker) encode() string {
	pairs := []string{
		"v=" + markerVersion,
		"key=" + url.QueryEscape(m.Key),
		"container=" + url.QueryEscape(m.Container),
		"index=" + strconv.Itoa(m.Index),
	}
	if m.ContainerID != "" {
		pairs = append(pairs, "container_id="+url.QueryEscape(m.ContainerID))
	}
	if m.Instance != "" {
		pairs = append(pairs, "instance="+url.QueryEscape(m.Instance))
	}
	if m.Prefix != "" {
		pairs = append(pairs, "prefix="+url.QueryEscape(m.Prefix))
	}
	return markerPrefix + " " + markerNotice + " | " + strings.Join(pairs, " ")
}

// parseMarker reads one marker line. It fails for a line that is not a marker
// or one written by a marker version this release does not know.
func parseMarker(line string) (marker, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, markerPrefix) {
		return marker{}, false
	}
	_, data, ok := strings.Cut(line, "|")
	if !ok {
		return marker{}, false
	}
	values := make(map[string]string)
	for _, pair := range strings.Fields(data) {
		name, raw, ok := strings.Cut(pair, "=")
		if !ok {
			return marker{}, false
		}
		value, err := url.QueryUnescape(raw)
		if err != nil {
			return marker{}, false
		}
		values[name] = value
	}
	if values["v"] != markerVersion {
		return marker{}, false
	}
	index, err := strconv.Atoi(values["index"])
	if err != nil {
		return marker{}, false
	}
	m := marker{
		Key:         values["key"],
		Container:   values["container"],
		ContainerID: values["container_id"],
		Index:       index,
		Instance:    values["instance"],
		Prefix:      values["prefix"],
	}
	if m.Key == "" {
		return marker{}, false
	}
	return m, true
}

// isMarkerLine reports whether a line belongs to this tool, valid or not.
func isMarkerLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), markerPrefix)
}

// splitMarker separates the marker from the configuration the user wrote.
//
// Every marker line is removed, wherever it sits, so the remainder is exactly
// what the labels configured. found reports that there was at least one;
// valid that exactly one distinct, readable marker was among them.
func splitMarker(config string) (user string, m marker, found, valid bool) {
	if !strings.Contains(config, markerPrefix) {
		return config, marker{}, false, false
	}
	lines := strings.Split(config, "\n")
	kept := make([]string, 0, len(lines))
	var markers []marker
	broken := false
	for _, line := range lines {
		if !isMarkerLine(line) {
			kept = append(kept, line)
			continue
		}
		found = true
		parsed, ok := parseMarker(line)
		if !ok {
			broken = true
			continue
		}
		markers = append(markers, parsed)
	}
	user = strings.Join(kept, "\n")
	if broken || len(markers) == 0 {
		return user, marker{}, found, false
	}
	for _, other := range markers[1:] {
		if other != markers[0] {
			return user, marker{}, found, false
		}
	}
	return user, markers[0], found, true
}

// withMarker puts the marker line in front of the user's configuration.
func withMarker(user string, m marker) string {
	line := m.encode()
	if user == "" {
		return line
	}
	return line + "\n" + user
}

// markerFor derives the marker of a resource this tool owns from its meta
// object. A resource that is not ours gets none.
func markerFor(meta Meta, key string) (marker, bool) {
	if !meta.ManagedBy(ManagedByValue) || key == "" {
		return marker{}, false
	}
	_, container, index, instance := ownership(meta)
	return marker{
		Key:         key,
		Container:   container,
		ContainerID: meta.ContainerID(),
		Index:       index,
		Instance:    instance,
		Prefix:      meta.Prefix(),
	}, true
}

// ownershipKeys are the meta keys the marker stands for.
var ownershipKeys = []string{MetaManagedBy, MetaContainer, MetaContainerID, MetaIndex, MetaInstance, MetaPrefix}

// liftOwnership is applied to every resource read from the API. It removes the
// marker from the advanced configuration and records the ownership it carries
// in the meta object, where the rest of the tool looks for it - whichever of
// the two the server kept.
//
// A marker that cannot be trusted (unreadable, contradicting itself, or
// written for another key) also discards the ownership keys of the meta
// object: the resource was evidently copied or edited by hand, and a resource
// of unclear origin must never be deleted as an orphan. If its labels still
// ask for it, it is adopted and stamped afresh.
//
// It reports whether a trusted marker was found.
func liftOwnership(advanced string, meta Meta, key string) (string, Meta, bool) {
	user, m, found, valid := splitMarker(advanced)
	if !found {
		return advanced, meta, false
	}
	if !valid || m.Key != key {
		if meta.ManagedBy(ManagedByValue) {
			for _, k := range ownershipKeys {
				delete(meta, k)
			}
		}
		return user, meta, false
	}
	if meta == nil {
		meta = Meta{}
	}
	meta[MetaManagedBy] = ManagedByValue
	meta[MetaContainer] = m.Container
	meta[MetaIndex] = m.Index
	setOrDelete(meta, MetaContainerID, m.ContainerID)
	setOrDelete(meta, MetaInstance, m.Instance)
	setOrDelete(meta, MetaPrefix, m.Prefix)
	return user, meta, true
}

// liftDirectory moves NPMplus' `npmplus_directory` column into the meta key
// the group has always been read from, so grouping works the same before and
// after NPMplus moved it out of the meta object.
func liftDirectory(meta Meta, column string) Meta {
	column = strings.TrimSpace(column)
	if column == "" {
		return meta
	}
	if meta == nil {
		meta = Meta{}
	}
	if _, set := meta[MetaDirectory]; !set {
		meta[MetaDirectory] = column
	}
	return meta
}

func setOrDelete(meta Meta, key, value string) {
	if value == "" {
		delete(meta, key)
		return
	}
	meta[key] = value
}

// serverMetaKeys are written by the server itself. They are never sent back:
// NPMplus moved them into columns of their own, and on upstream NPM they are
// recomputed with every write anyway.
var serverMetaKeys = []string{"nginx_online", "nginx_err"}

// metaForWrite returns the meta object a request may carry, or nil when the
// server no longer accepts one.
func metaForWrite(dialect Dialect, kind Kind, meta Meta) Meta {
	if !dialect.Accepts(kind, "meta") {
		return nil
	}
	out := make(Meta, len(meta))
	for k, v := range meta {
		out[k] = v
	}
	for _, k := range serverMetaKeys {
		delete(out, k)
	}
	return out
}
