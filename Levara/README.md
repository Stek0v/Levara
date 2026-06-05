# Levara

> High-performance knowledge graph engine for AI applications. Built in Go.

[![Go Version](https://img.shields.io/badge/Go-1.26+-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## Overview

Levara is a production-ready knowledge graph engine that transforms raw data into persistent, searchable knowledge graphs. It combines vector search (HNSW + AVX2 SIMD), BM25 full-text, a knowledge graph with temporal validity, a write-ahead log, and an LLM-powered cognify pipeline into a single Go binary.

Levara is the engine layer of **LevaraOS**, a unified persistent memory platform for AI agents and IDEs (Claude Code, Cursor). On top of the vector/graph core it ships a **memory palace** (durable agent memory with room × hall taxonomy), a **verifiable workspace** (Markdown-as-source-of-truth write layer), **System2 consolidation** (background memory compaction), and **Mac ↔ Pi sync**.

**Key features:**

- **15 search types** -- vector, graph, hybrid, temporal, chain-of-thought, NL-to-Cypher, and more
- **22+ document formats** -- PDF, DOCX, audio transcription via Whisper, images, code, and more
- **Multi-provider LLM** -- OpenAI, Anthropic, Ollama (local)
- **MCP server** for Claude Desktop / Cursor / Claude Code -- 66 tools across graph, memory, workspace, and observability surfaces
- **Memory palace** -- durable agent memory with room × hall taxonomy, pinning, diaries, and wake-up briefings
- **Verifiable workspace (Variant B)** -- `.md` files are the source of truth; vector/graph indexes are disposable derivatives, with commits, ACL, and an audit log
- **System2 consolidation** -- background janitor clusters near-duplicate memories and merges (cosine ≥ 0.97) or LLM-abstracts (0.85–0.97) them; fully reversible
- **Knowledge graph with temporal validity** -- entity edges carry valid-from/valid-until windows and auto-supersede on update
- **gRPC v1 + v2** -- canonical 47-RPC v1 surface plus a minimal write-only v2, both on `:50051`
- **JWT auth + rate limiting** -- shared HS256 auth across HTTP and gRPC, per-user and per-IP token buckets
- **Raft clustering** -- optional sharding and replication
- **Cross-encoder + graph-aware reranking** -- Cohere-compatible reranker and α·vector + β·graph + γ·rerank fusion
- **Louvain community detection** -- graph clustering for community-aware retrieval
- **Mac ↔ Pi sync** -- bearer-authenticated bidirectional sync with version-skew detection
- **SQLite + PostgreSQL** dual-database support
- **ARM64 support** -- runs on Raspberry Pi
- **S3 cloud storage** + Langfuse tracing
- **Zero-copy arena allocator** -- minimizes GC pauses
- **HNSW indexing** with AVX2 SIMD distance -- O(log N) ANN search
- **WAL group commit** -- 100% crash recovery validated
- **Prometheus metrics** -- Grafana-ready

## Quick Start

```bash
# Download
go install github.com/stek0v/levara/cmd/server@latest

# Start with SQLite (zero dependencies)
./levara-server -standalone=true -dim=768 -port=8080

# Or with Docker
docker compose up -d --build
# Levara: http://localhost:8080 | gRPC: localhost:50051
```

## Architecture

```
Client --> Levara HTTP :8080 / gRPC :50051 (v1 + v2) / MCP
              |
   transport  |-- JWT auth (HS256, shared HTTP + gRPC) + rate limiting
              |-- MCP server (66 tools)
              |
     storage  |-- HNSW Vector Index (in-process, AVX2 SIMD)
              |-- WAL Durability (group commit, crash recovery 100%)
              |-- Arena Memory Allocator (zero-copy, GC-free)
              |-- PostgreSQL / SQLite
              |-- Neo4j Graph DB (optional, SQL fallback)
              |
     compute  |-- BM25 Full-Text + Hybrid RRF
              |-- Cognify pipeline (chunk -> extract -> dedup -> embed -> write)
              |-- Cross-encoder + graph-aware reranking
              |-- Louvain community detection
              |-- LLM (OpenAI / Anthropic / Ollama)
              |
   platform   |-- Memory palace (room x hall, pins, diaries)
              |-- Verifiable workspace (Variant B, .md source of truth)
              |-- System2 consolidation (merge / abstract, reversible)
              |-- Sync (Mac <-> Pi, bearer auth, version-skew warning)
              |-- Raft cluster (optional sharding / replication)
              |-- Prometheus Metrics
```

```
Levara/
  cmd/
    server/         # HTTP + gRPC entry point (registers v1 + v2)
    cli/            # levara CLI
    backup/         # levara-backup (backup / restore)
    reconcile/      # SQL <-> vector consistency tool
    contract/       # MCP agent-contract codegen / check
    agent-hosts/    # agent host registry tooling
    loadtest/       # load testing
    qwen3rerank/    # rerank sidecar helper
  internal/
    store/          # db, wal, hnsw, arena, disk, collections
    http/           # REST router + 50+ handlers (search, cognify, mcp,
                    #   auth, sync, workspace, ratelimit, observe)
    grpc/           # service v1 (47 RPCs) + v2 + auth / ratelimit interceptors
    cluster/        # Raft sharding / replication (shard, node, fsm)
    metrics/        # Prometheus telemetry + bounded-cardinality user buckets
    contract/       # MCP contract types
  pkg/
    orchestrator/   # cognify pipeline (chunk -> extract -> dedup -> embed -> write)
    bm25/           # BM25 inverted index + hybrid RRF
    graph/          # knowledge graph construction
    graphdb/        # Neo4j integration + SQL fallback
    graphstore/     # graph persistence
    graphrank/      # graph-aware reranking (vector + graph + rerank)
    community/      # Louvain community detection
    rerank/         # cross-encoder reranking (Cohere-compatible)
    router/         # smart search routing + adaptive weights
    mcp/            # 66 MCP tools + Deps interface + output schemas
    consolidate/    # System2 memory consolidation (merge / abstract)
    workspace/      # verifiable memory workspace (Variant B)
    auth/           # shared JWT verification
    audit/          # audit log
    embed/          # embedding providers
    extract/        # entity extraction
    chunker/        # document chunking
    classify/       # text classification
    fileio/         # file I/O (22+ formats)
    fetch/          # URL / document fetching
    ingest/         # ingestion pipeline
    llm/            # LLM providers (OpenAI, Anthropic, Ollama)
    llmcache/       # LLM response caching
    llmproxy/       # LLM proxy / routing
    observe/        # Langfuse tracing
    ontology/       # ontology management
    temporal/       # temporal validity / time-aware edges
    aggregator/     # search result aggregation
    audio/          # audio transcription (Whisper)
    git/            # git repository analysis
    storage/        # S3 cloud storage
    vectorstore/    # vector store abstraction
    backup/         # backup / restore library
    runreg/         # background-run registry (TTL janitor)
    agenthosts/     # agent host registry
  proto/            # gRPC definitions (v1 + v2)
  webui/            # Next.js 15 WebUI
```

## Performance

Benchmarked on i7-7700 @ 3.60 GHz, Linux 6.8, dim=1024, gRPC transport.

### Search Latency

| Scale | p50 Latency | QPS | Notes |
|-------|-------------|-----|-------|
| 1K vectors | **0.99 ms** | **589** | HNSW + AVX2 |
| 10K vectors | **7.88 ms** | **480** | +695% scale, +3.7% latency |
| 100K vectors | **23.7 ms** | **143** | O(log N) stable |

### vs LanceDB (1.4K real embeddings)

| Metric | Levara | LanceDB | Speedup |
|--------|--------|---------|---------|
| Search p50 | **2.6 ms** | 9.1 ms | **3.5x** |
| Concurrent QPS | **589** | 109 | **5.4x** |
| Data ingestion | **0.08 ms/item** | 287+ ms | **3,379x** |
| Scale 100K search | **23.7 ms** | 203.7 ms | **8.6x** |
| Crash recovery | **100%** | N/A | Levara |

**Levara** wins on read-heavy concurrent workloads. **LanceDB** wins on batch ingestion of raw vectors.

## Search Types (15)

| # | Type | Description |
|---|------|-------------|
| 1 | `VECTOR` | Cosine similarity nearest-neighbor search |
| 2 | `GRAPH_COMPLETION` | Graph-aware search with entity relationship traversal |
| 3 | `HYBRID` | Combined vector + BM25 full-text search |
| 4 | `TEMPORAL` | Time-aware search across document versions |
| 5 | `COT` | Chain-of-thought multi-step reasoning search |
| 6 | `NL_CYPHER` | Natural language to Cypher query translation |
| 7 | `ENTITY` | Named entity extraction and search |
| 8 | `KEYWORD` | BM25 keyword-based full-text search |
| 9 | `SUMMARY` | Document summarization search |
| 10 | `CLASSIFICATION` | Text classification search |
| 11 | `ONTOLOGY` | Ontology-guided semantic search |
| 12 | `PROVENANCE` | Data lineage and source tracking |
| 13 | `AGGREGATE` | Multi-source aggregated ranked search |
| 14 | `STRUCTURED` | Structured metadata filtering + vector search |
| 15 | `GIT` | Git repository analysis and code search |

### Reranking (default-on)

Phase 2 ships a cross-encoder reranker that runs by default whenever
`RERANK_ENDPOINT` is configured. Clients do not need to set any flag --
search responses carry a per-result `reranked: true` to indicate which
rows were reordered by the sidecar. To opt out, send `"rerank": false`
in the `/api/v1/search` body. An adaptive gate (`RERANK_SCORE_GAP_THRESHOLD`)
can skip the cross-encoder when the candidate score spread is already wide.
See `docs/api-reference.md` and `docs/phase2-rerank-default-design.md` for the
tri-state semantics, latency budget (`RERANK_BUDGET_MS`, default 1500ms) and the
`levara_rerank_invocations_total{outcome=...}` Prometheus counter.

On top of the cross-encoder, `pkg/graphrank` fuses three signals --
`α·vector + β·graph + γ·rerank` -- so graph-connected results are
boosted alongside semantic relevance.

## Subsystems

### Memory palace

A durable agent-memory layer addressed on two independent axes: **room**
(*what* the memory is about -- a free-form subsystem/topic) and **hall**
(*what kind* of memory -- a controlled vocabulary: `fact`, `event`,
`decision`, `preference`, `advice`, `discovery`). Supports pinning critical
facts for cheap `wake_up` briefings, per-agent diaries (isolated namespaces),
and recall filtered by room/hall. Backed by the same HNSW + SQL stack, kept
SQL ↔ vector consistent on every write with a `reconcile_memory` sweep.

### Verifiable workspace (Variant B)

A write layer where `.md` files are the source of truth and the Levara
vector/graph indexes are disposable derivatives. Agents write through
`workspace_write` + `workspace_commit`; the workspace then indexes the
committed Markdown into Levara. Humans read the `.md` files directly in the
repo. Ships ACL/access checks, an audit log, conflict detection, background
index jobs, and reconcile/GC operations.

### System2 consolidation

A background janitor (opt-in via `CONSOLIDATION_INTERVAL`) periodically
compresses a collection's memory: it clusters near-duplicate/related records,
then mechanically **merges** them (cosine ≥ 0.97, keep newest) or
LLM-**abstracts** them (0.85 ≤ cosine < 0.97) into a single record. Fully
reversible -- sources are superseded and archived, not deleted, and restorable
via `consolidation_revert(run_id)`. Run on demand with `consolidate(..., dry_run=true)`
to preview.

### Sync (Mac ↔ Pi)

Bidirectional, bearer-authenticated sync between a local instance and an edge
server (Raspberry Pi). `sync(remote_url=..., direction=pull|push)` reconciles
collections; a version-skew check warns when the two binaries diverge.

### gRPC v1 + v2

Both services register on the same `:50051` listener. `levara.v1.LevaraService`
is the canonical surface (47 RPCs -- write, read, cognify, graph, hybrid
search). `levara.v2.LevaraServiceV2` is a minimal write-only subset (Insert,
BatchInsert, Delete, Search, Info) with typed `ErrorDetail`. v1 is long-term;
v2 is positioned as a minimal write API for new clients that don't need
graph/cognify endpoints.

## MCP Integration

Levara exposes an MCP (Model Context Protocol) server for seamless integration
with Claude Desktop, Cursor, Claude Code, and other MCP-compatible clients.
**66 tools** are registered (`pkg/mcp/tools.go`), grouped by surface:

| Surface | # | Tools |
|---------|---|-------|
| Knowledge graph & ingestion | 9 | `add`, `cognify`, `cognify_status`, `codify`, `list_data`, `delete`, `prune`, `prune_graph`, `ingestion_status` |
| Search & retrieval | 6 | `search`, `cross_search`, `query_entity`, `list_communities`, `analyze_commits`, `git_search` |
| Memory palace | 8 | `save_memory`, `recall_memory`, `list_memories`, `pin_memory`, `unpin_memory`, `wake_up`, `diary_write`, `diary_read` |
| Consolidation (System2) | 3 | `consolidate`, `consolidation_revert`, `reconcile_memory` |
| Chat history | 3 | `save_chat`, `recall_chat`, `search_chats` |
| Verifiable workspace | 25 | `workspace_write`, `workspace_read`, `workspace_index`, `workspace_search`, `workspace_commit`, `workspace_log`, `workspace_revert`, `workspace_manifest`, `workspace_delete`, `workspace_reconcile`, `workspace_reindex_paths`, `workspace_reindex_artifacts`, `workspace_context_artifacts`, `workspace_index_jobs`, `workspace_enqueue_index_job`, `workspace_retry_index_job`, `workspace_watch_status`, `workspace_run_start`, `workspace_run_get`, `workspace_access_check`, `workspace_audit_log`, `workspace_ops_status`, `workspace_conflicts`, `workspace_context`, `workspace_gc` |
| Context & sync | 7 | `set_context`, `get_project_context`, `sync`, `sync_status`, `add_feedback`, `get_feedback_stats`, `levara_instructions` |
| Observability | 5 | `doctor`, `heartbeat`, `runtime_stats`, `recent_errors`, `check_drift` |

### Configuration

Add to your MCP client config:

```json
{
  "mcpServers": {
    "levara": {
      "url": "http://localhost:8080/mcp"
    }
  }
}
```

## CLI

```bash
# Health check
levara health

# Add data
levara add "Your text content here" --dataset=my_project
levara add ./documents/ --dataset=my_project

# Process into knowledge graph
levara cognify --dataset=my_project --wait

# Search
levara search "What is the main architecture?" --type=GRAPH_COMPLETION
levara search "recent changes" --type=TEMPORAL --since=2025-01-01

# Dataset management
levara datasets list
levara datasets delete my_project

# Git analysis
levara git analyze --repo=. --since=2024-01-01
levara git diff-summary --repo=. --branch=main
```

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DB_PROVIDER` | `postgres` | Database backend (`sqlite` or `postgres`) |
| `DATABASE_URL` | `data/levara.db` | Database connection string (SQLite) |
| `VECTOR_DIM` | `768` | Vector dimensionality |
| `HTTP_PORT` | `8080` | HTTP server port |
| `GRPC_PORT` | `50051` | gRPC server port |
| `JWT_SECRET` | _(auto)_ | Shared HS256 secret for HTTP + gRPC auth. Random 32 bytes on empty (fine for dev, **must be set in prod** so tokens survive restarts) |
| `ENV` | _(unset)_ | `dev` enables `/swagger/*`; any other value disables it |
| `NEO4J_URI` | _(disabled)_ | Neo4j connection URI (SQL fallback when absent) |
| `LLM_PROVIDER` | `ollama` | LLM provider (`openai`, `anthropic`, `ollama`) |
| `LLM_MODEL` | `qwen3.5:latest` | LLM model name |
| `LLM_API_KEY` | _(none)_ | API key for OpenAI/Anthropic |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Ollama server URL |
| `EMBED_URL` | `http://localhost:9001` | Embedding server URL |
| `RERANK_ENDPOINT` / `RERANK_MODEL` | _(disabled)_ | Cross-encoder reranker; default-on when endpoint set |
| `RERANK_BUDGET_MS` / `RERANK_TIMEOUT_MS` | `1500` / `5000` | Rerank pass budget and per-request HTTP timeout |
| `RERANK_SCORE_GAP_THRESHOLD` | `0` | Adaptive gate: skip rerank when candidate score spread exceeds it (0 = always rerank) |
| `CONSOLIDATION_INTERVAL` | _(off)_ | Background consolidation janitor tick (Go duration, e.g. `30m`) |
| `S3_BUCKET` | _(disabled)_ | S3 bucket for cloud storage |
| `LANGFUSE_PUBLIC_KEY` | _(disabled)_ | Langfuse tracing public key |
| `MCP_ENABLED` | `true` | Enable MCP server |
| `HNSW_M` | `16` | Max HNSW connections per node |
| `HNSW_EF_MULT` | `8` | efConstruction multiplier |
| `HNSW_EF_MIN` | `32` | Minimum efSearch beam width |

### CLI Flags

```bash
levara-server \
  -standalone=true \
  -dim=768 \
  -port=8080 \
  -grpc-port=50051 \
  -hnsw-m=20 \
  -hnsw-ef-mult=10 \
  -hnsw-ef-min=50 \
  -data-dir=./data
```

## Deployment

### Docker

```bash
docker compose up -d --build
```

### Raspberry Pi (ARM64)

```bash
# Cross-compile
make arm64

# Copy to Pi
scp levara-arm64 pi@raspberrypi:~/levara/

# Install as systemd service
# See deploy/raspberry/ for full setup
```

### systemd

```ini
[Unit]
Description=Levara Knowledge Graph Engine
After=network.target

[Service]
Type=simple
ExecStart=/opt/levara/levara-server -standalone=true -dim=768
WorkingDirectory=/opt/levara
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Monitoring

Prometheus metrics at `http://localhost:8080/metrics`:

- `levara_insert_requests_total` / `levara_insert_duration_seconds`
- `levara_search_requests_total` / `levara_search_duration_seconds`
- `levara_vectors_total`
- `levara_wal_sync_duration_seconds`
- `levara_arena_pages_allocated`
- `levara_rate_limit_rejected_total{channel, bucket}`
- `levara_rerank_invocations_total{outcome}`

## API Reference

See [docs/api-reference.md](docs/api-reference.md) for complete HTTP and gRPC API documentation.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, code style, and pull request guidelines.

## License

[MIT](LICENSE)
