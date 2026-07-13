# Getting Started with Levara

This guide describes the current checked-in server and the verified local Mac runtime. For a full snapshot of the running instance, see [current-state.md](current-state.md).

## Prerequisites

- Go 1.26+ for source builds.
- Docker and Docker Compose if you use the container path.
- Optional: PostgreSQL for SQL-backed memory/metadata.
- Optional: an OpenAI-compatible embedding endpoint for vector search and cognify.
- Optional: an OpenAI-compatible LLM endpoint for full graph/entity extraction.
- Optional: Neo4j and rerank sidecars; both are disabled in the current local Mac runtime.

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
| `levara` CLI | `./cmd/cli` | If a directory named `levara/` exists, Go writes the executable inside it, currently `./levara/cli`. |

The current repository has a `levara/` directory, so use `./levara/cli` locally unless you explicitly build the CLI to another path:

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

## Current Mac runtime

The verified local deployment is not the minimal run. It is a launchd service on `:8081` with PostgreSQL, local embeddings, and local LLM configured:

```bash
./levara-server \
  -profile=standalone-embed \
  -dim=256 \
  -port=8081 \
  -grpc-port=0 \
  -data-dir=/Users/stek0v/src/levara/data \
  -node-id=mac1 \
  -require-auth=false \
  -embed-endpoint=http://127.0.0.1:9101/v1/embeddings \
  -embed-model=potion-code-16M \
  -llm-upstream=http://localhost:11434/v1 \
  -pg-url='postgres://stek0v@localhost:5432/levara?sslmode=disable' \
  -embed-keepalive-interval=5m
```

Verified state on 2026-07-05T01:46:42Z:

| Component | State |
|---|---|
| HTTP/MCP | `http://127.0.0.1:8081`, `/mcp` |
| gRPC | disabled (`-grpc-port=0`) |
| Embeddings | `potion-code-16M`, 256 dimensions, `http://127.0.0.1:9101/v1/embeddings` |
| LLM | `gemma4:e2b` via `http://localhost:11434/v1` |
| PostgreSQL | connected |
| Neo4j | disabled |
| Rerank | disabled |
| Doctor | `8/9 ok`, one BM25 warning for `_memories_pd` |
| Collections | 54 collections, 582 records |

## Embedding sidecar

The local embedding service is separate from Levara:

```bash
curl -fsS http://127.0.0.1:9101/health
```

Expected current response:

```json
{"model":"potion-code-16M","dim":256,"ram_mb":259776,"backend":"model2vec"}
```

Use the full embeddings path in Levara config:

```text
http://127.0.0.1:9101/v1/embeddings
```

Do not shorten it to `/v1`; the doctor health derivation depends on the `/v1/embeddings` suffix.

## CLI usage

The CLI defaults to `http://localhost:8080/api/v1`. For the current Mac runtime:

```bash
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health --details
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
./levara/cli --url=http://127.0.0.1:8081/api/v1 health
./levara/cli --token=$LEVARA_TOKEN datasets list
```

`./levara/cli --help` is currently rejected as an unknown global flag. Use:

```bash
./levara/cli help
```

## Add data and search

Via CLI:

```bash
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli add \
  "Levara is a memory and search layer for AI agents." \
  --dataset=demo

LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli cognify \
  --dataset=demo \
  --collection=demo \
  --wait

LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli search \
  "memory layer" \
  --type=HYBRID \
  --top-k=5
```

Via HTTP/MCP clients, use the server base URL `http://127.0.0.1:8081` for the current Mac runtime.

## Server configuration facts

Current functional server profiles:

| Profile | Meaning |
|---|---|
| `standalone` | WAL/local mode, external subsystems disabled unless explicit flags/env enable them. |
| `standalone-embed` | Local mode with embeddings enabled; current Mac runtime uses this. |
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

## MCP integration

For MCP clients against the current Mac runtime:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://127.0.0.1:8081/mcp"
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
curl -fsS http://127.0.0.1:8081/health
curl -fsS http://127.0.0.1:8081/version
curl -fsS http://127.0.0.1:9101/health
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health --details
```

Inside Hermes:

```text
mcp_levara_doctor(verbose=true)
mcp_levara_runtime_stats()
mcp_levara_check_drift()
```

## Next steps

- [current-state.md](current-state.md) — verified local runtime snapshot.
- [api-reference.md](api-reference.md) — HTTP and gRPC API documentation.
- [deployment.md](deployment.md) — deployment recipes, launchd/systemd/Docker notes.
- [profile-presets.md](profile-presets.md) — product profile packaging.
