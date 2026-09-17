# Configuration

All configuration is environment based; there is no config file. Invalid
values are reported together at startup, not one at a time.

## Connecting to NPM

| Variable | Default | Description |
|---|---|---|
| `NPM_URL` (`NPM_BASE_URL`) | — | **Required.** API base URL, e.g. `http://npm:81`. |
| `NPM_IDENTITY` (`NPM_EMAIL`, `NPM_USER`) | — | **Required.** Admin e-mail. |
| `NPM_SECRET` (`NPM_PASSWORD`) | — | **Required.** Admin password. |
| `NPM_SECRET_FILE` | — | Path to a file holding the password; takes precedence. |

Every variable on this page can also be supplied as `<NAME>_FILE` pointing at
a file, which is how Docker and Podman secrets are mounted:
`NPM_SECRET_FILE`, `NPM_PASSWORD_FILE`, and the same for anything else.
| `NPM_TIMEOUT` | `30s` | Per-request HTTP timeout. |
| `NPM_INSECURE_SKIP_VERIFY` | `false` | Skip TLS verification (self-signed NPM). |

`NPM_URL` must point at the **admin/API** port. In the default NPM deployment
that is `81`, not the `80`/`443` proxy ports.

Authentication mode is detected automatically:

* a `Set-Cookie` header in the `POST /api/tokens` response → cookie mode
  (NPMplus); the cookie jar handles everything afterwards,
* a `{"token": "..."}` body → bearer mode (upstream NPM).

Sessions are refreshed five minutes before the reported expiry, and any `401`
or `403` triggers one transparent re-login plus retry.

## API flavour

| Variable | Default | Description |
|---|---|---|
| `NPM_FLAVOUR` | `auto` | `auto`, `npmplus` or `npm` (alias `NPM_FLAVOR`). |

Nginx Proxy Manager and NPMplus share their endpoints but validate request
bodies against different JSON schemas, both with `additionalProperties: false`.
A body built for the wrong one is rejected in full:

```
400 data must NOT have additional properties
```

With `auto` the flavour is probed once after login: an existing proxy host is
inspected for `npmplus_access_list_ids` (NPMplus) or `access_list_id`
(upstream NPM), and if the collection is empty the `version` object of
`GET /api/` decides. Pin it explicitly if the probe ever guesses wrong.

## Connecting to Docker

| Variable | Default | Description |
|---|---|---|
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | `unix://`, `tcp://`, `npipe://` or `ssh://`. |
| `DOCKER_CERT_PATH` | — | TLS material for a remote daemon (standard Docker variable). |
| `DOCKER_TLS_VERIFY` | — | Standard Docker variable. |
| `NPM_NETWORK` | — | The Docker network upstream IPs are taken from (alias `NPM_DOCKER_NETWORK`). |
| `NPM_CONTAINER_NAME` | — | Name of the NPM/NPMplus container. Its networks are used when `NPM_NETWORK` is unset (alias `NPM_CONTAINER`). |
| `NPM_NETWORK_STRICT` | `true` | With `NPM_NETWORK` set, skip a container that is not attached to it instead of falling back to another network. |
| `RESOLVE_CONTAINER_IP` | `true` | Use container IPs instead of names as upstream hosts. |

Use `tcp://docker-socket-proxy:2375` with a filtered socket proxy — see
[SECURITY.md](../SECURITY.md).

## Behaviour

| Variable | Default | Description |
|---|---|---|
| `LABEL_PREFIX` | `npm` | Label namespace. A trailing dot is stripped; `.` and `-` both separate it from the field. |
| `NPM_EXPOSED_BY_DEFAULT` | `true` | Manage every container carrying labels of the namespace. `false` requires `npm.enable=true` (alias `EXPOSED_BY_DEFAULT`). |
| `STRICT_LABELS` | `false` | Skip a resource whose labels contain an unknown field instead of only warning (alias `NPM_STRICT_LABELS`). |
| `NPM_PORT_PREFERENCE` | `80,8080,3000,8000,443` | Order in which an exposed container port is picked when there is more than one. |
| `MIGRATE_FROM_REDTH` | `false` | Take over hosts created by [Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync). |
| `NPM_ON_STOP` | `disable` | What happens to the resources of a stopped container: `disable`, `keep` or `delete`. |
| `NPM_STOP_GRACE` | `1m` | How long a stopped container is ignored, so a restart produces no churn. |
| `SYNC_INSTANCE_ID` | the Docker daemon id | Identifies this instance in the resource meta; resources of another instance are never touched (alias `NPM_INSTANCE_ID`). Needs `INFO=1` on a socket proxy - without it there is no id, and every resource with our marker counts as ours. |
| `DELETE_GUARD` | `0.5` | Largest share of the managed resources a single run may delete. Accepts `0.5`, `50%` or `off`. |
| `DELETE_GUARD_MIN` | `3` | How many deletions a run needs before the share guard applies. |
| `SYNC_KINDS` | all four | Resource types to manage, e.g. `proxy,stream` (alias `RESOURCE_KINDS`). Values: `proxy`, `redirect`, `stream`, `404` (and the spellings `proxies`, `redirection`, `dead`). |
| `DEBOUNCE_INTERVAL` | `3s` | Quiet period after the last event. |
| `DEBOUNCE_MAX_WAIT` | `30s` | Cap for a continuous event stream. Must be ≥ interval. |
| `RESYNC_INTERVAL` | `5m` | Periodic full reconcile; `0` disables it. |
| `DELETE_ORPHANS` | `true` | Delete managed hosts without a container. Never applies to the shutdown flush (see below). |
| `ADOPT_EXISTING` | `true` | Take over an existing host for a labelled domain. |
| `DRY_RUN` | `false` | Log intended changes, call no write endpoint. |
| `SHUTDOWN_TIMEOUT` | `30s` | Budget for the final flush after SIGTERM. |

Durations use Go syntax (`500ms`, `3s`, `1m30s`); a bare number is seconds.

## Certificates

| Variable | Default | Description |
|---|---|---|
| `NPM_DEFAULT_CERTIFICATE` | `auto` | Default for the `certificate` label: `auto`, an id, a domain, `name:<nice name>`, `new` or `none`. |
| `NPM_CERTIFICATE_PARTIAL` | `primary` | When no certificate covers every domain of a host: `primary` uses one for the first domain and warns, `none` attaches nothing. |
| `NPM_CERTIFICATE_AUTO_CREATE` | `false` | Request a Let's Encrypt certificate when the automatic selection finds nothing. |
| `CERTIFICATE_POLL_INTERVAL` | `1m` | How often the certificate list is checked for changes; `0` disables the poll. |

The selection rules are documented in [LABELS.md](LABELS.md#certificates).

## Field defaults

Every label field has an environment variable that changes its default for all
containers:

```
NPM_<KIND>_<FIELD>     kind specific, e.g. NPM_PROXY_WEBSOCKETS, NPM_STREAM_UDP
NPM_DEFAULT_<FIELD>    every kind that has the field, e.g. NPM_DEFAULT_CERTIFICATE
```

`<KIND>` is `PROXY`, `REDIRECT`, `STREAM` or `DEAD`. The field name is upper
cased with `.`, `-` and `_` all written as `_`, and every alias of a field has
its own variable — which is why Redth's `NPM_PROXY_SSL_FORCE`,
`NPM_PROXY_BLOCK_EXPLOITS` and `NPM_PROXY_HSTS_SUBDOMAINS` work unchanged.

Labels on a container always win over these. Values are validated at start-up,
the effective defaults that came from the environment are logged, and a
variable in the `NPM_<KIND>_` or `NPM_DEFAULT_` namespace that names no field
is reported as a warning instead of being ignored silently.

The full list is in [FIELDS.md](FIELDS.md).

### Deletion safeguards

Which of these actually delete anything — and which stop a run that would — is
documented in [DELETION.md](DELETION.md). In short: a stopped container no
longer loses its host, a container whose labels cannot be parsed is protected,
and a run that would remove an implausible share of the managed resources is
refused.

### The shutdown flush never deletes

On SIGINT/SIGTERM the worker runs one last reconcile on a detached context so
pending creates and updates still reach the API. That final run **ignores
`DELETE_ORPHANS` and deletes nothing.**

The reason is `docker compose down`: the labelled containers usually stop
*before* this one, so from the worker's point of view every managed resource
has just become an orphan. Deleting them would empty NPM on every routine
restart of the stack. Deletion resumes on the next start, or at the next
`RESYNC_INTERVAL` tick, once the container list is trustworthy again.

### Tuning notes

* **Busy hosts**: raise `DEBOUNCE_INTERVAL` to `5s`–`10s` to batch more
  aggressively.
* **Someone edits NPM by hand**: lower `RESYNC_INTERVAL` so drift is repaired
  sooner, or raise it to `0` if you want manual edits to survive until the next
  container event.
* **Shared NPM instance**: set `DELETE_ORPHANS=false` and `ADOPT_EXISTING=false`
  for the most conservative behaviour.
* **Only proxy hosts needed**: `SYNC_KINDS=proxy` cuts a reconcile from four
  `GET`s to one.
* **NPM on another host**: IP-based upstreams only work when NPM can route to
  the container network. Set `RESOLVE_CONTAINER_IP=false` and give each
  resource an explicit `forward_host` in that case.

## Subcommands

```bash
npmplus-docker-sync              # the daemon: watch Docker until stopped
npmplus-docker-sync sync         # one reconcile, then exit (exit code 1 on error)
npmplus-docker-sync validate     # parse the labels and report; never writes, never contacts NPM
npmplus-docker-sync healthcheck  # probe HEALTH_ADDR/readyz (used by the container HEALTHCHECK)
npmplus-docker-sync version
```

`validate` is the pre-deploy check: it exits non-zero when a container carries a
label set that cannot be parsed, and prints every resource the labels would
produce. With a file argument it checks a compose stack **before** it runs, and
then needs neither Docker nor NPM:

```bash
docker compose config --format json > stack.json
npmplus-docker-sync validate stack.json
```

The rendered configuration is read rather than the YAML, so variables,
`extends` and multiple `-f` files are already resolved by compose itself.

`sync` is the form for a pipeline or a test harness — one deterministic run, no
debounce window. Both exit `1` when something went wrong, `0` otherwise.

## Observability

| Variable | Default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `text` | `text` (human) or `json` (`log/slog`). |
| `HEALTH_ADDR` | — | e.g. `:8080` to expose `/healthz`, `/readyz`, `/status` and `/metrics`. Disabled when unset. |
| `LOG_PAYLOADS` | `false` | Mirror full API request bodies into the debug log (alias `NPM_DEBUG_PAYLOADS`). |

`/readyz` returns `503` until the first reconcile, whenever Docker or the NPM
API is unreachable, and when the Docker event stream has been disconnected for
more than two minutes. A resource the API *rejected* does not make the process
unready — the other hosts are still in sync and a restart would not help — so
those are reported as a counter in the body instead:

```
ok: 12 managed hosts, 1 failed, last sync 2026-09-16T13:26:35Z
```

That makes it a good compose/Kubernetes probe: it flips only when a dependency
is actually gone.

At `LOG_LEVEL=debug` every request is logged with its top-level field names,
and a rejected one additionally with the status and the API's message — which
is what an `additionalProperties: false` rejection otherwise withholds.
`LOG_PAYLOADS=true` adds the complete body; values of credential-looking keys
(`*credential*`, `*secret*`, `*password*`, `*token*`, `*api_key*`) are replaced
with `***`, but host names and raw nginx config are not, so it stays opt-in.

The runtime image is `FROM scratch` and contains no shell, `curl` or `wget`, so
the binary ships its own probe as a subcommand:

```bash
HEALTH_ADDR=:8080 npmplus-docker-sync healthcheck; echo $?
```

It `GET`s `/readyz` on `HEALTH_ADDR` and exits `0` on `200`, `1` on anything
else (non-200, connection refused, timeout). With `HEALTH_ADDR` unset the
health endpoint is disabled and the probe exits `0` without checking anything,
so it never fails a container that was deliberately configured without it.

The image already declares a `HEALTHCHECK` using it, and both compose files
repeat it explicitly so it is visible where you configure the service:

```yaml
environment:
  HEALTH_ADDR: ":8080"          # required - without it the probe is a no-op
healthcheck:
  test: ["CMD", "/npmplus-docker-sync", "healthcheck"]
  interval: 30s
  timeout: 5s
  start_period: 20s             # covers NPM login + the first reconcile
  retries: 3
```

```bash
docker inspect --format '{{.State.Health.Status}}' npmplus-docker-sync
```

For an external HTTP probe (Kubernetes, Uptime Kuma), query `/readyz` directly
instead.

## Status and metrics

With `HEALTH_ADDR` set, four endpoints are served:

| Path | Content |
|---|---|
| `/healthz` | liveness: the process is running |
| `/readyz` | readiness: a reconcile happened, Docker and NPM are reachable, and the event stream is connected |
| `/status` | JSON: every managed resource with its container, id, certificate, enabled state, last error and backoff |
| `/metrics` | Prometheus: runs, errors, created/updated/deleted/failed/skipped, blocked deletions, managed resources per kind, certificate matches per class, event stream state |

`/readyz` answers `503` before the first run, while Docker or NPM is
unreachable, and when the Docker event stream has been down for more than two
minutes — a silently disconnected listener only reacts on the periodic resync,
which is not healthy.

```
$ curl -s localhost:8080/status | jq '.resources[0]'
{
  "kind": "proxy",
  "key": "app.example.com",
  "id": 42,
  "container": "app",
  "index": 0,
  "enabled": true,
  "container_running": true,
  "certificate_id": 12
}
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | clean shutdown after SIGINT/SIGTERM |
| `1` | fatal: invalid configuration, Docker unreachable, or NPM login failed |
