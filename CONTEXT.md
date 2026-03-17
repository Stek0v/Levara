# Project Context & Knowledge Base

## Что это за проект

VectraDB — custom Go HNSW vector database, сравниваемая с LanceDB как backend
для Cognee AI memory platform. Проект включает: Go сервер, Python тесты (143),
бенчмарки, RAG pipeline тесты с Qwen LLM.

## Ключевые результаты бенчмарков (2026-03-17)

- VectraDB search: 2.6ms mean, 719 QPS, 93% keyword hit rate
- LanceDB search: 9.1ms mean, 150 QPS, 100% keyword hit rate
- VectraDB insert: 741 dp/s (bottleneck: WAL fsync + db.mu lock + HTTP)
- LanceDB insert: 5,067 dp/s (in-process, no overhead)
- Crash recovery: VectraDB 100% (WAL), LanceDB N/A
- All 143 tests PASSED, cases.md 20/20 covered

## Архитектурные решения

1. VectraDB работает в standalone/WAL mode (без Raft) — быстрее для single-node
2. HNSW indexing async — вставка не блокируется построением графа
3. Python adapter использует prefix-based коллекции (hack, нет нативных в Go)
4. conftest.py стабит все Cognee зависимости через sys.modules injection
5. Тесты работают напрямую с HTTP API, минуя Cognee pipeline

## Bottleneck-анализ VectraDB write path

1. WAL fsync (wal.go:115) — 10-30ms/batch (durability cost)
2. db.mu lock (db.go:199-236) — 20-70ms (JSON marshal + arena + disk under one mutex)
3. HTTP round-trip — 2-5ms/batch (microservice tax)

Из 2.6ms search latency: только 0.5ms = HNSW search, остальные 2.1ms = HTTP+JSON+Python overhead.

## Go rewrite analysis (2026-03-17)

- Vector search gain: 5-8x (in-process) / 2-3x (gRPC)
- Cognify gain: <5% (LLM API = 99% runtime)
- Recommendation: hybrid Go+Python (Go for hot path, Python for LLM)
- Phase 1-4 plan: 8-12 weeks
- Critical blocker: no Go equivalent of Python instructor library
- Full plan: see ~/.claude/plans/structured-tinkering-puffin.md

## Qwen 3.5 (LLM) — важные настройки

- Model: qwen3.5:latest (9.7B Q4_K_M) via Ollama at localhost:11434
- MUST prepend /no_think to user messages
- MUST set num_predict: 3000-5000 (thinking mode eats tokens)
- temperature: 0.1 for judge/evaluation tasks
- Remote llama.cpp at 10.23.0.64:9004 is NOT always reachable

## Use case recommendations

- VectraDB: read-heavy (>100:1 R/W), concurrent API, latency SLA <10ms
- LanceDB: batch ingestion, CRUD, prototyping, zero-ops

## Стек на хосте

- VectraDB: Docker, port 8080, dim=1024, 3 shards
- embed-server: port 9001, pplx-embed-context-v1-0.6b, FP16, CUDA, RTX 3090
- Ollama: port 11434, qwen3.5:latest (9.7B) + qwen3.5:27b (27.8B)
- Prometheus: port 9090

## Тестовые данные

- Книга: "Ураган" (Janet Edwards), 1430 chunks at 600 chars, dim=1024
- 15 semantic RU queries with expected keywords
- 5 off-topic queries for hallucination testing
- 5 adversarial/injection queries for safety testing
- 10 typo + 5 translit queries for adversarial robustness
- 5 EN + 3 mixed-language queries

## Git history (key commits)

```
fcdfa2b test: comprehensive benchmark + RAG cases (143 tests)
08366b2 test: book head-to-head VectraDB vs LanceDB
c5fed8f perf: async HNSW indexing
944abcc perf: standalone WAL-only mode
381da19 fix: cosine distance, WAL recovery, data persistence
```

## Python test dependencies

```
pytest>=9.0
pytest-asyncio>=1.3
aiohttp>=3.9
lancedb>=0.24
pydantic>=2.10
orjson>=3.10
```

## Migration notes

- Claude memory is at ~/.claude/projects/-home-USER-src-new_db/ (path-dependent)
- .env has hardcoded IP 10.23.0.64 (GPU server) — reconfigure for new host
- cognee/ is not committed (23MB, external dependency) — install separately
- VectraDB data/ contains WAL files (not committed, regenerated on start)
