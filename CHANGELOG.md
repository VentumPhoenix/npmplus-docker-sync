# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/VentumPhoenix/npmplus-docker-sync/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v2.0.0
[1.0.0]: https://github.com/VentumPhoenix/npmplus-docker-sync/releases/tag/v1.0.0
