# CLAUDE.md

## Project Overview

VectraDB benchmark and testing project: comparing custom Go HNSW vector database (VectraDB) with LanceDB (Rust/Arrow) as vector storage backends for the Cognee AI memory platform.

## Stack

| Component | Port | Description |
|-----------|------|-------------|
| VectraDB | 8080 | Go HNSW + WAL, standalone mode, 3 shards, dim=1024 |
| embed-server | 9001 | pplx-embed-context-v1-0.6b (dim=1024, FP16, CUDA) |
| Ollama | 11434 | Qwen 3.5 (9.7B/27B), local LLM for RAG tests |
| Prometheus | 9090 | VectraDB metrics |
| LanceDB | in-process | Rust/Python Arrow, used in tests directly |

## Project Structure

```
new_db/
  VectraDB/           # Go source (HNSW server)
    cmd/server/       # main.go entry point
    internal/store/   # db.go, wal.go, hnsw.go, arena.go, disk.go
    internal/http/    # handler.go (Fiber HTTP)
    internal/cluster/ # shard.go, node.go, fsm.go (Raft)
  tests/              # Python test suite (143 tests)
  cognee/             # Cognee platform (external, not committed)
  BENCHMARK_RESULTS.md # Comparison findings
  cases.md            # RAG test case specifications
  docker-compose.yml  # VectraDB + Prometheus
```

## Development Commands

```bash
# Start services
docker compose up -d --build

# Run all tests (requires embed-server + VectraDB + Ollama)
pytest tests/ -v -s

# Run only vector DB tests (no LLM needed)
pytest tests/test_rag_cases.py tests/test_comprehensive_comparison.py -v -s

# Run LLM pipeline tests (needs Ollama with qwen3.5)
pytest tests/test_rag_llm_cases.py -v -s

# Check service health
curl http://localhost:9001/health    # embed-server
curl http://localhost:8080/metrics   # VectraDB
curl http://127.0.0.1:11434/api/tags # Ollama

# Rebuild VectraDB after Go changes
docker compose down && docker compose up -d --build
```

## Key Benchmark Results

| Metric | VectraDB | LanceDB | Winner |
|--------|----------|---------|--------|
| Search latency (mean) | **2.6 ms** | 9.1 ms | VectraDB (3.5x) |
| Concurrent QPS | **719** | 150 | VectraDB (4.8x) |
| Insert throughput | 741 dp/s | **5,067 dp/s** | LanceDB (6.8x) |
| Scale 10K search | **2.6 ms** | 16.4 ms | VectraDB (6.3x) |
| Crash recovery | **100%** | N/A | VectraDB |

**VectraDB**: best for read-heavy concurrent API workloads (read:write > 100:1).
**LanceDB**: best for batch ingestion, CRUD, simple deployments.

## Test Architecture

Tests bypass Cognee pipeline and hit VectraDB HTTP API + LanceDB Python API directly with the same pre-computed embeddings from embed-server. This isolates vector DB performance from LLM/pipeline overhead.

Key shared components (duplicated across test files):
- `load_and_chunk_book()` — paragraph-based chunking with chapter detection
- `embed_texts()` — batch embedding via embed-server HTTP API
- `vectra_insert/search/delete()` — VectraDB HTTP helpers
- `LanceRecord/LancePayload` — LanceDB Pydantic schema (dim=1024)
- `QUERIES[]` — 15 semantic queries with expected keywords

## VectraDB Write Bottlenecks

1. **WAL fsync** (~10-30ms/batch) — `wal.go:115 file.Sync()`
2. **db.mu lock scope** (~20-70ms) — JSON marshal + arena + disk under one mutex (`db.go:199-236`)
3. **HTTP round-trip** (~2-5ms/batch) — microservice architecture tax

## Conventions

- Tests use `pytest.mark.asyncio` + `aiohttp.ClientSession` for async HTTP
- VectraDB record IDs use prefixes for test isolation: `book:`, `comp:`, `rag:`, `llm:`
- LanceDB uses temp directories (`tempfile.mkdtemp()`) per test to avoid state leakage
- conftest.py stubs all Cognee dependencies via `sys.modules` injection
- Qwen 3.5 via Ollama needs `/no_think` prefix + `num_predict: 3000-5000`

## Important Notes

- VectraDB is a **global store without native collections** — search returns ALL vectors. Collection isolation is done via ID prefix filtering on the client side.
- NDCG/Recall metrics for VectraDB can be misleadingly low when multiple tests insert data with different prefixes — run in clean state (`docker compose down -v`) for accurate quality metrics.
- Remote llama.cpp at `10.23.0.64:9004` is not always reachable. Use local Ollama at `127.0.0.1:11434`.
