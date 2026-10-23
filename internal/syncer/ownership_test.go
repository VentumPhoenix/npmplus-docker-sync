package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// rawServer stores what a request body would have written - as JSON rows, the
// way the server's database does - and answers List with those rows. It
// rejects any property the dialect does not accept, like the servers'
// `additionalProperties: false`, and it can replay NPMplus' meta-to-columns
// migration on its rows. That makes it the place to prove that ownership
// survives a server upgrade, which no fake that hands back the worker's own
// objects could show.
type rawServer struct {
	mu      sync.Mutex
	dialect npm.Dialect
	rows    map[npm.Kind]map[int]map[string]any
	nextID  int
	writes  []string
}

func newRawServer(dialect npm.Dialect) *rawServer {
	return &rawServer{dialect: dialect, rows: map[npm.Kind]map[int]map[string]any{}, nextID: 1}
}

func (s *rawServer) Dialect() npm.Dialect {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dialect
}

func (s *rawServer) Recheck(context.Context) error                               { return nil }
func (s *rawServer) ListCertificates(context.Context) ([]npm.Certificate, error) { return nil, nil }
func (s *rawServer) ListAccessLists(context.Context) ([]npm.AccessList, error)   { return nil, nil }

func (s *rawServer) List(_ context.Context, kind npm.Kind) ([]npm.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int, 0, len(s.rows[kind]))
	for id := range s.rows[kind] {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	out := make([]npm.Resource, 0, len(ids))
	for _, id := range ids {
		r, err := decodeRow(kind, id, s.rows[kind][id])
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (s *rawServer) Create(_ context.Context, resource npm.Resource) (npm.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.body(resource)
	if err != nil {
		return nil, err
	}
	kind := resource.Kind()
	id := s.nextID
	s.nextID++
	row["enabled"] = true
	if s.rows[kind] == nil {
		s.rows[kind] = map[int]map[string]any{}
	}
	s.rows[kind][id] = row
	s.writes = append(s.writes, fmt.Sprintf("create %s %s", kind, resource.ResourceKey()))
	return decodeRow(kind, id, row)
}

func (s *rawServer) Update(_ context.Context, id int, resource npm.Resource) (npm.Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := s.body(resource)
	if err != nil {
		return nil, err
	}
	kind := resource.Kind()
	row, ok := s.rows[kind][id]
	if !ok {
		return nil, &npm.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	// Both servers patch: a property the body leaves out keeps its value.
	for k, v := range body {
		row[k] = v
	}
	s.writes = append(s.writes, fmt.Sprintf("update %s %d", kind, id))
	return decodeRow(kind, id, row)
}

func (s *rawServer) Delete(_ context.Context, kind npm.Kind, id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows[kind], id)
	s.writes = append(s.writes, fmt.Sprintf("delete %s %d", kind, id))
	return nil
}

func (s *rawServer) SetEnabled(_ context.Context, kind npm.Kind, id int, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[kind][id]; ok {
		row["enabled"] = enabled
	}
	return nil
}

// body renders the request body and refuses what the dialect does not accept.
func (s *rawServer) body(resource npm.Resource) (map[string]any, error) {
	payload, err := resource.Payload(s.dialect)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		return nil, err
	}
	for field := range row {
		if !s.dialect.Accepts(resource.Kind(), field) {
			return nil, &npm.APIError{Status: http.StatusBadRequest,
				Message: fmt.Sprintf("data must NOT have additional properties (%s)", field)}
		}
	}
	return row, nil
}

// migrateMetaToColumns replays NPMplus' 20260926160524_meta_to_columns: the
// group moves into a column, the meta object is emptied for good, and the
// retired switches are gone. From then on the server is the new dialect.
func (s *rawServer) migrateMetaToColumns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	retired := []string{"allow_websocket_upgrade", "http2_support", "block_exploits", "caching_enabled", "trust_forwarded_proto"}
	for _, rows := range s.rows {
		for _, row := range rows {
			group := ""
			if meta, ok := row["meta"].(map[string]any); ok {
				group, _ = meta["directory"].(string)
			}
			row["npmplus_directory"] = strings.TrimSpace(group)
			delete(row, "meta")
			for _, field := range retired {
				delete(row, field)
			}
			if protocol, ok := row["npmplus_proxy_protocol_forwarding"].(bool); ok {
				row["npmplus_proxy_protocol_forwarding"] = map[bool]int{false: 0, true: 1}[protocol]
			}
		}
	}
	s.dialect = npm.Dialect{Flavour: npm.FlavourNPMplus, MetaRetired: true}
}

// insert stores a row as it is, for resources this release did not write.
func (s *rawServer) insert(kind npm.Kind, row map[string]any) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextID
	s.nextID++
	if _, ok := row["enabled"]; !ok {
		row["enabled"] = true
	}
	if s.rows[kind] == nil {
		s.rows[kind] = map[int]map[string]any{}
	}
	s.rows[kind][id] = row
	return id
}

func (s *rawServer) row(kind npm.Kind, id int) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[kind][id]
}

func (s *rawServer) count(kind npm.Kind) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows[kind])
}

func (s *rawServer) resetWrites() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.writes
	s.writes = nil
	return out
}

func decodeRow(kind npm.Kind, id int, row map[string]any) (npm.Resource, error) {
	withID := make(map[string]any, len(row)+1)
	for k, v := range row {
		withID[k] = v
	}
	withID["id"] = id
	raw, err := json.Marshal(withID)
	if err != nil {
		return nil, err
	}
	r := newResourceOf(kind)
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, err
	}
	return r, nil
}

// legacyRow is a resource as v1.0.0-beta.9 wrote it: the owner in the meta
// object only, no marker.
func legacyRow(t *testing.T, target *docker.Target, dialect npm.Dialect) map[string]any {
	t.Helper()
	r := BuildResource(target, targetCertificate(target), "", "npm")
	payload, err := r.Payload(dialect)
	if err != nil {
		t.Fatalf("Payload() error: %v", err)
	}
	raw, _ := json.Marshal(payload)
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"advanced_config", "npmplus_advanced_config"} {
		if text, ok := row[field].(string); ok {
			_, user, _ := strings.Cut(text, "\n")
			row[field] = user
		}
	}
	return row
}

func ownershipOptions() Options {
	opts := defaultOptions()
	opts.OnStop = docker.OnStopDisable
	return opts
}

func reconcile(t *testing.T, w *Worker) Result {
	t.Helper()
	res, err := w.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	return res
}

func groupedProxyTarget(name, domain, group string) *docker.Target {
	t := proxyTarget(name, domain, 80)
	t.Group = &group
	return t
}

// TestOwnershipSurvivesTheMetaMigration is the reason for the marker: NPMplus
// empties the meta object of every host when it moves to columns. Hosts this
// release wrote must still be ours afterwards - unchanged, in their groups,
// and deleted when their container goes.
func TestOwnershipSurvivesTheMetaMigration(t *testing.T) {
	t.Parallel()

	server := newRawServer(npm.Dialect{Flavour: npm.FlavourNPMplus})
	web := groupedProxyTarget("web", "web.example.com", "Production")
	gone := proxyTarget("gone", "gone.example.com", 80)
	db := streamTarget("db", 5432, 5432)
	db.ProxyProtocol = 1
	old := redirectTarget("web", "old.example.com", "web.example.com")
	parked := deadTarget("web", "parked.example.com")
	src := newFakeSource(web, gone, db, old, parked)
	w := NewWorker(server, src, ownershipOptions(), nil)

	if res := reconcile(t, w); res.Created != 5 {
		t.Fatalf("first run = %+v, want five resources created", res)
	}
	server.migrateMetaToColumns()
	server.resetWrites()

	cold := NewWorker(server, src, ownershipOptions(), nil)
	if res := reconcile(t, cold); res.Created+res.Updated+res.Deleted+res.Failed+res.Skipped != 0 {
		t.Fatalf("run after the migration = %+v, writes %v; want nothing to do", res, server.resetWrites())
	}

	src.set(web, db, old, parked)
	res := reconcile(t, cold)
	if res.Deleted != 1 || server.count(npm.KindProxy) != 1 {
		t.Fatalf("Result = %+v, want the host of the removed container deleted", res)
	}
}

// TestHostsOfEarlierReleasesAreMarkedBeforeTheMigration: a host written by
// v1.0.0-beta.9 has its owner in the meta object only. This release marks
// every one of them while the server still has the meta object - including the
// host of a stopped container, which is otherwise never written - so the
// migration finds nothing left to lose.
func TestHostsOfEarlierReleasesAreMarkedBeforeTheMigration(t *testing.T) {
	t.Parallel()

	dialect := npm.Dialect{Flavour: npm.FlavourNPMplus}
	server := newRawServer(dialect)
	web := proxyTarget("web", "web.example.com", 80)
	stopped := proxyTarget("idle", "idle.example.com", 80)
	db := streamTarget("db", 5432, 5432)
	webID := server.insert(npm.KindProxy, legacyRow(t, web, dialect))
	idleID := server.insert(npm.KindProxy, legacyRow(t, stopped, dialect))
	server.insert(npm.KindStream, legacyRow(t, db, dialect))

	stopped.Running = false
	src := newFakeSource(web, stopped, db)
	w := NewWorker(server, src, ownershipOptions(), nil)

	res := reconcile(t, w)
	if res.Created != 0 || res.Deleted != 0 || res.Failed != 0 {
		t.Fatalf("Result = %+v, want the existing hosts adopted in place", res)
	}
	for _, id := range []int{webID, idleID} {
		row := server.row(npm.KindProxy, id)
		if !strings.Contains(row["advanced_config"].(string), "# npmplus-docker-sync:") {
			t.Errorf("host %d was not marked: %v", id, row["advanced_config"])
		}
		if _, ok := row["meta"]; !ok {
			t.Errorf("host %d lost its meta object on a server that still has one", id)
		}
	}
	if forwardHost := server.row(npm.KindProxy, idleID)["forward_host"]; forwardHost != "idle" {
		t.Errorf("the stopped container's host was reconfigured: forward_host = %v", forwardHost)
	}

	if res := reconcile(t, w); res.Updated != 0 {
		t.Errorf("second run = %+v, want the marking to happen once", res)
	}

	server.migrateMetaToColumns()
	cold := NewWorker(server, src, ownershipOptions(), nil)
	if res := reconcile(t, cold); res.Created+res.Deleted+res.Skipped+res.Failed != 0 || server.count(npm.KindProxy) != 2 {
		t.Fatalf("run after the migration = %+v, want every host still ours", res)
	}
}

// TestHostsThatLostTheirOwnerAreAdoptedNotDeleted: NPMplus was upgraded before
// this release ever ran, so the meta object is gone and there is no marker.
// Nothing may be deleted; a host the labels still ask for is adopted and
// marked, every other one is left alone.
func TestHostsThatLostTheirOwnerAreAdoptedNotDeleted(t *testing.T) {
	t.Parallel()

	server := newRawServer(npm.Dialect{Flavour: npm.FlavourNPMplus, MetaRetired: true})
	server.insert(npm.KindProxy, map[string]any{
		"domain_names": []string{"web.example.com"}, "forward_scheme": "http", "forward_host": "web",
		"forward_port": 80, "advanced_config": "", "npmplus_directory": "",
	})
	strayID := server.insert(npm.KindProxy, map[string]any{
		"domain_names": []string{"stray.example.com"}, "forward_scheme": "http", "forward_host": "stray",
		"forward_port": 80, "advanced_config": "", "npmplus_directory": "",
	})

	src := newFakeSource(proxyTarget("web", "web.example.com", 80))
	w := NewWorker(server, src, ownershipOptions(), nil)
	res := reconcile(t, w)
	if res.Deleted != 0 || res.Created != 0 || res.Updated != 1 {
		t.Fatalf("Result = %+v, want the labelled host adopted and nothing deleted", res)
	}
	if server.row(npm.KindProxy, strayID) == nil {
		t.Fatal("a host of unknown origin was deleted")
	}
	if res := reconcile(t, w); res.Updated+res.Deleted != 0 {
		t.Errorf("second run = %+v, want the adopted host to be settled", res)
	}
}

// TestACopiedMarkerDoesNotMakeAHostOurs: the marker names the host it was
// written for. A host that carries one for another key - copied in the UI, or
// given another first domain by hand - is nobody's and is never deleted, even
// if its meta object says otherwise.
func TestACopiedMarkerDoesNotMakeAHostOurs(t *testing.T) {
	t.Parallel()

	dialect := npm.Dialect{Flavour: npm.FlavourNPMplus}
	server := newRawServer(dialect)
	src := newFakeSource(proxyTarget("web", "web.example.com", 80))
	w := NewWorker(server, src, ownershipOptions(), nil)
	reconcile(t, w)

	original := server.row(npm.KindProxy, 1)
	copied := make(map[string]any, len(original))
	for k, v := range original {
		copied[k] = v
	}
	copied["domain_names"] = []string{"copy.example.com"}
	copyID := server.insert(npm.KindProxy, copied)

	if res := reconcile(t, w); res.Deleted != 0 || server.row(npm.KindProxy, copyID) == nil {
		t.Fatalf("Result = %+v, want the copy left alone", res)
	}
}

// TestUpstreamNPMIsMarkedTheSameWay: upstream NPM gets the marker too, next to
// the meta object it still has. Its streams have no advanced configuration and
// keep their owner in the meta object alone.
func TestUpstreamNPMIsMarkedTheSameWay(t *testing.T) {
	t.Parallel()

	server := newRawServer(npm.Dialect{Flavour: npm.FlavourNPM, Version: npm.Version{Major: 2, Minor: 16}})
	web := proxyTarget("web", "web.example.com", 80)
	db := streamTarget("db", 5432, 5432)
	src := newFakeSource(web, db)
	w := NewWorker(server, src, ownershipOptions(), nil)
	if res := reconcile(t, w); res.Created != 2 {
		t.Fatalf("Result = %+v, want two resources created", res)
	}

	proxy := server.row(npm.KindProxy, 1)
	if !strings.HasPrefix(proxy["advanced_config"].(string), "# npmplus-docker-sync:") || proxy["meta"] == nil {
		t.Errorf("proxy host = %v, want the marker and the meta object", proxy)
	}
	stream := server.row(npm.KindStream, 2)
	if _, ok := stream["npmplus_advanced_config"]; ok || stream["meta"] == nil {
		t.Errorf("stream = %v, want the owner in the meta object only", stream)
	}

	if res := reconcile(t, NewWorker(server, src, ownershipOptions(), nil)); res.Updated != 0 {
		t.Errorf("cold run = %+v, want nothing to do", res)
	}
	src.set(web)
	if res := reconcile(t, w); res.Deleted != 1 {
		t.Errorf("Result = %+v, want the stream of the removed container deleted", res)
	}
}

// TestRetiredSettingsAreReportedOnlyWhenAsked: against an NPMplus without the
// meta object, a label that sets a retired switch is told it has no effect -
// once - and the defaults of those switches say nothing at all.
func TestRetiredSettingsAreReportedOnlyWhenAsked(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	server := newRawServer(npm.Dialect{Flavour: npm.FlavourNPMplus, MetaRetired: true})

	asked := proxyTarget("asked", "asked.example.com", 80)
	asked.Websockets = false
	asked.Explicit = []string{"domains", "websockets"}
	quiet := proxyTarget("quiet", "quiet.example.com", 80)
	db := streamTarget("db", 5432, 5432)
	db.ProxyProtocol = 2
	db.Explicit = []string{"incoming_port", "proxy_protocol"}

	w := NewWorker(server, newFakeSource(asked, quiet, db), ownershipOptions(), log)
	if res := reconcile(t, w); res.Created != 3 || res.Failed != 0 {
		t.Fatalf("Result = %+v, want everything created", res)
	}
	reconcile(t, w)

	out := logs.String()
	if got := strings.Count(out, "settings this server does not offer are ignored"); got != 1 {
		t.Errorf("warned %d times about retired settings, want once:\n%s", got, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "does not offer") && !strings.Contains(line, "key=asked.example.com flavour=npmplus fields=websockets") {
			t.Errorf("want the warning only for the host that asked, naming websockets: %s", line)
		}
	}
	// This NPMplus knows the levels, so v2 is not weakened here.
	if strings.Contains(out, "weaker form") {
		t.Errorf("proxy protocol v2 reported as weakened on a server that knows it:\n%s", out)
	}
}
