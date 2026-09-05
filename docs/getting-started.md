# Getting Started with Levara

This guide describes a portable first run of the checked-in server.
[current-state.md](current-state.md) preserves a dated local deployment snapshot;
its paths, ports and counts are examples, not defaults for a fresh installation.

## Prerequisites

- Go 1.26+ for source builds.
- Docker and Docker Compose if you use the container path.
- Optional: PostgreSQL for SQL-backed memory/metadata.
- Optional: an OpenAI-compatible embedding endpoint for vector search and cognify.
- Optional: an OpenAI-compatible LLM endpoint for full graph/entity extraction.
- Optional: Neo4j and rerank sidecars; the first-run recipe does not require them.

## Build locally

```bash
git clone https://github.com/Stek0v/Levara.git
cd Levara
make build
```

Build targets:

| Binary | Package | Notes |
|---|---|---|
| `levara-server` | `./cmd/server` | Main HTTP/MCP/gRPC server. |
| `levara` CLI | `./cmd/cli` | `make build` writes `./levara` in a fresh checkout. |

If a local legacy directory already occupies that name, choose an explicit path:

```bash
go build -o /tmp/levara-cli ./cmd/cli
```

## Minimal first run

For a dependency-light local server:

```bash
./levara-server \
  -profile=standalone \
  -dim=768 \
  -port=8080 \
  -grpc-port=0
```

Verify:

```bash
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/version
```

Current health responses use this shape:

```json
{"health":"healthy","status":"ready","version":"levara-go"}
```

## Add optional local models

Basic memory and lexical search do not require an external model. For semantic
search, configure an OpenAI-compatible embedding endpoint and match `-dim` to
its output. Full entity/graph extraction additionally needs an LLM provider.
Use [integrations](integrations.md) for model setup and
[document management](document-management.md) for format/extraction limits.

A fully local setup needs local embedding, LLM and extraction providers.
Choosing an external endpoint sends the relevant input there. The separately
operated services in [current-state.md](current-state.md) are a historical Mac
example, not processes started by the server.

## CLI usage

The CLI defaults to `http://localhost:8080/api/v1`. For the first-run server:

```bash
LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara health
LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara health --details
```

Implemented top-level commands:

```text
health [--details]
add <file|url|text> [--dataset=name]
cognify [--dataset=name] [--collection=name] [--wait]
search <query> [--type=CHUNKS] [--top-k=10]
datasets list|create|delete
cache stats
workspace ...
git ...
```

Global CLI flags must appear before the subcommand:

```bash
./levara --url=http://127.0.0.1:8080/api/v1 health
./levara --token=$LEVARA_TOKEN datasets list
```

`./levara --help` is currently rejected as an unknown global flag. Use:

```bash
./levara help
```

## Add data and search

Via CLI:

```bash
LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara add \
  "Levara is a memory and search layer for AI agents." \
  --dataset=demo

LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara cognify \
  --dataset=demo \
  --collection=demo \
  --wait

LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara search \
  "memory layer" \
  --type=HYBRID \
  --top-k=5
```

For WebUI upload, extraction status, reprocessing and individual sharing,
follow [document management](document-management.md). Verify these workflows
with [document scenarios](document-workflow-scenarios.md).

## Server configuration facts

Current functional server profiles:

| Profile | Meaning |
|---|---|
| `standalone` | WAL/local mode, external subsystems disabled unless explicit flags/env enable them. |
| `standalone-embed` | Local mode with embeddings enabled. |
| `full` / empty | Full configuration surface; no profile suppression. |

Important flags:

```text
-config-check
-profile standalone|standalone-embed|full
-dim <vector-dimension>
-port <http-port>
-grpc-port <port-or-0>
-pg-url <postgres-dsn>
-embed-endpoint <openai-compatible-embeddings-url>
-embed-model <model-name>
-embed-require
-embed-keepalive-interval <duration>
-llm-upstream <openai-compatible-base-url>
-require-auth
-mcp-audit-log <dir|-|empty>
```

There is no `-llm-model` flag. Set the LLM model with `LLM_MODEL` in the environment.

## Team identity and access

Use [profile presets](profile-presets.md) for PostgreSQL and required auth,
[enterprise identity](enterprise-identity.md) for LDAP/AD or SSO integration,
and [document management](document-management.md) for individual dataset grants.
Native LDAP, browser OIDC login and effective group/document ACLs are not
available. gRPC raw-storage operations require an active global superuser when
auth is enabled; ordinary user workflows should use the documented REST/MCP APIs.

## Security hardening

The dev default is an open server (no auth) for local single-user use. For
anything reachable beyond localhost:

```bash
# Require authentication on protected resource endpoints
./levara-server -require-auth ...

# Require tenant context where tenant enforcement applies; this is not a document ACL
LEVARA_TENANT_ENFORCED=1 ./levara-server -require-auth ...

# HMAC-pepper API-key hashing (falls back to JWT_SECRET)
LEVARA_API_KEY_PEPPER=<random> ./levara-server -require-auth ...
```

MCP transports: the stateless endpoint `/mcp/2026-07-28` enforces auth on
`tools/call` and resource reads (discovery stays public); the legacy session
transport `/mcp` keeps session-bound authorization.

Cognify batching is opt-in and off by default:

```bash
# Coalesce 4 chunks per LLM extraction call (fewer requests on large runs)
LEVARA_LLM_EXTRACT_BATCH_SIZE=4 ./levara-server ...
```

_Documentation/source cross-check: 2026-09-05. This does not assert a live deployment check._

## MCP integration

For MCP clients against the first-run server:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://127.0.0.1:8080/mcp"
    }
  }
}
```

If auth is enabled in another deployment, add the `Authorization` header with a Levara token.

## Docker

The Docker path remains available:

```bash
docker compose -f deploy/docker/docker-compose.yml up -d --build
```

Docker defaults are not the same as the current Mac launchd runtime. Verify ports, dimensions, and external endpoints after starting a container.

## Verification checklist

```bash
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://127.0.0.1:8080/version
curl -fsS http://127.0.0.1:9101/health
LEVARA_URL=http://127.0.0.1:8080/api/v1 ./levara health --details
```

Inside Hermes:

```text
mcp_levara_doctor(verbose=true)
mcp_levara_runtime_stats()
mcp_levara_check_drift()
```

## Next steps

- [README.md](README.md) — documentation index by role.
- [features-guide.md](features-guide.md) — every feature mapped to use cases and commands.
- [tutorials/00-getting-started-ru.md](tutorials/00-getting-started-ru.md) — Russian step-by-step guide.
- [current-state.md](current-state.md) — verified local runtime snapshot.
- [api-reference.md](api-reference.md) — complete REST endpoint reference (generated from the contract).
- [deployment.md](deployment.md) — deployment recipes, launchd/systemd/Docker notes.
- [profile-presets.md](profile-presets.md) — product profile packaging.
