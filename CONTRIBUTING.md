# Contributing

Thanks for taking the time to improve `npmplus-docker-sync`. Bug reports, new
labels, documentation fixes and PRs are all welcome.

## Getting started

```bash
git clone https://github.com/VentumPhoenix/npmplus-docker-sync.git
cd npmplus-docker-sync
make test     # race-enabled unit tests, no Docker or NPM required
make check    # fmt + vet + lint + test — this is what CI runs
```

Requirements: Go 1.26 or newer. `golangci-lint` for `make lint`
([install](https://golangci-lint.run/welcome/install/)); everything else is
standard Go tooling.

## Running it locally

```bash
cp .env.example .env    # point NPM_URL at a test instance
make run
```

Use `DRY_RUN=true` while developing: the reconcile logic runs and logs every
intended change without writing to the API.

A full local playground:

```bash
docker compose -f docker-compose.yml -f docker-compose.example-app.yml up -d
```

## Project layout

| Path | Responsibility |
|---|---|
| `main.go` | wiring, signal handling, health endpoint |
| `internal/config` | environment parsing and validation |
| `internal/npm` | API client, the four resource models, dual-auth detection |
| `internal/docker` | event listener, indexed label parser, IP resolution |
| `internal/syncer` | debouncer, per-kind state cache, reconcile worker |

Keep the boundaries: `internal/npm` knows nothing about Docker,
`internal/docker` knows nothing about the NPM API, and only `internal/syncer`
maps between them (`BuildResource`).

Adding a resource type means: a struct implementing `npm.Resource`, a `Kind`
with its API path, a label section in `internal/docker/build.go`, a case in
`syncer.BuildResource` — the worker itself does not change.

## Code style

* `gofmt -s` formatted, `go vet` and `golangci-lint` clean.
* Exported identifiers carry doc comments; comments explain *why*, not *what*.
* Errors are wrapped with `%w` and contextual, lower-case messages.
* Structured logging via `log/slog` — never log credentials or tokens.
* No new third-party dependencies without a good reason. The Docker SDK is the
  only direct dependency and should stay that way.

## Tests

Every behavioural change needs a test. The suite is hermetic: no Docker daemon
and no NPM instance are involved.

* Table-driven tests with the standard `testing` package.
* The NPM API is mocked with `net/http/httptest`.
* The Docker API is mocked through the narrow `docker.APIClient` interface.
* Concurrency-sensitive code must pass `go test -race`.

```bash
go test -race ./...
make cover      # writes coverage.html
```

## Commits and pull requests

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):

```
feat(labels): support custom location blocks
fix(npm): re-authenticate after a 403 response
docs(readme): clarify forward_host defaults
```

Before opening a PR:

1. `make check` passes.
2. New labels or environment variables are documented in `README.md` and
   `docs/LABELS.md` / `docs/CONFIGURATION.md`.
3. The PR description says what changed, why, and how it was tested — ideally
   against both NPM and NPMplus.

Please open an issue first for larger features so we can agree on the design
before you spend time on it.

## Reporting bugs

Use the issue template and include the version, the proxy manager flavour
(NPM or NPMplus), your container labels and `LOG_LEVEL=debug` output. Redact
passwords and real domain names.

Security issues go through [SECURITY.md](SECURITY.md), never a public issue.

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
