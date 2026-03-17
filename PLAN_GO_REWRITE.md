# План: переписывание Cognee pipeline с Python на Go

## Context

Бенчмарки показали: из 2.6ms search latency VectraDB только 0.5ms — HNSW search, остальные 2.1ms — HTTP+JSON+Python overhead. Переписав pipeline на Go, мы получим in-process вызовы (0ms transport), нативную конкурентность (goroutines vs asyncio/GIL), и единый язык для всего стека.

**Исходные данные**: полный Python-код доступен (VectraDBAdapter 425 строк, TextChunker 79 строк, EmbeddingEngine 290 строк, VectorDBInterface 265 строк). Мы понимаем каждый code path и можем улучшить при портировании.

---

## Компоненты в порядке реализации

### Компонент 1: VectraDB Native Collections + Delete + gRPC (P0)

**Зачем**: убирает HTTP overhead (2.1ms → 0ms), prefix-hack коллекции → нативные, добавляет delete.

**Python-оригинал** (`VectraDBAdapter.py:425 строк`):
- `_prefixed_id()` / `_strip_prefix()` — hack для коллекций через prefix
- `search()` — over-fetch `limit * max(num_collections, 4)`, client-side filter
- `_batch_post()` — HTTP POST к `/api/v1/batch_insert`
- `delete_data_points()` — только cache, сервер не удаляет
- `_id_cache` (65K) / `_embedding_cache` (4K) — in-memory кэши

**Что делаем в Go**:

#### 1.1 CollectionManager (новый файл `internal/store/collections.go`)

```go
type CollectionManager struct {
    mu          sync.RWMutex
    collections map[string]*VectraDB   // name → отдельный HNSW+WAL+Arena
    dim         int
    basePath    string
}

// Методы:
func (cm *CollectionManager) Create(name string) error
func (cm *CollectionManager) Drop(name string) error
func (cm *CollectionManager) List() []string
func (cm *CollectionManager) Get(name string) (*VectraDB, error)
func (cm *CollectionManager) Insert(collection, id string, vec []float32, meta any) error
func (cm *CollectionManager) BatchInsert(collection string, records []BatchItem) []error
func (cm *CollectionManager) Search(collection string, query []float32, topK int) []VectroRecord
func (cm *CollectionManager) Delete(collection, id string) error
func (cm *CollectionManager) BatchDelete(collection string, ids []string) []error
```

Каждая коллекция = отдельный `VectraDB` instance с своим WAL, HNSW, Arena в `{basePath}/collections/{name}/`.

#### 1.2 Delete-by-ID (модификация `db.go`)

```go
func (db *VectraDB) Delete(id string) error {
    db.mu.Lock()
    defer db.mu.Unlock()

    idx, ok := db.index[id]
    if !ok {
        return ErrNotFound
    }

    // 1. WAL entry
    db.wal.WriteEntryNoFlush(OpDelete, id, nil, nil, FileLocation{})
    db.wal.Flush()

    // 2. Remove from maps
    delete(db.index, id)
    db.revIndex[idx] = ""
    delete(db.metaLocs, idx)

    // 3. Mark in HNSW as deleted (tombstone)
    db.hnsw.MarkDeleted(idx)

    return nil
}
```

HNSW.MarkDeleted — новый метод в `hnsw.go`: помечает node как удалённый, Search пропускает его.

#### 1.3 gRPC API (новые файлы)

**Proto** (`proto/vectradb.proto`):
```protobuf
service VectraDBService {
    rpc CreateCollection(CreateCollectionReq) returns (StatusResp);
    rpc DropCollection(DropCollectionReq) returns (StatusResp);
    rpc ListCollections(Empty) returns (ListCollectionsResp);
    rpc Insert(InsertReq) returns (StatusResp);
    rpc BatchInsert(BatchInsertReq) returns (BatchInsertResp);
    rpc Search(SearchReq) returns (SearchResp);
    rpc Delete(DeleteReq) returns (StatusResp);
    rpc BatchDelete(BatchDeleteReq) returns (BatchDeleteResp);
    rpc ChunkText(ChunkReq) returns (ChunkResp);       // Phase 2
    rpc EmbedTexts(EmbedReq) returns (EmbedResp);       // Phase 2
}
```

gRPC server на порту `:50051`, HTTP REST остаётся на `:8080` для обратной совместимости.

#### 1.4 Python gRPC adapter (модификация `VectraDBAdapter.py`)

Заменить `aiohttp._post()` → `grpcio` вызовы. Все методы интерфейса сохраняются, меняется только transport layer.

**Файлы**:

| Файл | Действие | Строк |
|------|----------|-------|
| `internal/store/collections.go` | Создать | ~200 |
| `internal/store/db.go` | Модифицировать (Delete, BatchDelete) | +60 |
| `internal/store/hnsw.go` | Модифицировать (MarkDeleted) | +30 |
| `internal/store/wal.go` | Модифицировать (OpDelete в Recover) | +20 |
| `internal/store/shard.go` | Модифицировать (collection-aware routing) | +40 |
| `internal/http/handler.go` | Модифицировать (delete, collections endpoints) | +80 |
| `internal/http/dto.go` | Модифицировать (new request/response structs) | +50 |
| `proto/vectradb.proto` | Создать | ~100 |
| `internal/grpc/service.go` | Создать | ~250 |
| `cmd/server/main.go` | Модифицировать (start gRPC + HTTP) | +30 |

**DoD (Definition of Done)**:
- [x] `CollectionManager` создаёт/удаляет/листит коллекции ✅ DONE
- [x] Каждая коллекция имеет свой HNSW index, WAL, Arena, DiskStore ✅ DONE
- [x] `Delete(id)` удаляет из index + HNSW tombstone + WAL OpDelete ✅ DONE
- [x] WAL Recover корректно реплеит OpDelete ✅ DONE
- [x] gRPC server стартует на `:50051` параллельно с HTTP `:8080` ✅ DONE
- [ ] Python adapter работает через gRPC (все 9 методов VectorDBInterface)
- [x] Search latency < 1.5ms через gRPC — **0.31ms achieved (8.4x faster)** ✅ DONE
- [ ] Insert throughput > 1200 dp/s (было 741)
- [ ] 0% cross-collection leakage (нативные коллекции)
- [ ] Все 143 существующих Python-теста PASSED

**Тесты**:

| Тест | Что проверяет | Файл |
|------|--------------|------|
| `TestCollectionCRUD` | Create/Drop/List коллекций | Go: `collections_test.go` |
| `TestCollectionIsolation` | Search в col_A не возвращает данные из col_B | Go: `collections_test.go` |
| `TestDeleteByID` | Insert → Delete → Search не находит | Go: `db_test.go` |
| `TestDeleteWALRecovery` | Insert → Delete → Restart → Search не находит | Go: `db_test.go` |
| `TestGRPCInsertSearch` | gRPC Insert + Search round-trip | Go: `grpc_test.go` |
| `TestGRPCvsHTTPLatency` | Benchmark: gRPC < HTTP на search | Go: `bench_test.go` |
| `test_grpc_adapter` | Python gRPC adapter vs HTTP adapter parity | Python: `test_grpc_adapter.py` |

---

### Компонент 2: Go Text Chunker + Embedding Client (P1)

**Зачем**: chunking 3-5x быстрее (pure CPU, нет GIL), embedding client без asyncio overhead.

**Python-оригинал** (`TextChunker.py:79 строк`):
- Async generator: `async def read() -> DocumentChunk`
- Paragraph batching: merge small paragraphs до `max_chunk_size`
- Deterministic UUID: `uuid5(NAMESPACE_OID, f"{doc_id}-{chunk_index}")`
- `chunk_by_paragraph()` — split на `\n\n`, возвращает `[{text, chunk_size, cut_type}]`

**Python-оригинал** (`LiteLLMEmbeddingEngine.py:290 строк`):
- Retry с exponential backoff (tenacity: 128s total, 2-128s jitter)
- Context window overflow → split + pool embeddings (numpy mean)
- Multi-provider: OpenAI, Ollama, Gemini, Mistral, HuggingFace
- Rate limiting: `embedding_rate_limiter_context_manager`
- Mock mode: zero vectors for testing

**Что делаем в Go**:

#### 2.1 Chunker (`pkg/chunker/`)

```go
// paragraph.go
func ChunkByParagraph(text string, maxChunkSize int) []Chunk

// sentence.go
func ChunkBySentence(text string, maxChunkSize int) []Chunk

// types.go
type Chunk struct {
    ID         string  // uuid5(namespace, docID-chunkIndex)
    Text       string
    ChunkSize  int
    ChunkIndex int
    CutType    string  // "paragraph", "sentence"
}
```

**Улучшения vs Python**:
- `strings.Builder` вместо `" ".join()` — O(n) вместо O(n²) конкатенация
- Goroutines для parallel chunking больших документов
- Нет async generator overhead — простой `[]Chunk` return

#### 2.2 Embedding Client (`pkg/embed/`)

```go
// client.go
type EmbedClient struct {
    url        string
    model      string
    batchSize  int
    httpClient *http.Client  // connection pooling
    limiter    *rate.Limiter
}

func (c *EmbedClient) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
func (c *EmbedClient) EmbedSingle(ctx context.Context, text string) ([]float32, error)
```

**Улучшения vs Python**:
- `net/http` с HTTP/2 — быстрее aiohttp
- `golang.org/x/time/rate.Limiter` вместо tenacity
- Retry: простой for-loop с backoff вместо decorator
- Нет numpy — averaging vectors через `for i := range vec { result[i] = (a[i]+b[i])/2 }`

#### 2.3 gRPC методы (добавить в proto)

```protobuf
rpc ChunkText(ChunkReq) returns (ChunkResp);
rpc EmbedTexts(EmbedReq) returns (EmbedResp);
```

**Файлы**:

| Файл | Действие | Строк |
|------|----------|-------|
| `pkg/chunker/paragraph.go` | Создать | ~100 |
| `pkg/chunker/sentence.go` | Создать | ~80 |
| `pkg/chunker/types.go` | Создать | ~30 |
| `pkg/embed/client.go` | Создать | ~150 |
| `proto/vectradb.proto` | Обновить (Chunk + Embed RPCs) | +30 |
| `internal/grpc/service.go` | Обновить (ChunkText, EmbedTexts handlers) | +60 |

**DoD**:
- [x] `ChunkByParagraph` выдаёт идентичные чанки что и Python TextChunker — **1430 chunks, 45 chapters, exact parity** ✅ DONE
- [ ] UUID детерминистичен (uuid5): одинаковый input → одинаковый ID (currently uuid4, uuid5 TODO)
- [ ] `EmbedClient` корректно вызывает embed-server, возвращает dim=1024 vectors
- [x] Chunking 1430 чанков < 50ms — **7.4ms achieved (7-27x faster)** ✅ DONE
- [ ] Embedding throughput не хуже Python (не bottleneck — GPU bound)
- [ ] gRPC ChunkText/EmbedTexts работают из Python

**Тесты**:

| Тест | Что проверяет | Файл |
|------|--------------|------|
| `TestChunkByParagraph` | Правильное число чанков, min/max размер | Go: `chunker_test.go` |
| `TestChunkDeterministicUUID` | Один input → один UUID | Go: `chunker_test.go` |
| `TestChunkPythonParity` | Сравнение с Python output на книге | Go: `chunker_test.go` |
| `TestEmbedClient` | Real embed-server call, dim=1024 | Go: `embed_test.go` |
| `TestEmbedBatch` | Batch of 100 texts | Go: `embed_test.go` |
| `BenchmarkChunking` | Latency vs Python baseline | Go: `bench_test.go` |

---

### Компонент 3: Go Search Pipeline (P2)

**Зачем**: весь CHUNKS search path в Go — от embedding запроса до возврата результатов. Убираем Python из hot path.

**Python-оригинал** (`search.py → VectraDBAdapter.search()`):
```python
# Текущий путь:
query_vec = await embed_data([query_text])           # 5-15ms (HTTP)
results = await _post("/api/v1/search", {vec, k})    # 2.6ms (HTTP+JSON)
filtered = [r for r in results if r.id.startswith(collection)]  # 0.1ms
return filtered[:limit]
```

**Go-эквивалент** (`pipeline/search.go`):
```go
func (p *Pipeline) SearchChunks(ctx context.Context, collection, queryText string, limit int) ([]ScoredResult, error) {
    // 1. Embed query
    vec, err := p.embedClient.EmbedSingle(ctx, queryText)  // 5-15ms (HTTP → embed-server)

    // 2. Search (IN-PROCESS — 0ms transport!)
    results := p.collections.Search(collection, vec, limit)  // ~0.3-0.5ms HNSW

    return results, nil  // Нет client-side filtering — нативные коллекции
}
```

**Файлы**:

| Файл | Действие | Строк |
|------|----------|-------|
| `pipeline/search.go` | Создать | ~120 |
| `pipeline/types.go` | Создать (ScoredResult, SearchQuery) | ~40 |
| `proto/vectradb.proto` | Обновить (SearchPipeline RPC) | +15 |
| `internal/grpc/service.go` | Обновить (SearchPipeline handler) | +30 |

**DoD**:
- [x] CHUNKS search (vector only) — **0.10ms in-process, 26x faster** ✅ DONE
- [ ] Search QPS > 1500 under concurrent load (TBD with real data)
- [ ] Keyword hit rate >= 93% (не хуже текущего)
- [ ] NDCG@10 корректен на чистых данных
- [ ] gRPC SearchPipeline вызывается из Python

**Тесты**:

| Тест | Что проверяет | Файл |
|------|--------------|------|
| `TestSearchPipeline` | embed → search → results correctness | Go: `search_test.go` |
| `TestSearchQPS` | 1000 concurrent goroutines | Go: `bench_test.go` |
| `TestSearchKeywordHitRate` | 15 queries, >= 93% keywords found | Go: `search_quality_test.go` |
| `test_go_vs_python_search` | Compare Go gRPC vs Python HTTP latency | Python: `test_migration_benchmark.py` |

---

## Сводная таблица

| # | Компонент | Effort | Search gain | Write gain | Тесты | DoD items |
|---|-----------|--------|-------------|------------|-------|-----------|
| 1 | Collections + Delete + gRPC | 3-4 нед | 2-3x latency, 2-3x QPS | 2-3x dp/s | 7 Go + 1 Python | 10 |
| 2 | Chunker + Embed Client | 1-2 нед | — | 3-5x chunking | 6 Go | 6 |
| 3 | Search Pipeline | 1-2 нед | 1.5-2x E2E, 2-3x QPS | — | 4 Go + 1 Python | 5 |

**Total**: 5-8 недель, 17 Go тестов + 2 Python теста, 21 DoD item

---

## Новые Go файлы (полный список)

```
VectraDB/
  proto/
    vectradb.proto                    # gRPC service definition
  internal/
    store/
      collections.go                  # CollectionManager
      collections_test.go             # Collection CRUD + isolation tests
      db_test.go                      # Delete + WAL recovery tests
    grpc/
      service.go                      # gRPC server implementation
      interceptors.go                 # Logging, metrics middleware
      grpc_test.go                    # gRPC round-trip tests
  pkg/
    chunker/
      paragraph.go                    # Paragraph-based chunker
      sentence.go                     # Sentence-based chunker
      types.go                        # Chunk struct
      chunker_test.go                 # Chunker tests + parity check
    embed/
      client.go                       # Embedding HTTP client
      embed_test.go                   # Embed client tests
  pipeline/
    search.go                         # Search pipeline (embed → search)
    types.go                          # ScoredResult, SearchQuery
    search_test.go                    # Pipeline integration tests
    search_quality_test.go            # Hit rate, NDCG tests
    bench_test.go                     # Benchmark: gRPC vs HTTP
```

## Модифицируемые файлы

| Файл | Что меняется |
|------|-------------|
| `internal/store/db.go` | +Delete(), +BatchDelete(), +Close() improvements |
| `internal/store/hnsw.go` | +MarkDeleted(), Search пропускает tombstones |
| `internal/store/wal.go` | OpDelete в Recover() |
| `internal/store/shard.go` | Collection-aware routing |
| `internal/http/handler.go` | +DELETE endpoint, +collections endpoints |
| `internal/http/dto.go` | New request/response structs |
| `cmd/server/main.go` | +gRPC server startup параллельно с HTTP |
| `go.mod` | +google.golang.org/grpc, +tiktoken-go |

---

## Порядок реализации (по дням)

### Неделя 1: Collections + Delete в Go
- Дни 1-2: `collections.go` — CollectionManager с Create/Drop/List/Get
- Дни 3-4: Delete в `db.go` + `hnsw.go` (MarkDeleted) + WAL OpDelete
- День 5: Тесты: `TestCollectionCRUD`, `TestCollectionIsolation`, `TestDeleteByID`, `TestDeleteWALRecovery`

### Неделя 2: gRPC server
- Дни 1-2: `vectradb.proto` + codegen (`protoc`)
- Дни 3-4: `service.go` — реализация всех RPC methods
- День 5: Тесты: `TestGRPCInsertSearch`, `TestGRPCvsHTTPLatency`

### Неделя 3: Python gRPC adapter + Chunker
- Дни 1-2: Python `VectraDBAdapter.py` → gRPC transport
- День 3: Go Chunker (`paragraph.go`, `sentence.go`)
- Дни 4-5: Тесты: `test_grpc_adapter`, `TestChunkByParagraph`, `TestChunkPythonParity`

### Неделя 4: Embed Client + Search Pipeline
- День 1: Go Embedding Client (`client.go`)
- Дни 2-3: Search Pipeline (`search.go`)
- Дни 4-5: Integration тесты + benchmark: `TestSearchPipeline`, `TestSearchQPS`, `test_go_vs_python_search`

### Неделя 5: Polish + Full benchmark
- День 1-2: Прогнать все 143 Python-теста, фиксить regression
- День 3: Полный benchmark (Go gRPC path vs Python HTTP path)
- День 4: Документация (обновить CLAUDE.md, BENCHMARK_RESULTS.md)
- День 5: Buffer / tech debt

---

## Метрики успеха (финальные)

| Метрика | Сейчас (Python+HTTP) | Цель (Go+gRPC) | Метод проверки |
|---------|---------------------|-----------------|----------------|
| Search latency | 2.6ms | **< 1.0ms** | `BenchmarkSearch` |
| Search QPS | 719 | **> 1500** | `TestSearchQPS` |
| Insert dp/s | 741 | **> 1500** | `BenchmarkInsert` |
| Chunking 1430 chunks | 50-200ms | **< 40ms** | `BenchmarkChunking` |
| Collection isolation | 0% (prefix hack) | **0% (native)** | `TestCollectionIsolation` |
| Delete works | Cache-only | **WAL+HNSW** | `TestDeleteWALRecovery` |
| Python tests | 143 PASSED | **143 PASSED** | `pytest tests/ -v` |
| Keyword hit rate | 93% | **>= 93%** | `TestSearchKeywordHitRate` |

---

## Верификация

После каждого компонента:
1. Go тесты: `go test ./... -v -count=1`
2. Python тесты: `pytest tests/ -v` (все 143 PASSED)
3. Benchmark: `go test -bench=. -benchtime=10s ./...`
4. Integration: `pytest tests/test_comprehensive_comparison.py -v -s`
