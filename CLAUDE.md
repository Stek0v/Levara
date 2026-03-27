# CLAUDE.md

## Project Overview

Cognevra benchmark and testing project: production-optimized vector database combining the Go HNSW engine (originally VectraDB by Rupam) with the Cognee AI memory platform interface. Benchmarked against LanceDB (Rust/Arrow) as an alternative vector storage backend.

## Stack

| Component | Port | Description |
|-----------|------|-------------|
| Cognevra | 8080 | Go HNSW + WAL, standalone mode, 3 shards, dim=1024 |
| embed-server | 9001 | pplx-embed-context-v1-0.6b (dim=1024, FP16, CUDA) |
| Ollama | 11434 | Qwen 3.5 (9.7B/27B), local LLM for RAG tests |
| Prometheus | 9090 | Cognevra metrics |
| LanceDB | in-process | Rust/Python Arrow, used in tests directly |

## Project Structure

```
new_db/
  Cognevra/           # Go source (HNSW server)
    cmd/server/       # main.go entry point
    internal/store/   # db.go, wal.go, hnsw.go, arena.go, disk.go
    internal/http/    # handler.go (Fiber HTTP)
    internal/cluster/ # shard.go, node.go, fsm.go (Raft)
  tests/              # Python test suite (143 tests)
  cognee/             # Cognee platform (external, not committed)
  BENCHMARK_RESULTS.md # Comparison findings
  cases.md            # RAG test case specifications
  docker-compose.yml  # Cognevra + Prometheus
```

## Development Commands

```bash
# Start services
docker compose up -d --build

# Run all tests (requires embed-server + Cognevra + Ollama)
pytest tests/ -v -s

# Run only vector DB tests (no LLM needed)
pytest tests/test_rag_cases.py tests/test_comprehensive_comparison.py -v -s

# Run LLM pipeline tests (needs Ollama with qwen3.5)
pytest tests/test_rag_llm_cases.py -v -s

# Check service health
curl http://localhost:9001/health    # embed-server
curl http://localhost:8080/metrics   # Cognevra
curl http://127.0.0.1:11434/api/tags # Ollama

# Rebuild Cognevra after Go changes
docker compose down && docker compose up -d --build
```

## Key Benchmark Results

| Metric | Cognevra | LanceDB | Winner |
|--------|----------|---------|--------|
| Search latency (mean) | **2.6 ms** | 9.1 ms | Cognevra (3.5x) |
| Concurrent QPS | **719** | 150 | Cognevra (4.8x) |
| Insert throughput | 741 dp/s | **5,067 dp/s** | LanceDB (6.8x) |
| Scale 10K search | **2.6 ms** | 16.4 ms | Cognevra (6.3x) |
| Crash recovery | **100%** | N/A | Cognevra |

**Cognevra**: best for read-heavy concurrent API workloads (read:write > 100:1).
**LanceDB**: best for batch ingestion, CRUD, simple deployments.

## Test Architecture

Tests bypass Cognee pipeline and hit Cognevra HTTP API + LanceDB Python API directly with the same pre-computed embeddings from embed-server. This isolates vector DB performance from LLM/pipeline overhead.

Key shared components (duplicated across test files):
- `load_and_chunk_book()` — paragraph-based chunking with chapter detection
- `embed_texts()` — batch embedding via embed-server HTTP API
- `cognevra_insert/search/delete()` — Cognevra HTTP helpers
- `LanceRecord/LancePayload` — LanceDB Pydantic schema (dim=1024)
- `QUERIES[]` — 15 semantic queries with expected keywords

## Cognevra Write Bottlenecks

1. **WAL fsync** (~10-30ms/batch) — `wal.go:115 file.Sync()`
2. **db.mu lock scope** (~20-70ms) — JSON marshal + arena + disk under one mutex (`db.go:199-236`)
3. **HTTP round-trip** (~2-5ms/batch) — microservice architecture tax

## Conventions

- Tests use `pytest.mark.asyncio` + `aiohttp.ClientSession` for async HTTP
- Cognevra record IDs use prefixes for test isolation: `book:`, `comp:`, `rag:`, `llm:`
- LanceDB uses temp directories (`tempfile.mkdtemp()`) per test to avoid state leakage
- conftest.py stubs all Cognee dependencies via `sys.modules` injection
- Qwen 3.5 via Ollama needs `/no_think` prefix + `num_predict: 3000-5000`

## Important Notes

- Cognevra supports native collections via `CollectionManager` — each collection is an independent HNSW+Arena+WAL stack. No prefix hacks needed.
- NDCG/Recall metrics can be misleadingly low when multiple tests insert data with different prefixes — run in clean state (`docker compose down -v`) for accurate quality metrics.
- Remote llama.cpp at `10.23.0.64:9004` is not always reachable. Use local Ollama at `127.0.0.1:11434`.

## Levara MCP Memory

Levara MCP подключена и содержит проектный контекст. Используй её проактивно:

**При старте работы:**
- Контекст загружается автоматически через SessionStart hook
- Если нужна конкретная информация — `recall_memory(query="тема")`

**Во время работы:**
- Перед исследованием незнакомой области — `recall_memory` или `search` по Levara
- Для поиска по нескольким проектам — `cross_search(collections=[...])`
- Для переключения контекста — `set_context(collection="project_name")`

**После завершения значимой задачи:**
- Сохрани ключевые решения и результаты: `save_memory(key="...", value="...", type="project")`
- Не сохраняй: код, пути к файлам, git history — это доступно через git/grep

**Синхронизация:**
- Mac (localhost:8081) ↔ Pi (10.23.0.53:8080)
- `sync(remote_url="http://10.23.0.53:8080/api/v1", direction="pull")`
- CLI: `sync_levara` / `man_levara`

**19 MCP tools:** cognify, search, list_data, delete, prune, cognify_status, add, analyze_commits, git_search, save_memory, recall_memory, list_memories, save_chat, recall_chat, search_chats, get_project_context, set_context, cross_search, sync
