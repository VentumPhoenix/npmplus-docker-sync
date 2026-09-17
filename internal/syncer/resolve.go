package syncer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// loadCertificates refreshes the certificate list once per run. A failing
// request keeps the previous list: a temporarily unreachable endpoint must not
// strip every host of its certificate.
func (w *Worker) loadCertificates(ctx context.Context) {
	list, err := w.api.ListCertificates(ctx)
	if err != nil {
		w.log.Warn("could not list certificates, using the last known list",
			slog.String("error", err.Error()))
		return
	}
	hash := certs.Hash(list)

	w.refMu.Lock()
	changed := hash != w.certHash
	w.certificates, w.certHash = list, hash
	w.refMu.Unlock()

	if changed {
		w.log.Debug("certificate list changed", slog.Int("certificates", len(list)))
	}
}

// CertificateHash returns the fingerprint of the certificate list of the last
// successful fetch. The poll loop compares it to detect new certificates.
func (w *Worker) CertificateHash() string {
	w.refMu.RLock()
	defer w.refMu.RUnlock()
	return w.certHash
}

// PollCertificates fetches the certificate list and reports whether it
// changed since the last run, which is what makes a freshly issued
// certificate reach its hosts without a restart.
func (w *Worker) PollCertificates(ctx context.Context) bool {
	list, err := w.api.ListCertificates(ctx)
	if err != nil {
		return false
	}
	hash := certs.Hash(list)

	w.refMu.Lock()
	defer w.refMu.Unlock()
	if hash == w.certHash {
		return false
	}
	w.certificates, w.certHash = list, hash
	return true
}

// certificateList returns the cached certificates.
func (w *Worker) certificateList() []npm.Certificate {
	w.refMu.RLock()
	defer w.refMu.RUnlock()
	return w.certificates
}

// loadAccessLists refreshes the access list index used to resolve names.
func (w *Worker) loadAccessLists(ctx context.Context, needed bool) {
	if !needed {
		return
	}
	lists, err := w.api.ListAccessLists(ctx)
	if err != nil {
		w.log.Warn("could not list access lists, using the last known list",
			slog.String("error", err.Error()))
		return
	}
	index := make(map[string]int, len(lists))
	for _, l := range lists {
		index[strings.ToLower(strings.TrimSpace(l.Name))] = l.ID
	}

	w.refMu.Lock()
	w.accessLists = index
	w.refMu.Unlock()
}

// resolveAccessLists turns access list names into ids.
//
// An unknown name is an error, never a silent "public": a typo must not put a
// protected host on the open internet.
func (w *Worker) resolveAccessLists(t *docker.Target) error {
	w.refMu.RLock()
	index := w.accessLists
	w.refMu.RUnlock()

	resolve := func(names []string) ([]int, error) {
		var ids []int
		for _, name := range names {
			id, ok := index[strings.ToLower(strings.TrimSpace(name))]
			if !ok {
				return nil, fmt.Errorf("unknown access list %q", name)
			}
			ids = append(ids, id)
		}
		return ids, nil
	}

	ids, err := resolve(t.AccessListNames)
	if err != nil {
		return err
	}
	t.AccessListIDs = append(t.AccessListIDs, ids...)
	t.AccessListNames = nil

	for i := range t.Locations {
		ids, err := resolve(t.Locations[i].AccessListNames)
		if err != nil {
			return fmt.Errorf("location %s: %w", t.Locations[i].Path, err)
		}
		t.Locations[i].AccessListIDs = append(t.Locations[i].AccessListIDs, ids...)
		t.Locations[i].AccessListNames = nil
	}
	return nil
}

// needsAccessListNames reports whether any target references an access list by
// name, so the list is only fetched when it is actually needed.
func needsAccessListNames(targets []*docker.Target) bool {
	for _, t := range targets {
		if len(t.AccessListNames) > 0 {
			return true
		}
		for _, l := range t.Locations {
			if len(l.AccessListNames) > 0 {
				return true
			}
		}
	}
	return false
}

// resolveCertificate turns the certificate wish of a target into an id.
//
// live is the resource currently stored in NPM (nil for a new one); its
// certificate is what keeps the choice stable across runs.
func (w *Worker) resolveCertificate(log *slog.Logger, t *docker.Target, live npm.Resource) (npm.CertificateID, error) {
	current := 0
	if live != nil {
		current = live.ResourceCertificate().ID
	}

	selection, err := certs.Resolve(t.Certificate, t.Domains(), w.certificateList(), certs.Options{
		Current: current,
		Partial: w.opts.CertificatePartial,
	})
	if err != nil {
		return npm.CertificateID{}, fmt.Errorf("%s: certificate: %w", t.Describe(), err)
	}

	switch {
	case selection.New:
		return npm.NewCertificate(), nil
	case selection.ID == 0 && t.Certificate.Mode == certs.ModeAuto && w.opts.CertificateAutoCreate && len(t.Domains()) > 0:
		log.Info("no certificate matches, requesting a new one",
			slog.String("key", t.Key()), slog.String("domains", strings.Join(t.Domains(), ",")))
		return npm.NewCertificate(), nil
	}

	if len(selection.Uncovered) > 0 {
		log.Warn("certificate does not cover every domain",
			slog.String("key", t.Key()),
			slog.Int("certificate", selection.ID),
			slog.String("uncovered", strings.Join(selection.Uncovered, ",")))
	}
	if selection.ID > 0 {
		w.countCertificate(selection.Class.String())
	}
	if selection.ID > 0 && selection.ID != current {
		log.Info("certificate selected",
			slog.String("key", t.Key()),
			slog.Int("id", selection.ID),
			slog.String("match", selection.Class.String()),
			slog.String("pattern", selection.Pattern))
	}
	return npm.CertificateRef(selection.ID), nil
}

// ---------------------------------------------------------------------------
// per-resource backoff
// ---------------------------------------------------------------------------

// Backoff bounds. A resource NPM keeps rejecting is retried ever more slowly
// instead of on every Docker event, which is what turns one broken label set
// into a log and API flood.
const (
	backoffMin = 30 * time.Second
	backoffMax = 30 * time.Minute
)

// failure remembers a rejected resource.
type failure struct {
	until  time.Time
	delay  time.Duration
	hash   string
	reason string
}

// backoffKey identifies a resource across runs.
func backoffKey(kind npm.Kind, key string) string { return string(kind) + "|" + key }

// deferred reports whether a resource is still in its backoff window. A
// changed configuration clears it: the user fixing the labels should take
// effect immediately.
func (w *Worker) deferred(kind npm.Kind, key, hash string, now time.Time) (bool, time.Duration) {
	w.refMu.Lock()
	defer w.refMu.Unlock()

	f, ok := w.failures[backoffKey(kind, key)]
	switch {
	case !ok:
		return false, 0
	case f.hash != hash:
		delete(w.failures, backoffKey(kind, key))
		return false, 0
	case now.Before(f.until):
		return true, f.until.Sub(now)
	default:
		return false, 0
	}
}

// noteFailure records a rejected resource and doubles its retry delay.
func (w *Worker) noteFailure(kind npm.Kind, key, hash, reason string, now time.Time) time.Duration {
	w.refMu.Lock()
	defer w.refMu.Unlock()

	id := backoffKey(kind, key)
	f, ok := w.failures[id]
	delay := backoffMin
	if ok && f.hash == hash {
		if delay = f.delay * 2; delay > backoffMax {
			delay = backoffMax
		}
	}
	w.failures[id] = failure{until: now.Add(delay), delay: delay, hash: hash, reason: reason}
	return delay
}

// noteSuccess clears the backoff of a resource.
func (w *Worker) noteSuccess(kind npm.Kind, key string) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	delete(w.failures, backoffKey(kind, key))
}

// warnUnsupported reports NPMplus-only settings once per resource when
// talking to upstream nginx-proxy-manager, where they are left out of the
// request instead of failing it.
func (w *Worker) warnUnsupported(log *slog.Logger, key string, t *docker.Target, resource npm.Resource) {
	if w.api.Flavour() != npm.FlavourNPM {
		return
	}
	// Only what the labels asked for is worth a warning. Everything else is a
	// default this tool chose, and a warning the operator cannot act on is
	// noise that hides the ones they can.
	unsupported := intersect(npm.UnsupportedByNPM(resource), t.ExplicitPlus)
	if len(unsupported) == 0 {
		return
	}

	w.refMu.Lock()
	id := backoffKey(resource.Kind(), key)
	_, warned := w.warned[id]
	w.warned[id] = struct{}{}
	w.refMu.Unlock()

	if warned {
		return
	}
	log.Warn("npmplus-only settings ignored by upstream nginx-proxy-manager",
		slog.String("key", key), slog.String("fields", strings.Join(unsupported, ",")))
}

// intersect returns the elements of a that also appear in b, order preserved.
func intersect(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	index := make(map[string]struct{}, len(b))
	for _, v := range b {
		index[v] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if _, ok := index[v]; ok {
			out = append(out, v)
		}
	}
	return out
}
