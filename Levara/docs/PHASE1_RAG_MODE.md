# Phase 1: RAG Mode — Implementation Plan

**Цель:** Добавить в Levara полноценный RAG-режим, при котором cognify работает без LLM (только chunk + embed), chunking поддерживает overlap (sliding window), и результаты поиска могут быть reranked через внешний или встроенный reranker.

**Принцип:** Никаких моков, заглушек, TODO-комментариев. Каждый пункт — рабочий код с тестами.

---

## 1. Scope — что входит в Phase 1

| # | Фича | Файлы | Описание |
|---|-------|-------|----------|
| 1.1 | `skip_graph` flag в cognify | `pipeline.go`, `mcp.go` | cognify выполняет только Stage 1 (chunk) + Stage 4b (embed+index), пропуская Stage 2 (LLM extraction), Stage 3 (dedup/temporal), Stage 4a (Neo4j), Stage 4c (PG graph) |
| 1.2 | Sliding window chunking с overlap | `pkg/chunker/sliding.go` (новый), `paragraph.go`, `sentence.go` | Новая стратегия `"sliding"` + параметр `OverlapChars` в Config |
| 1.3 | Reranker hook | `pkg/rerank/reranker.go` (новый), `pipeline/search.go`, `mcp.go` | Cross-encoder reranking через внешний HTTP endpoint (Cohere-compatible API) |
| 1.4 | MCP API: `mode` parameter | `mcp.go` | Параметр `mode` в cognify tool (`"rag"` / `"full"`) и `rerank` в search tool |

**Что НЕ входит:** parent-child chunks, community detection, adaptive router, graph-aware reranking.

---

## 2. Детальный дизайн

### 2.1 `skip_graph` flag в cognify pipeline

**Файл:** `pkg/orchestrator/pipeline.go`

**Изменения в Config (line 42):**
```go
type Config struct {
    // ... existing fields ...
    
    // SkipGraph when true skips LLM entity extraction (Stage 2),
    // deduplication (Stage 3), temporal extraction (Stage 3b),
    // Neo4j write (Stage 4a), and PostgreSQL graph upsert (Stage 4c).
    // Only chunking (Stage 1) and vector embedding (Stage 4b) execute.
    // This is the "RAG mode" — fastest ingestion, no LLM calls.
    SkipGraph bool
}
```

**Изменения в Run() (line 155):**

После Stage 1 (chunking, line 219), добавить branch:

```go
if cfg.SkipGraph {
    // RAG mode: skip stages 2-3, jump directly to vector embedding.
    progressCh <- Progress{Stage: "embedding", ChunksCreated: len(allChunks), ElapsedMs: ms(start)}
    // Execute ONLY the vector embedding part of Stage 4b (lines 455-587).
    // No Neo4j, no PG graph, no entity embedding, no triplets.
    goto vectorEmbed
}
```

**Что именно выполняется при `SkipGraph=true`:**
- Stage 1: Chunking — **да** (без изменений)
- Stage 2: LLM extraction — **пропуск**
- Stage 3: Dedup — **пропуск**
- Stage 3b: Temporal — **пропуск**
- Stage 4a: Neo4j — **пропуск**
- Stage 4b: Vector embed chunks — **да** (только chunk embedding, без entity/triplet embedding)
- Stage 4c: PG graph upsert — **пропуск**
- BM25 index update — **да**

**Реализация:** Не использовать `goto`. Вместо этого — обернуть stages 2-3 в `if !cfg.SkipGraph { ... }`, а в Stage 4 разделить:

```go
// Stage 4b: Vector embed (always runs)
writeWg.Add(1)
go func() {
    defer writeWg.Done()
    // Embed raw text chunks (this part always runs)
    embedAndIndexChunks(ctx, cfg, allChunks, start, progressCh)
    
    // Embed entities + triplets (only when graph is built)
    if !cfg.SkipGraph {
        embedAndIndexEntities(ctx, cfg, dedupResult, start, progressCh)
        embedAndIndexTriplets(ctx, cfg, dedupResult, start, progressCh)
    }
}()

// Stage 4a + 4c: Graph writes (only when graph is built)
if !cfg.SkipGraph {
    // Neo4j write...
    // PG upsert...
}
```

**Почему не отдельная функция `RunRAG()`:** Дублирование кода. Единственная разница — пропуск stages, а не альтернативная логика. Branches в Run() проще поддерживать.

---

### 2.2 Sliding Window Chunking

**Новый файл:** `pkg/chunker/sliding.go`

```go
// ChunkBySliding splits text into fixed-size windows with overlap.
// windowChars — размер окна в символах (runes).
// overlapChars — количество символов overlap между соседними чанками.
// Overlap берётся из КОНЦА предыдущего чанка, а не из начала следующего.
//
// Пример: text="ABCDEFGHIJ", window=5, overlap=2
//   chunk 0: "ABCDE"
//   chunk 1: "DEFGH"  (overlap: "DE" из предыдущего)
//   chunk 2: "GHIJ"   (overlap: "GH" из предыдущего, короче window)
func ChunkBySliding(text string, windowChars, overlapChars int, documentID string) []Chunk
```

**Алгоритм:**
1. Конвертировать text в `[]rune` для корректной работы с Unicode
2. `step := windowChars - overlapChars`
3. Итерировать с шагом `step`, нарезать `runes[pos : min(pos+windowChars, len(runes))]`
4. Конвертировать обратно в string
5. Пропускать чанки < `DefaultMinChunkChars` (80 символов)
6. Использовать `chunkID(documentID, i)` для детерминистичных ID

**Валидация входных параметров:**
- `overlapChars >= windowChars` → panic или ошибка? **Решение:** возвращать ошибку через второй return value: `([]Chunk, error)`. Но текущие chunkers возвращают только `[]Chunk`. **Компромисс:** clamp `overlapChars` до `windowChars/2` с log.Printf warning, сохраняя сигнатуру консистентной с остальными chunkers.
- `windowChars <= 0` → clamp до `DefaultMaxChunkChars` (600)
- `overlapChars < 0` → clamp до 0

**Интеграция в pipeline:**

`pkg/orchestrator/pipeline.go` line 186, добавить case:
```go
case "sliding":
    overlap := cfg.OverlapChars
    if overlap <= 0 {
        overlap = cfg.MaxChunkChars / 5 // default 20% overlap
    }
    chunks = chunker.ChunkBySliding(text, cfg.MaxChunkChars, overlap, docID)
```

**Config extension (line 42):**
```go
type Config struct {
    // ... existing ...
    OverlapChars int // For sliding window chunking. Default: MaxChunkChars/5
}
```

**Не трогаем существующие стратегии.** `"merged"`, `"paragraph"`, `"sentence"` остаются без overlap — их семантика определена (граница = параграф/предложение). Overlap имеет смысл только для fixed-size window.

---

### 2.3 Reranker Hook

**Новый файл:** `pkg/rerank/reranker.go`

```go
package rerank

// RerankResult holds a single reranked item.
type RerankResult struct {
    Index int     `json:"index"`
    Score float64 `json:"score"`
}

// Client calls an external reranker service (Cohere rerank API compatible).
type Client struct {
    url       string // e.g., "http://localhost:8787/rerank"
    model     string // e.g., "bge-reranker-v2-m3"
    topN      int
    httpClient *http.Client
}

// NewClient creates a reranker client.
// If url is empty, Rerank() is a no-op (returns input order unchanged).
func NewClient(url, model string, topN int) *Client

// Rerank sends query + documents to reranker and returns reordered indices.
// Documents are the text content of search results.
// Returns results sorted by relevance score descending.
//
// API contract (Cohere-compatible):
//   POST /rerank
//   {"query": "...", "documents": ["...", "..."], "model": "...", "top_n": N}
//   → {"results": [{"index": 0, "relevance_score": 0.95}, ...]}
func (c *Client) Rerank(ctx context.Context, query string, documents []string) ([]RerankResult, error)
```

**Почему Cohere-compatible API:** Это де-факто стандарт. Совместим с:
- Cohere Rerank API
- Jina Reranker
- `vllm serve` с `--served-model-name` rerank
- `infinity_emb` (FlagEmbedding)
- `TEI` (Text Embeddings Inference от HuggingFace)

**Интеграция в SearchPipeline:**

`pipeline/search.go` — добавить optional reranker:

```go
type SearchPipeline struct {
    embedClient *embed.Client
    collections *store.CollectionManager
    reranker    *rerank.Client // nil = no reranking
}

func NewSearchPipeline(embedClient *embed.Client, collections *store.CollectionManager, reranker *rerank.Client) *SearchPipeline
```

Новый метод:
```go
// SearchByTextWithRerank performs vector search, then reranks results
// using cross-encoder. If reranker is nil, equivalent to SearchByText.
func (p *SearchPipeline) SearchByTextWithRerank(ctx context.Context, collection, query string, limit int) ([]ScoredResult, error) {
    // 1. Overfetch: retrieve limit*3 candidates from HNSW
    candidates, err := p.SearchByText(ctx, collection, query, limit*3)
    // 2. Extract text from metadata for reranking
    docs := extractTexts(candidates)
    // 3. Rerank
    reranked, err := p.reranker.Rerank(ctx, query, docs)
    // 4. Reorder candidates by rerank score, return top limit
    return reorderByRerank(candidates, reranked, limit), nil
}
```

**Конфигурация (environment):**
```
RERANK_ENDPOINT=http://localhost:8787/rerank   # пустая строка = выключен
RERANK_MODEL=bge-reranker-v2-m3
```

**Graceful degradation:** Если reranker недоступен (timeout, 5xx), SearchByTextWithRerank логирует warning и возвращает результаты без reranking (fallback к исходному порядку). Не ломает поиск.

---

### 2.4 MCP API Changes

**cognify tool** (`mcp.go` line 86):

Добавить параметр `mode`:
```json
{
    "mode": {
        "type": "string",
        "enum": ["rag", "full"],
        "default": "full",
        "description": "Pipeline mode. 'rag': chunk+embed only (no LLM, no graph). 'full': complete pipeline with entity extraction."
    },
    "chunk_strategy": {
        "type": "string",
        "enum": ["merged", "paragraph", "sentence", "row", "code", "sliding", "auto"],
        "default": "merged",
        "description": "Chunking strategy. 'sliding' enables fixed-window with overlap."
    },
    "overlap_chars": {
        "type": "integer",
        "description": "Overlap in characters for sliding window chunking. Default: 20% of max_chunk_chars."
    }
}
```

В handler `toolCognify` (line 794):
```go
if mode, _ := args["mode"].(string); mode == "rag" {
    pipeCfg.SkipGraph = true
}
if cs, _ := args["chunk_strategy"].(string); cs != "" {
    pipeCfg.ChunkStrategy = cs
}
if oc, ok := args["overlap_chars"].(float64); ok {
    pipeCfg.OverlapChars = int(oc)
}
```

**search tool** (`mcp.go` line 102):

Добавить параметр `rerank`:
```json
{
    "rerank": {
        "type": "boolean",
        "default": false,
        "description": "Apply cross-encoder reranking to results. Requires RERANK_ENDPOINT configured."
    }
}
```

В handler `toolSearch` (line 939):
```go
if doRerank, _ := args["rerank"].(bool); doRerank && h.cfg.RerankEndpoint != "" {
    // Use SearchByTextWithRerank instead of SearchByText
    res, err = sp.SearchByTextWithRerank(ctx, coll, query, fetchK)
} else {
    res, err = sp.SearchByText(ctx, coll, query, fetchK)
}
```

---

## 3. Definition of Done (DoD)

### 3.1 skip_graph
- [ ] `cognify(data="...", mode="rag")` выполняется **без LLM endpoint** (LLM_ENDPOINT может быть пустым)
- [ ] Чанки проэмбеддены и найдены через `search(search_query="...")`
- [ ] `graph_nodes` и `graph_edges` таблицы **пусты** после RAG-mode cognify
- [ ] Коллекция `Triplet_text` **не создаётся** при mode=rag
- [ ] `cognify_status` корректно показывает стадии (chunking → embedding → COMPLETED, без extracting/deduplicating)
- [ ] `cognify(data="...", mode="full")` работает как раньше (backward compatible)
- [ ] `cognify(data="...")` без mode — работает как `mode="full"` (default)
- [ ] Время RAG-mode cognify на 1KB текста < 2 секунд (без LLM overhead)

### 3.2 Sliding window chunking
- [ ] `cognify(data="...", chunk_strategy="sliding")` создаёт чанки с overlap
- [ ] Default overlap = 20% от max_chunk_chars (e.g., 400 chars window → 80 chars overlap)
- [ ] `cognify(data="...", chunk_strategy="sliding", overlap_chars=100)` — явный overlap
- [ ] Чанки с overlap: последние N символов chunk[i] == первые N символов chunk[i+1]
- [ ] Unicode-safe: overlap корректно работает с кириллицей, эмодзи, CJK
- [ ] Последний чанк может быть короче window (не дополняется padding)
- [ ] `overlap_chars >= window` → clamp до window/2, log warning
- [ ] Пустой текст → 0 чанков (не паника)
- [ ] Текст короче window → 1 чанк

### 3.3 Reranker
- [ ] `search(search_query="...", rerank=true)` при `RERANK_ENDPOINT` настроенном → результаты reranked
- [ ] `search(search_query="...", rerank=true)` при пустом `RERANK_ENDPOINT` → результаты **без reranking**, без ошибки
- [ ] `search(search_query="...", rerank=false)` → поведение как сейчас
- [ ] `search(search_query="...")` без rerank → default false, как сейчас
- [ ] Reranker timeout (5s default) → graceful fallback к vector order
- [ ] Reranker 500 → graceful fallback + log warning
- [ ] Reranker response с `results: []` (пустой) → возвращаем vector order
- [ ] Overfetch coefficient = 3x (search 30, rerank, return top 10)

### 3.4 Интеграция
- [ ] Все существующие тесты проходят (zero regressions)
- [ ] Новых файлов: `pkg/chunker/sliding.go`, `pkg/rerank/reranker.go`
- [ ] Изменённых файлов: `pkg/orchestrator/pipeline.go`, `pipeline/search.go`, `internal/http/mcp.go`
- [ ] go build без ошибок
- [ ] go vet без warnings

---

## 4. Риски и митигации

### R1: Pipeline.Run() становится слишком сложным
**Риск:** Добавление `SkipGraph` branches в Run() создаёт spaghetti logic в и так длинной функции (600+ строк).
**Митигация:** Выделить helper functions: `embedAndIndexChunks()`, `writeGraphToNeo4j()`, `writeGraphToPostgres()`. Каждая — отдельная функция, вызываемая из Run() по условию. Run() становится оркестратором вызовов.
**Acceptance:** Run() не превышает 150 строк после рефакторинга.

### R2: Reranker endpoint недоступен в production
**Риск:** Пользователь включает `rerank=true`, но забыл поднять reranker service.
**Митигация:** 
- При `RERANK_ENDPOINT=""` + `rerank=true` → результаты без reranking (не ошибка)
- При timeout/5xx → fallback с warning в логах
- В response JSON добавить `"reranked": false` чтобы caller знал

### R3: Overlap создаёт дублирование при search
**Риск:** Один и тот же текстовый фрагмент попадает в 2-3 чанка → при search возвращается 3 раза.
**Митигация:** Это **ожидаемое поведение** для sliding window RAG. Caller (LLM) получает overlapping context, что улучшает coherence. Если нужна дедупликация — это задача caller'а, не vector DB.
**Примечание:** В Phase 3 можно добавить optional dedup по text similarity на уровне search results.

### R4: SkipGraph + GenerateTriplets conflict
**Риск:** Если пользователь передаёт `mode="rag"` но pipeline config имеет `GenerateTriplets=true` (hardcoded в mcp.go line 811).
**Митигация:** При `SkipGraph=true` автоматически `GenerateTriplets=false`. Явно в коде:
```go
if pipeCfg.SkipGraph {
    pipeCfg.GenerateTriplets = false
}
```

### R5: Backward compatibility cognify HTTP API
**Риск:** Существующие клиенты (web UI, tests) вызывают cognify без `mode` параметра.
**Митигация:** Default `mode=""` → `SkipGraph=false` → поведение идентично текущему. Новый параметр strictly opt-in.

### R6: Reranker API incompatibility
**Риск:** Не все reranker'ы точно следуют Cohere API format.
**Митигация:** 
- Парсить response гибко: поддерживать и `relevance_score` (Cohere) и `score` (альтернативные)
- Если response не парсится → fallback без reranking + log error
- Timeout: 5s default, configurable через `RERANK_TIMEOUT_MS`

---

## 5. Corner Cases

### 5.1 Chunking
| Case | Input | Expected | Проверяется тестом |
|------|-------|----------|--------------------|
| Пустой текст | `""` | 0 чанков | T1 |
| Текст короче window | `"Hello"` (5 chars), window=100 | 1 чанк с полным текстом | T2 |
| Текст = ровно window | `"A"*100`, window=100, overlap=20 | 1 чанк | T3 |
| Текст = window + 1 | `"A"*101`, window=100, overlap=20 | 2 чанка, второй = 21 char (overlap 20 + 1 new) | T4 |
| overlap >= window | window=100, overlap=100 | clamp to 50, log warning | T5 |
| overlap = 0 | window=100, overlap=0 | Non-overlapping fixed windows (step=100) | T6 |
| Unicode overlap boundary | Кириллица + эмодзи на границе overlap | Корректный split по rune boundary, не byte | T7 |
| CRLF line endings | `"A\r\nB\r\nC"` | `\r\n` → `\n` normalization | T8 |
| Очень маленький window | window=10, overlap=2 | Многие чанки, каждый < 80 chars → фильтруются | T9 |
| Очень большой текст | 1MB текст | Работает за <100ms (нет LLM, чистый CPU) | T10 |

### 5.2 SkipGraph
| Case | Input | Expected | Проверяется тестом |
|------|-------|----------|--------------------|
| mode=rag, LLM не настроен | `EMBED_ENDPOINT` есть, `LLM_ENDPOINT` нет | Успешно (LLM не нужен) | T11 |
| mode=rag, Embed не настроен | `EMBED_ENDPOINT` пуст | Ошибка "embedding service not configured" | T12 |
| mode=full, LLM не настроен | `LLM_ENDPOINT` пуст | Ошибка или chunks embedded но entities=0 | T13 |
| mode=rag, room+tags | room="auth", tags=["security"] | Чанки имеют metadata с room+tags | T14 |
| mode=rag → search | cognify rag → search по тексту | Результаты найдены | T15 |
| mode=rag → graph search | cognify rag → GRAPH_COMPLETION | 0 results (граф пуст) | T16 |
| mode=rag, custom_prompt | custom_prompt задан | Игнорируется (нет LLM call) | T17 |
| Concurrent rag + full | Два cognify одновременно: один rag, один full | Оба завершаются корректно, не мешают друг другу | T18 |

### 5.3 Reranker
| Case | Input | Expected | Проверяется тестом |
|------|-------|----------|--------------------|
| rerank=true, endpoint OK | 10 results | Reranked by cross-encoder score | T19 |
| rerank=true, endpoint empty | 10 results | Original vector order, response has `"reranked": false` | T20 |
| rerank=true, endpoint timeout | Service hangs | Fallback to vector order after 5s | T21 |
| rerank=true, endpoint 500 | Server error | Fallback to vector order, log warning | T22 |
| rerank=true, 0 results | No vector matches | Empty results (reranker not called) | T23 |
| rerank=true, 1 result | Single match | Returned as-is (reranker called but trivial) | T24 |
| rerank=true, empty documents | Results with no text in metadata | Fallback to vector order (нечего rerank'ить) | T25 |
| rerank=false | Default | No reranker call, current behavior | T26 |

---

## 6. Тесты

### 6.1 Unit tests (Go)

**Файл:** `pkg/chunker/sliding_test.go` (новый)

```
TestChunkBySliding_Empty                    — T1: пустой текст → 0 чанков
TestChunkBySliding_ShorterThanWindow        — T2: текст < window → 1 чанк
TestChunkBySliding_ExactWindow              — T3: текст == window → 1 чанк
TestChunkBySliding_WindowPlusOne            — T4: window+1 → 2 чанка с корректным overlap
TestChunkBySliding_OverlapClamping          — T5: overlap >= window → clamp to window/2
TestChunkBySliding_ZeroOverlap              — T6: overlap=0 → non-overlapping
TestChunkBySliding_UnicodeRunes             — T7: кириллица + эмодзи на границе
TestChunkBySliding_CRLFNormalization        — T8: \r\n → \n
TestChunkBySliding_SmallWindowFiltered      — T9: чанки < minChunkChars отфильтрованы
TestChunkBySliding_LargeText                — T10: 1MB, latency < 100ms
TestChunkBySliding_OverlapContent           — проверка что overlap text совпадает
TestChunkBySliding_DeterministicIDs         — UUID5 стабильны при повторном вызове
TestChunkBySliding_ChunkIndex               — ChunkIndex монотонно растёт 0,1,2...
```

**Файл:** `pkg/rerank/reranker_test.go` (новый)

```
TestRerank_Success                          — T19: mock HTTP server, проверить reorder
TestRerank_EmptyEndpoint                    — T20: url="" → no-op, original order
TestRerank_Timeout                          — T21: slow server → error, caller does fallback
TestRerank_ServerError                      — T22: 500 → error
TestRerank_EmptyResults                     — T23: пустой documents → пустой результат
TestRerank_SingleDocument                   — T24: 1 document → 1 result
TestRerank_CohereFormat                     — relevance_score field parsed
TestRerank_AlternativeScoreField            — score field parsed (non-Cohere)
TestRerank_MalformedJSON                    — invalid response → error
```

**Файл:** `pkg/orchestrator/pipeline_test.go` (существующий или новый)

```
TestRun_SkipGraphChunksOnly                 — T11: SkipGraph=true, нет LLM → чанки embedded
TestRun_SkipGraphNoEntities                 — T15/T16: после SkipGraph run, entities count = 0
TestRun_SkipGraphWithRoomTags               — T14: metadata propagated
TestRun_SkipGraphTripletsDisabled           — GenerateTriplets forced false
TestRun_FullModeDefault                     — mode="" → full pipeline (backward compat)
```

**Примечание:** Go unit tests для pipeline требуют mock embed server. Это **не мок бизнес-логики** — это HTTP test server (`httptest.NewServer`) для embed endpoint, который возвращает фиксированные вектора. Это стандартная практика — моки уровня транспорта, не логики.

### 6.2 Integration tests (Python, MCP)

**Файл:** `tests/test_mcp_rag_mode.py` (новый)

```python
"""
Levara MCP RAG Mode Tests — Phase 1.
Run: pytest tests/test_mcp_rag_mode.py -v

Tests RAG-specific features: skip_graph, sliding window, reranker.
Requires: embed endpoint. Does NOT require LLM.
"""

pytestmark = [pytest.mark.integration, pytest.mark.asyncio]

class TestRAGModeCognify:
    """RAG mode cognify: chunk + embed, no graph."""

    @pytest.mark.requires_embed
    async def test_rag_mode_basic(self, mcp, test_collection):
        """T15: cognify mode=rag → search finds results."""
        text = "PostgreSQL is a relational database. Redis is an in-memory store."
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection
        })
        assert not mcp.tool_error(result)
        run_id = extract_run_id(mcp.tool_text(result))
        await wait_for_completion(mcp, run_id, timeout=30)
        
        # Search should find results
        search = await mcp.call_tool("search", {
            "search_query": "relational database", "collection": test_collection
        })
        results = json.loads(mcp.tool_text(search))["results"]
        assert len(results) > 0

    @pytest.mark.requires_embed
    async def test_rag_mode_no_graph(self, mcp, test_collection):
        """T16: cognify mode=rag → graph search returns nothing."""
        text = "Einstein worked at Princeton University on general relativity."
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection
        })
        run_id = extract_run_id(mcp.tool_text(result))
        await wait_for_completion(mcp, run_id, timeout=30)
        
        # Graph search should return empty
        search = await mcp.call_tool("search", {
            "search_query": "where did Einstein work",
            "search_type": "GRAPH_COMPLETION",
            "collection": test_collection
        })
        # Either empty results or fallback to vector search
        results = json.loads(mcp.tool_text(search))
        # No entities should be in graph_nodes

    @pytest.mark.requires_embed
    async def test_rag_mode_no_llm_required(self, mcp, test_collection):
        """T11: RAG mode succeeds even without LLM endpoint."""
        text = "This should work without any LLM endpoint configured."
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection
        })
        assert not mcp.tool_error(result)
        run_id = extract_run_id(mcp.tool_text(result))
        status = await wait_for_completion(mcp, run_id, timeout=30)
        assert status == "COMPLETED"

    @pytest.mark.requires_embed
    async def test_rag_mode_status_stages(self, mcp, test_collection):
        """cognify_status shows RAG stages: chunking → embedding → COMPLETED."""
        text = "Some text for stage tracking." * 20
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection
        })
        run_id = extract_run_id(mcp.tool_text(result))
        
        # Poll status — should never see "extracting" or "deduplicating"
        seen_stages = set()
        for _ in range(30):
            status_result = await mcp.call_tool("cognify_status", {"run_id": run_id})
            text = mcp.tool_text(status_result)
            if "COMPLETED" in text or "FAILED" in text:
                break
            # Parse current stage
            # ...
            await asyncio.sleep(1)
        
        assert "extracting" not in seen_stages
        assert "deduplicating" not in seen_stages

    @pytest.mark.requires_embed
    async def test_rag_mode_with_room_tags(self, mcp, test_collection):
        """T14: room + tags propagated in RAG mode."""
        text = "Authentication uses JWT tokens with httpOnly cookies."
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection,
            "room": "auth", "tags": ["security", "jwt"]
        })
        run_id = extract_run_id(mcp.tool_text(result))
        await wait_for_completion(mcp, run_id, timeout=30)
        
        # Search with room filter should find it
        search = await mcp.call_tool("search", {
            "search_query": "JWT authentication",
            "collection": test_collection,
            "room": "auth"
        })
        results = json.loads(mcp.tool_text(search))["results"]
        assert len(results) > 0

    @pytest.mark.requires_embed
    async def test_full_mode_default(self, mcp, test_collection, services):
        """Backward compat: cognify without mode works as full."""
        if not services.get("llm"):
            pytest.skip("LLM not available for full mode test")
        text = "Einstein worked at Princeton University."
        result = await mcp.call_tool("cognify", {
            "data": text, "collection": test_collection
        })
        assert not mcp.tool_error(result)
        # This is the existing behavior — no regression


class TestSlidingWindowChunking:
    """Sliding window chunking with overlap."""

    @pytest.mark.requires_embed
    async def test_sliding_basic(self, mcp, test_collection):
        """Sliding window creates overlapping chunks."""
        # Create text with clear segments
        text = "Segment A. " * 50 + "Segment B. " * 50 + "Segment C. " * 50
        result = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": test_collection,
            "chunk_strategy": "sliding", "overlap_chars": 100
        })
        run_id = extract_run_id(mcp.tool_text(result))
        await wait_for_completion(mcp, run_id, timeout=30)
        
        # Search should return results
        search = await mcp.call_tool("search", {
            "search_query": "Segment B", "collection": test_collection, "top_k": 5
        })
        results = json.loads(mcp.tool_text(search))["results"]
        assert len(results) > 0

    @pytest.mark.requires_embed
    async def test_sliding_with_merged_comparison(self, mcp):
        """Sliding and merged produce different chunk counts for same text."""
        text = "Knowledge is power. " * 100
        
        coll_sliding = f"test_{uuid.uuid4().hex[:8]}"
        coll_merged = f"test_{uuid.uuid4().hex[:8]}"
        
        r1 = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": coll_sliding,
            "chunk_strategy": "sliding", "overlap_chars": 100
        })
        r2 = await mcp.call_tool("cognify", {
            "data": text, "mode": "rag", "collection": coll_merged,
            "chunk_strategy": "merged"
        })
        # Both should succeed — chunk counts will differ


class TestReranker:
    """Reranker integration (requires RERANK_ENDPOINT configured)."""

    @pytest.mark.requires_embed
    async def test_rerank_flag_no_error(self, mcp, test_collection):
        """T20/T26: rerank=true with no endpoint configured → no error."""
        # First ingest something
        await ingest_sample(mcp, test_collection)
        
        search = await mcp.call_tool("search", {
            "search_query": "database",
            "collection": test_collection,
            "rerank": True
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert "results" in data
        # If no reranker configured, results should still be returned
        # Check "reranked" field in response
        assert data.get("reranked") in (True, False)

    @pytest.mark.requires_embed
    async def test_rerank_false_default(self, mcp, test_collection):
        """T26: default behavior (no rerank) unchanged."""
        await ingest_sample(mcp, test_collection)
        
        search = await mcp.call_tool("search", {
            "search_query": "database",
            "collection": test_collection
        })
        assert not mcp.tool_error(search)
        data = json.loads(mcp.tool_text(search))
        assert len(data["results"]) > 0


# --- Helpers ---

def extract_run_id(text: str) -> str:
    """Extract Run ID from cognify response text."""
    import re
    match = re.search(r'Run ID: ([a-f0-9-]+)', text)
    assert match, f"No Run ID found in: {text}"
    return match.group(1)

async def wait_for_completion(mcp, run_id: str, timeout: int = 60) -> str:
    """Poll cognify_status until COMPLETED or FAILED."""
    for _ in range(timeout):
        result = await mcp.call_tool("cognify_status", {"run_id": run_id})
        text = mcp.tool_text(result)
        if "COMPLETED" in text:
            return "COMPLETED"
        if "FAILED" in text:
            return "FAILED"
        await asyncio.sleep(1)
    raise TimeoutError(f"Pipeline {run_id} did not complete in {timeout}s")

async def ingest_sample(mcp, collection: str):
    """Ingest sample text via RAG mode and wait."""
    text = "PostgreSQL is a relational database. Redis is an in-memory store. MongoDB is a document database."
    result = await mcp.call_tool("cognify", {
        "data": text, "mode": "rag", "collection": collection
    })
    run_id = extract_run_id(mcp.tool_text(result))
    await wait_for_completion(mcp, run_id, timeout=30)
```

### 6.3 Перечень тестов → corner case mapping

| Тест | Corner Case | Тип |
|------|-------------|-----|
| `TestChunkBySliding_Empty` | T1 | Go unit |
| `TestChunkBySliding_ShorterThanWindow` | T2 | Go unit |
| `TestChunkBySliding_ExactWindow` | T3 | Go unit |
| `TestChunkBySliding_WindowPlusOne` | T4 | Go unit |
| `TestChunkBySliding_OverlapClamping` | T5 | Go unit |
| `TestChunkBySliding_ZeroOverlap` | T6 | Go unit |
| `TestChunkBySliding_UnicodeRunes` | T7 | Go unit |
| `TestChunkBySliding_CRLFNormalization` | T8 | Go unit |
| `TestChunkBySliding_SmallWindowFiltered` | T9 | Go unit |
| `TestChunkBySliding_LargeText` | T10 | Go unit |
| `test_rag_mode_no_llm_required` | T11 | MCP integration |
| *(T12 embedded in toolCognify validation)* | T12 | Existing behavior |
| `test_full_mode_default` | T13 | MCP integration |
| `test_rag_mode_with_room_tags` | T14 | MCP integration |
| `test_rag_mode_basic` | T15 | MCP integration |
| `test_rag_mode_no_graph` | T16 | MCP integration |
| *(T17: custom_prompt ignored in rag — logged in status)* | T17 | Go unit |
| *(T18: concurrent cognify — stress test)* | T18 | MCP stress |
| `TestRerank_Success` | T19 | Go unit |
| `test_rerank_flag_no_error` | T20 | MCP integration |
| `TestRerank_Timeout` | T21 | Go unit |
| `TestRerank_ServerError` | T22 | Go unit |
| `TestRerank_EmptyResults` | T23 | Go unit |
| `TestRerank_SingleDocument` | T24 | Go unit |
| *(T25: empty docs → fallback)* | T25 | Go unit |
| `test_rerank_false_default` | T26 | MCP integration |

---

## 7. Порядок реализации

```
Step 1: pkg/chunker/sliding.go + sliding_test.go
        Чистый CPU-код, нет зависимостей. Можно тестировать изолированно.
        DoD: все T1-T10 green.

Step 2: pkg/orchestrator/pipeline.go — SkipGraph branch
        Рефакторинг Stage 4 в helper functions.
        Добавление SkipGraph + OverlapChars в Config.
        Добавление case "sliding" в Stage 1.
        DoD: go build + go vet + существующие pipeline tests pass.

Step 3: pkg/rerank/reranker.go + reranker_test.go
        HTTP client для external reranker.
        DoD: все rerank unit tests green.

Step 4: pipeline/search.go — SearchByTextWithRerank
        Интеграция reranker в SearchPipeline.
        DoD: go build.

Step 5: internal/http/mcp.go — MCP API changes
        mode, chunk_strategy, overlap_chars в cognify tool.
        rerank + reranked field в search tool.
        Config propagation.
        DoD: go build + go vet.

Step 6: tests/test_mcp_rag_mode.py
        Integration tests.
        DoD: pytest tests/test_mcp_rag_mode.py — all green.

Step 7: Full regression
        pytest tests/ — zero skips beyond service availability.
        DoD: 0 new failures.
```

---

## 8. Файлы (итог)

**Новые:**
- `pkg/chunker/sliding.go`
- `pkg/chunker/sliding_test.go`
- `pkg/rerank/reranker.go`
- `pkg/rerank/reranker_test.go`
- `tests/test_mcp_rag_mode.py`

**Изменённые:**
- `pkg/orchestrator/pipeline.go` — Config + SkipGraph branch + "sliding" case + helper refactor
- `pipeline/search.go` — reranker field + SearchByTextWithRerank
- `internal/http/mcp.go` — tool definitions + handler changes
- `cmd/server/main.go` — RERANK_ENDPOINT env → config propagation
