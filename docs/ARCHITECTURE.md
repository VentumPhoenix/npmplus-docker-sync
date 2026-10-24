# Architecture

`npmplus-docker-sync` is a reconciler: it keeps Nginx Proxy Manager or NPMplus
in line with the state described by Docker labels. Events, debouncing and
caching only exist to make that cheap and safe.

| Kind | API path | Identity |
|---|---|---|
| `proxy` | `/api/nginx/proxy-hosts` | first domain (alphabetically) |
| `redirect` | `/api/nginx/redirection-hosts` | first domain |
| `stream` | `/api/nginx/streams` | incoming port |
| `dead` (404) | `/api/nginx/dead-hosts` | first domain |

## Components

```
main.go
 ├── config            environment → validated configuration
 ├── fields            the field table: labels, aliases, env vars, defaults
 ├── docker            event listener, label parser → []*docker.Target
 ├── npm               API client, the four resource models, server capabilities
 ├── certs             certificate matching
 └── syncer
      ├── Debouncer    event burst → one batch
      ├── Worker       the only goroutine that writes to NPM
      └── Cache        last written state, one bucket per kind
```

**The field table** (`internal/fields`) is the single source of truth for every
label: name, aliases, type, default, environment variables, API field. The
parser, the configuration and `docs/FIELDS.md` all read it, and a test
regenerates the docs from it.

**One interface for four kinds.** `npm.ProxyHost`, `RedirectionHost`, `Stream`
and `DeadHost` implement `npm.Resource` (identity, ownership, fingerprint,
request payload), so the worker has one implementation for all of them.

```
Docker events ─▶ Debouncer ─▶ Worker ─▶ NPM API
                                 ▲
Docker container list ───────────┘  (desired state, read on every sync)
```

Events are only a trigger. Every sync compares the full Docker state with the
full NPM state, so a missed or duplicate event cannot break anything.

## A sync, step by step

For each kind:

1. List the live resources and parse the containers into targets — one per
   index and kind. Broken labels skip that resource and **protect** the
   container's hosts from deletion.
2. Match both sides by identity.
3. For each target: skip resources owned by someone else, report domain
   conflicts, resolve access list names and the certificate, build the desired
   resource and adapt it to what the server can store (`npm.Conform`).
4. Missing → create. Fingerprint differs → update. Equal → nothing.
5. Own resources nobody asks for any more → delete, unless a safety rule says no
   ([DELETION.md](DELETION.md)).

`enabled` is not part of create/update bodies on either server; it is compared
separately and switched through `/enable` and `/disable`. A resource the API
rejects is retried with backoff (30 s → 30 min).

## Ownership

Every resource the tool writes records its owner twice:

- in a **marker line** at the top of its advanced configuration —
  `# npmplus-docker-sync: managed from Docker labels, do not edit or remove this line | v=1 key=… container=… index=… instance=… prefix=…`
- in the `meta` object (`managed_by`, `managed_container`, …), where the server
  still has one.

The marker exists because `meta` is not reliable: NPMplus moves its own meta
keys into columns and empties `meta` on every host while doing so. The advanced
configuration survives that, exists on all kinds of both servers (except
upstream NPM streams, which keep `meta`) and is just a comment to nginx.

On reading, the marker is cut out of the advanced configuration — so the
user's config compares byte for byte — and merged into the ownership data; the
marker wins over `meta`. A marker naming a different key than its host (copied
in the UI, or domains edited by hand) makes the host **nobody's**: it is never
deleted. Hosts written before the marker existed get it on the next sync,
including those of stopped containers.

| Owner | Treatment |
|---|---|
| this tool (and this instance) | updated, disabled, deleted when orphaned |
| nobody (hand-made) | adopted if a label claims its domain, otherwise never touched |
| Redth's `npm-docker-sync` | left alone; adopted with `MIGRATE_FROM_REDTH=true` |
| another instance or tool | never touched |

## Server capabilities

NPM and NPMplus validate every request with `additionalProperties: false` and
have drifted apart — a body with one unknown field is rejected entirely. So
each resource builds its own request body (`Payload(Dialect)`), separate from
the response model, and every optional field is decided by
`Dialect.Accepts(kind, field)`.

The answer comes from `GET /api/schema`, the very document both servers
validate against — including types and allowed values (the NPMplus proxy
protocol is a boolean in one release and a number in the next; NPMplus wants
`$scheme` where NPM says `auto`). Without that endpoint, built-in knowledge
about flavour and version is used; the contract test checks it against every
vendored schema. Capabilities are read at start-up, after a rejected request
and after a version change.

`internal/npm/contract_test.go` validates every payload against the real
request schemas of several NPM and NPMplus versions, vendored by
`make schemas`.

## Fingerprints

Each resource hashes its configuration (domains or port, upstream,
certificate, switches, locations, ownership including the marker) with
SHA-256; ids and timestamps are excluded. A live resource and the payload that
created it hash the same, so a cold start writes nothing that is in sync. The
TLS cascade and `certificate: new` (whose issued id is adopted) are applied
before hashing, so hosts converge instead of being rewritten forever.

## Concurrency, debouncing, shutdown

| Goroutine | Role |
|---|---|
| listener | reads the Docker event stream, reconnects with backoff |
| debouncer | waits for `DEBOUNCE_INTERVAL` of quiet, at most `DEBOUNCE_MAX_WAIT` |
| worker | the **only** writer — NPM's SQLite does not like concurrent writers |
| health server | `/healthz`, `/readyz`, `/status`, `/metrics` |

Shared state (cache, auth, last result) is guarded by mutexes; CI runs
everything with `-race`.

On SIGTERM the listener stops and the worker runs one last sync on a detached
context, limited by `SHUTDOWN_TIMEOUT`. That last sync **never deletes**: during
`docker compose down` the apps usually stop first. A second SIGINT aborts
immediately.

## Failures

| Failure | Behaviour |
|---|---|
| Docker event stream drops | reconnect with backoff (1 s → 30 s) |
| Docker unreachable at start | exit — a misconfiguration should be loud |
| NPM unreachable at start | up to 10 login attempts, then exit |
| Session expired | log in again, retry once |
| A collection is missing (404) | warning, that kind is skipped |
| One host fails | logged and counted; the others continue |

Anything transient is retried by the next event or the periodic resync.
