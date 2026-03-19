# Архитектура Cognee — визуализация компонентов

## Цветовая легенда

- 🟢 **ЗЕЛЁНЫЙ** — полностью переписано на Go (Cognevra)
- 🟡 **ЖЁЛТЫЙ** — частично в Go (критический путь ускорен, остальное в Python)
- 🔴 **КРАСНЫЙ** — только Python (не стоит переписывать / LLM-bound)
- ⚪ **СЕРЫЙ** — инфраструктура/конфиг (не на критическом пути)

---

## Общая архитектура: add → cognify → search

```
┌─────────────────────────────────────────────────────────────────────┐
│                        COGNEE API LAYER                             │
│  ⚪ /add   🟡 /cognify   🟡 /search   ⚪ /memify   ⚪ /datasets    │
│  ⚪ /users  ⚪ /delete    ⚪ /visualize  ⚪ /health                  │
└─────────┬───────────┬────────────┬──────────────────────────────────┘
          │           │            │
          ▼           ▼            ▼
```

---

## Pipeline: ADD (Data Ingestion)

```
┌──────────────────────────── ADD PIPELINE ────────────────────────────┐
│                                                                      │
│  UploadFile / Text / URL                                             │
│       │                                                              │
│       ▼                                                              │
│  🟢 resolve_data_directories ─── pkg/fileio/walk.go (20-100x)       │
│       │                                                              │
│       ▼                                                              │
│  🟡 save_data_item_to_storage                                        │
│       ├── 🟢 HashFiles ─────────── pkg/fileio/hash.go (10-50x)      │
│       └── 🟡 astore() ─────────── aiofiles (async, non-blocking)    │
│       │                                                              │
│       ▼                                                              │
│  🔴 data_item_to_text_file ─── PDF/DOCX/Image loaders (Python)      │
│       │                                                              │
│       ▼                                                              │
│  🔴 classify() + identify() ─── MD5 hash + DB lookup (Python ORM)   │
│       │                                                              │
│       ▼                                                              │
│  ⚪ session.commit() ──────────── SQLAlchemy bulk INSERT             │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Pipeline: COGNIFY (Knowledge Graph Construction)

```
┌──────────────────────────── COGNIFY PIPELINE ───────────────────────┐
│                                                                      │
│  Dataset data items (from ADD)                                       │
│       │                                                              │
│       ▼                                                              │
│  🔴 classify_documents ──────── LLM doc classification               │
│       │                                                              │
│       ▼                                                              │
│  🟢 extract_chunks_from_documents                                    │
│       └── ChunkText RPC ────── pkg/chunker/ (100-400x faster)       │
│           ├── paragraph.go     (paragraph + merged strategy)         │
│           └── sentence.go      (sentence boundary detection)         │
│       │                                                              │
│       ▼                                                              │
│  🔴 extract_graph_and_summarize ─── LLM entity/relationship extract │
│       ├── 🔴 LLM structured output (instructor + litellm)           │
│       ├── 🔴 KnowledgeGraph schema validation (Pydantic)            │
│       └── 🔴 summarize_text (LLM call)                              │
│       │                                                              │
│       ▼                                                              │
│  🟢 deduplicate_nodes_and_edges                                      │
│       └── DeduplicateGraph RPC ── pkg/graph/dedup.go (50-200x)      │
│           ├── Node dedup (by ID, first wins)                         │
│           ├── Edge dedup (source+rel+target key)                     │
│           └── Triplet generation (UUID5 deterministic)               │
│       │                                                              │
│       ▼                                                              │
│  🟢 add_data_points → ParallelWriteDataPoints RPC                   │
│       │                                                              │
│       ├─── Phase 1 (PARALLEL goroutines): ──────────────────────┐   │
│       │    🟢 Neo4j MERGE nodes ── pkg/graphdb/neo4j.go         │   │
│       │    🟢 Embed + Index nodes ── pkg/embed/ + collections   │   │
│       ├────────────────────────────────────────────────────────-─┘   │
│       │                                                              │
│       ├─── Phase 2 (PARALLEL, after nodes): ────────────────────┐   │
│       │    🟢 Neo4j MERGE edges ── UNWIND + apoc.merge          │   │
│       │    🟢 Embed + Index edge types                           │   │
│       │    🟢 Generate + embed triplets (optional)               │   │
│       ├─────────────────────────────────────────────────────────-┘   │
│       │                                                              │
│       └─── 🟡 upsert_nodes + upsert_edges (PostgreSQL, Python)      │
│            (parallelized via asyncio.gather)                         │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Pipeline: SEARCH (Query & Retrieval)

```
┌──────────────────────────── SEARCH PIPELINE ────────────────────────┐
│                                                                      │
│  query_text + query_type                                             │
│       │                                                              │
│       ▼                                                              │
│  ⚪ Search dispatcher ──────── routes to retriever by SearchType     │
│       │                                                              │
│       ├── CHUNKS ──────────── 🟢 SearchByText RPC (embed+search)    │
│       │                                                              │
│       ├── GRAPH_COMPLETION ── 🟢 GraphCompletionSearch RPC          │
│       │    ├── 🟢 Embed query (Go HTTP → embed-server)              │
│       │    ├── 🟢 Parallel vector search (goroutines)               │
│       │    ├── 🟢 Neo4j graph read (GraphRead, ID-filtered)         │
│       │    ├── 🟢 Triplet scoring (in-memory, 0ms)                  │
│       │    └── 🔴 LLM completion (Python)                           │
│       │                                                              │
│       ├── TRIPLET_COMPLETION                                         │
│       │    ├── 🟢 SearchTriplets RPC ── pkg/graph/triplet.go        │
│       │    │    ├── In-memory graph build                            │
│       │    │    ├── Distance mapping (node + edge)                   │
│       │    │    ├── Heap-based top-k scoring (22-112x faster)        │
│       │    │    └── FormatTriplets → LLM context                     │
│       │    └── 🔴 LLM completion (Python)                           │
│       │                                                              │
│       ├── RAG_COMPLETION ──── 🟡 SearchByText + LLM                 │
│       ├── SUMMARIES ────────── 🟡 SearchByText on summaries         │
│       ├── CHUNKS_LEXICAL ──── 🟢 BM25Search RPC (Go inverted index) │
│       ├── HYBRID ───────────── 🟢 HybridSearch RPC (vector+BM25)   │
│       ├── NATURAL_LANGUAGE ── 🔴 NL→Cypher (LLM, Python)            │
│       ├── CYPHER ───────────── 🔴 Raw Cypher query (Python)         │
│       ├── TEMPORAL ─────────── 🔴 Time-aware graph (Python)          │
│       └── CODING_RULES ────── 🔴 Code-specific rules (Python)       │
│                                                                      │
│  🟢 AggregateSearch RPC ────── pkg/aggregator/ (ranking + dedup)    │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Инфраструктура: Database Adapters

```
┌──────────────── VECTOR DB ──────────────┐  ┌────── GRAPH DB ────────┐
│                                          │  │                        │
│  🟢 Cognevra (Go HNSW + WAL)            │  │  🟢 Neo4j (Go driver)  │
│     ├── CollectionManager                │  │     └── pkg/graphdb/   │
│     ├── HNSW Index (SIMD NEON)           │  │         UNWIND+MERGE   │
│     ├── VectorArena (mmap)               │  │                        │
│     ├── WAL (group commit)               │  │  🔴 Kuzu (Python)     │
│     └── DiskStore (append-only)          │  │  🔴 Neptune (Python)  │
│                                          │  │                        │
│  🔴 LanceDB (Rust, in-process)          │  └────────────────────────┘
│  🔴 PGVector (PostgreSQL ext)           │
│  🔴 ChromaDB (Python)                   │  ┌──── RELATIONAL DB ─────┐
│                                          │  │                        │
└──────────────────────────────────────────┘  │  🔴 PostgreSQL (ORM)  │
                                              │  🔴 SQLite (ORM)      │
┌──────────── EMBEDDING ──────────────────┐  │  ⚪ SQLAlchemy         │
│                                          │  │                        │
│  🟢 pkg/embed/client.go                 │  └────────────────────────┘
│     (OpenAI-compatible HTTP, batched)    │
│                                          │  ┌────── CACHE ───────────┐
│  🔴 FastEmbed (Python local)            │  │  ⚪ Redis (optional)   │
│  🔴 Ollama embed (Python)               │  │  ⚪ FsCache (default)  │
│                                          │  └────────────────────────┘
└──────────────────────────────────────────┘

┌──────────── LLM GATEWAY ────────────────┐  ┌──── FILE STORAGE ──────┐
│                                          │  │                        │
│  🔴 OpenAI                               │  │  🟡 LocalFileStorage   │
│  🔴 Anthropic Claude                     │  │     ├── 🟢 HashFiles   │
│  🔴 Google Gemini                        │  │     ├── 🟢 ListDir     │
│  🔴 Ollama                               │  │     └── 🟡 astore()   │
│  🔴 AWS Bedrock                          │  │                        │
│  🔴 Mistral / Groq                       │  │  🔴 S3 Storage        │
│  🔴 Instructor (structured output)       │  │                        │
│                                          │  └────────────────────────┘
└──────────────────────────────────────────┘
```

---

## Cognevra Go Server: gRPC RPCs (port 50051)

```
┌─────────────────────── COGNEVRA gRPC SERVICE ───────────────────────┐
│                                                                      │
│  🟢 VECTOR OPERATIONS (internal/store/)                              │
│     CreateCollection | DropCollection | ListCollections               │
│     Insert | BatchInsert | Delete | Search | GetByID                  │
│                                                                      │
│  🟢 TEXT PROCESSING (pkg/chunker/)                                   │
│     ChunkText (paragraph | sentence | merged)                        │
│                                                                      │
│  🟢 GRAPH PROCESSING (pkg/graph/)                                    │
│     DeduplicateGraph (dedup + triplet gen)                           │
│     SearchTriplets (in-memory scoring, 22-112x faster)               │
│     ProcessTriplets (edge→triplet dedup + UUID5)                     │
│                                                                      │
│  🟢 DATABASE WRITES (pkg/graphdb/ + pkg/embed/)                     │
│     BatchWriteGraph (Neo4j UNWIND+MERGE)                             │
│     BatchEmbedAndIndex (embed + vector insert)                       │
│     ParallelWriteDataPoints (ALL-IN-ONE: dedup→Neo4j→embed→index)   │
│                                                                      │
│  🟢 FILE I/O (pkg/fileio/)                                          │
│     HashFiles (concurrent SHA256 + MIME)                              │
│     ListDirectory (recursive walk + filter)                           │
│                                                                      │
│  🟢 SEARCH AGGREGATION (pkg/aggregator/)                            │
│     AggregateSearch (triplet ranking + context formatting)           │
│                                                                      │
│  🟢 LLM CACHE (pkg/llmcache/)                                        │
│     LLMCacheGet (0.18ms) | LLMCachePut | LLMCacheStats               │
│                                                                      │
│  🟢 BM25 LEXICAL SEARCH (pkg/bm25/)                                  │
│     BM25Index | BM25Search (2.9ms/10K) | HybridSearch (vector+BM25)  │
│                                                                      │
│  🟢 MAINTENANCE                                                      │
│     Info | Compact                                                    │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Статистика покрытия (обновлено 2026-03-20, v2)

| Категория | Всего | 🟢 Go | 🟡 Частично | 🔴 Python | Coverage |
|-----------|-------|-------|-------------|-----------|----------|
| **API endpoints** | 10 | 0 | 3 | 4 | 30% |
| **Pipeline tasks** | 18 | 7 | 2 | 9 | **56%** |
| **DB adapters** | 8 | 3 | 0 | 5 | **44%** |
| **LLM/Embedding** | 9 | 2 | 0 | 7 | **22%** |
| **Retrieval** | 12 | 6 | 1 | 5 | **58%** |
| **File I/O** | 4 | 2 | 1 | 1 | 75% |
| **gRPC RPCs** | **29** | **29** | 0 | 0 | **100%** |

### Все 29 Go gRPC RPCs:

| # | RPC | Пакет | Заменяет |
|---|-----|-------|----------|
| 1-9 | Vector CRUD (9 RPCs) | internal/store | Collection management, insert/search/delete |
| 10 | ChunkText | pkg/chunker | extract_chunks_from_documents |
| 11 | ProcessTriplets | service.go | triplet dedup + UUID5 |
| 12 | HashFiles | pkg/fileio | concurrent SHA256 + MIME |
| 13 | ListDirectory | pkg/fileio | recursive walk + filter |
| 14 | AggregateSearch | pkg/aggregator | triplet ranking + dedup |
| 15 | **SearchTriplets** | pkg/graph | brute_force_triplet_search (22-112x) |
| 16 | **DeduplicateGraph** | pkg/graph | deduplicate_nodes_and_edges (50-200x) |
| 17 | **BatchEmbedAndIndex** | pkg/embed | embed + vector insert |
| 18 | **BatchWriteGraph** | pkg/graphdb | Neo4j UNWIND+MERGE |
| 19 | **ParallelWriteDataPoints** | all combined | dedup→Neo4j→embed→index (one call) |
| 20 | **SearchByText** | pipeline/ | embed query + HNSW search |
| 21 | **BatchSearchByText** | pipeline/ | batch embed + concurrent search |
| 22 | **GraphRead** (4 modes) | pkg/graphdb | Neo4j graph projection |
| 23 | **GraphCompletionSearch** | all combined | full search pipeline (one call) |
| 24 | **LLMCacheGet** | pkg/llmcache | cached LLM response (0.18ms vs 5-30s) |
| 25 | **LLMCachePut** | pkg/llmcache | store LLM response |
| 26 | **LLMCacheStats** | pkg/llmcache | cache hit/miss statistics |
| 27 | **BM25Index** | pkg/bm25 | lexical inverted index |
| 28 | **BM25Search** | pkg/bm25 | keyword search (2.9ms/10K docs) |
| 29 | **HybridSearch** | pkg/bm25 | vector + BM25 via RRF fusion |

### Критический путь (cognify write path):
```
extract_chunks  → extract_graph → dedup → write_nodes → write_edges → index
    🟢 Go           🔴 LLM         🟢 Go    🟢 Go         🟢 Go       🟢 Go
   100-400x        (bottleneck)   50-200x   30-100x       30-100x     50-200x
```

### Критический путь (search):
```
embed_query → vector_search → graph_read → triplet_score → format_context → LLM
  🟢 Go        🟢 Go           🟢 Go        🟢 Go           🟢 Go         🔴 Python
  (embed-srv)   3ms             8-60ms        0ms             0ms          (LLM API)
```

**Весь search pipeline кроме LLM completion теперь в Go** через `GraphCompletionSearch`.

### Оставшиеся 🟡 жёлтые (low ROI):

| # | Компонент | Почему низкий ROI |
|---|-----------|-------------------|
| Y1 | file ingest | PDF/DOCX loaders = Python only, нет Go аналога |
| Y2 | upsert PostgreSQL | Уже parallelized через asyncio.gather |
| Y5/Y6 | RAG/SUMMARIES | Только Python adapter overhead, <5ms |
| Y7 | LocalFileStorage | astore() уже добавлен |

### Ожидаемый эффект по pipeline:

| Операция | До (Python) | После (Go) | Speedup |
|----------|-------------|------------|---------|
| Chunking 1430 chunks | ~200ms | **~2ms** | 100x |
| Dedup 1000 nodes | ~200ms | **~2ms** | 100x |
| Graph write (Neo4j) | ~2s | **~200ms** | 10x |
| Vector embed+index | ~1s | **~500ms** | 2x |
| Triplet search 10K | ~500ms | **~5ms** | 100x |
| BM25 search 10K | N/A (Python) | **~3ms** | NEW |
| Hybrid search | N/A | **~90ms** | NEW |
| LLM cache hit | 5-30s | **~0.18ms** | **26K-160Kx** |
| **Full search** (excl LLM) | ~600ms | **~90ms** | **7x** |
| **Total cognify** (excl LLM) | ~4s | **~700ms** | **6x** |

---

## Следующие задачи по ROI

### Tier A: Высокий ROI (новые возможности)

| # | Задача | Effort | Impact | Описание |
|---|--------|--------|--------|----------|
| **N1** | Go Pipeline Orchestrator | 1-2 нед | Высокий | Go goroutine-based pipeline: inter-task streaming вместо sequential batch. Chunks → LLM (Python callback) → dedup → write параллельно |
| **N2** | Persistent BM25 Index | 2-3 дня | Средний | BM25 index сохраняется на диск (сейчас in-memory, теряется при рестарте). WAL для BM25 аналогично vector WAL |
| **N3** | LLM Cache persistence (Redis/disk) | 1-2 дня | Средний | Текущий кеш in-memory. Добавить Redis backend или disk persistence для выживания рестартов |
| **N4** | Batch LLM proxy | 3-5 дней | Высокий | Go proxy перед LLM API: батчинг N запросов в один, дедупликация одинаковых промптов в полёте, rate limiting |

### Tier B: Средний ROI (улучшение качества)

| # | Задача | Effort | Impact | Описание |
|---|--------|--------|--------|----------|
| **N5** | Semantic dedup (vector-based) | 2-3 дня | Средний | Дедупликация чанков не только по ID, но по cosine similarity (>0.95 = duplicate). Уменьшает шум в retrieval |
| **N6** | Graph index (in-memory) | 3-5 дней | Средний | Постоянный in-memory граф в Go (как BM25 index). Сейчас GraphRead идёт в Neo4j каждый раз — кеш графа ускорит search |
| **N7** | Multi-query search | 1-2 дня | Средний | SearchByText с entity decomposition: "Кто такая Эмбер и как она связана с Лукасом?" → 2 sub-queries параллельно |

### Tier C: Low ROI / Специализированные

| # | Задача | Effort | Impact |
|---|--------|--------|--------|
| N8 | Streaming gRPC cognify progress | 2-3 дня | UX improvement |
| N9 | Go PostgreSQL driver (upserts) | 2-3 дня | Минимальный (ORM overhead < 50ms) |
| N10 | WASM embedding (local, no server) | 1-2 нед | Eliminates embed-server dependency |
