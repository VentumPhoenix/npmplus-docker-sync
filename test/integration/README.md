# Integration tests

These tests run the real binary against a real Nginx Proxy Manager (or
NPMplus), a real Docker daemon and a real nginx. They cover what unit and
contract tests structurally cannot:

* whether the API *accepts* a payload, not just whether it matches the schema,
* the `/enable` and `/disable` endpoints,
* login, token refresh and credential rotation,
* the flavour detection,
* and whether nginx actually serves the host afterwards.

## Running them

```bash
make integration                  # upstream nginx-proxy-manager
NPM_IMAGE=ghcr.io/zoeyvid/npmplus:<tag> make integration
```

The stack is defined in `docker-compose.yml`: NPM, a whoami backend and a
socket proxy. The sync tool itself is **not** part of the stack — the tests
build the binary and invoke `sync` (one reconcile, then exit) with the
environment each scenario needs. That makes every assertion deterministic
instead of racing a debounce window; the event-driven path has its own test
that runs the daemon.

Useful knobs:

| Variable | Default | Meaning |
|---|---|---|
| `NPM_IMAGE` | `jc21/nginx-proxy-manager:latest` | Which server to test against |
| `NPM_IDENTITY` / `NPM_SECRET` | `admin@example.com` / `integration-secret` | Admin account |
| `NPM_API_PORT` / `NPM_HTTP_PORT` | `8181` / `8180` | Published ports on 127.0.0.1 |
| `KEEP_STACK=1` | — | Leave the stack running for inspection |

The harness seeds the admin account from the environment and falls back to
rotating the shipped default credentials, so it works on images that do not
support `INITIAL_ADMIN_*`.

## What is covered

| Scenario | Test |
|---|---|
| Create, update, delete, and nginx actually serving the host | `TestProxyHostLifecycle` |
| All four resource kinds, including a real redirect and 404 | `TestAllKindsRoundTrip` |
| Stop, start, restart, rename, grace period | `TestStopStartKeepsTheHost`, `TestStopGraceSwallowsTheRestart`, `TestRenameKeepsTheHost` |
| Adoption; hand-made hosts stay untouched | `TestAdoptionLeavesForeignHostsAlone` |
| Redth migration | `TestRedthMigration` |
| Event stream, debouncing, readiness | `TestDaemonReactsToEvents` |
| A typo must not delete a host | `TestTypoDoesNotDeleteTheHost` |
| Changed `LABEL_PREFIX` | `TestPrefixChangeDoesNotDelete` |
| Two instances against one NPM | `TestTwoInstancesCoexist` |
| `DRY_RUN` changes nothing, and shows a field diff | `TestDryRunChangesNothing` |
| NPM restarted mid-run | `TestNPMRestartDoesNotDelete` |
| Socket proxy | `TestThroughSocketProxy` |
| Wrong password, `NPM_SECRET_FILE` | `TestCredentials` |
| `validate` before a deploy | `TestValidateSubcommand` |
| Certificates: auto, wildcard vs exact, `none`, poll | `TestCertificateSelection`, `TestCertificatePollPicksUpANewCertificate` |
| Access lists by name, including a typo | `TestAccessListByName` |
| Flavour detection | `TestFlavourDetection` |
| NPMplus-only fields against both flavours | `TestNPMplusOnlyFields` |
| Custom locations | `TestCustomLocations` |
| Domain conflicts with a foreign host | `TestDomainConflictIsReported` |

Not automated on purpose:

* **Let's Encrypt.** Rate limits and DNS make it unsuitable for CI; test it by
  hand against the staging endpoint.
* **SIGTERM in the middle of a write.** The shutdown flush is covered by unit
  tests; reproducing a torn write reliably needs a proxy that can stall a
  request, which is not worth the complexity yet.

## Requirements

Docker with the compose plugin, and the ability to publish ports on
`127.0.0.1`. The tests create containers named `it-*` on the `npmsync-it`
network and remove them again; `docker compose -p npmsync-it down -v` cleans up
after an aborted run.
