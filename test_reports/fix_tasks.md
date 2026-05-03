# Levara: план фиксов и тестирования

Модульный review от 2026-04-15, актуализирован 2026-04-17 после F-4 MCP split
и Wave A покрытия `internal/http`. Приоритеты сортируются по **влияние × риск
× объём непокрытого кода**.

## Что уже закрыто

- **FIX-1 (db.mu)** — done. Group-commit WAL + async fsync, `internal/store`
  показывает 8.6× scaling на 8 писателях (см. CLAUDE.md «Cognevra Write Path»).
- **FIX-2 частично** — MCP вынесен в `pkg/mcp/` (F-4, PRs #7–#30), 18 test
  files, 220+ тестов. `internal/http/api.go` больше не содержит MCP-handler'ы.
  Осталось покрыть search-handler'ы (идёт post-F-4 wave A-D).
- **FIX-3 (orchestrator)** — done. `pkg/orchestrator/pipeline_test.go`,
  `parseentities_test.go`, `pipeline_deepseek_test.go` (18 тестов).
- **FIX-4 (extract)** — done. `pkg/extract/extract_test.go` +
  `code_test.go` (23 теста).
- **FIX-5 (llm)** — done. `pkg/llm/mock/`, `ratelimit_test.go`,
  `structured_dispatch_test.go` + mock provider через `pkg/llm/mock`.
- **FIX-8 (community)** — done. `pkg/community/` уже имеет покрытие на
  Louvain + incremental.

## Что ещё открыто

### P0 — internal/http search coverage (идёт сейчас)

Post-F-4 coverage push. `internal/http` оставался самым большим untested
пакетом после выноса MCP. Делим на 4 волны:

- **Wave A — graphCompletionSearch + tripletCompletionSearch** ✅
  9 тестов, `internal/http/graph_search_test.go`, общий fixture в
  `search_test_helpers.go`. Branch `claude/test-wave-a-graph-search`.
- **Wave B — cypherSearch + naturalLanguageSearch** (pending)
  Фокус: security gates (`ALLOW_CYPHER_QUERY`, write-op blocking) +
  fallback paths когда LLM не возвращает валидный Cypher.
- **Wave C — contextExtensionSearch + cotSearch + communityLocal/GlobalSearch** (pending)
  2-hop traversal, chain-of-thought prompt shape, community retrieval.
- **Wave D — RBAC end-to-end** (pending)
  User A cognify → user B search → 0 results. Без моков по максимуму.

### P1 — внешние сервисы / side-paths

- ~~**FIX-6 (cluster sync).**~~ **Done** — 5 chaos/convergence
  тестов в `internal/cluster/replication_chaos_test.go`:
  real `*store.Levara` на обеих сторонах + `httptest.Server`, без моков.
  Покрывает snapshot-bootstrap, post-snapshot WAL-handoff, property-style
  random insert/delete convergence, flaky listener gap + reconnect,
  exponential backoff на HTTP 500. Мастер-пакет проходит `-race`.
- ~~**FIX-7 (graph architecture ADR).**~~ **Done** — см.
  `Levara/docs/adr/001-graph-layering.md` (accepted 2026-04-15).
  Фиксирует роли: `graph` = алгоритмы без persistence, `graphdb` = Neo4j
  backend, `graphstore` = dormant Postgres backend + interface, ждёт
  активации. `pkg/graphstore/store.go` помечен `TODO(ADR-001)`.
- ~~**FIX-9 (llmproxy contract tests).**~~ **Done** — 10 contract
  тестов в `pkg/llmproxy/proxy_contract_test.go` (byte-verbatim
  forwarding, auth header, error-not-cached, 502 на unreachable,
  temperature/model/order → разные cache keys, non-POST passthrough,
  MaxInFlight bound). В сумме с существующими smoke-тестами — 16/16.

### P2 — средние

- ~~**FIX-10 (S3 backend).**~~ **Done** — 6 тестов в
  `pkg/storage/s3_mock_test.go`: in-memory S3-compatible httptest-сервер
  покрывает PUT/GET/HEAD/DELETE + list-type=2, round-trip содержимого,
  sig-v4 зависимость подписи от payload, 404→error на Load и 503→error на
  Save, идемпотентность повторного Delete. MinIO-контейнер не нужен —
  контракт тот же, тесты < 1 секунды.
- ~~**FIX-11 (observe edge).**~~ **Done** — 3 edge-теста в
  `pkg/observe/observe_edge_test.go` поверх существующих 10:
  LOG_LEVEL=ERROR/DEBUG/TRACE → minLevel, Langfuse full-payload fields
  (Metadata+Status+usage), ErrorTracker post-eviction dedup index
  consistency (регрессия на переиндексацию кольцевого буфера).
- ~~**FIX-12 (ontology golden).**~~ **Done** — уже покрыт.
  `pkg/ontology/ontology_test.go` содержит 20 тестов с FOAF-like RDF
  фикстурой (классы + subClassOf + individuals) и schema.org-like
  иерархией (Organization → Corporation, fuzzy-поиск на вложенные
  классы). Публичная онтология моделируется inline-фикстурой, чтобы не
  тянуть внешние файлы в вендор.
- ~~**FIX-13 (gRPC contracts).**~~ **Done** — 9 contract-тестов в
  `internal/grpc/service_contract_test.go` поверх существующих 8:
  ChunkText (paragraph/sentence/merged/default), HashFiles round-trip,
  ListDirectory с фильтром по расширению и recursive, Compact,
  AggregateSearch (пере-дача в pkg/aggregator), LLMCache put/get/stats
  (temperature — часть ключа), BM25 index+search (ранжирование +
  InvalidArgument guards), SearchTriplets scoring, ExtractText
  plain-text. Симметрично HTTP wave-coverage, без embed-server/graphdb.
- ~~**FIX-14 (merge mini-packages).**~~ **Won't do** (решение
  2026-04-17). Задумывалось: `git` + `fetch` → `ingest`,
  `classify` → `extract`. После аудита (5 пакетов, 7 production-сайтов,
  ~1000 LOC тестов) — отказ. Причины:
  1. **Dependency bloat.** `fetch` тянет `goquery` (HTML DOM). Merge в
     `ingest` → все 4 caller'а (`cmd/server`, `grpc`, `http`, `mcp
     tool_data`) транзитивно линкуют goquery, хотя MCP-тул считает только
     SHA+диск. `extract` тянет `tabula` + `pkg/audio` (Whisper);
     `classify` — чистый stdlib. Merge → `pkg/orchestrator` (единственный
     caller classify, использует его только для выбора chunker'а) начнёт
     линковать document-parser. Это регресс.
  2. **SRP чище как есть.** `classify` — pure dispatcher
     `(filename, content) → {Type, Chunker, Min/Max}`. `extract` —
     трансформация байтов в текст. Разные сигнатуры, разные концерны.
     `git.ParseLog` — local git reader; имя `ingest.ParseGitLog()`
     читалось бы как "запись git-истории". `fetch` — URL/HTML text
     extraction с единственным use-case (HTTP `/add` детектит URL в
     body), обобщать нечего.
  3. **Нет измеримого выигрыша, есть риск.** Переименование тронет 3
     test-suite'а и 7 production-сайтов. Все пакеты уже покрыты тестами.
     FIX-14 — единственный в списке пункт без конкретной дыры
     (coverage/concurrency/security). Aesthetic refactor без sign-off
     закрывается как won't-do.

  Если в будущем dependency graphs сойдутся (например, `goquery` уйдёт
  из `fetch`), вопрос можно пересмотреть.

### P0 — HybridSearch panic + embed-client Ollama (обнаружено 2026-05-02)

Три связанных бага, обнаруженных во время интеграционного тестирования
MemoryFS eval suite (28816 чанков, 20 реальных запросов). Cognevra упала
с `panic: runtime error: nil pointer dereference` при вызове
`HybridSearch` с Ollama `/api/embed` endpoint.

---

#### FIX-15 — Data race в `HNSWIndex.Search()` (Cognevra)

**Статус:** ✅ Исправлено (2026-05-02)
**Файл:** `Cognevra/internal/store/hnsw.go`, метод `Search`
**Severity:** P0 (crash + data corruption risk)
**Аналог Levara:** уже исправлено ранее (F-6 в testing-roadmap.md)

**Симптом:** Panic при concurrent `HybridSearch` + `Insert` — `nil pointer
dereference` в `searchLayer()` при обращении к `curr.ArenaOffset`.

**Root cause:** Cognevra-версия `Search()` использовала **split-lock** паттерн —
`RLock` отпускался дважды, а `searchLayer`/`searchLayerTopK` выполнялись
**вообще без блокировки**:

```go
// БЫЛО (Cognevra) — broken
h.RLock()
entryID := h.EntryNodeID   // ← читаем под lock
maxL := h.MaxLayer
h.RUnlock()                 // ← отпускаем!

// ... efSearch config без lock ...

h.RLock()                   // ← re-acquire
curr := h.Nodes[entryID]   // ← node мог быть удалён между locks
h.RUnlock()                 // ← отпускаем снова!

for l := maxL; l > 0; l-- {
    curr = h.searchLayer(...)  // ← TRAVERSAL БЕЗ LOCK!
}
topResults := h.searchLayerTopK(...)  // ← ТОЖЕ БЕЗ LOCK
```

Между двумя `RLock`/`RUnlock` парами concurrent `Add()` мог:
1. Удалить entry node из `h.Nodes` → `Nodes[entryID]` возвращает `nil`
2. Мутировать `node.Connections` → `searchLayer` читает гонку

**Что изменено:**

```go
// СТАЛО — fixed (как в Levara F-6)
h.RLock()
defer h.RUnlock()            // ← держим на всё время traversal

entryID := h.EntryNodeID
maxL := h.MaxLayer

if entryID == "" { return nil }

curr := h.Nodes[entryID]
if curr == nil { return nil }  // ← NEW: nil guard

// ... efSearch, searchLayer, searchLayerTopK — всё под RLock
```

**Как проверить:**

```bash
# 1. Unit-тесты HNSW с race detector
cd Cognevra && go test ./internal/store/ -run "Search|HNSW" -race -v -timeout 60s

# 2. Нагрузочный concurrent тест (Search + Insert одновременно)
cd Cognevra && go test ./internal/store/ -run "TestConcurrentSearchInsert" -race -count 5

# 3. Полный прогон store
cd Cognevra && go test ./internal/store/ -race -timeout 120s
```

**Рекомендация по тесту (если нет TestConcurrentSearchInsert):**

Создать тест, который:
1. Запускает N=100 Insert в горутинах
2. Одновременно запускает M=100 Search в горутинах
3. Проверяет: нет паники, все Search возвращают `[]VectroRecord` (не nil)
4. Запускать с `-race -count 10`

---

#### FIX-16 — Nil guard для entry node в `Search()`

**Статус:** ✅ Исправлено (2026-05-02)
**Файлы:**
- `Cognevra/internal/store/hnsw.go` — добавлен `if curr == nil { return nil }`
- `Levara/internal/store/hnsw.go` — добавлен `if curr == nil { return nil }`
**Severity:** P0 (crash prevention)

**Симптом:** `panic: runtime error: nil pointer dereference` при обращении
к `searchLayer(query, nil, layer, getVec)` → `curr.ArenaOffset` на nil.

**Root cause:** `h.Nodes[entryID]` возвращает `nil` (zero value для
`*HNSWNode`) когда ключа нет в map. Это возможно:
- После WAL corruption / неполного recovery
- При concurrent delete entry node (даже с lock, если есть баг в deleteSet)
- При ошибке в CollectionManager.Get (stale EntryNodeID)

**Что изменено:**

```go
curr := h.Nodes[entryID]
if curr == nil {
    return nil   // ← NEW: graceful degradation вместо panic
}
```

Добавлено в ОБА экземпляра: Cognevra и Levara.

**Как проверить:**

```bash
# 1. Existing tests
cd Cognevra && go test ./internal/store/ -run Search -v
cd Levara && go test ./internal/store/ -run Search -v

# 2. Целенаправленный тест на nil entry
```

**Рекомендация по тесту:**

```go
func TestSearch_NilEntryNode(t *testing.T) {
    h := &HNSWIndex{
        EntryNodeID: "ghost",       // есть в поле...
        Nodes:       map[string]*HNSWNode{}, // ...но нет в map
        MaxLayer:    3,
        cfg:         DefaultHNSWConfig(),
    }
    // Должен вернуть nil, не паникнуть
    result := h.Search([]float32{0.1, 0.2, 0.3}, 5)
    assert.Nil(t, result)
}
```

---

#### FIX-17 — Embed-client не поддерживает Ollama `/api/embed` формат

**Статус:** ✅ Исправлено (2026-05-02)
**Файлы:**
- `Cognevra/pkg/embed/client.go` — `embeddingResponse` + `embedBatch()`
- `Levara/pkg/embed/client.go` — то же самое
**Severity:** P0 (функциональный блокер HybridSearch с Ollama)

**Симптом:** Server-side `HybridSearch` с `embed_endpoint` указывающим на
Ollama `/api/embed` **всегда возвращал пустые vector scores** (vector_score=0
у всех результатов). Фьюзинг работал только по BM25-компоненте.

**Root cause:** `pkg/embed/client.go` ожидал **только OpenAI-формат**
ответа от embedding API:

```json
// OpenAI format (поддерживался)
{"data": [{"index": 0, "embedding": [0.1, 0.2, ...]}]}

// Ollama /api/embed format (НЕ поддерживался)
{"model": "nomic-embed-text-v2-moe", "embeddings": [[0.1, 0.2, ...]]}
```

Ответ Ollama парсился в `embeddingResponse.Data` = nil (нет поля `data`).
`embedBatch()` возвращал `([][]float32{}, nil)` — пустой слайс без ошибки.
`EmbedTexts` → `EmbedSingle` возвращал `nil, nil`. Guard `len(vec) == 0`
в `service.go:1341` ловил это и отправлял ошибку через channel. Результат:
vector search внутри HybridSearch фейлился, возвращалась gRPC ошибка.

**Что изменено:**

```go
// 1. Расширен response struct
type embeddingResponse struct {
    // OpenAI format
    Data []struct {
        Index     int       `json:"index"`
        Embedding []float32 `json:"embedding"`
    } `json:"data"`
    // Ollama /api/embed format
    Embeddings [][]float32 `json:"embeddings"`
}

// 2. Fallback в embedBatch()
if len(result.Data) == 0 && len(result.Embeddings) > 0 {
    return result.Embeddings, nil
}
```

**Как проверить:**

```bash
# 1. Unit test: Ollama response parsing
cd Cognevra && go test ./pkg/embed/ -run "TestEmbedOllama" -v

# 2. Integration test: реальный Ollama (требует запущенный Ollama)
cd Cognevra && OLLAMA_URL=http://127.0.0.1:11434 go test ./pkg/embed/ -run "TestEmbedSingle" -v

# 3. End-to-end HybridSearch через gRPC
python3 -c "
import grpc, json, sys, hashlib, requests
sys.path.insert(0, 'Cognevra/proto')
import cognevra_pb2 as pb, cognevra_pb2_grpc as pb_grpc

stub = pb_grpc.CognevraServiceStub(grpc.insecure_channel('127.0.0.1:50051'))

# Создать коллекцию, вставить данные, запустить HybridSearch
# Проверить: vector_score > 0 у результатов
"
```

**Рекомендация по тесту:**

```go
func TestEmbedBatch_OllamaFormat(t *testing.T) {
    // Mock HTTP server returning Ollama format
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]any{
            "model":      "nomic-embed-text-v2-moe",
            "embeddings": [][]float32{{0.1, 0.2, 0.3}},
        })
    }))
    defer srv.Close()

    c := NewClient(srv.URL, "test", 16, 1)
    vec, err := c.EmbedSingle(context.Background(), "test text")
    require.NoError(t, err)
    assert.Equal(t, []float32{0.1, 0.2, 0.3}, vec)
}

func TestEmbedBatch_OpenAIFormat(t *testing.T) {
    // Regression: OpenAI format still works
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        json.NewEncoder(w).Encode(map[string]any{
            "data": []map[string]any{
                {"index": 0, "embedding": []float32{0.4, 0.5, 0.6}},
            },
        })
    }))
    defer srv.Close()

    c := NewClient(srv.URL, "test", 16, 1)
    vec, err := c.EmbedSingle(context.Background(), "test text")
    require.NoError(t, err)
    assert.Equal(t, []float32{0.4, 0.5, 0.6}, vec)
}
```

---

#### Общая верификация всех трёх фиксов

```bash
# 1. Build — оба проекта компилируются
cd Cognevra && go build ./...
cd ../Levara && go build ./...

# 2. Race-free tests
cd Cognevra && go test ./internal/store/ ./pkg/embed/ -race -timeout 120s
cd ../Levara && go test ./internal/store/ ./pkg/embed/ -race -timeout 120s

# 3. Docker rebuild + E2E
cd /path/to/Levara
COGNEVRA_DIM=768 docker compose up -d --build cognevra

# 4. E2E HybridSearch test
python3 test_hybrid_e2e.py  # см. скрипт ниже
```

**E2E верификационный скрипт** (`test_hybrid_e2e.py`):

```python
import grpc, json, hashlib, requests, sys, time
sys.path.insert(0, "Cognevra/proto")
import cognevra_pb2 as pb, cognevra_pb2_grpc as pb_grpc

OLLAMA = "http://127.0.0.1:11434"
GRPC = "127.0.0.1:50051"
COLLECTION = "hybrid_e2e_test"

stub = pb_grpc.CognevraServiceStub(grpc.insecure_channel(GRPC))

# Setup
try: stub.DropCollection(pb.DropCollectionReq(name=COLLECTION))
except: pass
stub.CreateCollection(pb.CreateCollectionReq(name=COLLECTION))

texts = [
    "Levara is a high-performance vector database written in Go",
    "BM25 full-text search uses TF-IDF scoring for keyword matching",
    "Hybrid search combines vector and keyword retrieval via RRF fusion",
]
vecs = requests.post(f"{OLLAMA}/api/embed",
    json={"model": "nomic-embed-text-v2-moe", "input": texts}).json()["embeddings"]

records, items = [], []
for i, (t, v) in enumerate(zip(texts, vecs)):
    pid = hashlib.sha256(f"e2e-{i}".encode()).hexdigest()[:16]
    records.append(pb.InsertRecord(id=pid, vector=v, metadata_json=json.dumps({"text": t})))
    items.append(pb.IndexItem(id=pid, text=t, metadata_json=json.dumps({"text": t})))

stub.BatchInsert(pb.BatchInsertReq(collection=COLLECTION, records=records))
stub.BM25Index(pb.BM25IndexReq(collection=COLLECTION, items=items))

# Test 1: HybridSearch не паникнул
resp = stub.HybridSearch(pb.HybridSearchReq(
    collection=COLLECTION, query_text="vector database search", top_k=5,
    embed_endpoint="http://host.docker.internal:11434/api/embed",
    embed_model="nomic-embed-text-v2-moe",
    vector_weight=1.0, bm25_weight=1.0,
))
assert len(resp.results) > 0, "FAIL: no results"
print(f"✓ HybridSearch returned {len(resp.results)} results (no panic)")

# Test 2: Vector scores > 0 (Ollama format works)
has_vector = any(r.vector_score > 0 for r in resp.results)
assert has_vector, f"FAIL: all vector_score=0"
print(f"✓ Vector scores present (Ollama embed format works)")

# Test 3: BM25 scores > 0
has_bm25 = any(r.bm25_score > 0 for r in resp.results)
assert has_bm25, f"FAIL: all bm25_score=0"
print(f"✓ BM25 scores present (fusion works)")

# Test 4: Concurrent (race condition check)
import concurrent.futures
def query(i):
    stub.HybridSearch(pb.HybridSearchReq(
        collection=COLLECTION, query_text=f"query {i}",
        top_k=3, embed_endpoint="http://host.docker.internal:11434/api/embed",
        embed_model="nomic-embed-text-v2-moe", vector_weight=1.0, bm25_weight=1.0,
    ))
    return True

with concurrent.futures.ThreadPoolExecutor(max_workers=8) as ex:
    results = list(ex.map(query, range(20)))
assert all(results), "FAIL: concurrent queries failed"
print(f"✓ 20 concurrent HybridSearch calls — no crash")

# Cleanup
stub.DropCollection(pb.DropCollectionReq(name=COLLECTION))
print("\nALL CHECKS PASSED")
```

#### Дополнительно: Dockerfile Cognevra

**Статус:** ✅ Исправлено (2026-05-02)
**Файл:** `Cognevra/Dockerfile`

Go version в Dockerfile отставала: `golang:1.25-alpine`, но `go.mod`
требует `go 1.26.0`. Docker build падал:

```
go: go.mod requires go >= 1.26.0 (running go 1.25.9; GOTOOLCHAIN=local)
```

**Фикс:** `FROM golang:1.25-alpine` → `FROM golang:1.26-alpine`

---

### P3 — наблюдать

- **OBS-1** — `pkg/audio` ранняя стадия, не трогать до явного use-case.
- **OBS-2** — `pkg/temporal` — фича работает, тесты есть.
- **OBS-3** — `pkg/rerank`, `pkg/llmcache`, `pkg/embed` — зрелые, покрыты.
- **OBS-4** — `pkg/embed` после FIX-17 поддерживает и OpenAI и Ollama формат. Добавить unit-тест на оба формата.

## Политика при первом падении

1. Остановить прогон.
2. Записать `test_reports/failures/<pkg>_<test>_<date>.md` со stack trace.
3. Классифицировать:
   - `flake` → добавить retry и продолжить;
   - `env` (missing binary, port busy) → починить инфру;
   - `regression` → `git bisect`, зафиксировать в `hall="discovery"` через Levara MCP;
   - `legit-bug` → создать задачу FIX-N, **не чинить автоматически**.
4. Перед фиксом — `recall_memory(query="<module>", hall="discovery")`.
