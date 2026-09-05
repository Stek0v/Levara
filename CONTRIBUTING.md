# Contributing to Levara

Use the Go version declared in [go.mod](go.mod). Docker/PostgreSQL, Neo4j,
model endpoints and browser binaries are needed only for the corresponding
integration scenarios. Run commands from the repository root.

```sh
make build
make test-commit
make profile-config-check
make contract-check
```

For a persistent local server, follow [getting started](docs/getting-started.md):
select a SQL backend explicitly. A bare server invocation does not configure
all persistence and model dependencies for you.

## Change and review

Keep changes focused, preserve unrelated work, format Go with `gofmt` and run
checks appropriate to the changed behavior. Add a regression test for a bug or
new behavior; use temporary directories and isolated test services. Security
changes must include denied-access cases, and SQL changes must cover both
SQLite and PostgreSQL where supported.

A pull request should state the problem, resulting behavior, validation and
remaining gaps. Performance claims need before/after measurements on the same
workload and hardware, with raw results. Do not describe an unrun test as passed.
Consult [testing](docs/testing.md) for release gates, environment variables,
opt-in integrations and the distinction between browser mocks and full E2E.

## Repository map

| Path | Responsibility |
|---|---|
| `cmd/server`, `cmd/cli` | Server and command-line entry points |
| `internal` | Engine, HTTP, SQL, vector storage, WAL and cluster |
| `pkg` | Auth/access, ingestion, extraction, MCP, workspace, tasks and providers |
| `proto`, `proto/pb` | Protobuf definitions and generated bindings |
| `pipeline` | Pipeline definitions |
| `deploy` | Deployment examples and profile validation |
| `docs` | Usage, configuration, architecture decisions and contracts |
| `benchmark`, `tests` | Measurement harnesses and integration scenarios |
| `webui` | Optional Next.js application |

## Public contracts

Edit the source inventories/descriptors, then run `make contract` and
`make contract-check`. Generated `docs/api-contract.md`, `docs/contract.json`
and the marked MCP table in `AGENTS.md` have one owner and are not hand-edited.
Swagger is annotation-derived; use `make swag` for its generated files when
annotations change. For protobuf changes, use the repository generation
commands and the matching compiler/plugins.

MCP changes must keep descriptor, schema, dispatch, profiles/feature flags and
backward-compatible result/error shapes consistent. Auth/bootstrap routes may
be registered separately from the generated application route inventory.

## Documentation and recipes

Keep one current guide for each workflow and link to it from [the index](docs/README.md).
A recipe should provide prerequisites, a copyable command using an explicit
server URL, necessary authentication, expected output, failure checks and a
scoped rollback. Explain dependencies such as embeddings, SQL, OCR and LLMs.
Never use instance-wide prune as rollback for a single document.

When consolidating obsolete guides, move useful unique instructions first,
delete obsolete files, and repair Markdown, HTML and fixture references.
Preserve ADR rationale and raw historical test evidence; remove stale product
claims. Local `docs/internal` and `docs/marketing` are ignored by Git, so updates
to them are not automatically published. Test reports must identify source
revision, environment, command, result and limitations; see [testing](docs/testing.md).

## Releases and issues

The current workflows define CI checks; this repository does not provide an
automatic tagged binary/container release pipeline. A release needs an explicit
packaging and publishing procedure for its target environment.

For issues, include reproduction steps, expected/actual behavior, version,
operating system and sanitized configuration. Do not include tokens or private
documents. Contributions are covered by the [MIT license](LICENSE).
