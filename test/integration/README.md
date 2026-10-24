# Integration tests

The real binary against a real NPM or NPMplus, a real Docker daemon and real
nginx. They prove what unit and contract tests cannot: that the API *accepts* a
request, that enable/disable and login work, that the flavour is detected, and
that nginx actually serves the host.

```bash
make integration                                         # nginx-proxy-manager
NPM_IMAGE=ghcr.io/zoeyvid/npmplus:latest make integration

# server upgrade under managed hosts (leaves the stack on the new image):
NPM_IMAGE=ghcr.io/zoeyvid/npmplus:latest NPM_UPGRADE_IMAGE=ghcr.io/zoeyvid/npmplus:develop \
  go test -tags integration -run TestUpgradeKeepsOwnership ./test/integration/
```

Needs Docker with compose and free ports on `127.0.0.1`; runs on Linux and
macOS. The stack (`docker-compose.yml`) is NPM, a whoami backend and a socket
proxy. The tool itself is not part of it — each test runs `sync` once with the
environment it needs, so results are deterministic; one test runs the daemon.
The suite runs with `DELETE_GUARD=off` and `NPM_STOP_GRACE=0`; the tests for
those turn them back on.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `NPM_IMAGE` | `jc21/nginx-proxy-manager:latest` | server to test against |
| `NPM_UPGRADE_IMAGE` | — | runs `TestUpgradeKeepsOwnership`: upgrade to this image |
| `NPM_IDENTITY` / `NPM_SECRET` | `admin@example.com` / `integration-secret` | admin account |
| `NPM_API_PORT` / `NPM_HTTP_PORT` | `8181` / `8180` | ports on 127.0.0.1 |
| `NPM_LETSENCRYPT_TARGET` | per image | where the letsencrypt volume is mounted |
| `KEEP_STACK=1` | — | leave the stack running |
| `NPM_FALLBACK_IDENTITY` / `NPM_FALLBACK_SECRET` | — | extra account for an image with an unknown default login |

The two images differ in ways the harness handles: jc21 needs
`/etc/letsencrypt` mounted and NPMplus refuses to start with it (hence the
target per image); NPMplus serves its admin API over https, NPM over http (the
harness probes both). The admin account is seeded from the environment, else
the image's default login is rotated into the configured one.

## Coverage

| Scenario | Test |
|---|---|
| Create, update, delete; nginx serves the host | `TestProxyHostLifecycle` |
| All four kinds, real redirect and 404 | `TestAllKindsRoundTrip` |
| Stop, start, restart, rename, grace period | `TestStopStartKeepsTheHost`, `TestStopGraceSwallowsTheRestart`, `TestRenameKeepsTheHost` |
| Adoption; hand-made hosts untouched | `TestAdoptionLeavesForeignHostsAlone` |
| Redth migration | `TestRedthMigration` |
| Server upgrade keeps ownership | `TestUpgradeKeepsOwnership` |
| Events, debouncing, readiness | `TestDaemonReactsToEvents` |
| Typo, prefix change, delete guard | `TestTypoDoesNotDeleteTheHost`, `TestPrefixChangeDoesNotDelete`, `TestDeleteGuardRefusesAMassDeletion` |
| Two instances, one NPM | `TestTwoInstancesCoexist` |
| Dry run | `TestDryRunChangesNothing` |
| NPM restarted mid-run | `TestNPMRestartDoesNotDelete` |
| Socket proxy, credentials, `validate` | `TestThroughSocketProxy`, `TestCredentials`, `TestValidateSubcommand` |
| Certificates, access lists | `TestCertificateSelection`, `TestCertificatePollPicksUpANewCertificate`, `TestAccessListByName` |
| Flavours, NPMplus fields, locations, conflicts | `TestFlavourDetection`, `TestNPMplusOnlyFields`, `TestCustomLocations`, `TestDomainConflictIsReported` |

Not automated: Let's Encrypt (rate limits — test by hand against staging) and
SIGTERM in the middle of a write (the shutdown flush has unit tests).

After an aborted run: `docker compose -p npmsync-it down -v`.
