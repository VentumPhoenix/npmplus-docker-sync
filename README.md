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
2. the IP in the network named by `NPM_NETWORK`, else
3. the IP in a network this sync container is itself attached to, else
4. the IP of the first network (alphabetically), else
5. the container name as a last resort.

Set `RESOLVE_CONTAINER_IP=false` to go back to name-based upstreams, or
`npm.<index>.<kind>.resolve_ip=false` for a single resource.

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
| `access_list_id` | `0` | Attach an NPM access list. |
| `advanced_config` | – | Raw nginx snippet for this host. |
| `location.<n>.path` | – | Custom location block, e.g. `/api` (see below). |
| `location.<n>.forward_host` / `.forward_port` / `.forward_scheme` | inherited | Upstream of that location block. |
| `location.<n>.advanced_config` | – | Raw nginx snippet inside the location. |
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
| `ssl.hsts` | `false` | Enable HSTS. |
| `ssl.hsts_subdomains` | `false` | Include subdomains in HSTS. |
| `letsencrypt.email` | – | Required with `certificate_id=new`. |
| `letsencrypt.agree` | `false` | Must be `true` with `certificate_id=new`. |
| `letsencrypt.dns_challenge` | `false` | Use a DNS-01 challenge. |
| `letsencrypt.dns_provider` | – | e.g. `cloudflare` (with DNS-01). |
| `letsencrypt.dns_credentials` | – | Provider credentials (with DNS-01). |
| `letsencrypt.propagation_seconds` | `0` | DNS propagation wait. |

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
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | `unix://`, `tcp://`, `npipe://` or `ssh://`. |
| `NPM_NETWORK` | — | Docker network preferred when resolving upstream IPs. |
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

Durations accept Go syntax (`3s`, `1m30s`); a bare number means seconds.
Tuning advice and the full reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md).

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
| `GET /readyz` | `200` after a successful reconcile, `503` while the last one failed. |

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
<summary><b>`502 Bad Gateway` on the new host</b></summary>

Upstreams default to the container's IP, which NPM can only reach if both
containers share a network. Put NPM and the target on the same network and set
`NPM_NETWORK` to its name so the right IP is picked. With
`RESOLVE_CONTAINER_IP=false` the container name is used instead, which
additionally requires NPM to resolve Docker's embedded DNS.
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
