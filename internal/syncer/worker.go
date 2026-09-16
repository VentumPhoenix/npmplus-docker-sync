package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

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
}

// TargetSource provides the desired state derived from Docker.
type TargetSource interface {
	Targets(ctx context.Context) ([]*docker.Target, error)
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
	return &Worker{api: api, src: src, cache: NewCache(), opts: opts, log: log}
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
}

// Ready reports whether at least one reconcile run finished without an
// infrastructure failure.
func (s Status) Ready() bool { return !s.LastRun.IsZero() && s.Err == nil }

// Status returns the outcome of the most recent reconcile run. It is used by
// the readiness probe.
func (w *Worker) Status() Status {
	w.statusMu.RLock()
	defer w.statusMu.RUnlock()
	return Status{
		LastRun: w.lastRun,
		Managed: w.cache.Len(),
		Failed:  w.lastFailed,
		Err:     w.lastErr,
	}
}

func (w *Worker) setStatus(err error, failed int) {
	w.statusMu.Lock()
	defer w.statusMu.Unlock()
	w.lastRun, w.lastErr, w.lastFailed = time.Now(), err, failed
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
			res, err := w.Reconcile(ctx)
			w.report("periodic resync", res, err)
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
	return w.reconcile(ctx, w.opts.DeleteOrphans)
}

// reconcile is Reconcile with an explicit orphan-deletion switch. The
// shutdown flush passes false: when a whole compose stack goes down, the
// labelled containers usually stop *before* this one, and a final run with
// deletion enabled would wipe every managed resource from NPM. Creating and
// updating still happens, only deletion is deferred to the next start.
func (w *Worker) reconcile(ctx context.Context, deleteOrphans bool) (Result, error) {
	var res Result

	targets, err := w.src.Targets(ctx)
	if err != nil {
		w.setStatus(err, 0)
		return res, err
	}

	byKind := make(map[npm.Kind][]*docker.Target, len(w.opts.Kinds))
	for _, t := range targets {
		byKind[t.Kind] = append(byKind[t.Kind], t)
	}

	var fatal, partial []error
	for _, kind := range w.opts.Kinds {
		kindResult, kindFatal, kindPartial := w.reconcileKind(ctx, kind, byKind[kind], deleteOrphans)
		res.add(kindResult)
		if kindFatal != nil {
			fatal = append(fatal, kindFatal)
		}
		if kindPartial != nil {
			partial = append(partial, kindPartial)
		}
	}

	// Only an unreachable collection marks the run itself as broken; a
	// resource NPM rejected is reported through Result.Failed so a single bad
	// label set cannot flip the readiness probe for everything else.
	w.setStatus(errors.Join(fatal...), res.Failed)
	return res, errors.Join(append(fatal, partial...)...)
}

// reconcileKind reconciles a single NPM collection. It returns the run result,
// a fatal error (the collection could not be listed at all) and the joined
// per-resource errors.
func (w *Worker) reconcileKind(ctx context.Context, kind npm.Kind, targets []*docker.Target, deleteOrphans bool) (Result, error, error) { //nolint:revive // three distinct outcomes
	var res Result
	log := w.log.With(slog.String("kind", string(kind)))

	live, err := w.api.List(ctx, kind)
	if err != nil {
		if npm.IsNotFound(err) {
			// Some NPM forks do not expose every collection. Skip it instead
			// of failing the whole run, and keep the cached state untouched.
			log.Warn("collection is not available on this npm instance, skipping",
				slog.String("path", kind.Path()))
			return res, nil, nil
		}
		return res, fmt.Errorf("list %s: %w", kind.Label(), err), nil
	}

	existing := indexResources(live)
	desired, conflicts := indexTargets(targets)
	for _, c := range conflicts {
		res.Skipped++
		log.Warn("duplicate definition ignored",
			slog.String("key", c.key),
			slog.String("container", c.container),
			slog.Int("index", c.index))
	}

	var errs []error
	next := make(map[string]Entry, len(desired))

	for _, key := range sortedKeys(desired) {
		target := desired[key]
		resource := BuildResource(target)
		if resource == nil {
			res.Failed++
			errs = append(errs, fmt.Errorf("%s: unsupported resource kind %q", target.Describe(), target.Kind))
			continue
		}

		current, found := existing[key]
		if !found {
			entry, created, err := w.create(ctx, log, target, resource)
			switch {
			case err != nil:
				res.Failed++
				errs = append(errs, err)
			case created:
				res.Created++
				if entry != nil {
					next[key] = *entry
				}
			}
			continue
		}

		if !npm.IsManaged(current.ResourceMeta()) && !w.opts.AdoptExisting {
			res.Skipped++
			log.Warn("resource exists but is not managed by this tool, skipping",
				slog.String("key", key), slog.Int("id", current.ResourceID()))
			continue
		}

		// Carry over values NPM assigned (issued certificates, ...) so the
		// desired state can converge.
		resource.AdoptServerState(current)
		hash := resource.Fingerprint()
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

		enabled := current.IsEnabled()
		changed := false

		if !inSync {
			if w.opts.DryRun {
				log.Info("[dry-run] would update resource",
					slog.String("key", key), slog.Int("id", id),
					slog.String("config", resource.Describe()))
				res.Updated++
				continue
			}
			if _, err := w.api.Update(ctx, id, resource); err != nil {
				res.Failed++
				errs = append(errs, fmt.Errorf("update %s %s: %w", kind.Label(), key, err))
				log.Error("failed to update resource", slog.String("key", key), slog.String("error", err.Error()))
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
		toggled, err := w.applyEnabled(ctx, log, kind, key, id, resource.IsEnabled(), enabled)
		if err != nil {
			res.Failed++
			errs = append(errs, err)
			continue
		}
		if toggled {
			enabled = resource.IsEnabled()
			changed = true
		}

		switch {
		case changed:
			res.Updated++
		default:
			res.Unchanged++
		}
		next[key] = Entry{ID: id, Hash: hash, Container: target.ContainerName, Index: target.Index, Enabled: enabled}
	}

	// Orphans: resources we created whose definition is gone. Resources
	// without our ownership marker are never touched.
	for _, key := range sortedKeys(existing) {
		resource := existing[key]
		if _, wanted := desired[key]; wanted {
			continue
		}
		if !npm.IsManaged(resource.ResourceMeta()) {
			continue
		}
		if !deleteOrphans {
			res.Skipped++
			log.Debug("orphaned resource kept (deletion disabled for this run)",
				slog.String("key", key), slog.Int("id", resource.ResourceID()))
			continue
		}
		if w.opts.DryRun {
			res.Deleted++
			log.Info("[dry-run] would delete orphaned resource",
				slog.String("key", key), slog.Int("id", resource.ResourceID()))
			continue
		}
		if err := w.api.Delete(ctx, kind, resource.ResourceID()); err != nil && !npm.IsNotFound(err) {
			res.Failed++
			errs = append(errs, fmt.Errorf("delete %s %s: %w", kind.Label(), key, err))
			log.Error("failed to delete resource", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		res.Deleted++
		log.Info("deleted orphaned resource",
			slog.String("key", key), slog.Int("id", resource.ResourceID()),
			slog.String("container", resource.ResourceMeta().Container()))
	}

	w.cache.ReplaceKind(kind, next)
	return res, nil, errors.Join(errs...)
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
		ID:        id,
		Hash:      resource.Fingerprint(),
		Container: target.ContainerName,
		Index:     target.Index,
		Enabled:   enabled,
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
