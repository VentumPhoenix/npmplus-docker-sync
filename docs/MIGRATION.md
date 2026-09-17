# Migration guide

## From Redth/npm-docker-sync

[Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync) is where this
project's label syntax comes from. Since v1.0.0-beta.3 its labels and
environment variables work here unchanged, so a migration is mostly a matter of
swapping the image.

### Labels

Nothing to change. Both index positions (`npm.proxy.1.domains` and
`npm.1.proxy.domains`), both namespace separators (`npm.` and `npm-`) and the
shorthand `npm.domains` are understood, and inside a field name `.`, `_` and
`-` are interchangeable.

| Redth label | Canonical here |
|---|---|
| `npm.proxy.domains` / `.domain` / `npm.domains` | `domains` |
| `npm.proxy.host` | `forward_host` |
| `npm.proxy.port` | `forward_port` (optional, guessed from the exposed ports) |
| `npm.proxy.scheme` | `forward_scheme` |
| `npm.proxy.ssl.force` | `ssl.forced` |
| `npm.proxy.ssl.certificate.id` | `certificate` (also takes `auto`, a domain or `name:…`) |
| `npm.proxy.ssl.http2` / `.hsts` / `.hsts.subdomains` | `ssl.http2` / `ssl.hsts` / `ssl.hsts_subdomains` |
| `npm.proxy.caching` | `caching` |
| `npm.proxy.block_common_exploits` | `block_exploits` |
| `npm.proxy.websockets` | `websockets` |
| `npm.proxy.accesslist.id` | `access_list` (also takes names) |
| `npm.proxy.advanced.config` | `advanced_config` |
| `npm.stream.incoming.port` | `incoming_port` |
| `npm.stream.forward.host` / `.port` | `forward_host` / `forward_port` |
| `npm.stream.forward.tcp` / `.udp` | `tcp` / `udp` |
| `npm.stream.ssl` | `certificate` |
| `npm.certificate_id` | `certificate` |

### Environment

| Redth variable | Status |
|---|---|
| `NPM_EMAIL` | accepted, canonical name `NPM_IDENTITY` |
| `NPM_PASSWORD` | accepted, canonical name `NPM_SECRET` (use `NPM_PASSWORD_FILE` for a secret) |
| `NPM_CONTAINER_NAME` | accepted — the networks of that container are used for upstream resolution |
| `NPM_PROXY_*` | accepted, and extended to every field and every kind (see [FIELDS.md](FIELDS.md)) |
| `DOCKER_HOST_IP` | not yet used; upstreams over the host IP arrive with the upstream modes in beta.4 |
| `SYNC_INSTANCE_ID` | not yet used; multi-instance ownership arrives in beta.4 |

Each alias that is actually used is logged once at start-up with the canonical
name it maps to.

### Taking over existing hosts

Hosts created by Redth carry `managed_by: npm-docker-sync` in their `meta`.
They are **not** treated as ours by default: both tools can run against the
same NPM instance, and silently adopting the other's hosts would have them
delete each other's work.

Run both side by side first, then migrate:

```yaml
environment:
  MIGRATE_FROM_REDTH: "true"
```

On the next reconcile every Redth host whose domain matches a labelled
container is re-stamped as `managed_by: npmplus-docker-sync`. Redth's own
bookkeeping (`container_id`, `sync_instance_id`, `npm_url`, `created_at`) is
kept in the meta, so nothing is lost if you decide to go back.

Hosts that no container claims are left alone: an orphan is only deleted when
it carries *our* marker.

Recommended order:

1. stop the Redth container,
2. start `npmplus-docker-sync` with `DRY_RUN=true` and `MIGRATE_FROM_REDTH=true`
   and read the log,
3. turn `DRY_RUN` off.

## From v1.0.0-beta.2 to v1.0.0-beta.3

### `host` is now the upstream, not the domain

This is the one change that needs action.

```yaml
# before (beta.1 / beta.2)
npm.proxy.host: "app.example.com"
npm.proxy.port: "8080"

# now
npm.proxy.domains: "app.example.com"
npm.proxy.port: "8080"            # optional, guessed from the exposed ports
npm.proxy.host: "10.0.0.5"        # optional, the upstream target
```

A proxy host or stream whose `host` looks like a domain and that has no
`domains` is rejected with a message naming both labels, so nothing is created
for the wrong name. Redirection and 404 hosts have no upstream, so `host`
remains an alias for `domains` there.

### Containers no longer need `npm.enable`

Every container carrying at least one label of the namespace is managed.
`npm.enable=false` opts out. Set `NPM_EXPOSED_BY_DEFAULT=false` to keep the old
behaviour.

### New defaults

| Field | beta.2 | beta.3 |
|---|---|---|
| `certificate` | none | `auto` — the best matching certificate |
| `ssl.forced` | `false` | `auto` — on as soon as a certificate is attached |
| `ssl.http2` | `false` | `true` |
| `ssl.http3` | `false` | `true` (NPMplus only) |
| `forward_port` | required | `auto` — from the container's exposed ports |
| `crowdsec_appsec`, `request_buffering`, `response_buffering` | NPMplus default | active |

Existing hosts are updated accordingly on the first run. Pin the old behaviour
per field if you would rather not have that:

```yaml
NPM_DEFAULT_CERTIFICATE: "none"
NPM_PROXY_SSL_FORCE: "false"
NPM_PROXY_HTTP2: "false"
NPM_DEFAULT_HTTP3: "false"
```

Run once with `DRY_RUN=true` to see exactly what would change.

### HTTP/3 against upstream NPM no longer fails

NPMplus-only fields used to be refused when talking to upstream
nginx-proxy-manager. They are now dropped from the request and reported once
per resource, so the new defaults do not break that flavour.

## From v1.0.0-beta.3 to v1.0.0-beta.4

Nothing to change: no labels or variables were renamed. What changed is *when
resources are deleted*, and all of it is in your favour.

### Stopped containers no longer lose their host

A container that is stopped or crash-looping used to have its resources
deleted, because only running containers were listed. Now `NPM_ON_STOP`
decides:

| Value | Behaviour |
|---|---|
| `disable` (default) | The host is disabled and keeps its id. |
| `keep` | Nothing happens at all. |
| `delete` | The old behaviour. |

`NPM_STOP_GRACE` (default `1m`) swallows a restart or a `docker compose up`
recreate entirely, so neither produces a disable/enable cycle.

Set `NPM_ON_STOP=delete` and `NPM_STOP_GRACE=0` to keep the beta.3 behaviour.

### A broken label no longer deletes anything

A container whose labels cannot be parsed is now protected: its resources are
kept and the problem is logged. See [DELETION.md](DELETION.md).

### New: the delete guard

`DELETE_GUARD` (default `0.5`) stops a run that would delete more than half of
the managed resources, and any run that would delete all of them or that saw no
containers at all. `DELETE_GUARD=off` restores the unguarded behaviour.

### New: sync instances

Every resource is now stamped with `managed_instance`. Resources carrying
another instance's id are never touched, so several Docker hosts can drive one
NPM. The id defaults to the Docker daemon id; `SYNC_INSTANCE_ID` overrides it.

Existing resources have no stamp yet and are adopted by the first instance that
sees them — so if you plan to run several, set `SYNC_INSTANCE_ID` on all of them
*before* the first run, and let each one see only its own containers.

### New subcommands

```bash
npmplus-docker-sync validate   # parse the labels, report, never write
npmplus-docker-sync sync       # one reconcile, then exit with a status code
```
