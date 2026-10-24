# Configuration

Everything is configured through environment variables on the sync container.
Invalid values are all reported together at start-up. Durations use Go syntax
(`500ms`, `3s`, `1m30s`); a bare number means seconds.

**Any variable can be read from a file** by appending `_FILE` — that is how
Docker secrets are used: `NPM_SECRET_FILE=/run/secrets/npm_password`.

## NPM

| Variable | Default | Description |
|---|---|---|
| `NPM_URL` | — | **Required.** The admin/API address, e.g. `http://npm:81` — port **81**, not 80/443. Alias `NPM_BASE_URL`. |
| `NPM_IDENTITY` | — | **Required.** Admin e-mail. Aliases `NPM_EMAIL`, `NPM_USER`. |
| `NPM_SECRET` | — | **Required.** Admin password. Alias `NPM_PASSWORD`. |
| `NPM_TIMEOUT` | `30s` | Timeout per API request. |
| `NPM_INSECURE_SKIP_VERIFY` | `false` | Accept a self-signed certificate on the NPM API. |
| `NPM_FLAVOUR` | `auto` | `auto`, `npmplus` or `npm` (alias `NPM_FLAVOR`). Only pin it if detection guesses wrong. |

Login works with both servers: upstream NPM returns a token, NPMplus sets a
cookie. Sessions are renewed before they expire and after any `401`/`403`.

**What the server accepts is read from the server.** At start-up — and again
after a rejected request or a new server version — the tool reads
`GET /api/schema`, the document NPM and NPMplus validate requests against. So a
new NPM or NPMplus release that adds or removes fields is handled without an
update of this tool. Pinning `NPM_FLAVOUR` does not stop that.

## Docker

| Variable | Default | Description |
|---|---|---|
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Recommended: `tcp://docker-socket-proxy:2375`, see [SECURITY.md](../SECURITY.md). |
| `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY` | — | The standard Docker variables for a remote daemon. |
| `NPM_NETWORK` | — | The network NPM and your apps share; upstream IPs are taken from it. Alias `NPM_DOCKER_NETWORK`. |
| `NPM_CONTAINER_NAME` | — | Name of the NPM container; its networks are used when `NPM_NETWORK` is unset. Alias `NPM_CONTAINER`. |
| `NPM_NETWORK_STRICT` | `true` | With `NPM_NETWORK` set, skip a container that is not on it instead of trying another network. |
| `RESOLVE_CONTAINER_IP` | `true` | `false` uses container names instead of IPs. |

How the upstream is chosen: [LABELS.md](LABELS.md#upstream-host).

## Behaviour

| Variable | Default | Description |
|---|---|---|
| `LABEL_PREFIX` | `npm` | Label namespace. |
| `NPM_EXPOSED_BY_DEFAULT` | `true` | Manage every container with `npm.*` labels. `false`: only those with `npm.enable=true`. |
| `SYNC_KINDS` | all | Types to manage: `proxy`, `redirect`, `stream`, `404`, comma separated. Alias `RESOURCE_KINDS`. |
| `STRICT_LABELS` | `false` | Skip a resource with an unknown label instead of only warning. |
| `NPM_PORT_PREFERENCE` | `80,8080,3000,8000,443` | Which exposed port to use when there are several. |
| `ADOPT_EXISTING` | `true` | Take over an existing, hand-made host when a label claims its domain. |
| `MIGRATE_FROM_REDTH` | `false` | Take over hosts created by [Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync). |
| `DRY_RUN` | `false` | Only log what would change. |
| `DEBOUNCE_INTERVAL` | `3s` | Wait this long after the last Docker event before syncing. |
| `DEBOUNCE_MAX_WAIT` | `30s` | Sync at the latest after this long, even if events keep coming. |
| `RESYNC_INTERVAL` | `5m` | Full sync on a timer, to repair manual edits; `0` turns it off. |
| `SHUTDOWN_TIMEOUT` | `30s` | Time for the last sync after SIGTERM. |

## Deletion and safety

What each of these does in practice: [DELETION.md](DELETION.md).

| Variable | Default | Description |
|---|---|---|
| `DELETE_ORPHANS` | `true` | Delete own hosts whose container is gone. |
| `NPM_ON_STOP` | `disable` | Host of a **stopped** container: `disable`, `keep` or `delete`. |
| `NPM_STOP_GRACE` | `1m` | Ignore a stopped container this long, so restarts change nothing. |
| `DELETE_GUARD` | `0.5` | Refuse a run that would delete more than this share of the own hosts (`0.5`, `50%`, `off`). |
| `DELETE_GUARD_MIN` | `3` | The share guard only applies from this many deletions. |
| `SYNC_INSTANCE_ID` | Docker daemon id | Lets several sync instances share one NPM; each only touches its own hosts. Needs `INFO=1` on the socket proxy. Alias `NPM_INSTANCE_ID`. |

## Certificates

| Variable | Default | Description |
|---|---|---|
| `NPM_DEFAULT_CERTIFICATE` | `auto` | Default for the `certificate` label. |
| `NPM_CERTIFICATE_PARTIAL` | `primary` | No certificate covers all domains: `primary` uses one for the first domain, `none` attaches nothing. |
| `NPM_CERTIFICATE_AUTO_CREATE` | `false` | Request a Let's Encrypt certificate when none matches. |
| `CERTIFICATE_POLL_INTERVAL` | `1m` | How often to look for new certificates; `0` turns it off. |

How certificates are chosen: [LABELS.md](LABELS.md#certificates).

## Defaults for labels

Every label has two variables that set its default for all containers:

```
NPM_<KIND>_<FIELD>     one kind:   NPM_PROXY_WEBSOCKETS, NPM_STREAM_UDP
NPM_DEFAULT_<FIELD>    all kinds:  NPM_DEFAULT_CERTIFICATE, NPM_DEFAULT_GROUP
```

`<KIND>` is `PROXY`, `REDIRECT`, `STREAM` or `DEAD`; the field is upper case
with `.` and `-` written as `_`. A label always wins. Defaults set this way are
logged at start-up, and a variable that names no field is reported. The full
list is in [FIELDS.md](FIELDS.md).

## Logs, health and metrics

| Variable | Default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `text` | `text` or `json`. |
| `LOG_PAYLOADS` | `false` | Log full request bodies at debug level (secrets are masked, host names and nginx config are not). |
| `HEALTH_ADDR` | — | e.g. `:8080` — turns on the endpoints below. |

| Endpoint | Content |
|---|---|
| `/healthz` | the process is alive |
| `/readyz` | `200` once a sync ran and Docker, NPM and the event stream are fine; `503` otherwise |
| `/status` | JSON: every managed host with container, id, certificate, state, last error |
| `/metrics` | Prometheus: runs, errors, changes, blocked deletions, hosts per kind, certificate matches |

A host the API rejects does **not** make `/readyz` fail — the other hosts are
fine and a restart would not help. It is counted in the body instead:
`ok: 12 managed hosts, 1 failed, last sync 2026-09-16T13:26:35Z`.

The image has no shell or curl, so the binary probes itself:
`npmplus-docker-sync healthcheck` exits `0` when `/readyz` answers `200`. The
image's `HEALTHCHECK` uses it, which enables
`depends_on: { condition: service_healthy }`. Without `HEALTH_ADDR` the probe
always passes.

At `LOG_LEVEL=debug` every API request is logged with its field names, and a
rejected one with the server's message.

## Commands

```bash
npmplus-docker-sync                    # the daemon: watch Docker until stopped
npmplus-docker-sync sync               # one sync, then exit
npmplus-docker-sync validate [file]    # check labels; never writes, never contacts NPM
npmplus-docker-sync healthcheck        # probe /readyz (used by the image)
npmplus-docker-sync version
```

`validate` with a file checks a stack **before** it starts — no Docker or NPM
needed:

```bash
docker compose config --format json > stack.json
npmplus-docker-sync validate stack.json
```

Exit codes: `0` success, `1` an error (invalid labels, failed sync, fatal
problem), `2` wrong usage.

## Tips

- **Many containers starting at once:** raise `DEBOUNCE_INTERVAL` to `5s`–`10s`.
- **Manual edits in NPM should survive:** `RESYNC_INTERVAL=0` — they are then
  only overwritten on the next container event.
- **Shared NPM, most careful setting:** `DELETE_ORPHANS=false`, `ADOPT_EXISTING=false`.
- **Only proxy hosts:** `SYNC_KINDS=proxy` saves three API calls per sync.
- **NPM on another machine:** IPs only work if NPM can route to the container
  network. Otherwise use `RESOLVE_CONTAINER_IP=false` and an explicit `host` label.
- **The last sync on shutdown never deletes.** During `docker compose down` the
  apps usually stop first; deleting then would empty NPM on every restart.
