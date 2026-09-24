# Getting Started with Levara

Start with durable SQL-backed memory on loopback, then add an embedding service
for semantic document search. The server does not start model services for you.
These instructions describe the checked-in source; they are not a live service
health report.

## Prerequisites and build

Use the Go version declared in [go.mod](../go.mod).
The first recipe uses embedded SQLite; PostgreSQL and Docker are optional.

```bash
git clone https://github.com/Stek0v/Levara.git
cd Levara
make build
```

`levara-server` serves HTTP/MCP and optional gRPC; `levara` is the client.
If an old local directory occupies `./levara`, build the client at another path
with `go build -o ./bin/levara ./cmd/cli` and use that path below.

## Minimal first run

Run from the checkout root in a dedicated terminal:

```bash
DB_PROVIDER=sqlite DB_PATH="$PWD/data/levara.db" \
./levara-server -profile=standalone -host=127.0.0.1 -port=8080 \
  -grpc-port=0 -dim=768 -data-dir="$PWD/data"
```

SQLite is explicit: a bare `-profile=standalone` can run without a SQL database,
which is insufficient for durable memory, users and dataset metadata. The
vector dimension is also explicit so the later 768-dimensional example uses
compatible storage. Authentication is deliberately disabled for this isolated
local example. Use [Team setup](tutorials/04-team-deploy.md) before sharing it.

In another terminal:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
curl -fsS http://127.0.0.1:8080/health
./levara health --details
```

## First durable memory, without models

The legacy MCP endpoint accepts a one-shot JSON-RPC tool call without a session.
Provide `collection` explicitly on independent calls; a separate `set_context`
call does not establish context for another sessionless request.

```bash
curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_memory","arguments":{"collection":"tutorial","room":"onboarding","hall":"fact","key":"first-memory","value":"The tutorial stores durable memory in SQLite."}}}'

curl -fsS http://127.0.0.1:8080/mcp \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall_memory","arguments":{"collection":"tutorial","query":"SQLite","limit":3}}}'
```

Check for `first-memory` in the returned tool content. MCP can report a tool
failure in an HTTP 200 response: inspect JSON-RPC `error` and `result.isError`,
not only the HTTP status. This is a lexical recall example, not a semantic
quality test. Stop the foreground server with Ctrl-C, restart the same command,
and recall again to check persistence using the same SQL path.

## Add semantic document search

Start a compatible embedding provider separately; [integrations](integrations.md)
explains the endpoint contract. This example assumes a local service already
serves `nomic-embed-text` at Ollama's OpenAI-compatible endpoint and returns 768
numbers per embedding. Confirm the actual model output before selecting `-dim`.
After stopping the first foreground server, start:

```bash
DB_PROVIDER=sqlite DB_PATH="$PWD/data/levara.db" \
EMBEDDING_ENDPOINT=http://127.0.0.1:11434/v1/embeddings \
EMBEDDING_MODEL=nomic-embed-text \
./levara-server -profile=standalone-embed -host=127.0.0.1 -port=8080 \
  -grpc-port=0 -dim=768 -data-dir="$PWD/data"
```

Then use the client from the checkout root:

```bash
export LEVARA_URL=http://127.0.0.1:8080/api/v1
./levara add 'Alice maintains the Acme payment service.' --dataset=demo
./levara cognify --dataset=demo --collection=demo --wait
./levara search 'Acme' --collection=demo --type=CHUNKS_LEXICAL --top-k=5
./levara search 'who maintains payments' --collection=demo --type=HYBRID --top-k=5
```

`add` stores input; `cognify` processes it. Wait for an explicit successful
terminal status before expecting new search results. Full entity and relation
extraction also needs an LLM provider; it is not part of this first semantic
search recipe. Existing memories are SQL-readable without embeddings; configuring
a model alone is not proof that every older record has been reindexed.

For files use `./levara add --file=./report.pdf --dataset=reports`; explicit
`--file` fails on a missing file instead of treating its name as text.
See [document management](document-management.md) for extraction, processing,
original downloads and individual sharing, and [document scenarios](document-workflow-scenarios.md)
for acceptance checks.

## CLI usage and endpoint namespaces

| Surface | Address or syntax |
|---|---|
| Server health/version | `http://127.0.0.1:8080/health`, `/version` |
| REST and CLI base | `http://127.0.0.1:8080/api/v1` |
| MCP client endpoint | `http://127.0.0.1:8080/mcp` |
| Lightweight MCP (memory profile) | `/mcp-light`; same session transport, pinned `memory` toolset for token-sensitive agents |
| Strict stateless MCP | `/mcp/2026-07-28`; use the required protocol headers in the [API guide](api-reference.md) |
| WebUI | Separate Next.js service, normally `http://127.0.0.1:3000` in development |

```bash
./levara help
./levara --url=http://127.0.0.1:8080/api/v1 health
./levara --token="$LEVARA_TOKEN" datasets list
```

Global flags precede the command; value flags use `--key=value`. `./levara
--help` is not the help command. CLI `add --dataset` takes a dataset name;
`cognify --dataset` accepts an accessible ID or an unambiguous name. Use the
WebUI/API dataset ID selector to upload into a shared dataset you do not own.
`collection` scopes retrieval and is distinct from a dataset and its grants.

## Server configuration facts

| Setting | Meaning |
|---|---|
| `-standalone=true` | Local WAL storage without Raft; separate from functional profiles |
| `-profile=standalone` | Suppresses unexplicit external-service flags, including inherited embed/LLM/PG defaults |
| `-profile=standalone-embed` | Keeps embedding configuration available in local mode |
| `-profile=full` or empty | Does not suppress configured integrations |
| `LEVARA_PROFILE` | Product requirements: `personal`, `solo_pro`, `team`, `enterprise` |
| `LEVARA_MCP_TOOLSET` | Advertised MCP tool surface; not an authorization boundary |
| `-dim` | Vector dimension; default 128, never automatically inferred from a model |
| `-config-check` | Validate assembled profile configuration without starting services |

The server does not load `.env` automatically. Export an edited environment file
before checking or starting it. `LLM_MODEL` is an environment variable, not a
`-llm-model` flag. See [profile presets](profile-presets.md) and
[deployment](deployment.md) for the full setup path.

## Next steps

- [First memory tutorial](tutorials/01-first-memory.md) — scope, recall and persistence.
- [Русский старт](tutorials/00-getting-started-ru.md) — тот же путь по-русски.
- [Connect an agent](tutorials/02-agent-integration.md) — client configuration and memory instructions.
- [WebUI](../webui/README.md) — separate frontend service.
- [Features](features-guide.md) — choose a supported workflow.
- [Enterprise identity](enterprise-identity.md) — LDAP/AD and SSO boundaries.
- [Testing](testing.md) — isolated checks and integration prerequisites.
