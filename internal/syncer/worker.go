package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/VentumPhoenix/npmplus-docker-sync/internal/certs"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/docker"
	"github.com/VentumPhoenix/npmplus-docker-sync/internal/npm"
)

// API is the NPM surface the worker needs. It is kind-agnostic, which is what
// lets one reconcile implementation serve proxy hosts, redirection hosts,
// streams and 404 hosts alike. Implemented by *npm.Client.
type API interface {
	List(ctx context.Context, kind npm.Kind) ([]npm.Resource, error)
	Create(ctx context.Context, resource npm.Resource) (npm.Resource, error)
	Update(ctx context.Context, id int, resource npm.Resource) (npm.Resource, error)
	Delete(ctx context.Context, kind npm.Kind, id int) error
	// SetEnabled toggles a resource through the dedicated /enable and
	// /disable endpoints. Neither API accepts `enabled` in a create or
	// update body.
	SetEnabled(ctx context.Context, kind npm.Kind, id int, enabled bool) error
	// ListCertificates backs the automatic certificate selection.
	ListCertificates(ctx context.Context) ([]npm.Certificate, error)
	// ListAccessLists resolves access lists referenced by name.
	ListAccessLists(ctx context.Context) ([]npm.AccessList, error)
	// Flavour reports the API dialect, which decides whether NPMplus-only
	// settings can be sent at all.
	Flavour() npm.Flavour
	// Recheck notices a server upgrade and re-probes the dialect if the
	// version changed. Called on the periodic resync.
	Recheck(ctx context.Context) error
}

// TargetSource provides the desired state derived from Docker.
type TargetSource interface {
	// Snapshot returns the desired resources plus what the parser learned
	// about the containers - above all which of them are protected from
	// deletion because their labels could not be read.
	Snapshot(ctx context.Context) (docker.Snapshot, error)
}

// StreamSource is implemented by a source that also streams Docker events. The
// readiness probe reports the stream: a silently disconnected listener only
// reacts on the periodic resync, which must not look healthy.
type StreamSource interface {
	StreamStatus() docker.StreamStatus
}

// Options configures the worker.
type Options struct {
	DeleteOrphans   bool
	AdoptExisting   bool
	DryRun          bool
	ResyncInterval  time.Duration
	ShutdownTimeout time.Duration
	// Kinds limits reconciliation to a subset of resource types. Empty means
	// all four.
	Kinds []npm.Kind
	// CertificatePartial decides what happens when no single certificate
	// covers every domain of a host.
	CertificatePartial certs.Partial
	// CertificateAutoCreate requests a Let's Encrypt certificate when the
	// automatic selection finds nothing.
	CertificateAutoCreate bool
	// MigrateFromRedth takes over hosts created by Redth/npm-docker-sync and
	// re-stamps them as ours.
	MigrateFromRedth bool
	// CertificatePoll is how often the certificate list is checked for
	// changes; a change triggers a reconcile so a certificate created in the
	// UI reaches its hosts without a restart. 0 disables the poll.
	CertificatePoll time.Duration
	// OnStop decides what happens to the resources of a container that is
	// stopped but still exists.
	OnStop docker.OnStop
	// StopGrace is how long a stopped container is ignored, so a restart or a
	// recreate does not produce a disable/enable cycle.
	StopGrace time.Duration
	// InstanceID identifies this sync instance. Resources carrying another
	// instance's id are never touched, which lets several Docker hosts drive
	// one NPM.
	InstanceID string
	// LabelPrefix is the namespace in use. A resource created from a
	// different prefix means the configuration changed under us, and a run
	// that would delete because of it is stopped.
	LabelPrefix string
	// DeleteGuard is the largest share of the managed resources a single run
	// may delete (0.5 = half). 0 disables the guard.
	DeleteGuard float64
	// DeleteGuardMin is how many deletions a run must reach before the guard
	// looks at the share at all, so small setups stay usable.
	DeleteGuardMin int
}

// Worker owns every write to the NPM API. Exactly one goroutine runs the
// reconcile loop, which keeps API calls sequential and avoids the SQLite
// "database is locked" errors NPM is prone to under concurrent writes.
type Worker struct {
	api   API
	src   TargetSource
	cache *Cache
	opts  Options
	log   *slog.Logger

	statusMu   sync.RWMutex
	lastRun    time.Time
	lastErr    error
	lastFailed int
	blocked    string

	// refMu guards everything that is remembered between runs: the
	// certificate and access list lookups, the per-resource backoff and the
	// "said it once" warnings.
	refMu        sync.RWMutex
	certificates []npm.Certificate
	certHash     string
	accessLists  map[string]int
	failures     map[string]failure
	warned       map[string]struct{}
	// stoppedSince remembers when a container was first seen stopped, which
	// is what the grace period is measured against.
	stoppedSince map[string]time.Time
	// metrics are the counters published by /metrics.
	metrics metrics
}

// NewWorker creates a worker.
func NewWorker(api API, src TargetSource, opts Options, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))
	}
	if opts.ShutdownTimeout <= 0 {
		opts.ShutdownTimeout = 30 * time.Second
	}
	if len(opts.Kinds) == 0 {
		opts.Kinds = npm.Kinds
	}
	if opts.CertificatePartial == "" {
		opts.CertificatePartial = certs.PartialPrimary
	}
	if opts.OnStop == "" {
		opts.OnStop = docker.OnStopDisable
	}
	return &Worker{
		api: api, src: src, cache: NewCache(), opts: opts, log: log,
		accessLists:  map[string]int{},
		failures:     map[string]failure{},
		warned:       map[string]struct{}{},
		stoppedSince: map[string]time.Time{},
	}
}

// Cache exposes the state cache (used by tests and the health endpoint).
func (w *Worker) Cache() *Cache { return w.cache }

// Status reports the outcome of the most recent reconcile run.
//
// Err is reserved for failures that make the whole run meaningless (Docker or
// the NPM API unreachable). A single rejected resource is counted in Failed
// instead: the other hosts are still in sync, so the process is not unhealthy.
type Status struct {
	LastRun time.Time
	Managed int
	Failed  int
	Err     error
	// Stream is the health of the Docker event subscription. A stream that
	// has been down for a while means changes are only noticed by the
	// periodic resync.
	Stream docker.StreamStatus
	// Blocked names the safety rule that stopped this run from deleting, if
	// one did.
	Blocked string
}

// StreamDownGrace is how long the Docker event stream may be disconnected
// before the readiness probe fails. Reconnects take seconds; minutes mean the
// daemon or the socket proxy is gone.
const StreamDownGrace = 2 * time.Minute

// Ready reports whether at least one reconcile run finished without an
// infrastructure failure and the event stream is healthy.
func (s Status) Ready() bool {
	switch {
	case s.LastRun.IsZero(), s.Err != nil:
		return false
	case s.Stream.Down(time.Now()) > StreamDownGrace:
		return false
	default:
		return true
	}
}

// Status returns the outcome of the most recent reconcile run. It is used by
// the readiness probe.
func (w *Worker) Status() Status {
	w.statusMu.RLock()
	status := Status{
		LastRun: w.lastRun,
		Managed: w.cache.Len(),
		Failed:  w.lastFailed,
		Err:     w.lastErr,
		Blocked: w.blocked,
	}
	w.statusMu.RUnlock()

	if source, ok := w.src.(StreamSource); ok {
		status.Stream = source.StreamStatus()
	}
	return status
}

func (w *Worker) setStatus(err error, failed int, blocked string) {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	w.lastRun, w.lastErr, w.lastFailed, w.blocked = time.Now(), err, failed, blocked
}

// Run processes reconcile triggers until ctx is cancelled. It performs an
// initial reconciliation on startup, a periodic full resync, and a final
// flush during graceful shutdown.
func (w *Worker) Run(ctx context.Context, triggers <-chan []docker.Event) error {
	res, err := w.Reconcile(ctx)
	if err != nil {
		// A failing initial sync must not kill the process: NPM may still be
		// starting up. The periodic resync retries.
		w.log.Error("initial reconciliation failed",
			slog.Any("result", res), slog.String("error", err.Error()))
	} else {
		w.log.Info("initial reconciliation complete", slog.Any("result", res))
	}

	var resync <-chan time.Time
	if w.opts.ResyncInterval > 0 {
		ticker := time.NewTicker(w.opts.ResyncInterval)
		defer ticker.Stop()
		resync = ticker.C
	}

	// Certificates are not a Docker event: a certificate issued or imported
	// in the NPM UI would otherwise only be picked up by the next resync.
	var certPoll <-chan time.Time
	if w.opts.CertificatePoll > 0 {
		ticker := time.NewTicker(w.opts.CertificatePoll)
		defer ticker.Stop()
		certPoll = ticker.C
	}

	for {
		select {
		case <-ctx.Done():
			return w.flush()

		case batch, ok := <-triggers:
			if !ok {
				return w.flush()
			}
			w.log.Debug("processing event batch",
				slog.Int("events", len(batch)),
				slog.String("containers", describe(batch)))
			res, err := w.Reconcile(ctx)
			w.report("reconciliation", res, err)

		case <-resync:
			// An NPM or NPMplus upgrade while this process runs changes which
			// request dialect is accepted; the resync is the natural place to
			// notice it.
			if err := w.api.Recheck(ctx); err != nil {
				w.log.Debug("could not check the npm server version", slog.String("error", err.Error()))
			}
			res, err := w.Reconcile(ctx)
			w.report("periodic resync", res, err)

		case <-certPoll:
			if !w.PollCertificates(ctx) {
				continue
			}
			w.log.Info("certificate list changed, reconciling")
			res, err := w.Reconcile(ctx)
			w.report("certificate resync", res, err)
		}
	}
}

// report logs the outcome of a run. A run that applied part of its changes and
// rejected the rest is neither "complete" nor "failed", and the old wording
// ("reconciliation failed") suggested nothing at all had happened - so the
// result counters always come along.
func (w *Worker) report(what string, res Result, err error) {
	switch {
	case err != nil:
		w.log.Error(what+" finished with errors",
			slog.Any("result", res), slog.String("error", err.Error()))
	case res.Changed():
		w.log.Info(what+" applied changes", slog.Any("result", res))
	}
}

// flush runs a final reconciliation with a detached context so pending work
// still reaches the API after SIGTERM.
func (w *Worker) flush() error {
	ctx, cancel := context.WithTimeout(context.Background(), w.opts.ShutdownTimeout)
	defer cancel()

	w.log.Info("flushing pending state before shutdown (orphan deletion skipped)")
	res, err := w.reconcile(ctx, false)
	if err != nil {
		return fmt.Errorf("final reconciliation: %w", err)
	}
	w.log.Info("shutdown flush complete", slog.Any("result", res))
	return nil
}

// Result summarises one reconcile run across all resource kinds.
type Result struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Deleted   int `json:"deleted"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
}

// Changed reports whether the run modified anything.
func (r Result) Changed() bool { return r.Created+r.Updated+r.Deleted+r.Failed > 0 }

// add accumulates another result.
func (r *Result) add(other Result) {
	r.Created += other.Created
	r.Updated += other.Updated
	r.Deleted += other.Deleted
	r.Unchanged += other.Unchanged
	r.Skipped += other.Skipped
	r.Failed += other.Failed
}

// LogValue implements slog.LogValuer.
func (r Result) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int("created", r.Created),
		slog.Int("updated", r.Updated),
		slog.Int("deleted", r.Deleted),
		slog.Int("unchanged", r.Unchanged),
		slog.Int("skipped", r.Skipped),
		slog.Int("failed", r.Failed),
	)
}

// Reconcile compares the desired Docker state with the live NPM state for
// every resource kind and applies the delta. Individual failures are
// collected so one broken resource cannot block the rest.
func (w *Worker) Reconcile(ctx context.Context) (Result, error) {
	started := time.Now()
	res, err := w.reconcile(ctx, w.opts.DeleteOrphans)
	w.record(res, err, time.Since(started))
	return res, err
}

// reconcile is Reconcile with an explicit orphan-deletion switch. The
// shutdown flush passes false: when a whole compose stack goes down, the
// labelled containers usually stop *before* this one, and a final run with
// deletion enabled would wipe every managed resource from NPM. Creating and
// updating still happens, only deletion is deferred to the next start.
func (w *Worker) reconcile(ctx context.Context, deleteOrphans bool) (Result, error) {
	var res Result

	snapshot, err := w.src.Snapshot(ctx)
	if err != nil {
		w.setStatus(err, 0, "")
		return res, err
	}
	w.trackStopped(snapshot.Targets)

	// The certificate list is fetched once per run: the automatic selection
	// of every host reads from the same snapshot, so one run cannot hand two
	// hosts contradictory answers.
	w.loadCertificates(ctx)
	w.loadAccessLists(ctx, needsAccessListNames(snapshot.Targets))

	byKind := make(map[npm.Kind][]*docker.Target, len(w.opts.Kinds))
	for _, t := range snapshot.Targets {
		byKind[t.Kind] = append(byKind[t.Kind], t)
	}

	var (
		fatal, partial []error
		orphans        []orphan
		owned          int
		foreignPrefix  int
	)
	for _, kind := range w.opts.Kinds {
		outcome := w.reconcileKind(ctx, kind, byKind[kind], snapshot)
		res.add(outcome.result)
		owned += outcome.owned
		foreignPrefix += outcome.foreignPrefix
		orphans = append(orphans, outcome.orphans...)
		if outcome.fatal != nil {
			fatal = append(fatal, outcome.fatal)
		}
		if outcome.partial != nil {
			partial = append(partial, outcome.partial)
		}
	}

	blocked := ""
	if len(orphans) > 0 {
		switch {
		case !deleteOrphans:
			res.Skipped += len(orphans)
			w.log.Debug("orphaned resources kept (deletion disabled for this run)",
				slog.Int("orphans", len(orphans)))
		default:
			blocked = w.guardDeletions(orphans, owned, foreignPrefix, snapshot)
			if blocked != "" {
				res.Skipped += len(orphans)
				w.countBlockedDeletion(len(orphans))
				w.log.Error("refusing to delete: "+blocked,
					slog.Int("would_delete", len(orphans)),
					slog.Int("managed", owned),
					slog.Int("containers", snapshot.Containers),
					slog.String("resources", describeOrphans(orphans)))
			} else {
				deleted, deleteErrs := w.deleteOrphans(ctx, orphans)
				res.Deleted += deleted
				res.Failed += len(deleteErrs)
				partial = append(partial, deleteErrs...)
			}
		}
	}

	// Only an unreachable collection marks the run itself as broken; a
	// resource NPM rejected is reported through Result.Failed so a single bad
	// label set cannot flip the readiness probe for everything else.
	w.setStatus(errors.Join(fatal...), res.Failed, blocked)
	return res, errors.Join(append(fatal, partial...)...)
}

// orphan is a managed resource whose definition disappeared.
type orphan struct {
	kind      npm.Kind
	key       string
	id        int
	container string
}

// kindOutcome is what reconciling one collection produced.
type kindOutcome struct {
	result  Result
	fatal   error
	partial error
	orphans []orphan
	// owned counts the live resources that belong to this instance.
	owned int
	// foreignPrefix counts owned resources created from another label
	// namespace, which means LABEL_PREFIX changed under us.
	foreignPrefix int
}

// trackStopped remembers since when a container has been down, so the grace
// period can tell a restart from a shutdown.
func (w *Worker) trackStopped(targets []*docker.Target) {
	now := time.Now()
	seen := make(map[string]bool, len(targets))

	w.refMu.Lock()
	defer w.refMu.Unlock()
	for _, t := range targets {
		id := containerKey(t)
		if t.Running {
			delete(w.stoppedSince, id)
			seen[id] = true
			continue
		}
		seen[id] = true
		if _, known := w.stoppedSince[id]; !known {
			w.stoppedSince[id] = now
		}
	}
	for id := range w.stoppedSince {
		if !seen[id] {
			delete(w.stoppedSince, id)
		}
	}
}

// inGrace reports whether a stopped container is still within NPM_STOP_GRACE,
// during which nothing about its resources is changed at all. A restart or a
// `docker compose up` recreate finishes inside that window, so it produces no
// disable/enable churn.
func (w *Worker) inGrace(t *docker.Target, now time.Time) bool {
	if t.Running || w.opts.StopGrace <= 0 {
		return false
	}
	w.refMu.RLock()
	since, known := w.stoppedSince[containerKey(t)]
	w.refMu.RUnlock()
	if !known {
		return true // first sighting: give it the benefit of the doubt
	}
	return now.Sub(since) < w.opts.StopGrace
}

func containerKey(t *docker.Target) string {
	if t.ContainerID != "" {
		return t.ContainerID
	}
	return t.ContainerName
}

// reconcileKind reconciles a single NPM collection. Deletions are not
// performed here: they are collected and decided for the whole run, so a
// safety guard can look at the total.
func (w *Worker) reconcileKind(ctx context.Context, kind npm.Kind, targets []*docker.Target, snapshot docker.Snapshot) kindOutcome {
	var out kindOutcome
	log := w.log.With(slog.String("kind", string(kind)))

	live, err := w.api.List(ctx, kind)
	if err != nil {
		if npm.IsNotFound(err) {
			// Some NPM forks do not expose every collection. Skip it instead
			// of failing the whole run, and keep the cached state untouched.
			log.Warn("collection is not available on this npm instance, skipping",
				slog.String("path", kind.Path()))
			return out
		}
		out.fatal = fmt.Errorf("list %s: %w", kind.Label(), err)
		return out
	}

	existing := indexResources(live)
	byDomain := indexDomains(live)
	for _, r := range live {
		if !w.owns(r) {
			continue
		}
		out.owned++
		if prefix := r.ResourceMeta().Prefix(); prefix != "" && w.opts.LabelPrefix != "" && prefix != w.opts.LabelPrefix {
			out.foreignPrefix++
		}
	}

	desired, conflicts := indexTargets(w.applyStopPolicy(log, targets))
	for _, c := range conflicts {
		out.result.Skipped++
		log.Warn("duplicate definition ignored",
			slog.String("key", c.key),
			slog.String("container", c.container),
			slog.Int("index", c.index))
	}

	var errs []error
	next := make(map[string]Entry, len(desired))
	now := time.Now()

	for _, key := range sortedKeys(desired) {
		target := desired[key]
		current, found := existing[key]

		// A resource created by another tool - or by another instance of this
		// one - is never touched, whoever the other party is.
		if found {
			skip, reason := w.ownership(current)
			if skip {
				out.result.Skipped++
				log.Warn("resource is not ours, skipping",
					slog.String("key", key), slog.Int("id", current.ResourceID()),
					slog.String("reason", reason))
				continue
			}
			if reason != "" {
				log.Info("adopting resource", slog.String("key", key),
					slog.Int("id", current.ResourceID()), slog.String("from", reason))
			}
		}

		// A stopped container keeps its resource exactly as it is; only the
		// enabled flag follows the container. Nothing else may change,
		// because Docker no longer reports the address or the ports.
		frozen := !target.Running
		if frozen && w.inGrace(target, now) {
			out.result.Unchanged++
			if found {
				next[key] = Entry{
					ID: current.ResourceID(), Hash: current.Fingerprint(),
					Container: target.ContainerName, Index: target.Index,
					Enabled:     current.IsEnabled(),
					Certificate: current.ResourceCertificate().ID,
					Running:     target.Running,
				}
			}
			log.Debug("container recently stopped, waiting out the grace period",
				slog.String("key", key), slog.String("container", target.ContainerName))
			continue
		}

		// A domain another host already serves would make NPM reject the
		// write with "domain already in use"; say so in our own words and
		// leave the foreign host alone.
		if owner, domain := foreignDomain(byDomain, target, current, w.owns); owner != nil {
			out.result.Skipped++
			log.Warn("domain already used by another host, skipping",
				slog.String("key", key), slog.String("domain", domain),
				slog.Int("conflicting_id", owner.ResourceID()),
				slog.String("owner", ownerName(owner)))
			continue
		}

		if err := w.resolveAccessLists(target); err != nil {
			out.result.Failed++
			errs = append(errs, fmt.Errorf("%s: %w", target.Describe(), err))
			log.Error("cannot resolve access lists, skipping",
				slog.String("key", key), slog.String("error", err.Error()))
			continue
		}

		var liveResource npm.Resource
		if found {
			liveResource = current
		}
		certificate, err := w.resolveCertificate(log, target, liveResource)
		if err != nil {
			out.result.Failed++
			errs = append(errs, err)
			log.Error("cannot resolve the certificate, skipping",
				slog.String("key", key), slog.String("error", err.Error()))
			continue
		}

		resource := BuildResource(target, certificate, w.opts.InstanceID, w.opts.LabelPrefix)
		if resource == nil {
			out.result.Failed++
			errs = append(errs, fmt.Errorf("%s: unsupported resource kind %q", target.Describe(), target.Kind))
			continue
		}
		w.warnUnsupported(log, key, target, resource)
		if w.api.Flavour() == npm.FlavourNPM {
			// Upstream nginx-proxy-manager neither stores nor returns the
			// NPMplus fields, so they must not take part in the comparison
			// either - otherwise every run would see a difference.
			npm.StripNPMplus(resource)
		}
		if found {
			// Carry over values NPM assigned (issued certificates, the meta
			// of a migrated host, ...) so the desired state can converge.
			resource.AdoptServerState(current)
		}
		hash := resource.Fingerprint()

		// A resource the API keeps rejecting is retried with a growing delay
		// instead of on every event.
		if waiting, retryIn := w.deferred(kind, key, hash, now); waiting {
			out.result.Skipped++
			log.Debug("resource is in backoff after a failure, skipping",
				slog.String("key", key), slog.Duration("retry_in", retryIn))
			continue
		}

		if !found {
			if frozen && !target.Complete() {
				// A stopped container that never had a host: there is nothing
				// to disable and no address to create one with.
				out.result.Skipped++
				log.Debug("container is stopped and has no resource yet, skipping",
					slog.String("key", key), slog.String("container", target.ContainerName))
				continue
			}
			entry, created, err := w.create(ctx, log, target, resource)
			switch {
			case err != nil:
				out.result.Failed++
				errs = append(errs, err)
				w.backOff(log, kind, key, hash, err, now)
			case created:
				out.result.Created++
				w.noteSuccess(kind, key)
				if entry != nil {
					next[key] = *entry
				}
			}
			continue
		}

		id := current.ResourceID()

		// The configuration is in sync when the live resource hashes the same
		// or when we last wrote exactly this hash - the server normalises some
		// fields on the way in, so an echo that differs is not a drift.
		inSync := current.Fingerprint() == hash
		if !inSync {
			if cached, ok := w.cache.Get(kind, key); ok && cached.ID == id && cached.Hash == hash {
				inSync = true
			}
		}
		if frozen {
			// Keep whatever is stored; only the enabled flag follows.
			inSync = true
			hash = current.Fingerprint()
		}

		enabled := current.IsEnabled()
		changed := false

		if !inSync {
			w.reportDrift(log, kind, key, id, current, resource)
			if w.opts.DryRun {
				log.Info("[dry-run] would update resource",
					slog.String("key", key), slog.Int("id", id),
					slog.String("config", resource.Describe()),
					slog.String("diff", npm.Diff(current, resource)))
				out.result.Updated++
				continue
			}
			if _, err := w.api.Update(ctx, id, resource); err != nil {
				out.result.Failed++
				errs = append(errs, fmt.Errorf("update %s %s: %w", kind.Label(), key, err))
				log.Error("failed to update resource", slog.String("key", key), slog.String("error", err.Error()))
				w.backOff(log, kind, key, hash, err, now)
				continue
			}
			changed = true
			log.Info("updated resource",
				slog.String("key", key), slog.Int("id", id),
				slog.String("container", target.ContainerName),
				slog.Int("index", target.Index),
				slog.String("config", resource.Describe()))
		}

		// The enabled flag lives outside the write payload, so it is
		// reconciled on its own. Without this step `enabled: false` would be
		// silently ignored forever.
		desiredEnabled := resource.IsEnabled()
		if frozen {
			switch w.opts.OnStop {
			case docker.OnStopDisable:
				desiredEnabled = false
			case docker.OnStopKeep:
				desiredEnabled = enabled
			case docker.OnStopDelete:
				desiredEnabled = enabled // handled by applyStopPolicy
			}
		}
		toggled, err := w.applyEnabled(ctx, log, kind, key, id, desiredEnabled, enabled)
		if err != nil {
			out.result.Failed++
			errs = append(errs, err)
			continue
		}
		if toggled {
			enabled = desiredEnabled
			changed = true
		}

		w.noteSuccess(kind, key)
		switch {
		case changed:
			out.result.Updated++
		default:
			out.result.Unchanged++
		}
		next[key] = Entry{
			ID: id, Hash: hash, Container: target.ContainerName, Index: target.Index,
			Enabled: enabled, Certificate: resource.ResourceCertificate().ID, Running: target.Running,
		}
	}

	// Orphans: resources we created whose definition is gone. Resources
	// without our ownership marker are never touched, and neither are those of
	// a container whose labels we could not read this run.
	for _, key := range sortedKeys(existing) {
		resource := existing[key]
		if _, wanted := desired[key]; wanted {
			continue
		}
		if !w.owns(resource) {
			continue
		}
		meta := resource.ResourceMeta()
		if snapshot.IsProtected(meta.Container(), meta.ContainerID()) {
			out.result.Skipped++
			log.Warn("keeping the resource of a container whose labels could not be read",
				slog.String("key", key), slog.Int("id", resource.ResourceID()),
				slog.String("container", meta.Container()))
			continue
		}
		if w.opts.DryRun {
			out.result.Deleted++
			log.Info("[dry-run] would delete orphaned resource",
				slog.String("key", key), slog.Int("id", resource.ResourceID()))
			continue
		}
		out.orphans = append(out.orphans, orphan{
			kind: kind, key: key, id: resource.ResourceID(), container: meta.Container(),
		})
	}

	w.cache.ReplaceKind(kind, next)
	out.partial = errors.Join(errs...)
	return out
}

// applyStopPolicy drops the targets of stopped containers when NPM_ON_STOP is
// "delete", so their resources become orphans like a removed container's.
func (w *Worker) applyStopPolicy(log *slog.Logger, targets []*docker.Target) []*docker.Target {
	if w.opts.OnStop != docker.OnStopDelete {
		return targets
	}
	now := time.Now()
	out := make([]*docker.Target, 0, len(targets))
	for _, t := range targets {
		if t.Running || w.inGrace(t, now) {
			out = append(out, t)
			continue
		}
		log.Info("container is stopped, its resource is treated as an orphan (NPM_ON_STOP=delete)",
			slog.String("container", t.ContainerName), slog.String("key", t.Key()))
	}
	return out
}

// deleteOrphans removes the resources collected during the run.
func (w *Worker) deleteOrphans(ctx context.Context, orphans []orphan) (int, []error) {
	var (
		deleted int
		errs    []error
	)
	for _, o := range orphans {
		if err := w.api.Delete(ctx, o.kind, o.id); err != nil && !npm.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("delete %s %s: %w", o.kind.Label(), o.key, err))
			w.log.Error("failed to delete resource",
				slog.String("kind", string(o.kind)), slog.String("key", o.key),
				slog.String("error", err.Error()))
			continue
		}
		deleted++
		w.log.Info("deleted orphaned resource",
			slog.String("kind", string(o.kind)), slog.String("key", o.key),
			slog.Int("id", o.id), slog.String("container", o.container))
	}
	return deleted, errs
}

// guardDeletions is the last line of defence against a run that wipes NPM.
//
// A changed LABEL_PREFIX, NPM_EXPOSED_BY_DEFAULT=false, a socket proxy that
// answers with an empty container list - each of them turns every managed
// resource into an orphan at once. None of those is a state a reconciler
// should act on, so a run that would delete an implausible share of the
// managed resources is stopped and reported instead.
//
// It returns the reason deletion was refused, or "" when the run may proceed.
func (w *Worker) guardDeletions(orphans []orphan, owned, foreignPrefix int, snapshot docker.Snapshot) string {
	if foreignPrefix > 0 {
		return fmt.Sprintf("%d managed resources were created with a different label prefix "+
			"(LABEL_PREFIX is now %q); fix the prefix or remove those resources by hand",
			foreignPrefix, w.opts.LabelPrefix)
	}
	if w.opts.DeleteGuard <= 0 {
		return ""
	}

	deletions := len(orphans)
	minimum := w.opts.DeleteGuardMin
	if minimum <= 0 {
		minimum = DefaultDeleteGuardMin
	}
	if deletions < minimum {
		return ""
	}
	if snapshot.Containers == 0 {
		return "docker reported no containers at all, which is never a reason to delete"
	}
	if owned > 0 && deletions >= owned {
		return fmt.Sprintf("this would delete every managed resource (%d)", deletions)
	}
	if owned > 0 {
		share := float64(deletions) / float64(owned)
		if share > w.opts.DeleteGuard+math.SmallestNonzeroFloat64 {
			return fmt.Sprintf("this would delete %.0f%% of the managed resources (%d of %d), "+
				"more than DELETE_GUARD allows (%.0f%%)",
				share*100, deletions, owned, w.opts.DeleteGuard*100)
		}
	}
	return ""
}

// DefaultDeleteGuardMin is how many deletions a run needs before the share
// guard applies. Below it, deleting everything is a two-host homelab doing
// exactly what it was told.
const DefaultDeleteGuardMin = 3

// describeOrphans renders the resources a blocked run would have deleted.
func describeOrphans(orphans []orphan) string {
	parts := make([]string, 0, len(orphans))
	for _, o := range orphans {
		parts = append(parts, fmt.Sprintf("%s/%s#%d", o.kind, o.key, o.id))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// reportDrift says out loud when a managed resource was changed outside this
// tool and is about to be overwritten - otherwise an edit in the NPM UI simply
// disappears on the next event.
func (w *Worker) reportDrift(log *slog.Logger, kind npm.Kind, key string, id int, current, desired npm.Resource) {
	cached, ok := w.cache.Get(kind, key)
	if !ok || cached.ID != id || cached.Hash == current.Fingerprint() {
		return
	}
	log.Warn("managed resource was changed outside this tool, overwriting",
		slog.String("key", key), slog.Int("id", id),
		slog.String("diff", npm.Diff(current, desired)))
}

// owns reports whether a live resource carries our ownership marker - or, with
// MIGRATE_FROM_REDTH, the marker of Redth/npm-docker-sync.
func (w *Worker) owns(r npm.Resource) bool {
	meta := r.ResourceMeta()
	if npm.IsManaged(meta) {
		// A resource stamped with another instance's id belongs to that
		// instance: several sync containers can drive one NPM, and each of
		// them would otherwise see the others' hosts as orphans.
		return npm.OwnedByInstance(meta, w.opts.InstanceID)
	}
	return w.opts.MigrateFromRedth && npm.IsRedth(meta)
}

// ownership decides what to do with an existing resource: skip it (with a
// reason), adopt it (with the previous owner) or just use it.
func (w *Worker) ownership(r npm.Resource) (skip bool, reason string) {
	meta := r.ResourceMeta()
	switch {
	case npm.IsManaged(meta) && !npm.OwnedByInstance(meta, w.opts.InstanceID):
		return true, "managed by sync instance " + meta.Instance()
	case npm.IsManaged(meta) && meta.Instance() == "" && w.opts.InstanceID != "":
		return false, "an earlier release (adopting it for instance " + w.opts.InstanceID + ")"
	case npm.IsManaged(meta):
		return false, ""
	case npm.IsRedth(meta):
		if w.opts.MigrateFromRedth {
			return false, npm.RedthManagedByValue
		}
		return true, "managed by " + npm.RedthManagedByValue + " (set MIGRATE_FROM_REDTH=true to take it over)"
	case npm.Owner(meta) != "":
		return true, "managed by " + npm.Owner(meta)
	case !w.opts.AdoptExisting:
		return true, "created by hand (ADOPT_EXISTING=false)"
	default:
		return false, "an unmanaged host"
	}
}

// backOff records a failure and logs the delay before the next attempt.
func (w *Worker) backOff(log *slog.Logger, kind npm.Kind, key, hash string, err error, now time.Time) {
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	delay := w.noteFailure(kind, key, hash, reason, now)
	log.Debug("backing off after a failure",
		slog.String("key", key), slog.Duration("retry_in", delay))
}

// ownerName renders the ownership marker of a foreign resource.
func ownerName(r npm.Resource) string {
	if owner := npm.Owner(r.ResourceMeta()); owner != "" {
		return owner
	}
	return "unmanaged"
}

// foreignDomain returns the resource that already serves one of the target's
// domains, if it is not ours and not the resource we are about to update.
func foreignDomain(byDomain map[string]npm.Resource, t *docker.Target, current npm.Resource, owned func(npm.Resource) bool) (npm.Resource, string) {
	for _, domain := range t.Domains() {
		other, ok := byDomain[domain]
		if !ok || owned(other) {
			continue
		}
		if current != nil && other.ResourceID() == current.ResourceID() {
			continue
		}
		return other, domain
	}
	return nil, ""
}

// indexDomains maps every domain of every live resource to its owner, so an
// overlap with a foreign host is caught before the API rejects the write.
func indexDomains(resources []npm.Resource) map[string]npm.Resource {
	out := make(map[string]npm.Resource, len(resources))
	for _, r := range resources {
		host, ok := r.(interface{ Domains() []string })
		if !ok {
			continue
		}
		for _, domain := range host.Domains() {
			if existing, dup := out[domain]; dup && npm.IsManaged(existing.ResourceMeta()) {
				continue
			}
			out[domain] = r
		}
	}
	return out
}

// applyEnabled brings the live enabled state in line with the desired one and
// reports whether it had to act.
func (w *Worker) applyEnabled(ctx context.Context, log *slog.Logger, kind npm.Kind, key string, id int, desired, live bool) (bool, error) {
	if desired == live || id == 0 {
		return false, nil
	}
	verb, state := "disable", "disabled"
	if desired {
		verb, state = "enable", "enabled"
	}
	if w.opts.DryRun {
		log.Info("[dry-run] would toggle resource",
			slog.String("key", key), slog.Int("id", id), slog.String("to", state))
		return true, nil
	}
	if err := w.api.SetEnabled(ctx, kind, id, desired); err != nil {
		log.Error("failed to toggle resource",
			slog.String("key", key), slog.Int("id", id),
			slog.String("to", state), slog.String("error", err.Error()))
		return false, fmt.Errorf("%s %s %s: %w", verb, kind.Label(), key, err)
	}
	log.Info("toggled resource",
		slog.String("key", key), slog.Int("id", id), slog.String("to", state))
	return true, nil
}

// create adds a missing resource. It returns the cache entry to store, or nil
// in dry-run mode.
func (w *Worker) create(ctx context.Context, log *slog.Logger, target *docker.Target, resource npm.Resource) (*Entry, bool, error) {
	if w.opts.DryRun {
		log.Info("[dry-run] would create resource",
			slog.String("key", target.Key()),
			slog.String("config", resource.Describe()))
		return nil, true, nil
	}

	created, err := w.api.Create(ctx, resource)
	if err != nil {
		log.Error("failed to create resource",
			slog.String("key", target.Key()), slog.String("error", err.Error()))
		return nil, false, fmt.Errorf("create %s %s: %w", target.Kind.Label(), target.Key(), err)
	}

	// Both flavours create a resource in the enabled state, so an explicit
	// `enabled: false` needs a follow-up call.
	id := created.ResourceID()
	enabled := created.IsEnabled()
	toggled, err := w.applyEnabled(ctx, log, target.Kind, target.Key(), id, resource.IsEnabled(), enabled)
	if err != nil {
		return nil, false, err
	}
	if toggled {
		enabled = resource.IsEnabled()
	}

	entry := Entry{
		ID:          id,
		Hash:        resource.Fingerprint(),
		Container:   target.ContainerName,
		Index:       target.Index,
		Enabled:     enabled,
		Certificate: resource.ResourceCertificate().ID,
		Running:     target.Running,
	}
	log.Info("created resource",
		slog.String("key", target.Key()), slog.Int("id", id),
		slog.String("container", target.ContainerName),
		slog.Int("index", target.Index),
		slog.String("config", resource.Describe()))
	return &entry, true, nil
}

type conflict struct {
	key       string
	container string
	index     int
}

// indexTargets keys targets by their identity and reports definitions
// fighting over the same key (first one wins, deterministically).
func indexTargets(targets []*docker.Target) (map[string]*docker.Target, []conflict) {
	sorted := make([]*docker.Target, len(targets))
	copy(sorted, targets)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Key() != sorted[j].Key() {
			return sorted[i].Key() < sorted[j].Key()
		}
		if sorted[i].ContainerName != sorted[j].ContainerName {
			return sorted[i].ContainerName < sorted[j].ContainerName
		}
		return sorted[i].Index < sorted[j].Index
	})

	out := make(map[string]*docker.Target, len(sorted))
	var conflicts []conflict
	for _, t := range sorted {
		key := t.Key()
		if key == "" {
			continue
		}
		if _, dup := out[key]; dup {
			conflicts = append(conflicts, conflict{key: key, container: t.ContainerName, index: t.Index})
			continue
		}
		out[key] = t
	}
	return out, conflicts
}

// indexResources keys live NPM resources by their identity.
func indexResources(resources []npm.Resource) map[string]npm.Resource {
	out := make(map[string]npm.Resource, len(resources))
	for _, r := range resources {
		key := r.ResourceKey()
		if key == "" {
			continue
		}
		// Prefer the resource we manage if NPM holds duplicates.
		if prev, dup := out[key]; dup && npm.IsManaged(prev.ResourceMeta()) {
			continue
		}
		out[key] = r
	}
	return out
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func describe(batch []docker.Event) string {
	seen := make(map[string]struct{}, len(batch))
	names := make([]string, 0, len(batch))
	for _, e := range batch {
		name := e.Name
		if name == "" {
			name = e.ContainerID
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
