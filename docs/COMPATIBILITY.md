# Compatibility

## Supported servers

| Server | Versions | Tested by |
|---|---|---|
| Nginx Proxy Manager | 2.11.3 and newer | contract tests (2.16.0, `master`), integration tests (2.11.3, current 2.x), upgrade 2.11.3 → 2.x |
| NPMplus | current release (2026-10-22-r1) and `develop` | contract tests (both), integration tests (release), upgrade release → `develop` |

Pull requests run the supported versions. The moving targets — NPM `latest`,
NPMplus `develop` and the upgrade to it — run nightly, as an early warning.

What a server accepts is read from the server (`GET /api/schema`), at start-up,
after a rejected request and after a version change. A new release that adds or
removes fields therefore usually needs no update of this tool.

**NPMplus without the meta object.** NPMplus' development branch moves `meta`
into columns and empties it on every host. Fully supported: the group goes to
`npmplus_directory`, the DNS challenge to `npmplus_dns_*`, retired switches
(websockets, HTTP/2, caching, block exploits, trust forwarded proto) are left
out, and the owner of each host survives through the marker line — **as long as
this tool (≥ 1.0.0-beta.10) ran before NPMplus was updated.** See
[MIGRATION.md](MIGRATION.md).

**Docker:** any daemon from 20.10 on; the API version is negotiated.

### Adding a version

1. Add its tag to `scripts/schema-refs.json` and run `make schemas` — the
   contract test then checks every payload against it.
2. Add the image to `.github/workflows/integration.yml`.

"Supported" always means "there is a test".

## The 1.0 promise

From **1.0.0** on, these follow semantic versioning:

- label names and their documented aliases,
- environment variable names and their defaults,
- the ownership marker (format `v=1`) and the meta keys written next to it
  (`managed_by`, `managed_container`, `managed_container_id`, `managed_index`,
  `managed_instance`, `managed_prefix`), and the group (`meta.directory` or
  `npmplus_directory`),
- the exit codes of `sync` and `validate`, and the shape of `/status` and
  `/metrics`.

In practice:

- Nothing is removed in a minor release. A renamed label or variable keeps its
  old name as an alias, is marked deprecated in the changelog, and may go in
  the next major release at the earliest.
- Defaults only change in a major release.
- The marker and meta keys are only ever added to. A new marker format gets a
  new version number, and every release reads the older ones — so a downgrade
  keeps working.

Until 1.0.0, breaking changes are listed in the [changelog](../CHANGELOG.md)
with a note in [MIGRATION.md](MIGRATION.md).
