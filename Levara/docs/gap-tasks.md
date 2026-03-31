# Gap Tasks: Cognee → Levara (ВСЕ ЗАКРЫТЫ 2026-03-30)

## Приоритеты

| Цвет | Значение | Когда |
|------|---------|-------|
| 🔴 | Критический — блокирует production use cases | Немедленно |
| 🟠 | Средний — улучшает quality of life | 2-4 недели |
| 🟢 | Низкий — nice to have | Когда будет время |

---

## 🔴 GAP-1: Semantic Entity Dedup в Cognify Pipeline

**Статус:** Код есть (`pkg/graph/semantic_dedup.go`), но НЕ вызывается в pipeline.

**Проблема:** Cognify извлекает "Levara" из одного chunk и "levara" из другого — два разных node в графе. Должен быть один.

**Что сделать:**
1. В `pkg/orchestrator/pipeline.go` после Stage 3 (деdup по ID) добавить Stage 3b — вызов `SemanticDedup()` с cosine threshold 0.95
2. После слияния nodes — обновить все edges чтобы ссылались на merged ID
3. Добавить метрику `levara_dedup_merged_total` (сколько entities объединено)
4. Добавить log: `[pipeline] semantic dedup: merged N entities (threshold 0.95)`

**DoD:**
- [ ] SemanticDedup вызывается в pipeline
- [ ] Edges remapped после merge
- [ ] Метрика в Prometheus
- [ ] Unit test: 3 похожих entity → 1 после dedup

**Оценка:** 150-200 LOC, 1 файл

---

## 🔴 GAP-2: Multi-Tenant Isolation Middleware

**Статус:** Таблицы `tenants`, `user_tenant`, `roles`, `acl` существуют в schema.go. НЕ используются ни в одном endpoint.

**Проблема:** Все пользователи видят все данные. Нет query-level фильтрации по tenant.

**Что сделать:**
1. Middleware `TenantMiddleware(cfg)` — извлекает tenant_id из JWT claims или header → кладёт в `c.Locals("tenant_id")`
2. Все search/cognify/list endpoints — фильтруют по tenant_id
3. Collections — namespace по tenant: `{tenant_id}/{collection_name}`
4. Graph nodes/edges — добавить `tenant_id` column + index
5. Memories — добавить `tenant_id` column

**DoD:**
- [ ] Middleware создан и зарегистрирован
- [ ] Search endpoint фильтрует по tenant
- [ ] Cognify записывает tenant_id
- [ ] Collections namespaced
- [ ] Graph isolated
- [ ] Тест: 2 tenant'а, данные одного не видны другому

**Оценка:** 550-850 LOC, 5-6 файлов

---

## 🔴 GAP-3: API Key Authentication

**Статус:** Только JWT auth. Нет программного доступа без login.

**Проблема:** Скрипты, CI/CD, другие сервисы не могут аутентифицироваться без email/password → JWT flow.

**Что сделать:**
1. Таблица `api_keys`: id, key_hash, user_id, name, permissions, created_at, last_used, revoked
2. Endpoint `POST /api/v1/auth/keys` — создать ключ (возвращает plain key один раз)
3. Endpoint `GET /api/v1/auth/keys` — список ключей (без plain key)
4. Endpoint `DELETE /api/v1/auth/keys/:id` — отозвать ключ
5. Middleware: проверять header `X-API-Key` → lookup key_hash → inject user_id
6. Scoped permissions: read-only, read-write, admin

**DoD:**
- [ ] Schema migration
- [ ] CRUD endpoints
- [ ] Middleware проверяет X-API-Key
- [ ] last_used обновляется при каждом вызове
- [ ] Тест: создать key → вызвать API → работает

**Оценка:** 490-740 LOC, 3 файла

---

## 🟠 GAP-4: Feedback System

**Статус:** Нет таблицы и endpoints.

**Проблема:** Пользователь не может сказать "этот результат нерелевантен" — система не учится.

**Что сделать:**
1. Таблица `search_feedback`: id, query, result_id, collection, rating (1-5), comment, user_id, created_at
2. MCP tool `add_feedback(query, result_id, rating, comment)`
3. MCP tool `get_feedback_stats(collection)` — средний rating, worst queries
4. REST endpoint `POST /api/v1/feedback`
5. Опционально: boost/penalize результаты с feedback при re-ranking

**DoD:**
- [ ] Schema + таблица
- [ ] MCP tools (2 штуки)
- [ ] REST endpoint
- [ ] Stats aggregation
- [ ] Тест: submit feedback → stats показывают

**Оценка:** 450-700 LOC, 3 файла

---

## 🟠 GAP-5: Ontology-Guided Extraction

**Статус:** `pkg/ontology/ontology.go` парсит RDF/XML и имеет `FuzzyMatch()`. НЕ интегрирован в pipeline.

**Проблема:** LLM извлекает что хочет. С онтологией: "извлеки только Person, Company, Product" — контролируемое извлечение.

**Что сделать:**
1. REST endpoint `POST /api/v1/ontologies/upload` — загрузить RDF/XML
2. В cognify pipeline: если для collection задана ontology → добавить в system prompt список allowed entity types
3. После extraction: `ValidateEntity()` фильтрует entities не из ontology
4. MCP tool `list_ontologies()`

**DoD:**
- [ ] Upload endpoint работает
- [ ] Cognify использует ontology для prompts
- [ ] Entities фильтруются по ontology
- [ ] Тест: загрузить онтологию → cognify → только разрешённые типы

**Оценка:** 430-670 LOC, 3 файла

---

## 🟠 GAP-6: Conversational Memory (Context Propagation)

**Статус:** `interactions` table хранит query/response, но search НЕ использует историю.

**Проблема:** Каждый search независим. При диалоге "Расскажи про WAL" → "А как он связан с HNSW?" — второй запрос не знает что "он" = WAL.

**Что сделать:**
1. В searchHandler: если передан `session_id` → загрузить последние 5 interactions из этой сессии
2. Добавить историю в LLM prompt при RAG_COMPLETION: "Previous context: Q1→A1, Q2→A2..."
3. Опционально: query expansion — "он" → "WAL" через coreference resolution
4. MCP tool `search` — добавить optional `session_id` параметр

**DoD:**
- [ ] Search загружает историю по session_id
- [ ] RAG_COMPLETION включает контекст
- [ ] MCP search принимает session_id
- [ ] Тест: 2 запроса в одной сессии → второй использует контекст первого

**Оценка:** 550-800 LOC, 2 файла

---

## 🟠 GAP-7: Python SDK

**Статус:** Нет. Есть только `benchmark/mcp_client.py` для тестов.

**Проблема:** Python разработчики не могут использовать Levara без curl/HTTP.

**Что сделать:**
1. Создать `~/src/levara-python-sdk/levara/client.py` — sync + async HTTP клиент
2. Методы: `add()`, `cognify()`, `search()`, `save_memory()`, `recall_memory()`, `sync()`
3. `setup.py` / `pyproject.toml` для `pip install levara-client`
4. Type hints + docstrings
5. README с examples

**DoD:**
- [ ] `pip install levara-client` работает
- [ ] `client.search("query")` возвращает results
- [ ] Async поддержка (aiohttp)
- [ ] Тесты проходят
- [ ] README с примерами

**Оценка:** 900-1100 LOC, отдельный репозиторий

---

## 🟠 GAP-8: Memify — Coding Rules Extraction

**Статус:** Router детектирует code patterns, CODING_RULES search type работает. Но отдельного pipeline для анализа кода нет.

**Проблема:** Cognify извлекает entities из кода как текст. Нет понимания "функция A вызывает функцию B" на уровне AST.

**Что сделать:**
1. `pkg/extract/code.go` — парсинг Go/Python/JS файлов
2. Извлечение: функции, классы, импорты, вызовы
3. Создание edges: CALLS, IMPORTS, EXTENDS, IMPLEMENTS
4. Сохранение в `coding_rules` node set
5. MCP tool `codify(data, language)`

**DoD:**
- [ ] Go parser: functions + imports + calls
- [ ] Python parser: classes + methods + imports
- [ ] Edges создаются автоматически
- [ ] MCP tool работает
- [ ] Тест: Go файл → graph с function nodes + call edges

**Оценка:** 400-600 LOC per language, 2-3 файла

---

## 🟢 GAP-9: DataPoint Abstraction

**Статус:** Entities хранятся как `{name, type, description}`. Нет confidence, source, provenance.

**Что сделать:**
1. Расширить DedupNode: добавить `Confidence float32`, `SourceChunkID string`, `ExtractedAt time.Time`
2. При extraction — заполнять metadata
3. При search — показывать provenance в результатах

**Оценка:** 380-570 LOC

---

## 🟢 GAP-10: Alternative Vector Store Backend (Qdrant)

**Статус:** HNSW in-process единственный вариант. Работает отлично но не масштабируется.

**Что сделать:**
1. Interface `VectorStore { Insert, Search, Delete, Count }`
2. Адаптер для Qdrant (HTTP client)
3. Переключение через env var `VECTOR_STORE=hnsw|qdrant`

**Оценка:** 200-300 LOC per backend

---

## 🟢 GAP-11: Batch Multi-Query Integration

**Статус:** `pkg/graph/multiquery.go` имеет DecomposeQuery() + MergeResults(). НЕ вызывается в search.

**Что сделать:**
1. В `graph_search.go` COT search: вызвать DecomposeQuery() для split
2. Параллельный dispatch sub-queries через goroutines
3. MergeResults() через RRF

**Оценка:** 360-590 LOC

---

## 🟢 GAP-12: Graph Store Abstraction

**Статус:** Neo4j + PostgreSQL fallback. PostgreSQL для traversal ограничен (нет recursive CTE).

**Что сделать:**
1. Interface `GraphStore { BatchWrite, Query1Hop, Query2Hop, ReadFullGraph }`
2. PostgreSQL: добавить recursive CTE для multi-hop
3. Kuzu embedded backend (опционально)

**Оценка:** 580-900 LOC

---

## Порядок реализации

### Sprint 1 (Quick Wins — 1-2 дня)
1. **GAP-1** Semantic Dedup → 150-200 LOC
2. **GAP-3** API Key Auth → 490-740 LOC

### Sprint 2 (Security — 3-5 дней)
3. **GAP-2** Multi-Tenant Middleware → 550-850 LOC

### Sprint 3 (Quality — 1 неделя)
4. **GAP-4** Feedback System → 450-700 LOC
5. **GAP-5** Ontology Integration → 430-670 LOC
6. **GAP-6** Conversational Memory → 550-800 LOC

### Sprint 4 (Ecosystem — 1-2 недели)
7. **GAP-7** Python SDK → 900-1100 LOC
8. **GAP-8** Coding Rules → 400-600 LOC

### Sprint 5 (Abstractions — когда нужно)
9-12. DataPoint, Vector backends, Graph backends, Multi-query

**Итого Sprint 1-3:** ~2000-3000 LOC (критические gaps)
**Итого все:** ~6600-10000 LOC
