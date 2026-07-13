# Current implementation and local runtime state

_Last verified: 2026-07-05T01:46:42Z from the Mac launchd runtime, `/version`, `/health`, embed sidecar `/health`, `mcp_levara_doctor`, and `mcp_levara_runtime_stats`._

This page is the operational truth for the local Mac deployment. Product roadmap documents may describe target profiles or enterprise seams; use this page when you need to know what is actually running now.

## Source tree

| Item | Current value |
|---|---|
| Repo | `/Users/stek0v/src/levara` |
| Branch | `main` |
| Repo HEAD | `27efa39 remove agent dot-folders from repo (.claude .continue .cursor .gemini .kiro .opencode .vscode .pytest_cache)` |
| Running binary version | `991d61a` |
| Running build time | `2026-06-30T12:11:27Z` |
| Go version in running binary | `go1.26.4` |
| Protocols | MCP `2024-11-05`, gRPC `v1`, `v2` |

The checked-out source is ahead of the deployed binary. Rebuild + restart before assuming code at HEAD is active.

## Local Mac runtime

Levara runs under launchd as `com.stek0v.levara`.

```text
Binary:      /Users/stek0v/src/levara/levara-server
Working dir: /Users/stek0v/src/levara
HTTP:        http://127.0.0.1:8081
MCP:         http://127.0.0.1:8081/mcp
Health:      http://127.0.0.1:8081/health
Version:     http://127.0.0.1:8081/version
Log:         /Users/stek0v/src/levara/levara.log
Data:        /Users/stek0v/src/levara/data
```

Actual process args:

```bash
/Users/stek0v/src/levara/levara-server \
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
  -pg-url=postgres://stek0v@localhost:5432/levara?sslmode=disable \
  -embed-keepalive-interval=5m
```

Runtime health:

| Check | Current state |
|---|---|
| `/health` | `200 OK`, `{"health":"healthy","status":"ready","version":"levara-go"}` |
| Doctor summary | `8/9 ok, 1 warn, 0 fail` |
| PostgreSQL | connected |
| Embed service | connected at `http://127.0.0.1:9101/v1/embeddings` |
| LLM | connected at `http://localhost:11434/v1`, model `gemma4:e2b` |
| Neo4j | disabled |
| Rerank | disabled |
| Embedding drift | clean; all collections use `potion-code-16M` |
| BM25 coverage | warning: `51/52` user collections indexed; missing `_memories_pd` |

## Embedding sidecar

The embedding service is a separate launchd-managed FastAPI/model2vec sidecar.

```text
Endpoint: http://127.0.0.1:9101
Embeddings: http://127.0.0.1:9101/v1/embeddings
Health: curl http://127.0.0.1:9101/health
Model: potion-code-16M
Dim: 256
Backend: model2vec
```

Current health response:

```json
{"model":"potion-code-16M","dim":256,"ram_mb":259776,"backend":"model2vec"}
```

Important: Levara's `-embed-endpoint` must include `/v1/embeddings`. Passing only `/v1` makes the doctor derive the wrong health URL.

## Storage and index state

Current `runtime_stats` snapshot:

| Metric | Value |
|---|---:|
| Collections | 54 |
| Records | 582 |
| Vector dimension | 256 |
| Embedding model | `potion-code-16M` |
| Goroutines | 139 |
| Heap alloc | 230,365,136 bytes |
| Heap sys | 313,491,456 bytes |

Largest live collections include:

| Collection | Records |
|---|---:|
| `_memories_memeval_247b6d21b8` | 65 |
| `levara` | 56 |
| `_memories_memeval_247b6d21b8_scale` | 50 |
| `_memories_memeval_2d35a11391_scale` | 50 |
| `_memories_memeval_ec90b663a6` | 50 |
| `hermes` | 46 |
| `_memories_memeval_2d35a11391` | 43 |
| `Triplet_text` | 34 |
| `pd` | 23 |
| `_memories` | 22 |

## Profiles and server flags

Implemented functional server profiles are:

| Profile | Intended local behavior |
|---|---|
| `standalone` | WAL/local mode, external subsystems disabled unless explicitly provided by flags |
| `standalone-embed` | local mode with embedding enabled; current Mac deployment uses this |
| `full` / empty | no profile suppression; uses configured flags/env for full stack |

Active server flags from `./levara-server --help` include:

```text
-config-check
-profile string
-data-dir string
-dim int
-port int
-grpc-port int
-standalone
-require-auth
-pg-url string
-embed-endpoint string
-embed-model string
-embed-require
-embed-keepalive-interval string
-llm-upstream string
-llm-cache-size int
-llm-max-inflight int
-llm-proxy-port int
-neo4j-url string
-neo4j-user string
-neo4j-password string
-neo4j-database string
-mcp-audit-log string
-structured-extract-endpoint string
-structured-extract-timeout-ms int
-hnsw-m int
-hnsw-ef-min int
-hnsw-ef-mult int
-bootstrap
-node-id string
-shards int
-raft-addr string
-raft-port int
-join-addr string
```

There is no `-llm-model` flag. Set the model with `LLM_MODEL` in the launchd environment or shell. `-llm-upstream` only sets the upstream URL.

## CLI state

`make build` builds the server and CLI, but this repo currently also has a tracked/untracked directory named `levara/`. Because of that, `go build -o levara ./cmd/cli/` writes the CLI executable inside that directory instead of replacing it with a root-level binary.

Current local CLI executable:

```bash
./levara/cli
```

The CLI defaults to `LEVARA_URL=http://localhost:8080/api/v1`, so for the local Mac runtime use:

```bash
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health
```

Top-level CLI commands implemented in `cmd/cli/main.go`:

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

The CLI only supports global flags before the subcommand: `--url=...` and `--token=...`. `./levara/cli --help` is rejected as an unknown global flag; use `./levara/cli help`.

## Quick verification commands

```bash
curl -fsS http://127.0.0.1:8081/health
curl -fsS http://127.0.0.1:8081/version
curl -fsS http://127.0.0.1:9101/health
ps -p $(pgrep -f '/levara-server' | head -1) -o pid,lstart,args=
LEVARA_URL=http://127.0.0.1:8081/api/v1 ./levara/cli health
```

Inside Hermes, the authoritative runtime checks are:

```text
mcp_levara_doctor(verbose=true)
mcp_levara_runtime_stats()
mcp_levara_check_drift()
```

## Known gaps in the current deployment

- Running binary is not at repo HEAD; build/restart is needed to deploy `27efa39`.
- gRPC is disabled locally with `-grpc-port=0`.
- Neo4j and rerank are disabled.
- Doctor has one warning: BM25 is missing for `_memories_pd`.
- Product-profile docs (`personal`, `solo_pro`, `team`, `enterprise`) describe roadmap/product packaging and shipped config examples, not the current Mac runtime.
- Several older docs still mention `-dim=768`, port `8080`, and `./levara-server -standalone=true`; use this page and `docs/deployment.md` for the current local commands.
