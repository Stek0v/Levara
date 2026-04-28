# Архитектура Levara и устройство памяти

**Версия:** 20.04 release (2026-04-21, коммиты 95a95df → 84e45c5)
**Аудитория:** инженеры, контрибьюторы и AI-агенты, которые пишут/читают/меняют состояние через HTTP, gRPC или MCP.

Документ — не про production-benchmark'и, не про CLAUDE.md-deployment. Здесь **только** как устроены слои памяти и как они взаимодействуют — чтобы человек или агент понимал, что именно происходит, когда он вызывает `POST /cognify` или MCP-тул `save_memory`.

---

## Оглавление

1. [Общая картина](#1-общая-картина)
2. [Слои памяти](#2-слои-памяти)
3. [Транспорты](#3-транспорты-http-grpc-mcp)
4. [Запись — с нуля до готовой памяти](#4-запись--с-нуля-до-готовой-памяти)
5. [Чтение — как получить обратно](#5-чтение--как-получить-обратно)
6. [Модификация и удаление](#6-модификация-и-удаление)
7. [Работа пользователя](#7-работа-пользователя-webui--cli--sdk)
8. [Работа агента (MCP)](#8-работа-агента-mcp)
9. [Инварианты и гарантии](#9-инварианты-и-гарантии)
10. [Приложение: ссылки на код](#10-приложение-ссылки-на-код)

---

## 1. Общая картина

```
                   ┌───────────────────────────────────────────────┐
                   │             Клиенты / агенты                  │
                   │                                               │
                   │  WebUI (Next.js)  SDK (curl/Python)  AI agent │
                   │       │                │                 │    │
                   └───────┼────────────────┼─────────────────┼────┘
                           │                │                 │
                   ┌───────▼────────────────▼─────────────────▼────┐
                   │           Транспортный слой                   │
                   │                                               │
                   │   HTTP :8080       gRPC :50051    MCP /mcp    │
                   │  (Fiber + CORS)   (v1 + v2)     (JSON-RPC 2.0) │
                   │                                               │
                   │     └─── auth → rate-limit → metrics ─────┘    │
                   └───────┬────────────────┬─────────────────┬────┘
                           │                │                 │
                   ┌───────▼────────────────▼─────────────────▼────┐
                   │          Прикладной слой (internal/http)      │
                   │                                               │
                   │  api_datasets · api_cognify · api_search      │
                   │  api_upload · api_admin · search_strategy     │
                   │  memories · sessions · feedback · sync        │
                   └───────┬───────────────────────────────────────┘
                           │
        ┌──────────────────┼────────────────────────────────────────────┐
        │                  │                                            │
┌───────▼───────┐   ┌──────▼──────────┐   ┌────────────────┐   ┌────────▼────────┐
│  Vector store │   │  Graph store    │   │ Project memory │   │   Reliability   │
│  (HNSW+BM25)  │   │  (Neo4j + SQL)  │   │   (Postgres)   │   │   & control     │
│               │   │                 │   │                │   │                 │
│ internal/store│   │ pkg/graph       │   │ memories table │   │ runreg (TTL)    │
│ pkg/bm25      │   │ pkg/graphdb     │   │ interactions   │   │ adaptive router │
│ WAL durability│   │ pkg/community   │   │ chat + diary   │   │ heartbeats log  │
│               │   │ (Louvain)       │   │ feedback       │   │ rate-limit      │
└───────────────┘   └─────────────────┘   └────────────────┘   └─────────────────┘
```

**Принцип разделения:**

- **Vector** хранит только эмбеддинги + сырые чанки. Быстрый top-k поиск.
- **Graph** хранит извлечённые сущности и связи с темпоральной валидностью — что было верно в момент X.
- **Project memory** хранит структурированные ключ-значения (факты, решения, преференции) с опциональной индексацией в vector для семантического recall.
- **Reliability** — метаданные о самом процессе: кто что запустил, какие запросы шли, что сломалось.

---

## 2. Слои памяти

Семь физических слоёв, каждый со своим API и гарантиями.

### 2.1. Vector store — HNSW + BM25 + WAL

**Где живёт:** `internal/store/db.go` (HNSW + arena), `internal/store/wal.go` (durability), `pkg/bm25/` (инвертированный индекс).

**Что хранит:**
- Векторные представления чанков (768/1024/variable-dim).
- Сырой текст чанка + metadata JSON (`room`, `tags`, `dataset_id`, `document_title`, `section`).
- BM25-индекс терминов для лексического поиска и гибридных запросов (RRF-fusion).

**Durability:**
- Group-commit WAL — fsync батчит до 16 записей в одно IO (см. `wal.go:fsyncLoop`).
- Single-pass sequential recovery при старте (после T16) — `Insert(id,v1) → Delete(id) → Insert(id,v2)` восстанавливается корректно с `v2` на месте.
- Тумбстонинг в HNSW при Delete (после T16 даже во время recovery) — поиск пропускает удалённые.

**Коллекции:** `*store.CollectionManager` мультиплексирует несколько HNSW-графов под разные домены (один на датасет или на `_memories_<collection>` для семантического recall проектных фактов).

**Записывается при:**
- `POST /add` (файл-upload) → затем `POST /cognify` нарезает и пишет.
- `POST /cognify` напрямую с `texts[]`.
- MCP-тулы `cognify`, `add`.
- Авто-индексация при `save_memory` (если есть embed endpoint) — копия в `_memories` коллекцию.
- Cross-instance `sync` операция переносит + пере-эмбеддит.

### 2.2. Graph store — Neo4j + Postgres fallback

**Где живёт:** `pkg/graph/` (типы), `pkg/graphdb/` (Neo4j writer), таблицы `graph_nodes` + `graph_edges` в Postgres.

**Что хранит:**
- Узлы: `id`, `name`, `type` (`Person`, `Organization`, `TemporalEvent`, ...), `description`, `dataset_id`, `confidence`, `extracted_at`.
- Рёбра: `source_id`, `target_id`, `relationship_name` (`ASSIGNED_TO`, `LOCATED_IN`, `WORKS_AT`, ...), `edge_text`, `valid_from`, `valid_until`, `superseded_by`.

**Темпоральная логика:**
- Рёбра из whitelist (`assigned_to`, `role_is`, `status_is`, `located_in`, `lives_in`, `works_at`, `owns`, `reports_to`, `current_state`, `is_a`) автоматически супершедятся: новое ребро отмечает старое как `valid_until=now, superseded_by=<new_id>`.
- Запрос `query_entity(name, as_of)` возвращает снапшот на любой момент времени.
- Без `as_of` возвращаются только активные (`valid_until IS NULL`).

**Communities:** `pkg/community/louvain.go` — Louvain-detection кластеров, с иерархическим суммированием через LLM (`community.SummarizeHierarchy`) — результаты в `graph_communities` + эмбеддинги в коллекции `_community_summaries`.

**Записывается при:**
- `POST /cognify` (если не `mode=rag`) — стадия 2 извлекает через LLM, стадия 3 дедуп, стадия 4 пишет в Neo4j + SQL parallel.
- `POST /memify` — post-cognify обогащение (Louvain + саммари сообществ).
- MCP `analyze_commits` — специализированная цепочка для git-истории.

### 2.3. Project memory — таблица `memories`

**Где живёт:** `internal/http/memories.go` + `internal/http/schema.go`.

**Схема:**
```sql
memories (
  id TEXT PRIMARY KEY,
  key TEXT NOT NULL,           -- семантический ключ (auth_flow, deploy_freeze_date, ...)
  value TEXT NOT NULL,         -- содержимое памяти
  type TEXT NOT NULL,          -- 'user' | 'project' | 'feedback'
  owner_id TEXT NOT NULL,      -- user_id или 'agent:<name>' для diary
  collection_name TEXT NOT NULL,
  room TEXT NOT NULL,          -- auth, deploy, mcp, ocr-bench, ...
  hall TEXT NOT NULL,          -- fact, event, decision, preference, advice, discovery
  is_pinned BOOLEAN,           -- отмечено как критичное для wake_up
  pin_priority INTEGER,        -- сортировка при wake_up (10 > 1)
  created_at TIMESTAMPTZ,
  updated_at TIMESTAMPTZ
)
```

**Ось `hall` (контролируемый словарь):**

| hall | Когда использовать |
|---|---|
| `fact` | Объективная характеристика: «HNSW dim=1024», «IP сервера 10.23.0.64» |
| `event` | Что-то произошло в конкретный момент: deploy, merge, инцидент |
| `decision` | Архитектурный выбор с обоснованием: «sqlite вместо postgres — нет CGO» |
| `preference` | Стилевое указание пользователя: «терс отвечать без emoji» |
| `advice` | Reusable рекомендация: «перед X сделай Y» |
| `discovery` | Non-obvious инсайт/баг: «pipeline теряет Insert после Delete — T16» |

**Ось `room`:** свободная строка — подтопик коллекции. Даёт `recall_memory(query=..., room=auth, hall=decision)` точность выше, чем голый semantic search.

**Pinning:** `pin_memory(key, priority)` — включает в `wake_up` (критичный контекст сессии). Размер `wake_up` ограничен бюджетом токенов (~200 по-умолчанию), так что high-priority — для глобальных preferences; low-priority — опциональный контекст.

**Дополнительная векторизация:** при `save_memory` с настроенным `EmbedClient` + `Collections` мгновенный fire-and-forget создаёт вектор и пишет в `_memories_<collection>` — это делает `recall_memory` семантическим, а не текстовым LIKE.

### 2.4. Session interactions — `interactions`

**Где живёт:** `internal/http/sessions.go`.

**Схема:**
```sql
interactions (id, session_id, user_id, query, response, search_type, created_at)
```

**Назначение:** краткосрочная разговорная память. Когда пользователь (или агент) делает `search` с `session_id`, хендлер `GetSessionContext` поднимает последние N пар query/response и **pre-pends** их к пользовательскому промпту. LLM видит историю диалога без необходимости её передавать явно.

**Записывается автоматически:**
- После каждой `/search/text` с заполненным `session_id`.
- После каждого `/cognify` как «entities extracted» (трассировка, не полезная для контекста).

### 2.5. Chat history (MCP-tools only)

**Где живёт:** таблица не нарисована в миграциях — хранится в project memory с `type=chat`, `collection_name=<session_id>` (MCP-layer convention).

**API (MCP only — на момент 20.04 нет REST-эквивалента):**
- `save_chat(session_id, messages[], collection)` — аппенд массива `{role, content}`.
- `recall_chat(session_id, collection)` — возврат всех сообщений сессии.
- `search_chats(query, collection)` — семантический поиск по всем сессиям.

### 2.6. Diary — per-agent namespace

**Где живёт:** та же таблица `memories`, но `owner_id = 'agent:<name>'` вместо user_id.

**Назначение:** изолированное пространство для специализированных агентов (reviewer, architect, oncall). Агент пишет туда свои заметки, и они **не видны** в обычном `recall_memory` пользователя. Полезно когда агент запускается повторно и хочет поднять свой контекст без шума project memory.

**API:**
- `diary_write(agent, key, value, collection?)`
- `diary_read(agent, query?, collection?)`

### 2.7. Observability + control memory

Эти слои не держат пользовательскую информацию, но критичны для понимания состояния:

- **`heartbeats`** — событийный лог. Записи: doctor-runs, sync операции, cognify completions, prune. Пагинируемый GET `/heartbeats?type=&limit=`. Используется `doctor` для degradation-detection.
- **`runreg` (в памяти)** — активные pipeline-запуски (`cognify`, `analyze_commits`). После завершения статус держится 1h, потом TTL-janitor (добавлен в T3.1) удаляет.
- **`feedback`** → **`router_weights`** — feedback-driven адаптация весов search-strategies. Пользователь/агент рейтингуют результат 1-5; `pkg/router/adaptive` пересчитывает weights → `AUTO` мод начинает выбирать успешные стратегии чаще.
- **`rate_limit` buckets** — in-memory per-IP / per-user token buckets, не переживают restart.

---

## 3. Транспорты: HTTP, gRPC, MCP

Любая операция с памятью проходит через один из трёх транспортов. Выбор зависит от клиента.

### 3.1. HTTP (Fiber, :8080)

**Кто использует:** WebUI (Next.js React Query), curl-скрипты, Python-SDK на `requests`, любой внешний сервис.

**Middleware chain на `/api/v1/*`:**

```
CORS → logger → JWT auth → UserRateLimiter (100/min/user)
  → Prom HTTP instrumentation → Tenant isolation → handler
```

`/auth/login` + `/auth/register` — отдельно, до JWT middleware, с собственным `AuthRateLimiter` (10/min/IP, shared bucket для login+register — защита от credential stuffing).

**OpenAPI:** 26 аннотированных handler'ов → `docs/swagger.{json,yaml}`. Swagger UI на `/swagger/*` когда `ENV=dev`.

### 3.2. gRPC (:50051, v1 + v2)

**Кто использует:** внутренние сервисы, клиенты с типизированными stubs, streaming-потребители (SSE-эквивалент через server streaming).

**Interceptor chain (после T19):**

```
auth (JWT из metadata.authorization) → rate-limit (per peer IP)
  → metrics → handler
```

**Whitelist:** `levara.v1.LevaraService/Info` проходит без auth (для health-probes).

**v1 vs v2 (после T10):**
- `levara.v1.LevaraService` — существующий контракт, остаётся 3 месяца после релиза v2.
- `levara.v2.LevaraServiceV2` — новый контракт с **типизированным `ErrorDetail`** (code + message + details map) и naming-алиасами `Add`/`Save`/`Create` для `Insert`. Оба сервиса на одном порту; gRPC dispatcher сам выбирает по имени метода.

### 3.3. MCP (JSON-RPC 2.0, endpoint `/mcp`)

**Кто использует:** AI-агенты (Claude Code, Cursor, Cline, custom LLM-клиенты с MCP-стеком).

**Транспорт:** HTTP POST для унарных вызовов + SSE для stateful сессий. `Mcp-Session-Id` header keys сессию.

**33 инструмента** разбиты по функциональным группам:

| Группа | Tools |
|---|---|
| Knowledge graph / cognify | `cognify`, `cognify_status`, `list_communities`, `prune_graph` |
| Search | `search`, `check_drift`, `cross_search` |
| Data management | `add`, `list_data`, `delete`, `prune` |
| Memory (palace) | `save_memory`, `recall_memory`, `list_memories`, `wake_up`, `pin_memory`, `unpin_memory` |
| Temporal graph | `query_entity` (поддерживает `as_of` snapshot) |
| Chat | `save_chat`, `recall_chat`, `search_chats` |
| Diary (agent-scoped) | `diary_write`, `diary_read` |
| Context | `get_project_context`, `set_context` |
| Git / code | `analyze_commits`, `git_search`, `codify` |
| Sync / feedback | `sync`, `add_feedback`, `get_feedback_stats` |
| Diagnostics | `doctor`, `heartbeat` |

Все 33 теперь имеют **InputSchema + OutputSchema** (JSON Schema draft 2020-12, T14 + docs polish). Клиенты могут валидировать и вход, и выход.

---

## 4. Запись — с нуля до готовой памяти

Возьмём две типичные сценарки: пользователь загружает файл, агент сохраняет memo.

### 4.1. Путь «file → vector + graph»

```
┌─────────────┐   POST /add             ┌──────────────────┐
│   Пользова- │ ─────── file ──────────▶│ addHandler       │
│    тель     │                          │ (api_upload.go)  │
└─────────────┘                          └────────┬─────────┘
                                                  │
                                                  │ 1) Сохраняет файл на диск (StoragePath/<uuid>)
                                                  │ 2) Инсерт в `data` (metadata)
                                                  │ 3) Линкует в `dataset_data`
                                                  │
                                                  ▼
                                         ┌──────────────────┐
┌─────────────┐   POST /cognify         │ cognifyHandler   │
│   Пользова- │ ───── dataset_id ──────▶│ (api_cognify.go) │
│    тель     │                          └────────┬─────────┘
└─────────────┘                                   │
                                                  │ Кладёт в runreg.Status
                                                  │ Возвращает run_id немедленно (202 Accepted)
                                                  ▼
                                         ┌──────────────────────────────────┐
                                         │ outer goroutine + defer recover  │
                                         │  (T15 panic guard)               │
                                         └──────────┬───────────────────────┘
                                                    │
                                                    │ stageSnapshot atomic.Pointer[string]
                                                    │ (T15 C2 race fix)
                                                    │
                                                    ▼
                                         ┌──────────────────────────────┐
                                         │ orchestrator.Run              │
                                         │  (pkg/orchestrator)           │
                                         │                               │
                                         │  Stage 1: Chunking            │
                                         │   chunker.Chunk(strategy)     │
                                         │                               │
                                         │  Stage 2: LLM extract         │
                                         │   extractEntities (extract.go)│
                                         │   → LLMCache.Get/Put          │
                                         │                               │
                                         │  Stage 3: Dedup               │
                                         │   graph.Dedup + semantic      │
                                         │   dedup по EmbedClient        │
                                         │                               │
                                         │  Stage 3b: Temporal           │
                                         │   extract dates, bind edges   │
                                         │                               │
                                         │  Stage 4: Parallel write      │
                                         │   ├─ Neo4j (graphdb.Writer)   │
                                         │   ├─ Postgres graph_nodes +   │
                                         │   │  graph_edges              │
                                         │   └─ Vector (BatchInsert      │
                                         │      через Collections)       │
                                         │                               │
                                         │  Stage 5: Communities         │
                                         │   Louvain + hierarchical      │
                                         │   summarize                   │
                                         └──────────────────────────────┘
                                                    │
                                                    │ Progress events via progressCh
                                                    ▼
                                         ┌──────────────────────────────┐
                                         │ cognifyStreamHandler SSE     │
                                         │ GET /cognify/:id/stream      │
                                         │  event: progress (каждые 500мс)
                                         │  event: done     (terminal)  │
                                         └──────────────────────────────┘
                                                    │
                                                    ▼
                                         ┌──────────────────────────────┐
                                         │ PersistPipelineStatus         │
                                         │  → data.pipeline_status       │
                                         │  → runStatus FINAL            │
                                         └──────────────────────────────┘
```

**Durability/atomicity на каждой стадии:**

- Stage 1: чанки существуют только в памяти до Stage 4.
- Stage 2: LLM-cache key = `hash(model + text + prompt + temp)`. Повторный запуск пропускает LLM.
- Stage 4: Neo4j-запись и vector-запись **параллельны** (не в транзакции). Если упадёт Neo4j, векторы уйдут; если упадёт vector — граф в Neo4j будет без индекса. Recovery: повтор `cognify` пропускает уже-обработанные датасеты через `CheckPipelineStatus`.

**Что видит пользователь в реальном времени:**

- WebUI подключается к SSE stream и обновляет progress bar.
- На `event: done` фронт вызывает React Query invalidation → датасет появляется в списке, чанки — в поисковых результатах, граф — в visualizer'e.

### 4.2. Путь «факт → project memory»

```
┌───────────┐  POST /memories                ┌─────────────────────┐
│ Агент/    │ ────── key/value/hall ────────▶│ saveMemoryHandler   │
│ пользов.  │                                 │ (memories.go)       │
└───────────┘                                 └──────────┬──────────┘
                                                         │
                                  1. INSERT INTO memories ↓
                                                         │
                                                         ▼
                                               ┌─────────────────────┐
                                               │   Postgres/SQLite   │
                                               └──────────┬──────────┘
                                                          │
                                  2. Если cfg.EmbedClient ≠ nil:
                                                          │
                                                          ▼
                                               ┌─────────────────────┐
                                               │ cfg.EmbedClient      │
                                               │ .EmbedSingle(text)   │
                                               └──────────┬──────────┘
                                                          │
                                  3. Collections.Insert(_memories_<collection>, ...)
                                                          ▼
                                               ┌─────────────────────┐
                                               │   Vector store       │
                                               │   (HNSW + BM25)      │
                                               └─────────────────────┘
```

**Ключевые детали:**

- SQL-запись обязательна. Векторная — best-effort: если embed-endpoint недоступен, memory всё равно сохранится, просто `recall_memory` будет только по LIKE.
- При `pin: true` в запросе флаг `is_pinned=true` + `pin_priority` выставляются в той же транзакции — `wake_up` видит pinned сразу.

### 4.3. MCP-путь (агент с tool-calling)

Клиент (LLM с MCP-стеком) делает JSON-RPC:

```json
{
  "jsonrpc": "2.0",
  "method": "tools/call",
  "params": {
    "name": "save_memory",
    "arguments": {
      "key": "gemopus_deployed_20260412",
      "value": "Gemopus-4-26B-A4B-it-Preview-Q4_K_M.gguf as primary LLM (llama-server :11434), replaces Gemma3",
      "type": "project",
      "collection": "levara",
      "room": "deploy",
      "hall": "event",
      "pin": true,
      "pin_priority": 8
    }
  },
  "id": 1
}
```

**Под капотом (pkg/mcp + internal/http/mcp.go):**

1. `mcpHandler.HandleRequest` парсит JSON-RPC, находит `save_memory` в `ToolDescriptors()`.
2. Валидация через `InputSchema` — отклоняет запросы без `key` или `value`.
3. Диспатч в `ToolSaveMemory(ctx, deps, args)` (`pkg/mcp/tool_memory.go`).
4. Тул вызывает `deps.Embed(ctx, text)` для вектора и `deps.CollectionInsert(...)` для сохранения. Фактическое SQL-INSERT делает тот же `saveMemoryHandler` через общий `Deps` интерфейс.
5. Ответ упаковывается в `ToolResult{Content: [{type:"text", text:JSON}]}` с валидацией против `OutputSchema`.

**Чем отличается от прямого POST /memories:**

- MCP-тулы идут через тот же бэкенд, но с ограниченным `Deps` интерфейсом (34 метода, `pkg/mcp/deps.go`), что даёт контракт-чистоту и позволяет тестам подменять реализации.
- MCP сессия `set_context(collection="levara")` запоминает коллекцию по-умолчанию, и дальнейшие вызовы `save_memory` без явного `collection` используют её.

---

## 5. Чтение — как получить обратно

### 5.1. Stratified search через `/search/text` (REST) или `search` (MCP)

```
┌──────────────────┐
│ POST /search/text│
│                  │
│ query_type?      │
└────────┬─────────┘
         │
         ▼
┌──────────────────────────────────────────────────────────────┐
│ searchHandler (api_search.go)                                │
│                                                              │
│ 1. Если query_type="AUTO" или пустой:                        │
│    ├─ router.Route(query, caps) → Decision                   │
│    │   (pkg/router/router.go + adaptive.go)                  │
│    └─ Сигналы: cypher-pattern, temporal-tokens, code tokens, │
│       composite-entities, graph-keywords, fallback           │
│                                                              │
│ 2. Adaptive weights (feedback-driven): пересчёт confidence   │
│    через AdjustScore(strategy_name, base_score)              │
│                                                              │
│ 3. metrics.SearchRequestsByType{search_type, source="router"|"explicit"}
│                                                              │
│ 4. cfg.SearchStrategies.Get(queryType).Execute(c, cfg, req)  │
│    → один из 16 strategy: CHUNKS, HYBRID, GRAPH_COMPLETION,  │
│      CYPHER, NATURAL_LANGUAGE, TRIPLET_COMPLETION,           │
│      CODING_RULES, COMMUNITY_LOCAL/GLOBAL,                   │
│      GRAPH_COMPLETION_CONTEXT_EXTENSION/COT, TEMPORAL, ...   │
└──────────────────────────────────────────────────────────────┘
```

**Стратегии по семьям:**

| Family | Стратегии | Что делает |
|---|---|---|
| Pure vector | `CHUNKS`, `CHUNKS_LEXICAL` (BM25) | Топ-k по similarity / BM25 |
| Hybrid | `HYBRID`, `WEIGHTED_HYBRID` | RRF-fusion vector + BM25 |
| Graph | `GRAPH_COMPLETION`, `GRAPH_SUMMARY_COMPLETION` | Vector → entity-ids → neighbours → LLM answer |
| Graph-extended | `GRAPH_COMPLETION_CONTEXT_EXTENSION` | 2-hop вокруг найденных entities |
| Graph-CoT | `GRAPH_COMPLETION_COT` | LLM разбивает вопрос на под-вопросы, каждый получает graph-context |
| Raw Cypher | `CYPHER` | Прямой Cypher к Neo4j (только read, writes блокируются) |
| NL → Cypher | `NATURAL_LANGUAGE` | LLM генерирует Cypher из естественного языка |
| Triplet | `TRIPLET_COMPLETION` | Поиск в triplet-коллекциях (source→rel→target) |
| Community | `COMMUNITY_LOCAL`, `COMMUNITY_GLOBAL` | Ответ на базе Louvain-сообществ + summaries |
| Temporal | `TEMPORAL` | Date-aware поиск через графовые темпоральные метки |
| Code | `CODING_RULES` | Специализация для code-entities |
| Full-text | `SUMMARIES` | Поиск по коммьюнити-саммари или document summaries |
| RAG | `RAG_COMPLETION` | Vector retrieval + LLM answer |

Неизвестный `query_type` падает на fallback — `CHUNKS`.

**Room/tags фильтрация:** любая стратегия с `req.Room` или `req.Tags` делает overfetch ×3 в vector и пост-фильтрует по chunk.metadata (room/tags были прописаны при cognify).

**Rerank:** `req.Rerank=true` + настроенный `RerankEndpoint` → после основной стратегии все результаты переранжируются cross-encoder'ом. Поле `reranked: true/false` per-result (после A.2 fix).

### 5.2. Recall памяти — `recall_memory` / `wake_up`

**`recall_memory(query, hall?, room?)`:**

1. Семантический поиск: `EmbedBatch([query])` → top-k по `_memories_<collection>` vector коллекции.
2. Если эмбеддинг недоступен — fallback на SQL `WHERE value ILIKE '%query%'`.
3. Фильтрация по `hall` + `room` если заданы.
4. Обогащение полями из `memories` table (created_at, pinned, ...).

**`wake_up(collection, max_tokens=200, top_entities=5)`:**

1. Подгружает pinned-memories (priority-ordered).
2. Добавляет top-N entities из графа — именно те, у которых больше всего активных рёбер (ORDER BY edge_count DESC).
3. Обрезает по бюджету токенов (1 токен ≈ 4 символа).
4. Возвращает `{pinned: [...], top_entities: [...], tokens_used: ~190}`.

Это дешевле `get_project_context` (~300 vs ~2K токенов), хватает в большинстве session-start вариантов.

### 5.3. `query_entity` — темпоральные снапшоты

```
query_entity(name="Alice")
  → SELECT * FROM graph_edges 
    WHERE source.name = 'Alice' AND valid_until IS NULL
    ORDER BY valid_from DESC

query_entity(name="Alice", as_of="2026-01-01")
  → SELECT * FROM graph_edges
    WHERE source.name = 'Alice'
      AND valid_from <= '2026-01-01'
      AND (valid_until IS NULL OR valid_until > '2026-01-01')
```

Позволяет вопросы: *«На 2026-01-01 кто был назначен на проект X?»*

### 5.4. Session-aware search (RAG)

Если `req.SessionID` задан:

1. `prependSessionContext` подтягивает N прошлых interactions.
2. Собирает системный prompt: `"Previous conversation:\n<Q1>\n<A1>\n<Q2>\n<A2>\n---\nCurrent question: <Q>"`.
3. LLM отвечает с контекстом.
4. После ответа — `recordInteraction(session_id, user_id, query, answer, search_type)` пишет в `interactions`.

Результат: чат-сессия ведёт себя как диалог, а не как серия независимых поисков.

---

## 6. Модификация и удаление

### 6.1. Delete-семантика

Все delete-операции **идемпотентны** — повторный вызов с несуществующим id возвращает 200, не 404:

- `DELETE /datasets/:id` → soft: удаляет только link в `dataset_data`, реальные `data` остаются (для cross-dataset sharing).
- `DELETE /datasets/:id/data/:dataId` → удаляет data record (hard).
- `DELETE /memories/:key` → удаляет из SQL; **не удаляет** вектор в `_memories_<collection>` (это wart, надо чистить через отдельный prune).
- `delete` MCP-тул → аналогично.
- `POST /delete` (vector) → HNSW.MarkDeleted(idx) + WAL entry OpDelete.
- `pin_memory` / `unpin_memory` → меняют `is_pinned` флаг, не данные.

### 6.2. Superseding (graph)

Как уже описано в 2.2: при записи нового ребра с отношением из whitelist старое автоматически помечается `valid_until=now, superseded_by=<new>`. Старое остаётся читаемым через `query_entity(as_of=...)`.

### 6.3. Prune

**`POST /prune/data`** (superuser-only после M5) — стирает `datasets`, `dataset_data`, `data`. Граф, векторы, memories, interactions остаются.

**`POST /prune/system`** (superuser-only) — добавляет `graph_nodes`, `graph_edges`. Векторы тоже остаются — их нужно чистить отдельно через `DELETE /collections/:name` или на уровне store.

**`prune_graph` (MCP)** — целевая очистка: удаляет superseded edges старше N дней + опционально orphan nodes. Более хирургическое решение для regular maintenance.

### 6.4. Sync — cross-instance replication

**`sync(remote_url, direction, types[], collections?, since?)`:**

- `direction=pull`: тянет memories, interactions, graph, optionally vector-collections с remote Levara. Векторы **реэмбеддятся** локальным моделью — поэтому dim может отличаться.
- `direction=push`: то же в обратную сторону.
- `since=<RFC3339>` — инкрементально.
- Автоматически логирует в `heartbeats` (event_type=sync).

Используется для федерации Mac↔Pi (см. `sync_levara` CLI-команду).

### 6.5. Re-embedding migration

**`POST /reembed`** (API registration в main.go через `RegisterReembedAPI`):

- Запускает фоновый процесс: читает все chunks из коллекции, реэмбеддит новым endpoint/model, пишет в новую коллекцию.
- Прогресс через `syncImportRuns` registry.
- Используется когда меняется embedding model и старая коллекция нужна в новом пространстве.

---

## 7. Работа пользователя (WebUI / CLI / SDK)

### 7.1. Типичные сценарии WebUI

**Загрузить документ → поискать:**

1. `/login` → JWT cookie + localStorage token.
2. `/datasets` → создать dataset → upload.tsx выбрасывает файл через `POST /api/v1/add`.
3. Auto-cognify запускается в datasets/page.tsx (вызов `POST /api/v1/cognify`), runId подписывается на SSE через `useCognifyProgress` (T8 убрал дублирующий polling).
4. После `event: done` статус файла меняется на `ready`. React Query invalidates `['datasets']` и `['collections']`.
5. Пользователь идёт в `/search`, выбирает коллекцию в dropdown, делает запрос.
6. `useSearch()` mutation → POST → результат в UI (chunks либо RAG-ответ).
7. Клик «👍/👎» на результате → `submitFeedback` → adaptive router постепенно учится.

**Пересобрать знания (memify):**

- В WebUI пока нет кнопки — memify запускается через REST/MCP: `POST /memify` с dataset_id. Louvain + community summaries обогащают существующий граф.

### 7.2. CLI / SDK

**curl примеры:**

```bash
# Login + save token
TOKEN=$(curl -s -X POST localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"u@test.local","password":"pw"}' | jq -r .token)

# Upload a file
curl -X POST localhost:8080/api/v1/add \
  -H "Authorization: Bearer $TOKEN" \
  -F "data=@doc.pdf" -F "datasetName=docs"

# Search
curl -X POST localhost:8080/api/v1/search/text \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query_text":"quarterly revenue","query_type":"HYBRID","top_k":5}'

# Save project memory
curl -X POST localhost:8080/api/v1/memories \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"key":"q3_target","value":"$2M ARR","type":"project","room":"business","hall":"decision"}'
```

**gRPC (с токеном):**

```bash
grpcurl -H "authorization: Bearer $TOKEN" -plaintext \
  -d '{"collection":"docs","id":"x","vector":[0.1,0.2,0.3]}' \
  localhost:50051 levara.v1.LevaraService/Insert

# v2 с типизированной ошибкой
grpcurl -H "authorization: Bearer $TOKEN" -plaintext \
  -d '{"collection":"docs","query_text":"revenue"}' \
  localhost:50051 levara.v2.LevaraServiceV2/Search
```

---

## 8. Работа агента (MCP)

Агенты (Claude Code, Cursor, Cline, custom) подключаются к MCP-серверу и получают 33 инструмента. Это **рекомендованный паттерн для AI-системы, которая хочет надёжно помнить между сессиями**.

### 8.1. Стандартный старт сессии

```
1. set_context(collection="levara")
   → все дальнейшие tool-calls по-умолчанию используют levara.

2. wake_up(max_tokens=300)
   → возвращает pinned memories + top entities. Agent вкладывает это в
     свой system-prompt / скрытый контекст.

3. Опционально: recall_memory(query="<что агент собирается делать>")
   → семантически похожие решения/факты из прошлых сессий.
```

**Когда `wake_up` недостаточно:** большой cold-start, редкое возвращение к проекту → `get_project_context(collection="levara", include_related=["sibling_project"])` вернёт ~2K токенов с memories + collection stats + key entities + recent interactions.

### 8.2. Правила записи (что агент должен сохранять)

Из `CLAUDE.md` Decision rules (агент должен следовать без явного указания):

| Триггер | Что делать |
|---|---|
| Пользователь принял арх. решение (*«давай X, не Y»*, *«оставляем sqlite»*) | `save_memory(hall="decision", room="<подсистема>")` с **why** в value |
| Агент нашёл root cause бага после расследования | `save_memory(hall="discovery", room="<подсистема>")` симптом + причина + фикс |
| Пользователь поправил подход | `save_memory(hall="preference")` + `pin_memory(priority=10)` если глобально |
| Появился новый сервис/endpoint/IP/порт | `save_memory(hall="fact")` + `pin_memory` если критическое |
| Завершён значимый этап работы (фича, релиз) | `save_memory(hall="event")` с абсолютной датой в value |
| Агент дал reusable рекомендацию | `save_memory(hall="advice")` |
| Пользователь упомянул дедлайн, мерж-фриз | `save_memory(hall="event")` с абсолютной датой |

**НЕ сохранять:**
- Код, пути, имена функций (есть git/grep).
- Git-history / кто что коммитил (`git log` авторитетен).
- Промежуточные шаги текущей задачи (это `TaskCreate`, не memory).
- Дубли с CLAUDE.md автопамятью.

### 8.3. Правила чтения

| Сценарий | Рекомендованный tool |
|---|---|
| «Что мы решили по auth?» | `recall_memory(query="auth", hall="decision")` |
| «Все мои стилевые предпочтения» | `list_memories(hall="preference")` |
| «Какие баги мы находили в migrations?» | `recall_memory(query="migration", hall="discovery")` |
| «Что по теме deploy?» | `list_memories(room="deploy")` |
| Через несколько проектов | `cross_search(collections=["levara","other"])` |
| На какой дате кто был назначен на X? | `query_entity(name="X", as_of="2026-01-01")` |

### 8.4. Subagent-driven development (isolated memory)

Когда запускается subagent (reviewer, architect, oncall):

```
diary_write(agent="reviewer", key="pr_142_findings", value="...")
→ запись в isolated namespace owner_id="agent:reviewer"
  НЕ попадает в recall_memory основного пользователя

diary_read(agent="reviewer", query="pagination")
→ читает только свою ветку, не засоряет project memory
```

Используется агентом, который делает **повторяющуюся** работу и хочет вести свой контекст между запусками без шума для пользователя.

### 8.5. Проверка дрифта и здоровья

```
doctor(verbose=true)
→ структурированный отчёт: postgres connected, embed reachable,
  neo4j OK, BM25 coverage=98%, graph has N nodes, ...

check_drift()
→ коллекции, где сохранённый model ≠ текущему: кандидаты на reembed

heartbeat(event_type="cognify", limit=10)
→ последние 10 cognify событий с payload (entities extracted, elapsed)
```

Агенту стоит **periodически** вызывать `doctor` в длинных сессиях — degraded-состояние системы влияет на качество его же ответов.

---

## 9. Инварианты и гарантии

### 9.1. Durability

| Слой | Гарантия |
|---|---|
| Vector (HNSW + arena) | WAL fsync перед возвратом `Insert` |
| BM25 | In-memory; восстанавливается из disk + WAL на старте |
| Graph (Neo4j) | Neo4j own ACID |
| Graph (SQL fallback) | Postgres/SQLite ACID |
| `memories` | Postgres/SQLite ACID |
| `_memories_<collection>` vector | best-effort, может отстать от SQL |
| `heartbeats`, `interactions`, `feedback` | Postgres/SQLite ACID |
| `runreg` (runs) | in-memory, TTL 1h на terminal |
| rate-limit buckets | in-memory, restart flushes |

### 9.2. Consistency

- **SQL ↔ vector** — eventually consistent. SQL пишется сразу, вектор — fire-and-forget. При disaster recovery вектор может отставать.
- **SQL ↔ graph (Neo4j)** — eventually consistent. Stage 4 пишет параллельно без 2PC. Recovery: `CheckPipelineStatus` + повтор `cognify` проходит через уже-записанные части.
- **Collections между шардами** — WAL replication (cluster/replication.go) поддерживает sync между primary и replicas в Raft-режиме или через ReplicaClient в standalone-mode с `-join-addr`.

### 9.3. Безопасность

- **Auth:** JWT HS256 24h, shared secret между HTTP и gRPC (после T19). В prod обязателен `JWT_SECRET`.
- **Rate limit:** 10/min IP для /auth/*, 100/min user для остального HTTP, 100/min peer IP для gRPC (бакет — IP без порта после M1).
- **RBAC:** `dataset_shares` table управляет кто видит какие datasets. Superuser-flag bypass'ит. `/prune/*` требует superuser (после M5).
- **Tenant isolation:** `TenantMiddleware(pgDB)` проставляет `tenant_id` в context; handlers должны учитывать.

### 9.4. Observability

- **Prometheus** (`/metrics`):
  - `levara_search_requests_total{search_type, source}` + `_duration_seconds`.
  - `levara_http_requests_total{operation, status, user_id}` — с bounded user_id (top-50 whitelist + "other" + "anon", T17).
  - `levara_grpc_requests_total{method, status}` — unary + stream (A.3 fix).
  - `levara_rate_limit_rejected_total{channel, bucket}`.
  - `levara_cognify_panics_total{stage}` (T15).
- **Tracing:** Langfuse wrapper для LLM-calls.
- **Logs:** structured (`pkg/observe`), errors aggregated в `ErrorTracker`.

---

## 10. Приложение: ссылки на код

Прямые ссылки для «куда смотреть» в каждом слое:

| Что искать | Файл |
|---|---|
| APIConfig + root registration | `Levara/internal/http/api.go` |
| Dataset CRUD | `Levara/internal/http/api_datasets.go` |
| Cognify handler + SSE | `Levara/internal/http/api_cognify.go` |
| Search dispatch | `Levara/internal/http/api_search.go` + `search_strategy.go` |
| Memory HTTP handlers | `Levara/internal/http/memories.go` |
| Session interactions | `Levara/internal/http/sessions.go` |
| Graph search strategies | `Levara/internal/http/graph_search.go` |
| MCP JSON-RPC handler | `Levara/internal/http/mcp.go` |
| MCP tool registry | `Levara/pkg/mcp/tools.go` + `output_schemas.go` |
| MCP Deps interface | `Levara/pkg/mcp/deps.go` |
| Individual tool bodies | `Levara/pkg/mcp/tool_*.go` (33 файла) |
| HNSW + WAL core | `Levara/internal/store/db.go`, `hnsw.go`, `wal.go` |
| Orchestrator (cognify pipeline) | `Levara/pkg/orchestrator/pipeline.go` + `extract.go` + `helpers.go` |
| Graph types + Dedup | `Levara/pkg/graph/` |
| Neo4j writer | `Levara/pkg/graphdb/` |
| Communities (Louvain + summarize) | `Levara/pkg/community/` |
| Router + adaptive weights | `Levara/pkg/router/router.go`, `adaptive.go` |
| JWT verify (shared) | `Levara/pkg/auth/jwt.go` |
| gRPC interceptors | `Levara/internal/grpc/auth_interceptor.go`, `ratelimit.go`, `metrics.go` |
| gRPC v2 service | `Levara/internal/grpc/service_v2.go` |
| Proto definitions | `Levara/proto/levara.proto` (v1), `levara_v2.proto` |
| Background runreg | `Levara/pkg/runreg/runreg.go` |
| Rate-limit middleware | `Levara/internal/http/ratelimit.go`, `Levara/internal/grpc/ratelimit.go` |
| UserBucket (bounded labels) | `Levara/internal/metrics/user_bucket.go` |
| Swagger spec (generated) | `Levara/docs/swagger.{json,yaml}` |
| Schema (Postgres + SQLite) | `Levara/internal/http/schema.go` |
| Startup wiring | `Levara/cmd/server/main.go` + `bootstrap.go` |

---

## Навигация для разных ролей

**Оператор:**
- `docs/MIGRATION-20.04.md` — чеклист апгрейда
- `CLAUDE.md` — env vars, commands, benchmarks
- `docs/swagger.yaml` — полная HTTP-спека

**Разработчик:**
- `docs/reviews/20.04-review.md` — архитектурный аудит
- `docs/reviews/20.04-tasks.md` — 20 задач + decision log
- `test_reports/coverage_2026-04-21.md` — coverage по пакетам

**Агент (LLM с MCP):**
- Этот файл — полная карта
- `CLAUDE.md` → секция «Levara MCP Memory» — квик-гид для Claude
- MCP `tools/list` — полная schema-описанная surface в runtime

---

**Последнее обновление:** 2026-04-21, коммит `84e45c5` (docs polish + confidence). Обновляйте этот файл при изменении любого слоя памяти — он main-source-of-truth для «как эта штука работает».
