# Deployment examples

Ready-to-run Compose stacks. Pick your proxy manager and how far you want to
go — each folder has a `compose.yaml` and an `.env.example`:

```bash
cd deploy/npmplus/2-standard     # or any other folder below
cp .env.example .env             # fill in the values
docker compose up -d
```

The hardened stacks need one more step, a secret file for the tool's password —
the commands are at the top of their `compose.yaml`.

|  | [NPM](https://nginxproxymanager.com) | [NPMplus](https://github.com/ZoeyVid/NPMplus) |
|---|---|---|
| **1 · Simple** — try it out, home lab | [npm/1-simple](npm/1-simple/compose.yaml) | [npmplus/1-simple](npmplus/1-simple/compose.yaml) |
| **2 · Standard** — recommended | [npm/2-standard](npm/2-standard/compose.yaml) | [npmplus/2-standard](npmplus/2-standard/compose.yaml) |
| **3 · Hardened** — exposed servers | [npm/3-hardened](npm/3-hardened/compose.yaml) | [npmplus/3-hardened](npmplus/3-hardened/compose.yaml) |
| **Already running NPM?** | [existing-npm](existing-npm/compose.yaml) | same file |

Then label your apps: [example-app](example-app/compose.yaml).

## What each level adds

| | 1 · Simple | 2 · Standard | 3 · Hardened |
|---|:---:|:---:|:---:|
| NPM / NPMplus + this tool | ✅ | ✅ | ✅ |
| Tool: non-root, read-only, no capabilities | ✅ | ✅ | ✅ |
| Docker access | socket `:ro` | socket proxy | allowlist socket proxy, 5 endpoints |
| NPM password for the tool | `.env` | `.env` | secret file, not in `docker inspect` |
| Admin UI on port 81 | published | via its domain only | via its domain only |
| Healthchecks, start order, log rotation | – | ✅ | ✅ |
| Fixed IPs, containers isolated from each other (ICC off + firewall) | – | – | ✅ |
| Memory and process limits | – | – | ✅ |
| CrowdSec with WAF (AppSec) | – | – | NPMplus only |
| Containers | 2 | 3 | NPM 4 · NPMplus 6 |
| App network | `npm` | `npm` | `npm-frontend` |

**Simple** mounts the Docker socket. Even read-only that is root-equivalent
access to the host ([SECURITY.md](../SECURITY.md#docker-access)), which is why
everything from **Standard** on goes through a socket proxy.

**Hardened** assumes a Linux host with iptables (nft or legacy). On the shared
`npm-frontend` network only the proxy manager may open connections — an app
that gets compromised cannot reach the other apps, CrowdSec or the tool. The
`npm-firewall` container keeps those rules in place and reports itself
unhealthy the moment one is missing.

## Good to know

- **The tool logs in with the admin account** from `.env`. Change the
  password in the UI → change `NPM_ADMIN_PASSWORD` too (hardened:
  `secrets/npm_password.txt`), then `docker compose up -d`. Keep `.env` at
  `chmod 600` and the `secrets/` folder at `700`.
- **The admin UI gets a proxy host** through the labels on the proxy manager
  container (`NPM_UI_DOMAIN`). Keep those labels — removing them makes the tool
  delete that host.
- **Images:** the proxy managers use moving tags (`:latest`, `:2`) so the
  examples start out current. Pin exact versions in production, and update this
  tool before NPMplus ([why](../docs/MIGRATION.md)).
- **Apps in other Compose projects** join the network as external:

  ```yaml
  networks:
    npm:
      name: npm          # npm-frontend for the hardened stacks
      external: true
  ```

- **Every variable** of the tool: [docs/CONFIGURATION.md](../docs/CONFIGURATION.md).
  Defaults for labels (`NPM_DEFAULT_GROUP`, `NPM_PROXY_SSL_FORCED`, …):
  [docs/FIELDS.md](../docs/FIELDS.md).
