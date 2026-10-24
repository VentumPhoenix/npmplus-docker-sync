# Contributing

Bug reports, missing labels, doc fixes and pull requests are welcome. For a
larger feature, please open an issue first so we can agree on the design.

## Setup

Go 1.26 or newer, and [golangci-lint](https://golangci-lint.run/welcome/install/)
for `make lint`.

```bash
git clone https://github.com/VentumPhoenix/npmplus-docker-sync.git
cd npmplus-docker-sync
make test     # unit tests with -race; no Docker or NPM needed
make check    # fmt + vet + lint + test — what CI runs
```

Try it against a test instance with `cp .env.example .env`, then `make run`
with `DRY_RUN=true`. A complete playground:
`docker compose -f docker-compose.yml -f docker-compose.example-app.yml up -d`.

## Layout

| Path | Responsibility |
|---|---|
| `main.go` | wiring, signals, health endpoint, subcommands |
| `internal/config` | environment parsing and validation |
| `internal/fields` | the field table: every label, alias, env variable, default |
| `internal/docker` | event listener, label parser, upstream resolution |
| `internal/npm` | API client, resource models, ownership marker, server capabilities |
| `internal/certs` | certificate matching |
| `internal/syncer` | debouncer, cache, the reconcile worker |
| `test/integration` | tests against real NPM and NPMplus containers |

`internal/npm` knows nothing about Docker and `internal/docker` nothing about
the NPM API; only `internal/syncer` connects them.

**A new label** is a row in the field table (`internal/fields/fields.go`), the
parsing in `internal/docker/build.go`, the mapping in `syncer.BuildResource`
and the payload field. Then regenerate the reference:
`go test ./internal/fields -update` (it rewrites `docs/FIELDS.md`).

**A new server version:** see [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md#adding-a-version).

## Code and tests

- `gofmt`, `go vet` and `golangci-lint` clean; comments explain *why*.
- Errors wrapped with `%w`, lower-case messages; logging via `log/slog`, never
  credentials.
- Avoid new dependencies.
- Every behaviour change needs a test. Unit tests are hermetic — the NPM API is
  faked with `httptest`, Docker through a small interface.
- `internal/npm/contract_test.go` checks every request body against the real
  NPM and NPMplus schemas; `make schemas` refreshes them.
- Integration tests need Docker: `make integration`, see
  [test/integration/README.md](test/integration/README.md).

## Pull requests

Commits follow [Conventional Commits](https://www.conventionalcommits.org/):
`feat(labels): …`, `fix(npm): …`, `docs: …`.

Before opening one: `make check` passes, new labels or variables are documented,
and the description says what changed, why, and how it was tested — ideally
against both NPM and NPMplus.

## Bugs and security

Use the issue template with the version, NPM or NPMplus, your labels and
`LOG_LEVEL=debug` output (redact passwords and real domains). Security issues
go through [SECURITY.md](SECURITY.md), never a public issue.

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
