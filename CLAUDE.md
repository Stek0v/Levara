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

## Levara MCP Memory — usage guide for Claude

Levara MCP — это твой постоянный memory layer. Используй проактивно: цель — чтобы ничего из принятых решений и найденных проблем не терялось между сессиями.

### Session start

1. `set_context(collection="levara")` — установить рабочий проект для всей сессии (один раз).
2. `wake_up(max_tokens=300)` — загрузить запиненные критические факты + top entities графа. Это **дешевле** чем `get_project_context` (~300 vs ~2K токенов) и хватает в 80% случаев.
3. Если задача в незнакомой области — `recall_memory(query="тема")` или `recall_memory(query="...", room="...", hall="decision")` чтобы поднять уже принятые решения.

### Mental model: room × hall

Каждая память имеет две независимые оси. **Заполняй обе всегда когда сохраняешь** — пустые поля резко снижают полезность recall.

| Ось | Что значит | Контролируется |
|---|---|---|
| `room` | *О чём* память: подсистема/тема внутри проекта | Свободная строка (auth, deploy, mcp, ocr-bench, kg-temporal, ...) |
| `hall` | *Что это за* память: жанр факта | Контролируемый словарь (см. ниже) |

**Hall vocabulary** (на неизвестном значении `save_memory` вернёт ошибку):

| hall | когда использовать |
|---|---|
| `fact` | Объективная характеристика: версия, размерность, IP, путь, число. «Levara HNSW dim=1024» |
| `event` | Что-то произошло в конкретный момент: deploy, merge, инцидент, бенчмарк. Всегда с датой в value. |
| `decision` | Архитектурный/проектный выбор: «решили X вместо Y потому что Z». Будущему тебе важно знать **почему**. |
| `preference` | Предпочтение пользователя: стиль, тон, инструменты, флаги. Кросс-проектное обычно. |
| `advice` | Практическое правило: «перед X сделай Y». Reusable рекомендация. |
| `discovery` | Найденный non-obvious инсайт/баг/гетча, который ты бы хотел вспомнить через полгода. |

### Decision rules — когда Claude должен сохранять (без просьбы)

Вот триггеры. На каждом — **немедленно** вызывай `save_memory` с правильным hall. Не жди завершения разговора.

| Триггер | Что делать |
|---|---|
| Пользователь принял архитектурное решение («давай X, не Y», «оставляем sqlite») | `save_memory(hall="decision", room="<подсистема>")` с **why** в value |
| Ты нашёл root cause бага после расследования | `save_memory(hall="discovery", room="<подсистема>")` с симптомом + причиной + фиксом |
| Пользователь поправил твой подход или дал стилевое указание | `save_memory(hall="preference")`, `pin_memory(priority=10)` если глобально |
| Появился новый сервис/endpoint/IP/порт | `save_memory(hall="fact")` + `pin_memory` если критическое |
| Завершён значимый этап работы (фича, рефакторинг, релиз) | `save_memory(hall="event")` с датой в value, перечислением изменений |
| Ты дал пользователю reusable рекомендацию которая может пригодиться снова | `save_memory(hall="advice")` |
| Пользователь упомянул дедлайн, мерж-фриз, чужую зависимость | `save_memory(hall="event")` с абсолютной датой |

**НЕ сохраняй:**
- Код, пути к файлам, имена функций — это есть в git/grep, и при ренейме станет stale.
- Git history, кто что коммитил — `git log` авторитетен.
- Промежуточные шаги текущей задачи — это для TaskCreate, не memory.
- Дублирующее то что уже есть в auto-memory CLAUDE.md (стиль ответов, no_skip_tests, и т.п.).

### Pin policy

`pin_memory(key, priority)` помечает запись как «всегда возвращай в `wake_up`». Используй экономно — wake_up обрезается по бюджету.

| priority | для чего |
|---|---|
| **10** | Глобальные предпочтения пользователя (стиль, язык, ключевые правила) |
| **8** | Критическая инфраструктура (endpoints, IPs, порты, версии) |
| **5** | Текущие активные решения по основным подсистемам |
| **1-3** | Опциональный контекст |

Если в `wake_up` стало слишком много шума — `unpin_memory(key)` для устаревших.

### Recall patterns

| Сценарий | Команда |
|---|---|
| «Что мы решили по auth?» | `recall_memory(query="auth", hall="decision")` |
| «Все мои стилевые предпочтения» | `list_memories(hall="preference")` |
| «Какие баги мы находили в migrations?» | `recall_memory(query="migration", hall="discovery")` |
| «Что есть по теме deploy?» | `list_memories(room="deploy")` |
| «Через несколько проектов» | `cross_search(collections=["levara","other"])` |

### Knowledge graph + temporal validity

Когда `cognify` обработал текст — в графе появились entities/edges с временными окнами:
- `query_entity(name="X")` — текущее состояние entity (только активные edges).
- `query_entity(name="X", as_of="2026-01-01")` — снапшот на конкретную дату.
- Edges с relation из whitelist (`assigned_to`, `role_is`, `status_is`, `located_in`, `lives_in`, `works_at`, `owns`, `reports_to`, `current_state`, `is_a`) автоматически супершедятся при добавлении нового — старые помечаются `valid_until=now, superseded_by=<new>`.

### Per-agent diaries

При запуске специализированных subagents (Explore, Plan, code-review):
- `diary_write(agent="reviewer", key="...", value="...")` — запись в изолированный namespace `owner_id="agent:reviewer"`.
- `diary_read(agent="reviewer", query="...")` — читает только свои записи, не загрязняет project memory.

Используй когда subagent делает повторяющуюся работу (review, monitoring, planning) и хочет вести свой контекст между запусками.

### Search с фильтром по metadata

`search` теперь принимает `room` и `tags`. При наличии фильтра HNSW делает overfetch ×3 и отсеивает по chunk metadata. Используй когда коллекция большая и без фильтра вылезают результаты из других контекстов:
```
search(search_query="...", room="auth", tags=["security"])
```

`add` и `cognify` принимают те же `room`/`tags` чтобы chunks несли эту metadata.

### Sync (Mac ↔ Pi)

- Mac (`localhost:8081`) ↔ Pi (`10.23.0.53:8080`)
- `sync(remote_url="http://10.23.0.53:8080/api/v1", direction="pull")`
- CLI: `sync_levara` / `man_levara`

### Полный список MCP tools (25)

**Knowledge graph & search:** `cognify`, `cognify_status`, `search`, `cross_search`, `query_entity`, `analyze_commits`, `git_search`, `codify`

**Data:** `add`, `list_data`, `delete`, `prune`

**Memory (palace):** `save_memory`, `recall_memory`, `list_memories`, `pin_memory`, `unpin_memory`, `wake_up`, `diary_write`, `diary_read`

**Chat history:** `save_chat`, `recall_chat`, `search_chats`

**Context & sync:** `set_context`, `get_project_context`, `sync`, `add_feedback`, `get_feedback_stats`
