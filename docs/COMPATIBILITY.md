# Supported versions and compatibility

## Nginx Proxy Manager and NPMplus

| Server | Versions | How it is verified |
|---|---|---|
| Nginx Proxy Manager | 2.11.3 and newer | Contract tests against the request schemas of 2.16.0 and `master`; the integration suite on 2.11.3 and the current 2.x of every pull request, plus the upgrade from one to the other |
| NPMplus | current release (2026-10-22-r1) and its development branch | Contract tests against the request schemas of both; the integration suite on the current release of every pull request |

Three more scenarios run nightly rather than per pull request, because they
move under us: `jc21/nginx-proxy-manager:latest`,
`ghcr.io/zoeyvid/npmplus:develop`, and the upgrade from the current NPMplus
release to `develop`. A break there is an early warning, not a failing pull
request.

What a server accepts is read from its own schema (`GET /api/schema`) at
start-up, again when it rejects a payload, and again when it reports another
version - so an upgrade of the server, from one flavour to the other or to an
NPMplus release with a different API, is noticed while the process keeps
running. A server without that endpoint is handled from built-in knowledge,
which the contract test checks against every vendored schema.

### NPMplus without the meta object

NPMplus' development branch moves the meta object into columns of its own
(migration `meta_to_columns`): the group goes to `npmplus_directory`, the DNS
challenge of a certificate request to `npmplus_dns_*`, and `meta` is emptied on
every host and no longer accepted. It also retires the proxy switches for
websockets and HTTP/2 (always on), caching and "block exploits" (gone) and
`trust_forwarded_proto` (always trusted), and spells "keep the request's
scheme" of a redirection `$scheme` only.

All of that is supported. The owner of a host survives the migration because
it is also kept in the ownership marker of its advanced configuration - see
[ARCHITECTURE.md](ARCHITECTURE.md#ownership). That only holds for hosts that
carry the marker, so **update this tool before NPMplus**; see
[MIGRATION.md](MIGRATION.md).

### How a version gets onto that list

1. Add its ref to `scripts/schema-refs.json` and run `make schemas`. The
   request schemas of that release are vendored into
   `internal/npm/testdata/schema/<flavour>/<ref>/`, and the contract test
   validates every payload against each of them.
2. Add the image to the matrix in `.github/workflows/integration.yml`.

Both are checked in, so "supported" always means "there is a test".

## Docker

The Docker SDK negotiates the API version, so any daemon from 20.10 onwards
works. A filtered socket proxy is the recommended setup; see
[SECURITY.md](../SECURITY.md) for the endpoints it has to allow.

## Compatibility promise

From **v1.0.0** onwards, these are part of the public interface and follow
semantic versioning:

* **Label names**, including the documented aliases.
* **Environment variable names** and their defaults.
* **The ownership marker**: the comment line format `v=1` at the top of the
  advanced configuration, and the meta keys written next to it wherever the
  server has a meta object (`managed_by`, `managed_container`,
  `managed_container_id`, `managed_index`, `managed_instance`,
  `managed_prefix`), plus the group - `meta.directory`, or `npmplus_directory`
  on NPMplus releases with the column.
* **The exit codes** of `sync` and `validate`, and the shape of `/status` and
  `/metrics`.

That means:

* A label or variable is never removed in a minor release. When something is
  renamed, the old spelling keeps working as an alias and is announced as
  deprecated in the changelog; it may be removed in the next major release at
  the earliest.
* A default only changes in a major release. New fields may arrive in a minor
  one, with a default that keeps existing behaviour.
* The marker and the meta keys are only ever added to, so a downgrade keeps
  working. A new marker format would get a new version number, and every
  release reads the versions before it.

Until 1.0.0, breaking changes are possible between beta releases and are listed
at the top of the [changelog](../CHANGELOG.md) with a migration note in
[MIGRATION.md](MIGRATION.md).
