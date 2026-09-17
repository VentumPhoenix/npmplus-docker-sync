# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-beta.4] - 2026-09-16

**Deletion is now hard to trigger by accident.** Five situations in which this
tool could remove a production host are fixed, and a guard stops the ones
nobody thought of yet. See [docs/DELETION.md](docs/DELETION.md).

### Fixed

- **A typo in a label no longer deletes the host.** A container whose labels
  could not be parsed produced no resources, so its hosts looked orphaned and
  were deleted - `npm.proxy.port: "80a"` was enough to take a host down. Such a
  container is now recorded as *protected*: every resource whose meta names it
  survives the run, and the offending label is logged.
- **A stopped or crash-looping container no longer deletes its host.** Only
  running containers were listed, so `docker stop`, a failed update or a crash
  loop removed the host - and the restart created a new one with a new id.
  Containers are now listed regardless of state and `NPM_ON_STOP` decides:
  `disable` (the default), `keep` or `delete`. `NPM_STOP_GRACE` (default `1m`)
  swallows restarts and recreates entirely. A stopped container's resource is
  never reconfigured, only enabled or disabled, so its identity survives.
- **Several sync instances no longer delete each other's hosts.** Ownership was
  decided by the `managed_by` marker alone, so two Docker hosts driving one NPM
  each saw the other's resources as orphans. Every resource now carries a
  `managed_instance` stamp (`SYNC_INSTANCE_ID`, defaulting to the Docker daemon
  id); resources of another instance are never updated and never deleted.
  Resources written by earlier releases carry no stamp and are adopted once.
- **A mass deletion is refused.** `DELETE_GUARD` (default `0.5`) caps the share
  of the managed resources one run may delete; a run that would delete all of
  them, that saw no containers at all, or that finds resources created with a
  different `LABEL_PREFIX` is stopped and reported with the reason. That covers
  the accidents a reconciler cannot tell from an intention: a changed prefix,
  `NPM_EXPOSED_BY_DEFAULT=false`, a socket proxy answering with an empty list.
  `DELETE_GUARD_MIN` (default 3) keeps small setups usable, `DELETE_GUARD=off`
  disables the guard.
- **A broken Docker event stream is visible.** The listener reconnects
  silently, so a disconnected stream left `/readyz` green while the tool only
  reacted on the periodic resync. The stream state is now part of
  `worker.Status()`, reported by `/readyz` (`503` after two minutes down) and
  by `/status` and `/metrics`.
- **`ssl.http3` no longer warns on every host against upstream NPM.** Its
  default is `true`, so every proxy, redirect and 404 host reported an NPMplus
  field the user never asked for. Only fields a *label* set are reported now,
  and the NPMplus fields are stripped from the resource before it is compared,
  which also removes a permanent "update on every run" for that flavour.
- **`npm.proxy.location_config` was parsed as a malformed location block.**
  After separator normalisation it looks like `location.config`; the field
  table is now consulted first.
- **`ProxyHost.npmplusOnly()` was incomplete** (`access_list_type`,
  `auth_request_upstream`). A test now walks every `Plus` field of the table
  and checks that it is both reported and stripped.
- **Label normalisation was not idempotent** for names with stray spaces, and
  a domain like `".."` produced a resource without an identity. Both were found
  by the new fuzz targets.

### Added

- **`/status` and `/metrics`.** `/status` is a JSON document listing every
  managed resource with its container, id, certificate, enabled state, last
  error and backoff; `/metrics` is a Prometheus exposition with runs, errors,
  created/updated/deleted/failed/skipped counters, blocked deletions, managed
  resources per kind, certificate matches per class and the event stream state.
- **`validate` and `sync` subcommands.** `validate` parses the labels of the
  running containers - or of a rendered compose configuration
  (`docker compose config --format json`), in which case it needs neither
  Docker nor NPM - reports what they would produce and exits non-zero on a
  broken set. `sync` performs exactly one reconcile and exits with a status
  code, which is what a pipeline or a test harness wants.
- **A field diff in dry-run and drift reports.** `would update` now names the
  fields (`forward_port: 80 → 8080`), and a managed resource that was changed
  in the NPM UI is logged with the diff before it is overwritten.
- **Flavour re-detection at runtime.** A `400 ... additional properties` from a
  server whose dialect was auto-detected triggers a re-probe, and the periodic
  resync notices a changed server version - so upgrading NPM or NPMplus under a
  running process is picked up instead of failing forever.
- **An integration suite** (`make integration`) that runs the real binary
  against real NPM and NPMplus containers: every resource kind created, updated
  and deleted with an HTTP check through nginx, stop/start/restart/rename,
  adoption, the Redth migration, the typo and prefix safeguards, two instances,
  `DRY_RUN`, an NPM restart mid-run, the socket proxy, `NPM_SECRET_FILE`,
  certificates (upload, wildcard vs exact, poll) and access lists by name. It
  runs in CI against the minimum supported NPM, the current NPM and NPMplus on
  every pull request, and against the moving tags nightly.
- **Fuzz targets** for the label grammar (`make fuzz`) and a convergence
  property test that reconciles twice - and once more from a cold cache -
  against a fake that stores what the *payload* would have written.
- **Version-pinned schema vendoring.** `internal/npm/testdata/schema/<flavour>/<ref>/`
  holds one directory per supported release, configured in
  `scripts/schema-refs.json`; the contract test validates every payload against
  each of them, and the weekly drift job additionally checks the latest
  releases.
- **Documentation:** [DELETION.md](docs/DELETION.md) (when something is
  deleted, and what stops it), [COMPATIBILITY.md](docs/COMPATIBILITY.md)
  (supported versions and the 1.0 compatibility promise), a generated
  NPMplus-only field table, the socket proxy permissions in
  [SECURITY.md](SECURITY.md), and an upgrade guide in
  [MIGRATION.md](docs/MIGRATION.md).

### Changed

- `RESOLVE_CONTAINER_IP` gained the per-resource label back (`resolve_ip`),
  which beta.3 had dropped from the field table.
- Coverage is measured with `-coverpkg=./...` and a minimum of 75% is enforced
  in CI.

## [1.0.0-beta.3] - 2026-09-16

**Labels from [Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync)
work unchanged, certificates pick themselves, and one label is enough for a
complete host.** See [docs/MIGRATION.md](docs/MIGRATION.md).

### Breaking

- **`host` is the upstream, not the domain.** The domain is `domains` (alias
  `domain`), and `host` is the target to forward to - the meaning it has in
  Redth's labels. A proxy host or stream whose `host` looks like a domain and
  that has no `domains` is rejected with a message naming both labels, instead
  of quietly creating a host for the wrong name. Redirection and 404 hosts have
  no upstream, so `host` remains an alias for `domains` there.
- **`npm.enable` is no longer required.** Every container carrying at least one
  label of the namespace is managed; `npm.enable=false` opts out.
  `NPM_EXPOSED_BY_DEFAULT=false` restores the old opt-in behaviour.
- **New defaults.** `certificate` and `ssl.forced` default to `auto`,
  `ssl.http2` and `ssl.http3` to `true`, `forward_port` to `auto`, and the
  NPMplus switches `crowdsec_appsec`, `request_buffering` and
  `response_buffering` to active. Existing hosts are updated accordingly on the
  first run - use `DRY_RUN=true` first, and pin the old values with
  `NPM_DEFAULT_*` / `NPM_<KIND>_*` if you prefer them.
- **Hosts created by Redth are no longer adopted automatically.** Its marker
  (`managed_by: npm-docker-sync`) used to be recognised as a legacy marker of
  this tool, which would have let two running tools delete each other's hosts.
  Migration is now explicit: `MIGRATE_FROM_REDTH=true`.

### Added

- **Redth label compatibility.** Both index positions
  (`npm.proxy.1.domains` and `npm.1.proxy.domains`), both namespace separators
  (`npm.` and `npm-`), the shorthand `npm.domains`, and `.`/`_`/`-` as
  interchangeable separators inside a field name. Every entry of Redth's label
  table has a unit test.
- **Redth environment compatibility.** `NPM_EMAIL`, `NPM_PASSWORD`,
  `NPM_CONTAINER_NAME` (its networks are used for upstream resolution) and the
  `NPM_PROXY_*` defaults. Each alias that is used is logged once with the
  canonical name it maps to.
- **Automatic certificate selection.** `certificate` accepts `auto` (the
  default), an id, a domain, `name:<nice name>`, `new` or `none`. `auto` ranks
  the certificates NPM holds - exact, exact+SANs, mixed, wildcard - breaking
  ties by remaining validity and id, skipping expired entries, deleted ones and
  NPMplus client CAs (`provider: mtls`). Wildcards cover exactly one label
  (RFC 6125) and internationalised domains are compared as punycode. A
  certificate that already fits is kept unless a candidate matches in a better
  class, and the list is polled every `CERTIFICATE_POLL_INTERVAL` (default
  `1m`) so a certificate created in the UI reaches its hosts without a restart.
  `NPM_CERTIFICATE_PARTIAL` decides what happens when no certificate covers
  every domain, `NPM_CERTIFICATE_AUTO_CREATE` requests one when nothing
  matches.
- **Global defaults for every field.** `NPM_<KIND>_<FIELD>` and
  `NPM_DEFAULT_<FIELD>` set the default of any label field, including every
  alias, so `NPM_PROXY_SSL_FORCE` and `NPM_PROXY_HSTS_SUBDOMAINS` work as they
  do in Redth. Labels win over the environment, values are validated at
  start-up, the effective defaults are logged, and a variable in that namespace
  that names no field is reported instead of ignored.
- **Upstream port detection.** `forward_port` defaults to `auto`: a single
  exposed TCP port is used as-is, several are decided by
  `NPM_PORT_PREFERENCE` (default `80,8080,3000,8000,443`), and a container that
  offers no usable port is skipped with a message naming what it found.
- **The NPMplus feature set as labels.** `ssl.http3`, `noindex`,
  `crowdsec_appsec`, `request_buffering`, `response_buffering`,
  `upstream_compression`, `fancyindex`, `x_frame_options`, `auth_request`,
  `auth_request_upstream`, `location_config`, and for streams
  `proxy_protocol`, `proxy_tls`, `advanced_config` and `description` (which
  defaults to the container name). The three inverted API switches are written
  positively as labels and negated on the way in; the explicit `disable_*`
  spellings are accepted too.
- **Custom locations gained the NPMplus fields**, including the location type
  (`prefix`, `exact`, `regex`, `iregex`, `prefer`, `named`) and per-location
  switches, all inherited from the host unless overridden.
- **Access lists by name.** `access_list: Intern,VPN` is resolved against
  `/api/nginx/access-lists`. An unknown name skips the resource - a typo must
  never turn a protected host into a public one.
- **Unknown labels are reported** with a suggestion
  (`did you mean npm.proxy.ssl.forced?`). `STRICT_LABELS=true` skips the
  resource instead of only warning.
- **Domain conflicts are detected before the write.** A domain a foreign host
  already serves is reported with that host's id and owner instead of
  producing NPM's `domain already in use`.
- **Per-resource backoff.** A resource the API rejects is retried with a
  growing delay (30 s up to 30 min) instead of on every event; a change to its
  labels clears the backoff.
- **`<NAME>_FILE` for every variable**, not just the password.
- **A generated field reference**, [docs/FIELDS.md](docs/FIELDS.md), produced
  from the same table the parser uses and verified in CI, plus
  [docs/MIGRATION.md](docs/MIGRATION.md).

### Changed

- **NPMplus-only settings no longer fail against upstream NPM.** They are left
  out of the request and reported once per resource, so the new defaults
  (HTTP/3 among them) do not break that flavour. Configurations upstream NPM
  genuinely cannot express still fail with a message naming the feature.
- **`letsencrypt.email` / `.agree` are only required for upstream NPM.**
  NPMplus takes the ACME account from its own `ACME_EMAIL`, so `certificate:
  new` works there without them.
- **A start-up overview** reports how many containers are managed, opted out
  or unlabelled.

## [1.0.0-beta.2] - 2026-09-16

**Writing to NPMplus works again.** Every create and update failed in
`v1.0.0-beta.1` with `400 data must NOT have additional properties`, for all
four resource types, because the response model was marshalled straight back as
the request body.

### Fixed

- **Create and update no longer send fields the API rejects.** Both NPM and
  NPMplus validate every write against a JSON schema with
  `additionalProperties: false`. The old client sent its response struct as the
  request body, which carried read-only fields (`id`, `created_on`, ...), the
  write-protected `enabled` flag and, on NPMplus, the removed
  `access_list_id` - so nothing could ever be written. Request payloads are now
  separate types, one per resource kind and API flavour
  (`Resource.Payload(Flavour)`), each mirroring exactly one request schema.
- **`enabled` is actually applied.** Neither API accepts the property in a
  write body; both expose `POST <collection>/{id}/enable` and `/disable`
  instead. The worker now compares the desired state against the live one and
  calls those endpoints, for all four kinds. Previously `npm.<kind>.enabled=false`
  was silently ignored while still being part of the fingerprint, which - once
  writing worked - would have produced an endless update loop.
- **Custom locations satisfy the NPMplus schema.** `npmplus_access_list_ids`
  and `npmplus_access_list_type` are mandatory on every location block there;
  without them the first host using `location.<n>.*` labels was rejected. The
  type defaults to `global` (inherit the proxy host's access list).
- **TLS settings are normalised like the server does.** NPMplus clears
  `ssl_forced` without a certificate, `hsts_enabled` without `ssl_forced` and
  `hsts_subdomains` without HSTS (`internalHost.cleanSslHstsData`). The
  fingerprint was taken over the raw label values, so a half-configured host
  could never match what the server stored: the in-memory cache papered over it
  within a process lifetime, but every restart rewrote each affected resource
  once, forever. The same cascade is now applied before hashing. The same goes
  for access lists, where NPMplus drops the ids of any location that is not
  `custom`.
- **Upstream IPs no longer come from an unreachable network.** With
  `NPM_NETWORK` set, the address is taken from that network only; a container
  that is not attached to it is skipped with a warning naming the networks it
  *is* on, instead of falling through to an alphabetically chosen address NPM
  cannot route to. `NPM_NETWORK_STRICT=false` restores the old behaviour. The
  preferred-network list is also de-duplicated (it used to log
  `npm-frontend,npm-frontend,socket-proxy`).
- **A single rejected resource no longer makes the process unready.**
  `/readyz` now answers `503` only before the first run and while Docker or the
  NPM API is unreachable; resources the API refused are reported as a counter
  in the body (`ok: 12 managed hosts, 1 failed, ...`). A broken label set on one
  container used to flip the container to `unhealthy`.
- **Dry-run updates are reported on every run.** The dry-run path primed the
  state cache with a write that never happened, so `would update` appeared once
  and then vanished. It now leaves the cache alone, like the create path
  already did.
- **This container's own Docker events are ignored**, and `health_status` is no
  longer subscribed to at all. A container's health does not change its labels
  or its IP, and this container's own health flapping triggered a reconcile
  every time.
- `defer resp.Body.Close()` inside the retry loop of `Client.do` is gone; each
  attempt closes its own body. The login path no longer closed the body twice.
- `APIError.Path` is populated (the path used to be folded into `Method`).

### Added

- **API flavour detection.** NPM and NPMplus have incompatible request schemas,
  so the dialect is probed once after login - from an existing proxy host's
  field names, falling back to the `version` object of `GET /api/` - and
  logged. Pin it with `NPM_FLAVOUR=npmplus|npm` (alias `NPM_FLAVOR`) if the
  probe guesses wrong. A configuration the flavour cannot express (HTTP/3 on
  upstream NPM, several access lists on upstream NPM, `certificate_id=new` on
  an NPMplus stream) is refused locally with a message naming the feature,
  rather than as an opaque 400.
- **Diagnostics for schema rejections.** At `LOG_LEVEL=debug` every request is
  logged with its top-level field names, and a rejected one additionally with
  the status and the API's message - exactly what an
  `additionalProperties: false` error withholds. `LOG_PAYLOADS=true` (alias
  `NPM_DEBUG_PAYLOADS`) adds the full body; values of credential-looking keys
  are redacted, because the `meta` object can carry DNS provider tokens.
- `access_list_ids` / `access_list_type` labels for proxy hosts and custom
  locations, mapping to NPMplus' `npmplus_access_list_ids` /
  `npmplus_access_list_type` or NPM's single `access_list_id`. The old
  `access_list_id` label keeps working as the single-id shorthand.
- `ssl.http3` (NPMplus' `npmplus_http3_support`) and `trust_forwarded_proto`
  labels.
- `NPM_NETWORK_STRICT` (default `true`).

### Changed

- Stream ports are modelled as `npm.Port`, which decodes both the integer form
  (NPM) and the string form (NPMplus, including `"8080-8090"` ranges and
  `"$server_port"`), and encodes whichever the target flavour requires.
- `Worker.Status()` returns a `Status` struct separating infrastructure
  failures from per-resource ones.
- Log wording: a partially applied run is now `reconciliation finished with
  errors` with the result counters attached, instead of
  `reconciliation failed`, which suggested nothing had happened.

### Tests

- `internal/npm/contract_test.go` marshals a fully populated payload for every
  resource kind and validates it against the **real** NPM and NPMplus request
  schemas (16 combinations), vendored into `internal/npm/testdata/schema` by
  `scripts/vendor-schemas.py` (`make schemas`). Re-adding `enabled` to a
  payload fails the build with the same complaint the server made. A weekly
  `schema-drift` workflow re-vendors the schemas and re-runs the test, so an
  upstream schema change surfaces before a user hits it.
- Coverage for the enable/disable reconciliation (including that it converges
  and never issues a `PUT`), the TLS cascade, dry-run cache behaviour, the
  strict network selection, access list labels, flavour detection and the
  fatal/partial error split.

### Upgrading from v1.0.0-beta.1

No configuration change is required. Two behaviours differ in practice:

- With `NPM_NETWORK` set, containers outside that network are now skipped
  instead of getting an unreachable upstream. The warning names the networks
  they are on; attach them, set `forward_host`, or set
  `NPM_NETWORK_STRICT=false`.
- `npm.<kind>.enabled=false` now takes effect. Hosts you had labelled that way
  and that are currently enabled in NPM will be disabled on the next reconcile.

> [!CAUTION]
> Before enabling `DELETE_ORPHANS`, make sure every container whose hosts
> should survive still carries `npm.enable=true` - **including the NPM/NPMplus
> container itself**. A resource this tool created keeps its ownership marker
> for good, so once its labels are gone it is an orphan. `DRY_RUN=true` lists
> what would be removed.

## [1.0.0-beta.1] - 2026-09-16

### Added

- `npmplus-docker-sync healthcheck` subcommand that probes `HEALTH_ADDR/readyz`;
  the container image now ships a `HEALTHCHECK` using it, and both compose
  files define a matching `healthcheck:` block.

### Changed

- The shutdown flush no longer deletes orphaned resources. When a whole
  compose stack goes down, the labelled containers usually stop before this
  one; a final reconcile with deletion enabled wiped every managed resource.
  Creates and updates still happen on shutdown, deletion resumes on the next
  start (or the next periodic resync).
- The builder stage is pinned to `$BUILDPLATFORM` and cross-compiles; arm64
  and armv7 images are no longer built under QEMU emulation.
- CI builds the same platform set as the release (`linux/arm/v7` added) and
  smoke-tests the amd64 image.

### Fixed

- Release workflow: syft is installed before GoReleaser runs (`sboms:` needs
  it), `latest` is applied explicitly via `flavor: latest=auto`, and the
  duplicate buildx provenance attestation is disabled.
- CI: `golangci-lint-action` bumped to v7 to match the v2 config schema.
- `.gitignore`/`.dockerignore` ignore the current binary name.
- CI: `golangci-lint` pinned to `v2.13.2` (was `v2.1`). golangci-lint
  type-checks with its own compiled-in `go/types`, so a release built with an
  older toolchain than `GO_VERSION` fails every package with "export data
  version 4 is greater than maximum supported version 2". `v2.1` is built with
  go1.24 and cannot lint Go 1.26 sources.
- All GitHub Actions bumped to their current majors (`checkout@v7`,
  `setup-go@v7`, `upload-artifact@v7`, `docker/*@v4`/`v6`/`v7`,
  `goreleaser-action@v7`, `attest-build-provenance@v4`, `codeql-action@v4`,
  `stale@v11`, `golangci-lint-action@v9`). The previous pins were node20-era
  actions.
- CI: the `govulncheck` job runs through `scripts/govulncheck.sh`, which fails
  on any reachable vulnerability that is not listed with a justification in
  `.govulncheck-allow`, and warns about entries there that are no longer
  reported. A bare `govulncheck ./...` can never pass: GO-2026-4883 and
  GO-2026-4887 are daemon-side Moby bugs whose fix only exists in
  `github.com/moby/moby/v2`, so every Docker *client* symbol this project calls
  is reported as reachable. Both are listed as accepted.

### Tests

- `TestWorkerRunFlushDoesNotDeleteOrphans` guards the shutdown-flush change:
  a managed resource whose container is already gone must survive `Run` with a
  cancelled context. The fake NPM API now honours context cancellation, so the
  initial reconcile cannot delete before the flush.
- `TestHealthcheck` covers the new subcommand (unset address, 200, 503,
  malformed address, wildcard-to-loopback rewrite, connection refused). The
  `main` package previously had no tests at all.

## [2.0.0] - 2026-09-16

The project was renamed from `npm-docker-sync` to **`npmplus-docker-sync`** and
now targets full feature parity with
[Redth/npm-docker-sync](https://github.com/Redth/npm-docker-sync), plus NPMplus
compatibility.

### Added

- **All four NPM resource types**: proxy hosts (`npm.<i>.proxy.*`),
  redirection hosts (`npm.<i>.redirect.*`), TCP/UDP streams
  (`npm.<i>.stream.*`) and 404 hosts (`npm.<i>.404.*` / `.dead.*`), each with
  their own API endpoint, cache bucket and fingerprint.
- **Indexed labels**: a single container can define many resources
  (`npm.0.proxy.host`, `npm.1.redirect.host`, `npm.2.stream.incoming_port`).
  Labels without an index belong to index `0`; `npm.<i>.enable=false` switches
  one index off.
- **IP-based upstream resolution**: the upstream defaults to the container's
  IPv4 address (preferring `NPM_NETWORK`, then a network shared with this
  container), which removes the most common cause of `502 Bad Gateway`.
  Disable with `RESOLVE_CONTAINER_IP=false` or `<kind>.resolve_ip=false`.
- **Custom location blocks** for proxy hosts
  (`npm.proxy.location.<n>.path`, `.forward_host`, `.forward_port`,
  `.forward_scheme`, `.advanced_config`).
- `SYNC_KINDS` to limit which collections are managed.
- `npm.Resource` interface so one reconcile implementation serves every kind.

### Changed

- Module path is now `github.com/VentumPhoenix/npmplus-docker-sync`; the image
  is published as `ghcr.io/ventumphoenix/npmplus-docker-sync`.
- The ownership marker changed to `managed_by: npmplus-docker-sync` and now
  also records the label index (`managed_index`).
- The state cache tracks fingerprints per resource kind, so the same domain can
  be a proxy host and a 404 host without colliding.
- A collection that answers `404` (not supported by a fork) is skipped with a
  warning instead of failing the run.

### Migration from 1.x

Resources created by 1.x carry the old `managed_by: npm-docker-sync` marker,
which is still recognised: they are adopted, re-stamped with the new marker,
and cleaned up like any other managed resource when their container is gone.
Hand-made entries remain untouched. Run once with `DRY_RUN=true` to preview
the updates.

Upstreams also change from container names to container IPs in 2.0. Set
`RESOLVE_CONTAINER_IP=false` to keep the previous behaviour.

## [1.0.0] - 2026-09-15

### Added

- Label-driven synchronisation of Docker containers to Nginx Proxy Manager
  proxy hosts (`npm.enable`, `npm.proxy.host`, `npm.proxy.port`, …).
- Dual authentication: automatic detection of NPM's JWT bearer tokens and
  NPMplus' httpOnly session cookies, with proactive refresh and retry on 401.
- Initial reconciliation on startup and a periodic full resync
  (`RESYNC_INTERVAL`).
- Event debouncer (`DEBOUNCE_INTERVAL`, `DEBOUNCE_MAX_WAIT`) that collapses
  bursts such as `docker compose up` into a single reconcile.
- Single-worker queue so all NPM API writes stay sequential (no SQLite lock
  contention).
- Thread-safe fingerprint cache (`sync.RWMutex`) for idempotent reconciles.
- Ownership marker (`managed_by: npmplus-docker-sync`) so only self-created proxy
  hosts are ever deleted; `ADOPT_EXISTING` and `DELETE_ORPHANS` control the
  edge cases.
- Docker endpoint support for unix sockets and TCP (docker-socket-proxy), with
  server-side event filters.
- Graceful shutdown on SIGINT/SIGTERM including a final flush with a detached
  context.
- Let's Encrypt support via `certificate_id=new`, including adoption of the
  certificate id NPM assigns afterwards.
- Optional health endpoint (`HEALTH_ADDR`) exposing `/healthz` and `/readyz`.
- `DRY_RUN` mode, structured logging (`log/slog`, text or JSON) with redacted
  credentials.
- Multi-stage `scratch` image running as `1000:1000`, published for
  linux/amd64, arm64 and arm/v7.

[Unreleased]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.4...HEAD
[1.0.0-beta.4]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.3...v1.0.0-beta.4
[1.0.0-beta.3]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.2...v1.0.0-beta.3
[1.0.0-beta.2]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.1...v1.0.0-beta.2
[1.0.0-beta.1]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v1.0.0-beta.1
[2.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v2.0.0
[1.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v1.0.0
