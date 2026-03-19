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
│       ├── CHUNKS_LEXICAL ──── 🔴 BM25 keyword search (Python)       │
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
│  🟢 MAINTENANCE                                                      │
│     Info | Compact                                                    │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
```

---

## Статистика покрытия (обновлено 2026-03-20)

| Категория | Всего | 🟢 Go | 🟡 Частично | 🔴 Python | Coverage |
|-----------|-------|-------|-------------|-----------|----------|
| **API endpoints** | 10 | 0 | 3 | 4 | 30% |
| **Pipeline tasks** | 18 | 7 | 2 | 9 | **56%** |
| **DB adapters** | 8 | 3 | 0 | 5 | **44%** |
| **LLM/Embedding** | 9 | 1 | 0 | 8 | 11% |
| **Retrieval** | 12 | 4 | 1 | 7 | **50%** |
| **File I/O** | 4 | 2 | 1 | 1 | 75% |
| **gRPC RPCs** | **23** | **23** | 0 | 0 | **100%** |

### Все 23 Go gRPC RPCs:

| # | RPC | Категория | Заменяет |
|---|-----|-----------|----------|
| 1 | CreateCollection | Vector CRUD | — |
| 2 | DropCollection | Vector CRUD | — |
| 3 | ListCollections | Vector CRUD | — |
| 4 | HasCollection | Vector CRUD | — |
| 5 | Insert | Vector CRUD | — |
| 6 | BatchInsert | Vector CRUD | — |
| 7 | Delete | Vector CRUD | — |
| 8 | Search | Vector CRUD | — |
| 9 | GetByID | Vector CRUD | — |
| 10 | ChunkText | Text processing | extract_chunks_from_documents |
| 11 | ProcessTriplets | Graph processing | triplet dedup |
| 12 | HashFiles | File I/O | file hashing |
| 13 | ListDirectory | File I/O | directory walk |
| 14 | AggregateSearch | Search | triplet ranking |
| 15 | **SearchTriplets** | Search | brute_force_triplet_search |
| 16 | **DeduplicateGraph** | Graph | deduplicate_nodes_and_edges |
| 17 | **BatchEmbedAndIndex** | Write | index_data_points |
| 18 | **BatchWriteGraph** | Write | Neo4j add_nodes/add_edges |
| 19 | **ParallelWriteDataPoints** | Write | add_data_points (all-in-one) |
| 20 | **SearchByText** | Search | embed + search |
| 21 | **BatchSearchByText** | Search | batch embed + search |
| 22 | **GraphRead** | Read | Neo4j graph projection (4 modes) |
| 23 | **GraphCompletionSearch** | Search | TripletSearchContextProvider (full pipeline) |

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
| **Full search** (excl LLM) | ~600ms | **~90ms** | **7x** |
| **Total cognify** (excl LLM) | ~4s | **~700ms** | **6x** |
