package syncer

import (
	"sort"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// Metrics is what the /metrics endpoint publishes.
//
// Counters are cumulative for the lifetime of the process; gauges describe the
// current state. Everything is a plain value, so the HTTP handler stays a
// formatting exercise and this package keeps its "no dependencies" property.
type Metrics struct {
	Runs             uint64
	RunErrors        uint64
	Created          uint64
	Updated          uint64
	Deleted          uint64
	Failed           uint64
	Skipped          uint64
	DeletionsBlocked uint64
	// CertificateClass counts how often a certificate was chosen by match
	// class ("exact", "wildcard", ...).
	CertificateClass map[string]uint64
	// Managed is the number of resources currently under management.
	Managed int
	// ManagedByKind breaks that down per collection.
	ManagedByKind map[npm.Kind]int
	// LastRun is when the most recent reconcile finished.
	LastRun time.Time
	// LastDuration is how long it took.
	LastDuration time.Duration
}

// metrics is the mutable counter set owned by the worker.
type metrics struct {
	runs             uint64
	runErrors        uint64
	created          uint64
	updated          uint64
	deleted          uint64
	failed           uint64
	skipped          uint64
	deletionsBlocked uint64
	certificateClass map[string]uint64
	lastDuration     time.Duration
}

// record folds the outcome of one run into the counters.
func (w *Worker) record(res Result, err error, took time.Duration) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	w.metrics.runs++
	if err != nil {
		w.metrics.runErrors++
	}
	w.metrics.created += uint64(res.Created) //nolint:gosec // counters are never negative
	w.metrics.updated += uint64(res.Updated) //nolint:gosec
	w.metrics.deleted += uint64(res.Deleted) //nolint:gosec
	w.metrics.failed += uint64(res.Failed)   //nolint:gosec
	w.metrics.skipped += uint64(res.Skipped) //nolint:gosec
	w.metrics.lastDuration = took
}

// countCertificate records which match class won for one host.
func (w *Worker) countCertificate(class string) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	if w.metrics.certificateClass == nil {
		w.metrics.certificateClass = map[string]uint64{}
	}
	w.metrics.certificateClass[class]++
}

// countBlockedDeletion records a run the delete guard stopped.
func (w *Worker) countBlockedDeletion(n int) {
	w.refMu.Lock()
	defer w.refMu.Unlock()
	w.metrics.deletionsBlocked += uint64(n) //nolint:gosec // counters are never negative
}

// Metrics returns a snapshot of the counters.
func (w *Worker) Metrics() Metrics {
	w.refMu.RLock()
	classes := make(map[string]uint64, len(w.metrics.certificateClass))
	for class, count := range w.metrics.certificateClass {
		classes[class] = count
	}
	out := Metrics{
		Runs:             w.metrics.runs,
		RunErrors:        w.metrics.runErrors,
		Created:          w.metrics.created,
		Updated:          w.metrics.updated,
		Deleted:          w.metrics.deleted,
		Failed:           w.metrics.failed,
		Skipped:          w.metrics.skipped,
		DeletionsBlocked: w.metrics.deletionsBlocked,
		CertificateClass: classes,
		LastDuration:     w.metrics.lastDuration,
	}
	w.refMu.RUnlock()

	out.Managed = w.cache.Len()
	out.ManagedByKind = w.cache.Counts()
	out.LastRun = w.Status().LastRun
	return out
}

// ResourceStatus describes one managed resource for the /status endpoint.
type ResourceStatus struct {
	Kind        npm.Kind `json:"kind"`
	Key         string   `json:"key"`
	ID          int      `json:"id"`
	Container   string   `json:"container"`
	Index       int      `json:"index"`
	Enabled     bool     `json:"enabled"`
	Running     bool     `json:"container_running"`
	Certificate int      `json:"certificate_id"`
	// LastError is why the resource was last rejected, if it was.
	LastError string `json:"last_error,omitempty"`
	// RetryAfter is when the backoff of a failed resource expires.
	RetryAfter *time.Time `json:"retry_after,omitempty"`
}

// Resources lists every managed resource with what is known about it. It backs
// the /status endpoint, which is what a dashboard or an uptime check reads.
func (w *Worker) Resources() []ResourceStatus {
	snapshot := w.cache.Snapshot()

	w.refMu.RLock()
	failures := make(map[string]failure, len(w.failures))
	for key, f := range w.failures {
		failures[key] = f
	}
	w.refMu.RUnlock()

	var out []ResourceStatus
	for _, kind := range npm.Kinds {
		for key, entry := range snapshot[kind] {
			status := ResourceStatus{
				Kind: kind, Key: key, ID: entry.ID,
				Container: entry.Container, Index: entry.Index,
				Enabled: entry.Enabled, Running: entry.Running,
				Certificate: entry.Certificate,
			}
			if f, ok := failures[backoffKey(kind, key)]; ok {
				retry := f.until
				status.RetryAfter = &retry
				status.LastError = f.reason
			}
			out = append(out, status)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}
