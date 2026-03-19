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
│       ├── CHUNKS ──────────── 🟡 Vector similarity (Cognevra gRPC)  │
│       │                                                              │
│       ├── GRAPH_COMPLETION ── 🟡 Graph traversal + LLM completion   │
│       │    ├── 🟢 Vector search on nodes (Cognevra Search RPC)      │
│       │    ├── 🟢 Vector search on edges (Cognevra Search RPC)      │
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
│       ├── RAG_COMPLETION ──── 🟡 Chunk search + LLM                 │
│       ├── SUMMARIES ────────── 🟡 Summary vector search             │
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

## Статистика покрытия

| Категория | Всего компонентов | 🟢 Go | 🟡 Частично | 🔴 Python | Coverage |
|-----------|-------------------|-------|-------------|-----------|----------|
| **API endpoints** | 10 | 0 | 3 | 4 | 30% |
| **Pipeline tasks** | 18 | 5 | 4 | 9 | 50% |
| **DB adapters** | 8 | 2 | 1 | 5 | 38% |
| **LLM/Embedding** | 9 | 1 | 0 | 8 | 11% |
| **Retrieval** | 12 | 2 | 3 | 7 | 42% |
| **File I/O** | 4 | 2 | 1 | 1 | 75% |
| **gRPC RPCs** | 19 | **19** | 0 | 0 | **100%** |

### Критический путь (cognify write path):
```
extract_chunks  → extract_graph → dedup → write_nodes → write_edges → index
    🟢 Go           🔴 LLM         🟢 Go    🟢 Go         🟢 Go       🟢 Go
   100-400x        (bottleneck)   50-200x   30-100x       30-100x     50-200x
```

**LLM extraction = 60-70% total cognify time** — не переписываемо, но всё вокруг оптимизировано в Go.

### Ожидаемый эффект по pipeline:

| Операция | До (Python) | После (Go) | Speedup |
|----------|-------------|------------|---------|
| Chunking 1430 chunks | ~200ms | **~2ms** | 100x |
| Dedup 1000 nodes | ~200ms | **~2ms** | 100x |
| Graph write (Neo4j) | ~2s | **~200ms** | 10x |
| Vector embed+index | ~1s | **~500ms** | 2x |
| Triplet search 10K | ~500ms | **~5ms** | 100x |
| **Total cognify** (excl LLM) | ~4s | **~700ms** | **6x** |
| **Total cognify** (incl LLM) | ~30s | **~27s** | **1.1x** |
