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

## Connecting to Docker

| Variable | Default | Description |
|---|---|---|
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | `unix://`, `tcp://`, `npipe://` or `ssh://`. |
| `DOCKER_CERT_PATH` | — | TLS material for a remote daemon (standard Docker variable). |
| `DOCKER_TLS_VERIFY` | — | Standard Docker variable. |
| `NPM_NETWORK` | — | Docker network preferred when resolving upstream IPs (alias `NPM_DOCKER_NETWORK`). |
| `RESOLVE_CONTAINER_IP` | `true` | Use container IPs instead of names as upstream hosts. |

Use `tcp://docker-socket-proxy:2375` with a filtered socket proxy — see
[SECURITY.md](../SECURITY.md).

## Behaviour

| Variable | Default | Description |
|---|---|---|
| `LABEL_PREFIX` | `npm` | Label namespace. A trailing dot is stripped. |
| `SYNC_KINDS` | all four | Resource types to manage, e.g. `proxy,stream`. Aliases: `proxies`, `redirection`, `dead`, `404`. |
| `DEBOUNCE_INTERVAL` | `3s` | Quiet period after the last event. |
| `DEBOUNCE_MAX_WAIT` | `30s` | Cap for a continuous event stream. Must be ≥ interval. |
| `RESYNC_INTERVAL` | `5m` | Periodic full reconcile; `0` disables it. |
| `DELETE_ORPHANS` | `true` | Delete managed hosts without a container. Never applies to the shutdown flush (see below). |
| `ADOPT_EXISTING` | `true` | Take over an existing host for a labelled domain. |
| `DRY_RUN` | `false` | Log intended changes, call no write endpoint. |
| `SHUTDOWN_TIMEOUT` | `30s` | Budget for the final flush after SIGTERM. |

Durations use Go syntax (`500ms`, `3s`, `1m30s`); a bare number is seconds.

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

## Observability

| Variable | Default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `text` | `text` (human) or `json` (`log/slog`). |
| `HEALTH_ADDR` | — | e.g. `:8080` to expose `/healthz` and `/readyz`. |

`/readyz` returns `503` until the first successful reconcile and whenever the
most recent one failed, and its body reports how many resources are managed
across all kinds. That makes it a good compose/Kubernetes probe.

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

## Exit codes

| Code | Meaning |
|---|---|
| `0` | clean shutdown after SIGINT/SIGTERM |
| `1` | fatal: invalid configuration, Docker unreachable, or NPM login failed |
