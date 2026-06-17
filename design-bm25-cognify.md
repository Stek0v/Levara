# DESIGN: BM25 + Cognify в Levara — анализ включения

Status: **DRAFT**
Дата: 2026-06-17
Конфигурация: `standalone-embed`, MacBook Air M2

---

## 1. Текущее состояние

### 1.1 Инфраструктура

```
Levara: running standalone (PID via launchd)
  --dim=256 --port=8081 --grpc-port=0 --node-id=mac1 --data-dir=~/src/levara/data

EMBEDDING_ENDPOINT: http://127.0.0.1:9101/v1   ❌ DOWN (4 дня)
DB_PROVIDER:       sqlite (default, no $DB_HOST)
LLM_ENDPOINT:      не задан
Neo4j:             не настроен
BM25 index:        не инициализирован
```

### 1.2 Что уже реализовано в коде Levara

| Компонент | Где | Статус |
|---|---|---|
| **BM25** | `pkg/bm25/` — полный пакет | ✅ Работает |
|  | индекс (`bm25.Index`) | `pkg/bm25/bm25.go` |
|  | персистентность | `pkg/bm25/persist.go` — load/save to JSONL |
|  | HybridSearch (RRF fusion) | `pkg/bm25/hybrid.go` |
|  | создаётся на first use | `internal/grpc/service.go:64` — `make(map[string]*bm25.Index)` |
| **Cognify** | `pkg/orchestrator/` — полный pipeline | ✅ Работает |
|  | chunk → embed → HNSW | `pkg/orchestrator/pipeline.go` |
|  | RAG mode (`skip_graph=true`) | chunk + embed только, без LLM/PG |
|  | Full mode (entity extraction) | требует LLM + PG + embed |
|  | parent-child, sliding window | `pkg/chunker/` |
|  | BM25 auto-index при вставке | `pkg/orchestrator/pipeline.go:688-692` |
|  | Panic guard | `internal/http/cognify_panic_guard.go` |
| **Search** | `internal/http/api_search.go` | ✅ Работает |
|  | HYBRID = vector + BM25 (RRF) | `pkg/router/router.go` |
|  | CHUNKS_LEXICAL = BM25-only | router выбирает по capability |
|  | AUTO = router сам решает | учитывает HasBM25, HasEmbedding, HasNeo4j |

---

## 2. Требования для включения

### 2.1 Embed endpoint (:9101) — PRIMARY BLOCKER

**Что делает:** все embedding-операции.

| Путь | Куда идёт запрос | Что нужно |
|---|---|---|
| `keepalive` | `embed.NewClient().EmbedSingle("keepalive")` | Включить :9101 |
| `cognify (rag)` | `embed.NewClient().EmbedSingle(chunkText)` | Включить :9101 |
| `cognify (full)` | то же + LLM `Generate` | Включить :9101 + LLM |
| `vector search` | `embed.NewClient().EmbedSingle(query)` | Включить :9101 |

**Требования к модели:**
- Совместимость: OpenAI-compatible `/v1/embeddings` endpoint
- Размерность (dim): **256** (текущий `--dim=256` в Levara). Модель должна отдавать 256-d векторы
- Какая модель используется сейчас: вероятно `potion-code-16M` (из логов: `"Loaded collection ... dim=256, model=potion-code-16M"`)
- **Ограничение:** если поменять модель с другой размерностью → нужно переиндексировать все существующие векторы

**Куда настраивается:**
```
Флаг:    --embed-endpoint=http://127.0.0.1:9101/v1
Env:     EMBEDDING_ENDPOINT=http://127.0.0.1:9101/v1
         EMBEDDING_MODEL=potion-code-16M
```

### 2.2 BM25 lifecycle

**Как инициализируется:**
1. `internal/grpc/service.go:64` — `bm25Indexes: make(map[string]*bm25.Index)` при создании gRPC сервиса
2. Передаётся в API конфиг: `main.go:561` — `BM25Indexes: grpcSvc.BM25Indexes()`
3. Из API конфига → cognify handler → pipeline config
4. В pipeline: при вставке векторов проверяется `cfg.BM25Indexes != nil`, если да — создаётся/обновляется BM25 индекс для коллекции

**Текущее состояние:** BM25Indexes инициализируется как пустая map **всегда**. Проблемы нет. Когда прилетит первый cognify, BM25 начнёт наполняться.

**Требования:**
- Ничего менять не нужно — механизм уже встроен
- После первого cognify (RAG mode) в `pkg/mcp/tools_light.go:1` или через MCP `cognify` → BM25 наполнится
- После этого `search_type=HYBRID` будет работать (есть вектор + BM25)

**Ограничения:**
- BM25 индекс in-memory (персистится через `pkg/bm25/persist.go` в JSONL)
- При перезапуске сервера индекс перезагружается с диска (если есть persist-файл)
- **Отсутствует:** встроенная синхронизация BM25 при импорте данных без cognify (только batch-вставка)

### 2.3 LLM (entity extraction) — опционально

**Требуется только для cognify full mode** (graph entity extraction).

| Флаг | Значение | Откуда |
|---|---|---|
| `LLM_ENDPOINT` | `http://10.23.0.64:11434/v1` | env |
| `LLM_MODEL` | `qwen3.5:9b` | env |
| `LLM_PROVIDER` | `openai` (по умолчанию) | env |
| `--llm-proxy-port` | 0 (выключен) | флаг |
| `--llm-upstream` | `http://10.23.0.64:11434/v1` | флаг |

**Зачем:** entity extraction (Stage 2) — LLM анализирует каждый чанк и извлекает сущности (люди, места, технологии, события) и связи между ними.

**Без LLM:** работает RAG mode (`skip_graph=true`) — чистый chunk → embed → HNSW + BM25.

**Ограничения:**
- Entity extraction упирается в качество LLM. Qwen3.5-9B — reasonable для этой задачи
- Concurrency: `LLMConcurrency=1` в cognify handler (можно поднять до 3-5)
- Таймаут: `BACKGROUND_TASK_TIMEOUT_MS` (default 30min) — при большом количестве чанков может не хватить
- Cost: LLM вызов на каждый чанк. При 1000 чанках → 1000 LLM вызовов

### 2.4 PostgreSQL — опционально

**Требуется только для cognify full mode** (graph entity/edge storage).

**Что делает:**
- `graph_nodes` / `graph_edges` таблицы
- `datasets`, `data`, `dataset_data` — метаданные когнификации
- Community detection (Louvain) — stage 5
- История когнификации (`pipeline_status`)

**Без PG (SQLite):**

| Фича | SQLite | PostgreSQL |
|---|---|---|
| RAG mode (chunk → embed → HNSW) | ✅ Работает | ✅ |
| BM25 auto-index | ✅ Работает | ✅ |
| Entity extraction (LLM) | ❌ Не работает | ✅ |
| Graph upsert | ❌ Нет таблиц | ✅ |
| Community detection | ❌ | ✅ |
| `mcp_levara_cognify` full mode | ❌ | ✅ |
| `mcp_levara_add` (metadata) | ⚠ `$N → ?` конвертация | ✅ |

**Ключевое:** в RAG mode SQLite полностью достаточно.

---

## 3. SQLite vs PostgreSQL — детальное сравнение

### 3.1 Что поддерживает Levara

У Levara есть встроенный `SetSQLiteMode()` и функция `q()` (`pkg/ingest/metadata.go:19-40`), которая:
- Заменяет `$1`, `$2`... на `?` (Postgres → SQLite placeholder)
- Заменяет `NOW()` на `CURRENT_TIMESTAMP`
- Включается автоматически при `DB_PROVIDER=sqlite`

Схема (`internal/http/schema.go`) одна для обоих бекендов.

### 3.2 Ограничения SQLite

| Аспект | SQLite | PostgreSQL |
|---|---|---|
| **Concurrent writes** | Одна транзакция. WAL mode помогает, но не панацея | Полноценный MVCC |
| **Full-text search** | `FTS5` — работает, но отдельный API | `tsvector`/`tsquery` — встроен |
| **JSON operations** | Ограниченный `->`, `->>` | Полный `jsonb` с индексами |
| **User auth** | Нет | `users`, `principals`, JWT |
| **Tenancy** | Нет | `tenants`, `user_tenant`, `roles` |
| **Schema migrations** | Нет в Levara, только `CREATE TABLE IF NOT EXISTS` | `MigrateSchema()` — PostgreSQL |
| **Backup** | `cp` файла, WAL checkpoint | `pg_dump`/`pg_restore` (встроено в `pkg/backup/`) |
| **Connections** | `sql.Open("sqlite3", ...)` один коннектор | `MaxOpenConns=25` пул |
| **Entity graph** | Нет `graph_nodes`/`graph_edges` | Полноценные таблицы с FK |

### 3.3 Когда SQLite достаточно

- Только RAG mode (chunk → embed → HNSW + BM25)
- Single-user, single-process
- Нет потребности в entity graph и community detection
- Нет потребности в JWT-аутентификации

### 3.4 Когда нужен PostgreSQL

- Full mode cognify (entity extraction + graph)
- Multi-user / tenancy
- Community detection (Louvain)
- Persistent user management
- Schema migrations

### 3.5 Совместимость и миграция

**Обратная совместимость:**
- Полная — `q()` функция делает SQLite-адаптацию на лету
- Существующие данные в SQLite не пострадают при переключении на PG
- SQLite и PG могут сосуществовать (но не одновременно для одного instance)

**Миграция SQLite → PostgreSQL:**
- **Нет встроенного инструмента.** `pkg/backup/` умеет только PG→PG (pg_dump/psql)
- Ручной путь:
  1. `sssvms .dump` существующей SQLite БД
  2. Преобразовать SQLite DDL/DML в PostgreSQL совместимый формат
  3. Залить через psql
  4. **Проблема:** auto-increment, типы данных (INTEGER → SERIAL/BIGINT), `BLOB` → `BYTEA`
- **Рекомендация:** не мигрировать старые данные. При переключении на PG — начать с чистой БД. Старые HNSW/BM25 индексы и данные на диске остаются.
- Единственная потеря: pipeline_status, dataset метаданные. Векторы и BM25 в файлах, не в БД.

**Миграция PostgreSQL → SQLite:**
- Не имеет смысла — SQLite не поддерживает entity graph.
- Если нужно — те же ограничения.

---

## 4. Пошаговый план включения

### Phase 1: Поднять embed модель (:9101)

**Что делать на Mac:**
1. Запустить embed-сервис (potion-code-16M или другую модель с dim=256)
2. Проверить: `curl http://127.0.0.1:9101/v1/embeddings`
3. Levara keepalive начнёт успешно ходить каждые 10 минут
4. Проверить: `mcp_levara_search(search_query="test", mode="auto")` — должен работать векторный поиск

**Ключевой вопрос:** какая модель на :9101? Размерность должна совпадать с `--dim=256` в Levara. Если модель имеет другую dim → либо менять флаг Levara, либо модель.

### Phase 2: Первый cognify (RAG mode)

```
mcp_levara_cognify(
  data="твой текст",
  mode="rag",
  collection="levara"
)
```

**Что произойдёт:**
1. Chunking (merged strategy, 80-600 chars)
2. Embed каждого чанка через :9101
3. Insert в HNSW + BM25 index
4. BM25 JSONL persist на диск

**Проверить:**
- `mcp_levara_search(search_query="keyword", search_type="HYBRID")` — должны быть результаты
- `mcp_levara_search(search_query="keyword", search_type="CHUNKS_LEXICAL")` — BM25-only результаты

### Phase 3: Cognify full mode (если нужен entity graph)

**Требует:**
1. PostgreSQL (через Docker или внешний)
2. LLM (Qwen3.5-9B на 10.23.0.64)

**Запустить:**
```bash
levara-server \
  --profile standalone-embed \
  --embed-endpoint=http://127.0.0.1:9101/v1 \
  --pg-url=postgres://levara:***@host:5432/levara_db
```

**Проверить:**
- `mcp_levara_query_entity(name="...")` — graph traversal
- Community detection
- `mcp_levara_search(search_query="...", search_type="GRAPH")`

### Phase 4: BM25 инициализация для существующих данных

**Проблема:** если данные уже в HNSW (через `save_memory`), BM25 пуст.

**Решение:** запустить cognify RAG mode на те же тексты. Pipeline сам обновит BM25 при вставке векторов. Но HNSW upsert дедуплицирует по docID — дублирования не будет.

**Альтернатива:** нет встроенного «наполнить BM25 из существующего HNSW». Нужен cognify.

---

## 5. Риски и ограничения

| Риск | Серьёзность | Митигация |
|---|---|---|
| Embed модель и Levara имеют разные dim (256 vs 128/768/1024) | 🔴 | Проверить модель до включения. `--dim` флаг Levara |
| BM25 in-memory — потеря при краше без persist | 🟡 | Persist на каждый batch insert (уже реализован) |
| BM25 persist JSONL может вырасти большим | 🟢 | ~1KB на чанк. 1M чанков = 1GB. Норм |
| Cognify RAG mode без LLM — нет entity extraction | 🟢 | By-design |
| Slow embed endpoint тормозит cognify | 🟡 | `LLMConcurrency` + `BackgroundTaskTimeout=30min` |
| Нет миграции SQLite → PG | 🟡 | Ручной дамп + перезаливка или начать с чистой PG |
| `embed-require=true` с мёртвым :9101 — fatal | 🟡 | Включать только когда сервис стабилен |

---

## 6. Выводы

**Чтобы заработало прямо сейчас на SQLite (RAG mode)** — требуется только одно: поднять embed сервис на :9101.

- BM25: код готов, наполнится при первом cognify
- SQLite: достаточно для RAG mode
- LLM: не нужен
- PostgreSQL: не нужен
- Neo4j: не нужен

**Полноценный cognify (full mode)** дополнительно требует:
- PostgreSQL → поднять через Docker (`docker run postgres:17-alpine`)
- LLM → Qwen3.5-9B на 10.23.0.64 (уже есть)
- `--pg-url=postgres://...` флаг

**Миграция SQLite → PG:** не предусмотрена встроенными средствами. Рекомендуется стартовать с чистой PG для full mode, оставив старую SQLite БД для backward compat.
