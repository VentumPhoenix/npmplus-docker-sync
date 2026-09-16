# Architecture

`npmplus-docker-sync` is a reconciler. It continuously drives one system (Nginx
Proxy Manager / NPMplus) towards the desired state expressed in another
(Docker labels). Everything else — events, debouncing, caching — exists to make
that cheap and safe.

Four NPM collections are managed, and the reconcile logic is shared by all of
them through the `npm.Resource` interface:

| Kind | API path | Identity |
|---|---|---|
| `proxy` | `/api/nginx/proxy-hosts` | first domain name |
| `redirect` | `/api/nginx/redirection-hosts` | first domain name |
| `stream` | `/api/nginx/streams` | incoming port |
| `dead` (404) | `/api/nginx/dead-hosts` | first domain name |

## Components

```
main.go
 ├── config.Load(os.Getenv, os.Environ())   validated configuration
 │    └── fields.LoadDefaults               NPM_<KIND>_<FIELD> → effective defaults
 ├── docker.NewClient(DOCKER_HOST)     unix socket or TCP (socket proxy)
 │    └── docker.Listener              Containers(), Targets(), Watch()
 │         └── docker.Parse            indexed labels → []*docker.Target
 │              └── fields.Lookup      any spelling → canonical field
 ├── npm.New(...)                      dual-auth REST client, 4 collections
 ├── syncer.Debouncer[docker.Event]    burst → single batch
 └── syncer.Worker                     single goroutine, owns all writes
      ├── certs.Resolve                certificate wish → certificate id
      ├── syncer.BuildResource         target → npm.Resource of its kind
      └── syncer.Cache                 RWMutex-protected, one bucket per kind
```

### The field table

`internal/fields` is the single source of truth for every configurable
property: the canonical label name, its aliases (including the ones
Redth/npm-docker-sync uses), the value domain, the built-in default, the
environment variables that override it and the API field it writes to.

The label parser, the configuration loader, the start-up log and
`docs/FIELDS.md` all read from that one table, and a test regenerates the
documentation from it — so a field cannot exist in the parser without an
environment variable and a documented default.

### The Resource interface

`npm.ProxyHost`, `npm.RedirectionHost`, `npm.Stream` and `npm.DeadHost` each
keep their own JSON payload but implement one interface:

```go
type Resource interface {
    Kind() Kind                        // which collection it belongs to
    ResourceID() int                   // 0 until NPM assigns one
    SetResourceID(id int)
    ResourceKey() string               // identity within its kind
    ResourceMeta() Meta                // ownership marker lives here
    ResourceCertificate() CertificateID // what the live resource carries
    Fingerprint() string               // config hash, ids excluded
    AdoptServerState(current Resource) // e.g. an issued certificate id
    Describe() string                  // log friendly summary
}
```

That is what keeps the worker at one implementation instead of four: the kind
only decides which API path is called and which struct is decoded.

Data flows in one direction:

```
Docker events ─▶ Debouncer ─▶ Worker ─▶ NPM API
                                  ▲
Docker container list ────────────┘ (desired state, pulled per reconcile)
```

The event stream is a *hint*, never the source of truth. Every trigger causes
a full comparison of Docker state against NPM state, which is why a missed or
duplicated event cannot corrupt anything.

## Reconcile loop

1. `listener.Targets()` lists running containers and parses their labels into
   `docker.Target` values — **one per index and kind**, so a single container
   can yield many targets. Invalid definitions are logged and skipped; one bad
   label set must not stall the rest.
2. Targets are grouped by kind, and each kind is reconciled in turn
   (`proxy`, `redirect`, `stream`, `dead`).
3. `api.List(kind)` fetches the live collection. A `404` is treated as "this
   NPM fork does not expose that collection": it is logged once per run and the
   kind is skipped without touching its cache.
4. Both sides are keyed by identity: the alphabetically first domain name, or
   the incoming port for streams.
5. For each desired target, before anything is built:
   * a resource owned by another tool is skipped (see *Ownership* below),
   * a domain a foreign host already serves is reported as a conflict,
   * access list names are resolved into ids — an unknown name skips the
     resource rather than creating an unprotected host,
   * the certificate wish is resolved against the certificate list of this
     run, taking the live certificate into account so the choice stays stable.
6. Then:
   * not in NPM → `POST <collection>`
   * in NPM, fingerprint differs → `PUT <collection>/:id`
   * in NPM, fingerprint equal → nothing (counted as `unchanged`)
7. Resources carrying `meta.managed_by = npmplus-docker-sync` whose key no
   longer appears in the desired set are deleted (unless
   `DELETE_ORPHANS=false`).
8. The cache bucket of that kind is replaced atomically with the new state.

A resource the API rejected is remembered with an exponential backoff
(30 s → 30 min) and skipped until it expires; changing its labels — which
changes its fingerprint — clears the backoff at once.

## Certificate selection

The certificate list is fetched once per reconcile run, so every host of a run
reads the same snapshot; a failed fetch keeps the previous list rather than
stripping hosts of their certificates. Resolution happens *before* the TLS
cascade and the fingerprint, which is what lets `ssl.forced: auto` mean "on as
soon as a certificate is attached".

Candidates are filtered (no client CAs, no expired, no deleted entries) and
ranked by class — exact, exact+SANs, mixed, wildcard — then by remaining
validity and id. A certificate that is already attached and still fits is kept
unless a candidate matches in a strictly better class, so importing another
wildcard does not shuffle existing hosts around.

Certificates are not part of the Docker event stream, so the list is polled
every `CERTIFICATE_POLL_INTERVAL`; a changed list triggers a reconcile.

## Ownership

`meta.managed_by` decides what may be touched:

| Marker | Behaviour |
|---|---|
| `npmplus-docker-sync` | ours: updated, disabled, deleted as an orphan |
| *(none)* | created by hand: adopted and re-stamped unless `ADOPT_EXISTING=false`, never deleted while unmanaged |
| `npm-docker-sync` (Redth) | left alone; adopted only with `MIGRATE_FROM_REDTH=true`, keeping Redth's own meta keys |
| anything else | left alone |

Both tools can therefore run against the same NPM instance without deleting
each other's work.

Failures are collected with `errors.Join`: a single failing host is reported
but does not abort the run.

## Fingerprints and idempotency

Every resource implements `Fingerprint()`, which hashes its canonical,
configuration-relevant subset (domains or ports, upstream, certificate, flags,
location blocks, ownership meta) with SHA-256. The kind is part of the hashed
shape, so a domain that is both a proxy host and a 404 host never produces
colliding fingerprints. Server-side fields — `id`, `created_on`,
`owner_user_id` — are deliberately excluded, so a freshly fetched resource and
the payload that produced it hash identically.

Two independent checks make writes rare:

* the in-memory cache (`kind → key → {id, hash, container, index}`)
  short-circuits a matching entry, and
* even on a cold start, the hash of the live resource is compared to the
  desired hash before any `PUT`.

Special case: a target requesting `certificate_id=new` would otherwise never
converge, because NPM replies with a numeric id. `AdoptServerState` copies that
id into the desired payload before hashing, so the certificate is requested
exactly once — for proxy hosts, redirections, streams and 404 hosts alike.

## Upstream host resolution

`container.Summary.NetworkSettings.Networks` already carries every endpoint's
IP address, so resolving the upstream costs no extra API call. The order is:

* explicit label, else
* **`NPM_NETWORK` set** → the address on that network, and nothing else. A
  container that is not attached to it is skipped with a warning. Guessing
  here is worse than refusing: an address on a network NPM does not share
  produces a proxy host that answers `502` and looks correct in the UI.
* **`NPM_NETWORK` unset** → a network this container shares with the target →
  first network alphabetically → container name.

Preferring a shared network matters because reachability is what NPM needs: if
this process can reach a container, NPM on the same network can too. The
process finds its own networks by matching its hostname (the short container
id) against the container list; the same lookup records its own container id,
which is then used to drop this container's own events from the stream.
When the lookup fails — outside a container, or with a custom hostname — it
silently falls back to the remaining rules.

## API flavours

Nginx Proxy Manager and NPMplus expose the same endpoints but validate every
write against their own JSON schema, each with `additionalProperties: false`.
They have drifted far enough apart that one request body cannot satisfy both:
NPMplus removed `enabled` and `access_list_id`, added
`npmplus_access_list_ids`/`_type` (mandatory on custom locations too) and types
the stream ports as strings.

The client therefore keeps response models and request payloads apart. Each
resource implements `Payload(Flavour) (any, error)`, returning a struct that
mirrors exactly one request schema; `Client.Create`/`Update` send only that.
A configuration the flavour cannot express is refused locally, with the feature
named, instead of becoming an opaque `400`.

The flavour is probed once after login (an existing proxy host's field names,
falling back to the `version` object of `GET /api/`) and can be pinned with
`NPM_FLAVOUR`.

`internal/npm/contract_test.go` validates every payload against the real
upstream schemas, vendored into `internal/npm/testdata/schema` by
`scripts/vendor-schemas.py` (`make schemas`). Adding a field that a server
would reject fails the build.

## The enabled flag

`enabled` is the one configuration field that is not part of any write payload:
both APIs reserve it for `POST <collection>/{id}/enable` and `/disable`. It is
consequently excluded from the fingerprint and reconciled on its own — compare
desired against live, call the endpoint when they differ. Keeping it inside the
hash while it could never be written was what turned a single
`npm.proxy.enabled=false` into an endless update loop.

## Debouncing

`docker compose up` emits a `start` event per service, and `docker compose
down` a `die` plus `destroy` per service. Reconciling on each one would hammer
the API with identical work.

The debouncer emits a batch when either:

* no event arrived for `DEBOUNCE_INTERVAL` (default 3s), or
* `DEBOUNCE_MAX_WAIT` (default 30s) has passed since the first event of the
  burst — the safety valve against a container in a crash loop resetting the
  timer forever.

The batch content is only used for logging; the reconcile always reads full
state.

## Concurrency model

| Goroutine | Role |
|---|---|
| `listener.Watch` | reads the Docker event stream, reconnects with backoff |
| `Debouncer.Run` | coalesces events into batches |
| `Worker.Run` | the **only** writer to the NPM API |
| health server | serves `/healthz` and `/readyz` |

NPM stores its configuration in SQLite. Concurrent writers produce
`SQLITE_BUSY` / "database is locked" errors, so all writes funnel through one
worker goroutine. Shared state is limited to:

* `syncer.Cache` — `sync.RWMutex`, read by status/health, written by the worker
* `npm.Client` auth state — `sync.RWMutex` plus a separate login mutex, so a
  token refresh happens once even under concurrent use
* `Worker.lastRun/lastErr` — `sync.RWMutex`, read by the readiness probe

The whole suite runs under `go test -race` in CI.

## Graceful shutdown

```
SIGTERM ─▶ root context cancelled
           ├─ listener.Watch returns, closes the event channel
           ├─ debouncer drains and returns
           └─ worker leaves its select, runs a final Reconcile with
              context.WithoutCancel + SHUTDOWN_TIMEOUT, then returns
```

The final reconcile uses a detached context on purpose: the root context is
already cancelled, but the last state change still has to reach NPM. Because
it is a full reconcile, it supersedes any event batch that was still queued.

A second SIGINT bypasses the drain (signal handling is restored after the
first), so a stuck shutdown can always be interrupted.

## Failure behaviour

| Failure | Behaviour |
|---|---|
| Docker event stream drops | reconnect with exponential backoff (1s → 30s) |
| Docker API unreachable at startup | fatal: misconfiguration should be loud |
| NPM unreachable at startup | up to 10 login attempts with backoff, then exit |
| NPM session expired / revoked | transparent re-login, request retried once |
| A collection returns 404 (not supported by the fork) | logged at `warn`, kind skipped, cache kept |
| Listing one collection fails | that kind reports an error, the other three still reconcile |
| Single resource create/update fails | logged, counted, other resources continue |
| Invalid labels on one index | logged at `warn`, that resource ignored |

Anything transient is retried by the next event or the periodic resync; the
process only exits when the configuration itself is unusable.
