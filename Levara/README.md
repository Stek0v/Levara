# Levara

> High-performance knowledge graph engine for AI applications. Built in Go.

[![Go Version](https://img.shields.io/badge/Go-1.26+-blue.svg)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)

## Overview

Levara is a production-ready knowledge graph engine that transforms raw data into persistent, searchable knowledge graphs. It combines vector search (HNSW), graph databases (Neo4j), and LLM-powered entity extraction into a single binary.

**Key features:**

- **15 search types** -- vector, graph, hybrid, temporal, chain-of-thought, NL-to-Cypher, and more
- **22+ document formats** -- PDF, DOCX, audio transcription via Whisper, images, code, and more
- **Multi-provider LLM** -- OpenAI, Anthropic, Ollama (local)
- **MCP server** for Claude Desktop / Cursor integration (15 tools)
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
Client --> Levara HTTP :8080 / gRPC :50051 / MCP
              |-- HNSW Vector Index (in-process, AVX2 SIMD)
              |-- WAL Durability (group commit, crash recovery 100%)
              |-- Arena Memory Allocator (zero-copy, GC-free)
              |-- PostgreSQL / SQLite
              |-- Neo4j Graph DB (optional)
              |-- LLM (OpenAI / Anthropic / Ollama)
              |-- BM25 Full-Text Search
              |-- Prometheus Metrics
```

```
levara/
  cmd/
    server/         # Main entry point
    cli/            # CLI tool
    benchmark/      # Benchmark suite
    loadtest/       # Load testing
  internal/
    store/          # db.go, wal.go, hnsw.go, arena.go, disk.go
    http/           # handler.go (Fiber HTTP)
    cluster/        # shard.go, node.go, fsm.go (Raft)
  pkg/
    aggregator/     # Search result aggregation
    audio/          # Audio transcription (Whisper)
    bm25/           # Full-text search
    chunker/        # Document chunking
    classify/       # Text classification
    embed/          # Embedding providers
    extract/        # Entity extraction
    fetch/          # URL/document fetching
    fileio/         # File I/O (22+ formats)
    git/            # Git repository analysis
    graph/          # Knowledge graph construction
    graphdb/        # Neo4j integration
    ingest/         # Data ingestion pipeline
    llm/            # LLM providers (OpenAI, Anthropic, Ollama)
    llmcache/       # LLM response caching
    llmproxy/       # LLM proxy/routing
    observe/        # Langfuse tracing
    ontology/       # Ontology management
    orchestrator/   # Pipeline orchestrator
    storage/        # S3 cloud storage
    temporal/       # Temporal search
  pipeline/         # Pipeline definitions
  proto/            # Protobuf definitions
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

## MCP Integration

Levara exposes an MCP (Model Context Protocol) server for seamless integration with Claude Desktop, Cursor, and other MCP-compatible clients.

**15 tools available:** `add`, `cognify`, `search`, `datasets_list`, `dataset_delete`, `health`, `prune`, `status`, `git_analyze`, `git_diff_summary`, `classify`, `summarize`, `extract_entities`, `ontology_build`, `graph_query`.

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
| `DB_PROVIDER` | `sqlite` | Database backend (`sqlite` or `postgres`) |
| `DATABASE_URL` | `data/levara.db` | Database connection string |
| `VECTOR_DIM` | `768` | Vector dimensionality |
| `HTTP_PORT` | `8080` | HTTP server port |
| `GRPC_PORT` | `50051` | gRPC server port |
| `NEO4J_URI` | _(disabled)_ | Neo4j connection URI |
| `LLM_PROVIDER` | `ollama` | LLM provider (`openai`, `anthropic`, `ollama`) |
| `LLM_MODEL` | `qwen3.5:latest` | LLM model name |
| `LLM_API_KEY` | _(none)_ | API key for OpenAI/Anthropic |
| `OLLAMA_URL` | `http://127.0.0.1:11434` | Ollama server URL |
| `EMBED_URL` | `http://localhost:9001` | Embedding server URL |
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

## API Reference

See [docs/api-reference.md](docs/api-reference.md) for complete HTTP and gRPC API documentation.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development setup, code style, and pull request guidelines.

## License

[MIT](LICENSE)
