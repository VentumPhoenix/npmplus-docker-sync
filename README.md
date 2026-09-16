<div align="center">

# npmplus-docker-sync

**Docker labels → Nginx Proxy Manager & NPMplus. Automatically, safely, in one static binary.**

[![CI](https://github.com/VentumPhoenix/npmplus-docker-sync/actions/workflows/ci.yml/badge.svg)](https://github.com/VentumPhoenix/npmplus-docker-sync/actions/workflows/ci.yml)
[![CodeQL](https://github.com/VentumPhoenix/npmplus-docker-sync/actions/workflows/codeql.yml/badge.svg)](https://github.com/VentumPhoenix/npmplus-docker-sync/actions/workflows/codeql.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/VentumPhoenix/npmplus-docker-sync.svg)](https://pkg.go.dev/github.com/VentumPhoenix/npmplus-docker-sync)
[![Go Report Card](https://goreportcard.com/badge/github.com/VentumPhoenix/npmplus-docker-sync)](https://goreportcard.com/report/github.com/VentumPhoenix/npmplus-docker-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Image](https://img.shields.io/badge/ghcr.io-npmplus--docker--sync-2496ED?logo=docker&logoColor=white)](https://github.com/VentumPhoenix/npmplus-docker-sync/pkgs/container/npmplus-docker-sync)

Label a container, and its **proxy hosts, redirections, streams and 404 hosts**
appear in Nginx Proxy Manager. Stop the container, and they disappear again —
no clicking, no drift.

</div>

---

## Why

Nginx Proxy Manager has a lovely UI and a decent API, but every new container
still means the same six clicks. Traefik-style label-driven routing is the
better workflow — `npmplus-docker-sync` brings it to NPM without replacing it.

It watches the Docker event stream, reads a handful of labels and reconciles
them against the NPM API. It works with **both** upstream
[Nginx Proxy Manager](https://github.com/NginxProxyManager/nginx-proxy-manager)
(JWT auth) and [NPMplus](https://github.com/ZoeyVid/NPMplus) (httpOnly cookie
auth) — the auth mode is detected at login, nothing to configure.

## Features

| | |
|---|---|
| 🏷️ **Label driven** | `npm.enable=true`, a hostname, a port — done |
| 🧩 **All four resource types** | Proxy hosts, redirection hosts, TCP/UDP streams and 404 hosts |
| 🔢 **Many services per container** | Indexed labels: `npm.0.proxy.host`, `npm.1.redirect.host`, `npm.2.stream.incoming_port` |
| 🎯 **IP-based upstreams** | Resolves the container's IP instead of its name — no more DNS-related 502s |
| 🔁 **Event driven** | Reacts to `start` / `stop` / `die` / `destroy` within seconds |
| 🧠 **Idempotent** | Per-kind fingerprint cache: no API call when nothing changed |
| 🧹 **Self cleaning** | Removes resources whose container is gone — and only those it created |
| 🔐 **Socket-proxy ready** | Talks to Docker over TCP, so the raw socket never enters the container |
| 🔑 **Dual auth** | NPM (Bearer JWT) and NPMplus (httpOnly cookie), auto-detected |
| 🧊 **Debounced** | A `docker compose up` of 20 services triggers *one* reconcile |
| 🚦 **Serialised writes** | A single worker goroutine — no `SQLITE_BUSY` from concurrent writes |
| 🛑 **Graceful shutdown** | SIGTERM drains the queue and flushes state before exiting |
| 🪶 **Tiny** | `FROM scratch`, non-root (`1000:1000`), ~8 MB image, no runtime deps |
| 🧪 **Tested** | Table-driven unit tests, mocked Docker + NPM APIs, `-race` in CI |

## Quick start

```bash
git clone https://github.com/VentumPhoenix/npmplus-docker-sync.git
cd npmplus-docker-sync

cp .env.example .env            # NPM_URL, NPM_IDENTITY, ...
mkdir -p secrets && printf '%s' 'your-npm-password' > secrets/npm_password.txt
chmod 600 secrets/npm_password.txt

docker compose up -d            # starts docker-socket-proxy + npmplus-docker-sync
docker compose logs -f npmplus-docker-sync
```

Then label any container on the same Docker host:

```yaml
services:
  whoami:
    image: traefik/whoami
    networks: [npm]
    labels:
      npm.enable: "true"
      npm.proxy.host: "whoami.example.com"
      npm.proxy.port: "80"
```

Within a few seconds the proxy host `whoami.example.com → http://172.20.0.5:80`
exists in NPM. `docker compose down` removes it again.

### One container, many resources

Indexed labels expose several services from a single container — and each
index can be a different resource type:

```yaml
labels:
  npm.enable: "true"

  # 0: the web UI
  npm.0.proxy.host: "app.example.com"
  npm.0.proxy.port: "8080"

  # 1: the metrics endpoint on another port
  npm.1.proxy.host: "metrics.app.example.com"
  npm.1.proxy.port: "9090"
  npm.1.proxy.access_list_id: "2"

  # 2: the database, exposed as a TCP stream
  npm.2.stream.incoming_port: "5432"
  npm.2.stream.forward_port: "5432"

  # 3: redirect the old domain
  npm.3.redirect.host: "old-app.example.com"
  npm.3.redirect.forward_domain: "app.example.com"

  # 4: park a domain on a 404 page
  npm.4.404.host: "parked.example.com"
```

Labels without an index belong to index `0`, so `npm.proxy.host` and
`npm.0.proxy.host` are the same thing.

### Without compose

```bash
docker run -d --name npmplus-docker-sync \
  --network npm \
  -e DOCKER_HOST=tcp://docker-socket-proxy:2375 \
  -e NPM_URL=http://npm:81 \
  -e NPM_IDENTITY=admin@example.com \
  -e NPM_SECRET=changeme \
  -e NPM_NETWORK=npm \
  ghcr.io/ventumphoenix/npmplus-docker-sync:latest
```

### From source

Requires Go 1.26 or newer — that is the minimum of the official Docker SDK,
which is this project's only direct dependency.

```bash
make build     # static binary ./npmplus-docker-sync
make test      # race-enabled unit tests
make check     # fmt + vet + lint + test
```

## How it works

```
                    ┌────────────────────────┐
  docker events ───▶│ listener  (filtered)   │   type=container
  (start/die/stop)  │ internal/docker        │   event=start,die,stop,…
                    └───────────┬────────────┘
                                │ Event
                    ┌───────────▼────────────┐
                    │ debouncer              │   quiet period 3s
                    │ internal/syncer        │   hard cap 30s
                    └───────────┬────────────┘
                                │ []Event (one batch per burst)
                    ┌───────────▼────────────┐   ┌────────────────────┐
                    │ worker (single         │──▶│ state cache        │
                    │ goroutine, sequential) │◀──│ RWMutex, per kind: │
                    └───────────┬────────────┘   │ proxy · redirect · │
                                │                │ stream · 404       │
                                │                └────────────────────┘
                    ┌───────────▼────────────────────────────────────┐
                    │ NPM / NPMplus REST API                         │
                    │ /proxy-hosts /redirection-hosts                │
                    │ /streams     /dead-hosts                       │
                    └────────────────────────────────────────────────┘
```

1. **Reconciliation on startup.** Desired state (container labels) is compared
   against live state (all four NPM collections) and the delta is applied — so
   a restart after downtime repairs whatever drifted.
2. **Events** are filtered *server-side*: the daemon only ever sends container
   lifecycle events, which is what a socket proxy can be locked down to.
3. **Debouncing** collapses bursts. `docker compose up` with 20 services
   produces one reconcile, not 20.
4. **One worker** performs every write. NPM stores its config in SQLite, which
   does not appreciate concurrent writers.
5. **A per-kind fingerprint cache** (SHA-256 over the canonical config)
   short-circuits no-op updates, so a periodic resync costs four `GET`s.
6. **SIGTERM** stops event intake, runs a final reconcile with a detached
   context and exits — nothing is lost when Watchtower or Proxmox restarts the
   container.

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the details.

## Upstream host resolution

NPM needs to reach your container. Using the container *name* only works when
NPM can resolve Docker's internal DNS, which is the most common cause of
`502 Bad Gateway` after a proxy host is created.

`npmplus-docker-sync` therefore defaults the upstream to the container's **IP
address**:

1. an explicit `npm.proxy.forward_host` / `npm.stream.forward_host` label, else
2. **with `NPM_NETWORK` set** — the IP in exactly that network. A container
   that is not attached to it is skipped with a warning naming the networks it
   *is* on. Nothing else is tried: an address on a network NPM does not share
   is unreachable, and so is the container name, so a proxy host pointing
   there would only produce a silent `502`.
3. **without `NPM_NETWORK`** — the IP in a network this sync container is
   itself attached to, else the IP of the first network (alphabetically), else
   the container name as a last resort.

Set `RESOLVE_CONTAINER_IP=false` to go back to name-based upstreams, or
`npm.<index>.<kind>.resolve_ip=false` for a single resource.
`NPM_NETWORK_STRICT=false` restores the old fall-through behaviour if you
really do want it.

> [!NOTE]
> Container IPs change when a container is recreated. That is fine: the next
> event triggers a reconcile and the proxy host is updated within seconds.

## Labels

Grammar:

```
<prefix>.enable                          opt-in for the whole container (required)
<prefix>[.<index>].<kind>.<field>        one resource
<prefix>[.<index>].<field>               shorthand, <kind> defaults to proxy
<prefix>.<index>.enable=false            switch a single index off
```

`<prefix>` is `npm` by default (`LABEL_PREFIX`), `<index>` defaults to `0`, and
`<kind>` is one of `proxy`, `redirect`, `stream`, `404` (alias `dead`).
Booleans accept `true/false`, `1/0`, `yes/no`, `on/off`.

### Proxy hosts — `npm.<i>.proxy.*`

| Label | Default | Description |
|---|---|---|
| `host` | — | **Required.** Domain name(s), comma separated. |
| `port` | — | **Required.** Container-internal port. |
| `scheme` | `http` | Upstream scheme (`http`/`https`). |
| `forward_host` | container IP | Upstream host override (alias `forward_ip`). |
| `resolve_ip` | `RESOLVE_CONTAINER_IP` | Use the container IP as upstream. |
| `websockets` | `true` | Allow websocket upgrades. |
| `block_exploits` | `true` | NPM's common-exploit blocklist. |
| `caching` | `false` | Cache static assets. |
| `trust_forwarded_proto` | `false` | Trust `X-Forwarded-Proto` from the client. |
| `access_list_ids` | – | Access list id(s), comma separated (aliases `access_list`, `access_lists`). NPMplus accepts several, upstream NPM exactly one. |
| `access_list_id` | – | Shorthand for a single id; kept for compatibility. |
| `access_list_type` | derived | `public` (no list) or `custom`. Derived from the ids unless set. |
| `advanced_config` | – | Raw nginx snippet for this host. |
| `location.<n>.path` | – | Custom location block, e.g. `/api` (see below). |
| `location.<n>.forward_host` / `.forward_port` / `.forward_scheme` | inherited | Upstream of that location block. |
| `location.<n>.advanced_config` | – | Raw nginx snippet inside the location. |
| `location.<n>.access_list_ids` / `.access_list_type` | `global` | Per-location access list; `global` inherits the host's (NPMplus only). |
| `enabled` | `true` | Create the host but leave it disabled with `false`. |

Custom location blocks route parts of a domain somewhere else:

```yaml
npm.proxy.host: "app.example.com"
npm.proxy.port: "8080"
npm.proxy.location.0.path: "/api"
npm.proxy.location.0.forward_host: "api-backend"
npm.proxy.location.0.forward_port: "3000"
```

### Redirection hosts — `npm.<i>.redirect.*`

| Label | Default | Description |
|---|---|---|
| `host` | — | **Required.** Source domain(s) (alias `from`). |
| `forward_domain` | — | **Required.** Target domain, no scheme (aliases `to`, `target`). |
| `http_code` | `301` | Redirect status code, 300–308 (alias `code`). |
| `scheme` | `auto` | `auto`, `http` or `https`. |
| `preserve_path` | `true` | Append the request path to the target. |
| `block_exploits` | `true` | NPM's common-exploit blocklist. |
| `advanced_config` | – | Raw nginx snippet. |
| `enabled` | `true` | Enable the redirect. |

### Streams — `npm.<i>.stream.*`

| Label | Default | Description |
|---|---|---|
| `incoming_port` | — | **Required.** Port NPM listens on (alias `port`). |
| `forward_port` | = incoming | Upstream port (alias `forwarding_port`). |
| `forward_host` | container IP | Upstream host (alias `forwarding_host`). |
| `tcp` | `true` | Forward TCP. |
| `udp` | `false` | Forward UDP. |
| `protocol` | `tcp` | Shorthand: `tcp`, `udp` or `both`. |
| `certificate_id` | – | Certificate for TLS-terminating streams. |
| `enabled` | `true` | Enable the stream. |

### 404 hosts — `npm.<i>.404.*` (or `npm.<i>.dead.*`)

| Label | Default | Description |
|---|---|---|
| `host` | — | **Required.** Domain name(s) to park. |
| `advanced_config` | – | Raw nginx snippet. |
| `enabled` | `true` | Enable the host. |

### TLS — available for every kind

| Label | Default | Description |
|---|---|---|
| `certificate_id` | – | Existing certificate id, or `new` for Let's Encrypt. |
| `ssl.forced` | `false` | Redirect HTTP → HTTPS (needs a certificate). |
| `ssl.http2` | `false` | HTTP/2 support. |
| `ssl.http3` | `false` | HTTP/3 support (**NPMplus only**; rejected against upstream NPM). |
| `ssl.hsts` | `false` | Enable HSTS. |
| `ssl.hsts_subdomains` | `false` | Include subdomains in HSTS. |
| `letsencrypt.email` | – | Required with `certificate_id=new`. |
| `letsencrypt.agree` | `false` | Must be `true` with `certificate_id=new`. |
| `letsencrypt.dns_challenge` | `false` | Use a DNS-01 challenge. |
| `letsencrypt.dns_provider` | – | e.g. `cloudflare` (with DNS-01). |
| `letsencrypt.dns_credentials` | – | Provider credentials (with DNS-01). |
| `letsencrypt.propagation_seconds` | `0` | DNS propagation wait. |

> [!NOTE]
> `enabled` is not part of a create/update body — both APIs reject the property
> and expose `POST <collection>/{id}/enable` and `/disable` instead. The
> reconcile loop compares the live state and calls those endpoints when they
> differ, so `enabled=false` takes effect on the next run without producing an
> update loop.

> [!NOTE]
> The server clears TLS settings that cannot apply: without a certificate
> `ssl.forced` is dropped, without `ssl.forced` HSTS is dropped, and without
> HSTS `ssl.hsts_subdomains` is dropped. `npmplus-docker-sync` applies the same
> cascade before comparing, so a half-configured host converges instead of
> being rewritten on every event.

Full reference with examples: [docs/LABELS.md](docs/LABELS.md).

## Configuration

| Variable | Default | Description |
|---|---|---|
| `NPM_URL` | — | **Required.** API base URL, e.g. `http://npm:81`. |
| `NPM_IDENTITY` | — | **Required.** Admin e-mail (alias: `NPM_EMAIL`). |
| `NPM_SECRET` | — | **Required.** Admin password (alias: `NPM_PASSWORD`). |
| `NPM_SECRET_FILE` | — | Read the password from a file (Docker secrets). Wins over `NPM_SECRET`. |
| `NPM_TIMEOUT` | `30s` | HTTP timeout per API request. |
| `NPM_INSECURE_SKIP_VERIFY` | `false` | Accept self-signed NPM certificates. |
| `NPM_FLAVOUR` | `auto` | API dialect: `auto`, `npmplus` or `npm` (alias `NPM_FLAVOR`). See below. |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | `unix://`, `tcp://`, `npipe://` or `ssh://`. |
| `NPM_NETWORK` | — | The Docker network upstream IPs are taken from. |
| `NPM_NETWORK_STRICT` | `true` | With `NPM_NETWORK` set, skip containers that are not on it instead of using another network. |
| `RESOLVE_CONTAINER_IP` | `true` | Use container IPs instead of names as upstreams. |
| `SYNC_KINDS` | all | Resource types to manage: `proxy,redirect,stream,404`. |
| `LABEL_PREFIX` | `npm` | Label namespace. |
| `DEBOUNCE_INTERVAL` | `3s` | Quiet period after the last event. |
| `DEBOUNCE_MAX_WAIT` | `30s` | Hard cap for a continuous event stream. |
| `RESYNC_INTERVAL` | `5m` | Periodic full reconcile (`0` disables it). |
| `DELETE_ORPHANS` | `true` | Delete managed resources whose container disappeared. |
| `ADOPT_EXISTING` | `true` | Take over a pre-existing resource for a labelled key. |
| `DRY_RUN` | `false` | Log intended changes without calling the API. |
| `SHUTDOWN_TIMEOUT` | `30s` | Budget for the final flush after SIGTERM. |
| `HEALTH_ADDR` | — | Expose `/healthz` and `/readyz`, e.g. `:8080`. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |
| `LOG_FORMAT` | `text` | `text` or `json` (structured `log/slog`). |
| `LOG_PAYLOADS` | `false` | Mirror full API request bodies into the debug log (credentials redacted). |

Durations accept Go syntax (`3s`, `1m30s`); a bare number means seconds.
Tuning advice and the full reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

### NPM or NPMplus?

The two forks share their endpoints but not their request schemas, and both
validate with `additionalProperties: false` — a body built for the wrong one is
rejected in full with `400 data must NOT have additional properties`. The
differences that matter:

| | Nginx Proxy Manager | NPMplus |
|---|---|---|
| `enabled` in a write body | rejected¹ | rejected |
| Access lists | `access_list_id` (one) | `npmplus_access_list_ids` + `npmplus_access_list_type` |
| Location access lists | not applicable | mandatory on every location |
| Stream ports | integers | strings (`"8080-8090"` ranges allowed) |
| HTTP/3, gRPC upstreams | — | supported |

¹ accepted by the proxy-host schema, ignored everywhere else; this tool never
sends it and uses the `/enable` and `/disable` endpoints for all four kinds.

The flavour is detected once at startup — from an existing proxy host, falling
back to the `version` object in `GET /api/` — and logged:

```json
{"level":"INFO","msg":"detected npm api flavour","flavour":"npmplus","detected_from":"proxy host schema"}
```

Pin it with `NPM_FLAVOUR=npmplus` or `NPM_FLAVOUR=npm` if the detection ever
guesses wrong. A configuration the target flavour cannot express (HTTP/3 on
upstream NPM, several access lists on upstream NPM, a Let's Encrypt
`certificate_id=new` on an NPMplus stream) fails with a message naming the
feature instead of an opaque 400.

> [!TIP]
> Pin your NPM/NPMplus image to a version tag rather than `:latest`. The
> request schemas change between releases, and an unattended upgrade can start
> rejecting payloads that worked yesterday.

## Ownership: what gets deleted

Every resource created by this tool is stamped with
`managed_by: npmplus-docker-sync` in its NPM `meta` field.

* Resources **without** that marker are never deleted — your hand-made entries
  are safe, even if `DELETE_ORPHANS=true`. The marker of the 1.x releases
  (`npm-docker-sync`) is still recognised, so an upgrade adopts and re-stamps
  those resources instead of duplicating them.
* Resources **with** the marker are deleted as soon as no labelled container
  claims their key any more.
* If a labelled key already exists as an unmanaged resource, it is adopted and
  updated (set `ADOPT_EXISTING=false` to skip it instead).
* The final reconcile on shutdown (SIGTERM) **never deletes**. When a whole
  stack goes down with `docker compose down`, the labelled containers usually
  stop *before* this one, so a last run with deletion enabled would wipe every
  managed resource. Creates and updates are still flushed; deletion resumes on
  the next start or the next periodic resync.

Identity is the **alphabetically first domain name** per kind — and the
**incoming port** for streams. The same domain can therefore be a proxy host
and a 404 host at once without the two colliding. Change the first domain and
you get a new resource, not a rename.

> [!CAUTION]
> **Before switching `DELETE_ORPHANS` on, check the hosts that point at your
> own infrastructure** — the NPM admin UI itself, a dashboard, a status page.
> A resource this tool created during an earlier experiment keeps its marker
> for good, so once its labels are gone it is an orphan and will be deleted,
> even though it is the page you manage NPM with. Give those containers their
> labels back:
>
> ```yaml
> services:
>   npmplus:
>     labels:
>       npm.enable: "true"
>       npm.proxy.host: "npm.example.com"
>       npm.proxy.port: "81"
> ```
>
> A container without `npm.enable=true` is not evaluated at all, so its hosts
> look orphaned regardless of what else the container says.
>
> `DRY_RUN=true` lists every deletion it *would* perform — do that first:
>
> ```
> [dry-run] would delete orphaned resource key=npm.example.com id=64
> ```

Run with `DRY_RUN=true` once against a production NPM before letting it write.

## Security

The Docker socket is root-equivalent access to the host, which is why the
default deployment never hands it to this container:

```
npmplus-docker-sync ──tcp──▶ docker-socket-proxy ──unix, ro──▶ /var/run/docker.sock
   (no socket)                 CONTAINERS=1 EVENTS=1
                               PING=1 VERSION=1 POST=0
```

* The proxy allows exactly four read-only API groups — no `POST`, no `exec`,
  no `images`, no `volumes`. A compromise of `npmplus-docker-sync` cannot
  start, stop or modify anything.
* The `socket-proxy` network is `internal: true`, so the proxy has no route
  out of the host.
* The image is `FROM scratch` and runs as `USER 1000:1000` with `cap_drop: ALL`,
  `read_only: true` and `no-new-privileges`.
* The NPM password is read from a Docker secret and is redacted in every log
  line (`slog.LogValuer`).

Details, threat model and the mount-the-socket fallback:
[SECURITY.md](SECURITY.md).

## Health checks

With `HEALTH_ADDR=:8080`:

| Endpoint | Meaning |
|---|---|
| `GET /healthz` | Process is alive (`200 ok`). |
| `GET /readyz` | `200` once a reconcile ran with Docker and the NPM API reachable; `503` before the first run and while either is unreachable. |

`/readyz` tracks the dependencies, not the workload. A resource the API
rejected — a typo in one label, a feature the flavour does not have — is
reported as a counter in the body and in the reconcile result, not as
`503`: every other host is still in sync and a restart would not fix it.

```
ok: 12 managed hosts, 1 failed, last sync 2026-09-16T13:26:35Z
```

The runtime image is `FROM scratch` — there is no shell, no `curl` and no
`wget` to probe those endpoints with. The binary therefore carries its own
probe:

```bash
HEALTH_ADDR=:8080 npmplus-docker-sync healthcheck; echo $?
```

It requests `/readyz` on `HEALTH_ADDR` and exits `0` on `200`, `1` otherwise.
When `HEALTH_ADDR` is unset the health endpoint is disabled and the probe
exits `0` without checking anything, so it never fails a container that was
deliberately configured without it.

The image ships a matching `HEALTHCHECK` (30 s interval, 20 s start period,
3 retries), so `docker ps` and `docker inspect` report health out of the box:

```bash
docker inspect --format '{{.State.Health.Status}}' npmplus-docker-sync
```

Both compose files define the same check explicitly, which is what makes
`depends_on: { condition: service_healthy }` usable in your own stacks.

## Troubleshooting

<details>
<summary><b>Nothing happens when I start a labelled container</b></summary>

Run with `LOG_LEVEL=debug`. If no `docker event` lines appear, the event stream
is not reaching the tool — check `DOCKER_HOST` and, when using the socket
proxy, that `EVENTS=1` is set. If events arrive but nothing is created, the
labels are likely invalid; the parser logs the exact label and reason at
`warn`.
</details>

<details>
<summary><b>`login to http://npm:81 failed`</b></summary>

`NPM_URL` must point at the **API** port (81 in the default NPM setup, not 80
or 443). Verify the credentials with:

```bash
curl -s -X POST http://npm:81/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"identity":"admin@example.com","secret":"changeme"}'
```

NPM returns `{"token":"..."}`; NPMplus answers with a `Set-Cookie` header and
possibly an empty body. Both are supported — anything else is a credential or
URL problem.
</details>

<details>
<summary><b>`400 data must NOT have additional properties`</b></summary>

The request carried a field the server's JSON schema does not allow. Since
v1.0.0-beta.2 the payloads are built per flavour, so this normally means the
flavour was guessed wrong or your NPM/NPMplus version moved. Check which one
was detected:

```
{"msg":"detected npm api flavour","flavour":"npmplus","detected_from":"proxy host schema"}
```

Pin the right one with `NPM_FLAVOUR=npmplus` or `NPM_FLAVOUR=npm`. With
`LOG_LEVEL=debug` the rejected request is logged next to the fields it
contained, which names the offending property the API refuses to:

```
{"msg":"npm api rejected a request","method":"POST","path":"/api/nginx/proxy-hosts",
 "status":400,"request_fields":"advanced_config,domain_names,...","error":"..."}
```

`LOG_PAYLOADS=true` adds the full body (credential-looking values redacted).
If the schema really did change, please open an issue — the contract test in
`internal/npm/contract_test.go` validates every payload against the vendored
upstream schemas and needs refreshing via `make schemas`.
</details>

<details>
<summary><b>`502 Bad Gateway` on the new host</b></summary>

Upstreams default to the container's IP, which NPM can only reach if both
containers share a network. Put NPM and the target on the same network and set
`NPM_NETWORK` to its name: the IP is then taken from that network only, and a
container that is not attached to it is skipped with a warning rather than
silently pointed at an unreachable address. With `RESOLVE_CONTAINER_IP=false`
the container name is used instead, which additionally requires NPM to resolve
Docker's embedded DNS.
</details>

<details>
<summary><b>A host is updated on every event / every resync</b></summary>

The desired state and the stored state disagree about a field the server
rewrites. The two known cascades — TLS settings without a certificate, and the
`enabled` flag — are handled since v1.0.0-beta.2. For anything else, run with
`LOG_LEVEL=debug` and compare the `request_fields` of two consecutive updates,
then open an issue.
</details>

<details>
<summary><b>`container is not attached to <network>`</b></summary>

With `NPM_NETWORK` set, upstream IPs are taken from that network only. Either
attach the container to it, or set `npm.<kind>.forward_host` explicitly. If you
want the old fall-through behaviour back, set `NPM_NETWORK_STRICT=false` — but
expect upstreams NPM cannot route to.
</details>

<details>
<summary><b>Streams or 404 hosts are not created</b></summary>

Check `SYNC_KINDS` — it limits which collections are managed. A `warn` line
saying *"collection is not available on this npm instance"* means the API
returned 404 for that endpoint; the rest keeps working.
</details>

<details>
<summary><b>`permission denied` on /var/run/docker.sock</b></summary>

Only relevant for `docker-compose.simple.yml`: the container user needs the
host's docker group. Set `DOCKER_GID=$(getent group docker | cut -d: -f3)` in
your `.env`. The socket-proxy setup avoids this entirely.
</details>

<details>
<summary><b>A certificate is requested over and over</b></summary>

That would be a bug — once NPM issues a certificate for a `certificate_id=new`
resource, the assigned id is adopted on the next reconcile and the fingerprint
stays stable. If you see repeated issuance, please open an issue with debug
logs (Let's Encrypt rate limits are unforgiving).
</details>

## Project layout

```
main.go                     wiring, signals, health endpoint
internal/config/            environment parsing + validation
internal/npm/               API client and the four resource models
internal/docker/            event listener, indexed label parser, IP resolution
internal/syncer/            debouncer, per-kind state cache, reconcile worker
docs/                       architecture, labels, configuration
```

## Contributing

Bug reports, labels you are missing and PRs are welcome — see
[CONTRIBUTING.md](CONTRIBUTING.md). `make check` must pass, new behaviour needs
a test, and commits follow [Conventional Commits](https://www.conventionalcommits.org/).

## Roadmap

- [ ] Custom access lists from labels
- [ ] Prometheus metrics endpoint
- [ ] Docker Swarm service labels
- [ ] Certificate management (create/renew) from labels

## Acknowledgements

Built on [Nginx Proxy Manager](https://nginxproxymanager.com/) by jc21,
[NPMplus](https://github.com/ZoeyVid/NPMplus) by ZoeyVid and
[docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) by
Tecnativa. Inspired by the label-driven workflow of Traefik and by
[Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync).

Not affiliated with or endorsed by any of these projects.

## License

[MIT](LICENSE)
