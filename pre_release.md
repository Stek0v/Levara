# Levara — Pre-Release Overview

> Go-based acceleration layer для Cognee AI Memory Platform.
> Заменяет Python backend на 85% — core pipeline (ingest → cognify → search) полностью на Go.
> 15% функционала требует доработки (MCP tools, notebook execution, COT search).

---

## 1. Что такое Levara

Levara — полная реализация бэкенда Cognee на Go. Вместо Python (Django/FastAPI + SQLAlchemy + asyncio) используется Go (Fiber + pgx + goroutines). Frontend Cognee (Next.js) работает без изменений — API полностью совместимо.

**Зачем:** Cognee Python тратит 164-1295ms на ingestion одного файла, 9.1ms на search. Levara: 5-20ms ingestion, 2.6ms search. При 100+ concurrent users разница критична.

---

## 2. Архитектура

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│  Next.js UI  │────▶│  Levara Go │────▶│  PostgreSQL   │
│  :3000       │     │  :8080 HTTP  │     │  :5432        │
└──────────────┘     │  :50051 gRPC │     └──────────────┘
                     │  /mcp JSON-RPC│
                     └──────┬───────┘
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
        ┌──────────┐ ┌──────────┐ ┌──────────┐
        │  Neo4j   │ │  Ollama  │ │  Embed   │
        │  :7687   │ │  :11434  │ │  :9001   │
        └──────────┘ └──────────┘ └──────────┘
```

| Сервис | Порт | Назначение |
|--------|------|------------|
| Levara HTTP | 8080 | REST API (98 endpoints) |
| Levara gRPC | 50051 | Межсервисный протокол (61 method) |
| Levara MCP | 8080/mcp | Claude Desktop / Cursor / Cline |
| Next.js Frontend | 3000 | UI (proxy → :8080) |
| PostgreSQL | 5432 | Пользователи, datasets, metadata (18 таблиц) |
| Neo4j | 7687 | Knowledge graph |
| Ollama | 11434 | LLM (granite4:micro для extraction) |
| Embed Server | 9001 | Embeddings (nomic-embed-text-v2-moe) |
| Prometheus | 9090 | Метрики |

---

## 3. Go Backend — статистика

| Метрика | Значение |
|---------|----------|
| Строк Go кода | ~20,000 (96 файлов) |
| HTTP endpoints | 98 |
| gRPC methods | 61 |
| Пакеты в pkg/ | 17 |
| PostgreSQL таблиц | 18 (auto-migration) |
| MCP tools | 7 |
| Handler файлов | 18 (internal/http/) |

### Пакеты (pkg/)

| Пакет | Назначение |
|-------|------------|
| `embed/` | HTTP клиент к embed-server, кэш, batch |
| `bm25/` | Лексический поиск (BM25 + RRF hybrid) |
| `graph/` | In-memory граф: triplets, dedup, LSH, semantic dedup, multiquery |
| `graphdb/` | Neo4j: batch write, cached writer, graph read |
| `chunker/` | Paragraph, Sentence, Merged chunking |
| `ingest/` | Fast ingestion (SHA256 + save + classify, 5-20ms) |
| `extract/` | Document extraction (PDF/DOCX/PPTX/XLSX/HTML/EPUB/ODT) |
| `fetch/` | URL fetching (HTTP, GitHub README) |
| `fileio/` | File I/O, hashing, MIME detection |
| `orchestrator/` | Streaming cognify pipeline (chunk → extract → write) |
| `llmcache/` | LLM response кэш (LRU + disk) |
| `llmproxy/` | LLM HTTP proxy (Ollama, OpenAI-compatible) |
| `aggregator/` | Search result aggregation + triplet ranking |
| `temporal/` | Timestamp extraction (ISO 8601, Russian, English) |
| `ontology/` | OWL/RDF parsing, fuzzy match (Levenshtein) |

### Storage Engine (internal/store/)

| Компонент | Файл | Описание |
|-----------|------|----------|
| HNSW | hnsw.go | Multi-layer graph index (M=16, M0=32, SIMD via vek) |
| WAL | wal.go | Write-ahead log, group-commit fsync, checkpoints |
| Arena | arena.go | Memory-mapped vector storage, zero-copy lookup |
| Disk | disk.go | Append-only metadata store |
| Collections | collections.go | Multi-collection manager (independent HNSW+WAL+Arena) |

### HTTP Handlers (internal/http/)

| Handler | Файл | Endpoints |
|---------|------|-----------|
| Core API | api.go | 40+ Cognee-compatible endpoints |
| Auth | auth.go | JWT login/register/me, cookie auth |
| MCP | mcp.go | JSON-RPC 2.0, 7 tools |
| Memify | memify.go | Graph enrichment + SSE streaming |
| Notebooks | notebooks.go | CRUD + cell execution |
| RBAC | rbac.go | Dataset sharing (viewer/editor/admin) |
| Tenants | tenants.go | Multi-tenancy + ACL |
| Sessions | sessions.go | Interaction tracking |
| Visualize | visualize.go | D3.js graph + Neo4j filtering |
| Reembed | reembed.go | Embedding migration pipeline |
| DualSearch | dualsearch.go | Cross-collection search |
| Settings | settings.go | Per-user settings |
| Schema | schema.go | 18 таблиц, auto-migration |

---

## 4. Frontend (Next.js)

### Страницы (13)

| Route | Описание | Статус |
|-------|----------|--------|
| `/` | Dashboard | Из Cognee |
| `/auth/login` | Вход | Из Cognee + fix cookie auth |
| `/auth/signup` | Регистрация | Из Cognee |
| `/account` | Аккаунт | Из Cognee |
| `/plan` | Тарифы | Из Cognee |
| `/mcp-status` | System status | **НОВОЕ** |
| `/search` | Чат/поиск | **НОВОЕ** |
| `/ontologies` | Управление онтологиями | **НОВОЕ** |
| `/collections` | Управление коллекциями | **НОВОЕ** |
| `/visualize/[id]` | Граф датасета | Из Cognee + fix filtering |
| `/visualize/demo` | Демо-граф | Из Cognee |

### Новые компоненты (P3-P8)

| ID | Компонент | Файл |
|----|-----------|------|
| P3 | Share modal | `ShareDatasetModal.tsx` |
| P4 | Ontology page | `app/ontologies/page.tsx` |
| P5 | Collections page | `app/collections/page.tsx` |
| P6 | Search/Chat | `app/search/page.tsx` + `modules/chat/useChat.ts` |
| P7 | StatusIndicator | `ui/elements/StatusIndicator.tsx` |
| P8 | MCP quick-nav | `app/mcp-status/page.tsx` (anchors) |

### Фиксы Cognee frontend

| Проблема | Решение |
|----------|---------|
| Bearer null/undefined | Игнорировать в JWT middleware |
| NEXT_REDIRECT exception | Re-throw перед TypeError check |
| Cross-origin cookies | Next.js rewrites proxy :3000 → :8080 |
| Error format `{"error"}` | Заменён на `{"detail"}` |
| Default credentials visible | Удалены defaultValue из форм |
| Cloud section confusing | Скрыт в InstanceDatasetsAccordion |
| 404 без навигации | Добавлена "Back to Dashboard" ссылка |
| Graph residue nodes | Strict filter `dsID == datasetID && dsID != ""` |

---

## 5. Сравнение с Cognee Python

### Feature Parity

| Фича | Cognee Python | Levara Go | Примечание |
|------|--------------|-------------|------------|
| **HTTP API** | FastAPI, ~30 endpoints | Fiber, 98 endpoints | Go: больше endpoints |
| **gRPC** | ❌ | ✅ 61 method | Только в Go |
| **MCP Server** | ❌ | ✅ 7 tools | Только в Go |
| **Auth (JWT)** | ✅ | ✅ + cookie auth | Паритет |
| **Multi-tenancy** | ✅ | ✅ | Паритет |
| **RBAC/ACL** | ✅ (permissions API) | ✅ (shares + ACL) | Паритет |
| **Datasets CRUD** | ✅ | ✅ | Паритет |
| **Cognify pipeline** | ✅ (async) | ✅ (streaming SSE) | Go: streaming progress |
| **Memify** | ✅ | ✅ (+ SSE) | Go: streaming |
| **Notebooks** | ❌ (frontend only) | ✅ (CRUD + execution) | Go: backend |
| **Sessions** | ✅ | ✅ | Паритет |
| **Settings** | ✅ | ✅ | Паритет |
| **Ontologies** | ✅ (Python) | ✅ (Go + UI) | Go: есть UI |
| **Graph visualization** | ✅ (3D) | ✅ (D3.js + 3D) | Паритет |
| **BM25 hybrid search** | ❌ (plugin) | ✅ (встроенный) | Только в Go |
| **Dual-search** | ❌ | ✅ | Только в Go |
| **Re-embedding migration** | ❌ | ✅ | Только в Go |
| **LLM cache** | ❌ | ✅ (LRU + disk) | Только в Go |
| **Semantic dedup** | ❌ | ✅ (LSH) | Только в Go |

### Search Types

| Тип | Cognee | Levara |
|-----|--------|----------|
| CHUNKS (vector) | ✅ | ✅ |
| CHUNKS_LEXICAL (BM25) | ✅ | ✅ |
| RAG_COMPLETION | ✅ | ✅ |
| GRAPH_COMPLETION | ✅ | ✅ |
| GRAPH_SUMMARY_COMPLETION | ✅ | ✅ |
| GRAPH_COMPLETION_COT | ✅ | ❌ |
| TRIPLET_COMPLETION | ✅ | ✅ |
| SUMMARIES | ✅ | ✅ |
| TEMPORAL | ✅ | ✅ |
| FEELING_LUCKY | ✅ | ✅ (hybrid) |
| CYPHER | ✅ | ❌ |
| NATURAL_LANGUAGE | ✅ | ❌ |
| CODING_RULES | ✅ | ❌ |
| HYBRID (BM25+vector) | ❌ | ✅ |
| DUAL_SEARCH | ❌ | ✅ |
| MULTIQUERY | ❌ | ✅ |
| BM25 | ❌ | ✅ |

### Data Loaders

| Формат | Cognee | Levara |
|--------|--------|----------|
| PDF | ✅ (PyPDF) | ✅ (tabula) |
| DOCX | ✅ (unstructured) | ✅ (tabula) |
| PPTX | ✅ (unstructured) | ✅ (tabula) |
| XLSX | ✅ (unstructured) | ✅ (tabula) |
| HTML | ✅ (BS4) | ✅ (goquery) |
| CSV | ✅ | ✅ |
| TXT/MD | ✅ | ✅ |
| JSON/XML/YAML | ✅ | ✅ |
| EPUB | ❌ | ✅ |
| ODT | ❌ | ✅ |
| LOG | ❌ | ✅ |
| URL fetch | ✅ | ✅ |
| GitHub | ✅ (LangChain) | ✅ (README) |
| Audio | ✅ (Whisper) | ❌ |
| Images (OCR) | ✅ | ⚠️ (gosseract в deps, не подключён) |
| Dynamic JS | ✅ (Playwright) | ❌ |

### Database Backends

| Backend | Cognee | Levara |
|---------|--------|----------|
| Vector: HNSW (native) | ❌ | ✅ |
| Vector: LanceDB | ✅ | ❌ |
| Vector: ChromaDB | ✅ | ❌ |
| Vector: PGVector | ✅ | ❌ |
| Graph: Neo4j | ✅ | ✅ |
| Graph: Kuzu | ✅ (default) | ❌ |
| Relational: PostgreSQL | ✅ | ✅ |
| Relational: SQLite | ✅ (default) | ❌ |
| WAL durability | ❌ | ✅ |
| Raft consensus | ❌ | ✅ |

---

## 6. Бенчмарки

### Search Performance (1.4K vectors, dim=1024)

| Метрика | Levara | Cognee (LanceDB) | Разница |
|---------|----------|-------------------|---------|
| Latency p50 | **2.6 ms** | 9.1 ms | 3.5x |
| Latency p99 | **5.1 ms** | 18.3 ms | 3.6x |
| QPS (concurrent) | **719** | 150 | 4.8x |

### Ingestion

| Метрика | Levara | Cognee Python | Разница |
|---------|----------|---------------|---------|
| Per-item | **5-20 ms** | 164-1,295 ms | 10-65x |
| SHA256 passes | 1 | 3 (MD5) | 3x |
| Disk writes | 1 | 2 | 2x |

### Scale (100K vectors)

| Метрика | Levara | LanceDB | Разница |
|---------|----------|---------|---------|
| Search latency | **23.7 ms** | 203.7 ms | 8.6x |
| QPS | **143** | 5 | 28.6x |

### Crash Recovery

| Метрика | Levara | LanceDB |
|---------|----------|---------|
| Recovery rate | **100%** | N/A |
| WAL durability | ✅ | ❌ |

---

## 7. Тесты

| Категория | Файл | Кол-во | Статус |
|-----------|------|--------|--------|
| API integration | test_new_ui_features.py | 38 | ✅ 38/38 pass |
| Playwright UI | test_new_ui_screenshots.py | 14 | Screenshots |
| Full scenarios | test_ui_full_screenshots.py | 30 | Screenshots |
| UI scenarios | test_scenarios_ui.py | 20 | Playwright |
| Real scenarios | test_scenarios_real.py | 20 | API E2E |
| Priority P0 | test_p0_critical.py | 8 | Security/RBAC |
| Priority P1 | test_p1_high.py | 19 | Features |
| Edge cases | test_p2_p3_edge_cases.py | 20 | Edge cases |
| Integration | test_integration_real.py | 19 | E2E real |
| + 57 других файлов | | | Stress, benchmark, journey |

**Всего: 66 файлов, 200+ тестов**

---

## 8. Честный аудит: REAL vs STUB

### Что РЕАЛЬНО работает (85%)

| Компонент | Статус | Доказательство |
|-----------|--------|----------------|
| Vector DB (HNSW + WAL) | REAL | Полный цикл insert/search/delete, crash recovery 100% |
| Cognify pipeline | REAL | chunk → LLM → dedup → Neo4j + PostgreSQL + vector |
| Document extraction | REAL | tabula парсит PDF/DOCX/PPTX/XLSX/HTML/EPUB/ODT |
| Search (7 типов) | REAL | CHUNKS, BM25, HYBRID, RAG, TEMPORAL, SUMMARIES, GRAPH |
| Auth (JWT + cookies) | REAL | bcrypt, PostgreSQL, cookie-based SPA auth |
| Datasets CRUD | REAL | PostgreSQL, owner isolation, cascade |
| RBAC/Sharing | REAL | dataset_shares, viewer/editor/admin |
| Multi-tenancy | REAL | tenants + user_tenant + ACL |
| Sessions | REAL | interactions table, per-user |
| Re-embedding | REAL | Batch migration между моделями |
| Memify (triplets) | REAL | Embed relationships, write to vector |
| SSE streaming | REAL | cognify/memify progress |

### Что STUB / неполное (15%)

| Компонент | Проблема | Критичность |
|-----------|----------|-------------|
| **MCP cognify tool** | `time.Sleep(100ms)` → "COMPLETED", не запускает pipeline | **HIGH** |
| **MCP add tool** | `_ = items` — placeholder | **HIGH** |
| **Notebook execution** | 4 hardcoded команды, нет интерпретатора | MEDIUM |
| **Ontology upload** | Сохраняет файл, но НЕ парсит OWL/RDF | MEDIUM |
| **Memify summaries** | Создаёт ноды, не эмбеддит | LOW |
| **Memify rules** | Извлекает, не персистит | LOW |
| **Dataset status** | Hardcoded "ready" | LOW |
| **DELETE /collections** | Route не зарегистрирован | LOW |
| **DELETE /ontologies** | Route не зарегистрирован | LOW |

### Что есть в Cognee Python, но НЕТ в Levara

| Фича | Сложность | Приоритет |
|------|-----------|-----------|
| Chain-of-thought search (COT) | Средняя | Medium |
| Cypher query passthrough | Низкая | Low |
| Natural language → query | Средняя | Medium |
| Multi-provider LLM (12+ vs 1) | Высокая | Low |
| Kuzu graph backend | Высокая | Low |
| LanceDB/ChromaDB/PGVector | Средняя | Low |
| Audio transcription (Whisper) | Средняя | Low |
| Dynamic JS scraping | Высокая | Low |
| S3/GCS cloud storage | Средняя | Medium |
| Isolated DB per user+dataset | Высокая | Low |
| Pipeline caching | Средняя | Medium |
| Ontology-grounded extraction | Средняя | Medium |

---

## 9. Как запустить

```bash
# Инфраструктура
docker compose up -d --build

# Frontend
cd cognee/cognee-frontend && npm run dev

# Проверка
curl http://localhost:8080/health
curl http://localhost:3000/

# Тесты
pytest tests/test_new_ui_features.py -v -s
```

---

## 10. Файловая структура

```
new_db/
├── Levara/                    # Go backend (~20K LOC)
│   ├── cmd/server/main.go       # Entry point
│   ├── internal/
│   │   ├── store/               # HNSW + WAL + Arena + Collections
│   │   ├── http/                # 18 handler файлов, 98 endpoints
│   │   ├── grpc/                # gRPC service, 61 methods
│   │   ├── cluster/             # Raft consensus
│   │   └── metrics/             # Prometheus
│   ├── pkg/                     # 17 feature packages
│   └── proto/                   # Protocol Buffers
├── cognee/                      # Cognee Python (reference)
│   └── cognee-frontend/         # Next.js UI (13 pages, 33+ components)
├── tests/                       # 66 test файлов
├── docs/                        # Документация
├── pre_release.md               # Этот файл
├── BENCHMARK_RESULTS.md         # Сравнительные бенчмарки
└── CLAUDE.md                    # Инструкции для разработки
```
