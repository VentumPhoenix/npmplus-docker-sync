# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.2...HEAD
[1.0.0-beta.2]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v1.0.0-beta.1...v1.0.0-beta.2
[1.0.0-beta.1]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v1.0.0-beta.1
[2.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v2.0.0
[1.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v1.0.0
