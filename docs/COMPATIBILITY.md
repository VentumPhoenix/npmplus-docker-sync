# Supported versions and compatibility

## Nginx Proxy Manager and NPMplus

| Server | Versions | How it is verified |
|---|---|---|
| Nginx Proxy Manager | 2.11.3 and newer | Contract tests against the vendored request schemas, plus the integration suite on the minimum and the current release of every pull request |
| NPMplus | current release | Same |

Two more images run nightly rather than per pull request, because they move
under us: `jc21/nginx-proxy-manager:latest` and `ghcr.io/zoeyvid/npmplus:develop`.
A break there is an early warning, not a failing pull request.

The API dialect is detected at start-up and re-checked when the server rejects
a payload its schema should have accepted, so an upgrade from one flavour to
the other is noticed while the process keeps running.

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
* **The `meta` format** written into managed resources (`managed_by`,
  `managed_container`, `managed_container_id`, `managed_index`,
  `managed_instance`, `managed_prefix`).
* **The exit codes** of `sync` and `validate`, and the shape of `/status` and
  `/metrics`.

That means:

* A label or variable is never removed in a minor release. When something is
  renamed, the old spelling keeps working as an alias and is announced as
  deprecated in the changelog; it may be removed in the next major release at
  the earliest.
* A default only changes in a major release. New fields may arrive in a minor
  one, with a default that keeps existing behaviour.
* The meta keys are only ever added to, so a downgrade keeps working.

Until 1.0.0, breaking changes are possible between beta releases and are listed
at the top of the [changelog](../CHANGELOG.md) with a migration note in
[MIGRATION.md](MIGRATION.md).
