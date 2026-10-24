# Upgrading

Newest first. Most upgrades need no action — the ones that do say so at the top.

- [beta.9 → beta.10](#from-v100-beta9-to-v100-beta10) — **update this tool before NPMplus**
- [beta.4 → beta.9](#from-v100-beta4-to-v100-beta9) · [beta.3 → beta.4](#from-v100-beta3-to-v100-beta4) · [beta.2 → beta.3](#from-v100-beta2-to-v100-beta3) — `host` changed meaning
- [From Redth/npm-docker-sync](#from-redthnpm-docker-sync)

## From v1.0.0-beta.9 to v1.0.0-beta.10

No labels, variables or defaults changed. **Action: update this tool before you
update NPMplus.**

The next NPMplus release empties the meta object of every host — the place
where releases up to beta.9 recorded which hosts are theirs. Beta.10 also writes
a comment line at the top of each host's advanced config, which survives that:

```
# npmplus-docker-sync: managed from Docker labels, do not edit or remove this line | v=1 key=app.example.com container=web index=0 ...
```

1. Update to beta.10 and let it run once. Every managed host is updated **once**
   to receive the line — the hosts of stopped containers too.
   (`DRY_RUN=true` shows it as `ownership_marker: unset → true`.)
2. Then update NPMplus whenever you like.

Leave the line alone in the NPM UI. A host without it counts as hand-made.

**Updated NPMplus first?** Then those hosts have lost their owner. Nothing is
deleted: hosts a label still claims are adopted and marked on the first run;
hosts whose container is gone for good are no longer cleaned up — delete them
by hand.

Also in this release, all applied automatically on the first run:

- Upstream NPM gets the marker too (its streams keep the meta object).
- Redirections with `scheme: auto` are written as `$scheme` on NPMplus —
  `auto` produced a broken `auto://…` redirect there.
- Streams with `proxy_protocol: v2` work on NPMplus releases that only know
  on/off (stored as on, with a warning).
- On NPMplus without the meta object, the retired switches (`websockets`,
  `ssl.http2`, `caching`, `block_exploits`, `trust_forwarded_proto`) are left
  out, with a warning if a label set one.

## From v1.0.0-beta.4 to v1.0.0-beta.9

Nothing to change. These releases fixed compatibility with NPM 2.11 and with
NPMplus detection, and added `group`.

- The `v1.0.0-beta.4` tag sits on the beta.3 commit; the deletion safeguards
  below first shipped in beta.5.
- A host without a certificate now asks for HTTP/2 off, as the server stores it
  anyway — one last update per such host.

## From v1.0.0-beta.3 to v1.0.0-beta.4

Nothing to change; deletion became much safer:

- **Stopped containers keep their host.** `NPM_ON_STOP` decides: `disable`
  (default), `keep` or `delete`. `NPM_STOP_GRACE` (1 minute) swallows restarts.
  The old behaviour: `NPM_ON_STOP=delete`, `NPM_STOP_GRACE=0`.
- **Broken labels protect their hosts** instead of deleting them.
- **Delete guard:** `DELETE_GUARD` (0.5) stops implausible mass deletions.
- **Sync instances:** resources are stamped with an instance id (default: the
  Docker daemon id, needs `INFO=1` on the socket proxy). Running several
  instances? Set `SYNC_INSTANCE_ID` on each *before* their first run.
- New commands: `validate` and `sync`.

## From v1.0.0-beta.2 to v1.0.0-beta.3

**Action needed: `host` is now the upstream, not the domain.**

```yaml
# before
npm.proxy.host: "app.example.com"
# now
npm.proxy.domains: "app.example.com"
npm.proxy.host: "10.0.0.5"     # optional upstream
```

A host whose `host` looks like a domain and that has no `domains` is rejected
with a clear message. Further changes:

- `npm.enable` is no longer needed — every container with `npm.*` labels is
  managed (`NPM_EXPOSED_BY_DEFAULT=false` restores opt-in).
- New defaults, applied on the first run: `certificate: auto`,
  `ssl.forced: auto`, HTTP/2 and HTTP/3 on, port from the image, NPMplus
  security features on. Keep the old ones with e.g.
  `NPM_DEFAULT_CERTIFICATE=none`, `NPM_PROXY_SSL_FORCE=false`,
  `NPM_PROXY_HTTP2=false`, `NPM_DEFAULT_HTTP3=false`.

## From Redth/npm-docker-sync

Swap the image — Redth's labels and variables work unchanged: both index
positions (`npm.proxy.1.domains`, `npm.1.proxy.domains`), both separators
(`npm.`, `npm-`), the shorthand `npm.domains`, and `NPM_EMAIL`,
`NPM_PASSWORD`, `NPM_CONTAINER_NAME`, `NPM_PROXY_*`, `SYNC_INSTANCE_ID`.
`DOCKER_HOST_IP` is ignored (host-IP upstreams are on the roadmap). Every alias
in use is logged once at start-up with its canonical name; the full mapping is
in [FIELDS.md](FIELDS.md).

**Taking over Redth's hosts.** They carry `managed_by: npm-docker-sync` and are
left alone by default, so both tools can run side by side. To migrate:

1. stop the Redth container,
2. start this tool with `DRY_RUN=true` and `MIGRATE_FROM_REDTH=true`, read the log,
3. turn `DRY_RUN` off.

Every Redth host a label claims is then stamped as ours; Redth's own meta keys
are kept, so you can go back. Hosts no label claims are left alone.
