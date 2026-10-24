# Security

## Reporting a vulnerability

Please **do not** open a public issue. Use
[private vulnerability reporting](https://github.com/VentumPhoenix/npmplus-docker-sync/security/advisories/new)
with a description, the affected version, steps to reproduce and the impact.

You get an answer within 72 hours and an update at least weekly. Fixes ship as
a patch release with a security advisory; reporters are credited unless they
prefer not to be. Only the latest release is supported.

## What is worth protecting

The tool holds **read access to the Docker API** and **admin credentials for
NPM**. The default setup keeps both as small as possible.

### Docker access

The Docker socket is root on the host — even mounted `:ro`, because `:ro` only
protects the socket *file*, not the API behind it. So the recommended
`docker-compose.yml` puts a
[docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) in
between and never mounts the socket into this container:

```
npmplus-docker-sync ──tcp──▶ docker-socket-proxy ──read-only──▶ /var/run/docker.sock
```

The proxy allows exactly what the tool needs:

| Proxy flag | Endpoint | Why |
|---|---|---|
| `CONTAINERS=1` | `GET /containers/json` | labels, networks, ports, state |
| `EVENTS=1` | `GET /events` | react to start/stop |
| `PING=1`, `VERSION=1` | `GET /_ping`, `GET /version` | connection check, API version |
| `INFO=1` *(recommended)* | `GET /info` | daemon id = default `SYNC_INSTANCE_ID` |
| `POST=0` | — | **no write call at all** |

Everything else stays `0`, so a compromised sync container cannot start, stop
or change anything. Events are filtered on the server (`type=container`).

Note: `CONTAINERS=1` can read the labels **and environment** of every container
on the host — true for any such tool, worth knowing if containers keep secrets
in `environment:`.

Mounting the socket directly (`docker-compose.simple.yml`) works, but keep it
`:ro`, drop all capabilities and treat it as root access.

### NPM credentials

- Use `NPM_SECRET_FILE` (a Docker secret), not `NPM_SECRET` — environment
  variables leak through `docker inspect`.
- Credentials never reach the logs; secrets in request bodies are masked even
  with `LOG_PAYLOADS=true`.
- Keep `NPM_URL` on an internal network. Across an untrusted network, use
  `https://` and leave `NPM_INSECURE_SKIP_VERIFY=false`.

### The container

One static binary on `scratch` — no shell, no package manager.

| Control | Setting |
|---|---|
| User | `1000:1000`, non-root |
| Filesystem | `read_only: true` |
| Capabilities | `cap_drop: ALL`, `no-new-privileges` |
| Network | internal Docker networks, nothing published |

### Supply chain

- Runtime dependencies: the Docker SDK, `golang.org/x/net` and the Go standard
  library. The tests additionally use a JSON schema validator.
- Builds use the newest Go patch release; Dependabot updates modules, actions
  and base images.
- CI runs `govulncheck` and CodeQL on every push and weekly.
- Releases are built by GitHub Actions with provenance attestation and an SBOM.

## Checklist

- [ ] `docker-compose.yml` with the socket proxy, not the raw socket
- [ ] `NPM_SECRET_FILE` instead of an inline password
- [ ] socket-proxy network `internal: true`
- [ ] NPM admin port 81 not reachable from the internet
- [ ] first run against an existing NPM with `DRY_RUN=true`
- [ ] image pinned to a version tag
