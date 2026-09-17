# Security Policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Use [GitHub's private vulnerability reporting](https://github.com/VentumPhoenix/npmplus-docker-sync/security/advisories/new)
instead. Include a description, the affected version, reproduction steps and
the impact you see.

You can expect an acknowledgement within 72 hours and a status update at least
every seven days. Fixes are released as a patch version together with a GitHub
Security Advisory; reporters are credited unless they prefer otherwise.

## Supported versions

| Version | Supported |
|---|---|
| latest minor release | ✅ |
| older releases | ❌ (upgrade first) |

## Threat model

`npmplus-docker-sync` holds two things worth attacking: **read access to the
Docker API** and **admin credentials for Nginx Proxy Manager**. The design
minimises both.

### 1. Docker API access

Mounting `/var/run/docker.sock` into a container — even `:ro` — is effectively
root on the host: anyone who can talk to that socket can start a privileged
container and mount `/`. The `:ro` flag only prevents writing to the socket
*file*, not writing through the API.

The recommended deployment therefore puts a
[docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy)
in between (`docker-compose.yml`):

```
npmplus-docker-sync ──tcp──▶ docker-socket-proxy ──unix, ro──▶ /var/run/docker.sock
   no socket mount         CONTAINERS=1  EVENTS=1
                           PING=1  VERSION=1  POST=0
```

Endpoints this tool needs, and nothing else:

| Endpoint | Proxy flag | Required | Purpose |
|---|---|---|---|
| `GET /containers/json` | `CONTAINERS=1` | yes | read the labels, networks, ports and state of every container |
| `GET /events` | `EVENTS=1` | yes | react to lifecycle changes |
| `GET /_ping` | `PING=1` | yes | startup connectivity check |
| `GET /version` | `VERSION=1` | yes | API version negotiation |
| `GET /info` | `INFO=1` | recommended | the daemon id, used as the default `SYNC_INSTANCE_ID`. Without it there is no instance id at all: fine for a single instance, but several instances against one NPM then need an explicit `SYNC_INSTANCE_ID` each |

Everything else stays at `0`. In particular:

* `POST=0` — the proxy rejects every write call, so a compromised
  `npmplus-docker-sync` cannot create, modify, start or stop containers.
* `EXEC`, `IMAGES`, `NETWORKS`, `VOLUMES`, `SECRETS`, `SERVICES`, `SWARM`,
  `SYSTEM`, `AUTH`, `BUILD`, `COMMIT`, `CONFIGS`, `DISTRIBUTION`, `NODES`,
  `PLUGINS`, `SESSION`, `TASKS` — all unused. The tool never calls them.

Note that `CONTAINERS=1` is enough to read the labels *and* the environment of
every container on the host. That is not specific to this tool, but it is worth
knowing before you point it at a daemon with secrets in `environment:`.

The tool additionally applies **server-side event filters** (`type=container`,
`event=start,die,stop,destroy,rename,update`), so it never even receives
unrelated event data.

Minimal proxy configuration:

```yaml
docker-socket-proxy:
  image: tecnativa/docker-socket-proxy
  environment:
    CONTAINERS: "1"
    EVENTS: "1"
    PING: "1"
    VERSION: "1"
    INFO: "1"    # recommended, for a stable instance id
    POST: "0"
  volumes:
    - /var/run/docker.sock:/var/run/docker.sock:ro
```

If you accept the risk and mount the socket directly
(`docker-compose.simple.yml`), keep it read-only, drop all capabilities and
never expose the container to untrusted input.

### 2. NPM credentials

* Prefer `NPM_SECRET_FILE` (Docker/Podman secret) over `NPM_SECRET` in the
  environment — environment variables leak through `docker inspect`, crash
  dumps and process listings.
* Create a **dedicated NPM user** for the sync tool if your NPM version
  supports per-user permissions, instead of reusing the primary admin.
* Credentials are never written to logs: the config implements
  `slog.LogValuer` and redacts the secret, and API error bodies are truncated.
* Sessions are refreshed before expiry and re-established after a `401`, so no
  long-lived token sits in memory longer than necessary.
* Keep `NPM_URL` on an internal Docker network. If it must traverse an
  untrusted network, use `https://` and leave `NPM_INSECURE_SKIP_VERIFY=false`.

### 3. Container hardening

The published image contains a single static binary — no shell, no package
manager, no interpreter:

| Control | Setting |
|---|---|
| Base image | `scratch` |
| User | `1000:1000` (non-root, enforced in the Dockerfile) |
| Filesystem | `read_only: true` |
| Capabilities | `cap_drop: ALL` |
| Privilege escalation | `no-new-privileges:true` |
| Network | internal Docker networks only, nothing published |

### 4. Supply chain

* Dependencies: the official Docker SDK and the Go standard library. Nothing
  else is imported directly.
* Dependabot keeps Go modules, GitHub Actions and base images current.
* CI runs `govulncheck` and CodeQL on every push and weekly.
* Releases are built by GitHub Actions with provenance attestation and an SBOM.

## Hardening checklist

- [ ] Use `docker-compose.yml` (socket proxy), not the raw socket
- [ ] `NPM_SECRET_FILE` instead of an inline password
- [ ] `socket-proxy` network marked `internal: true`
- [ ] NPM admin port (81) not published to the internet
- [ ] `DRY_RUN=true` for the first run against an existing NPM instance
- [ ] Image pinned to a tag or digest, updated regularly
