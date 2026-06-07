# Phase 1B: RAG Quality — дополнения к Phase 1

**Цель:** Довести RAG-режим до production quality. Phase 1 дала базу (skip_graph, sliding window, reranker). Phase 1B закрывает 6 пробелов: parent-child chunks, result dedup, contextual headers, multi-query retrieval, sentence-aware boundaries, chunk document metadata.

**Принцип:** Никаких моков, заглушек, TODO. Каждый пункт — рабочий код с тестами.

---

## 1. Scope

| # | Фича | Файлы | Описание |
|---|-------|-------|----------|
| 1B.1 | Parent-child chunks | `pkg/chunker/types.go`, `pkg/chunker/parent_child.go` (новый), `pipeline.go` | Мелкие чанки для поиска (child), крупные для контекста (parent). Dual collection: `_child` для search, основная для retrieval |
| 1B.2 | Search result dedup | `pipeline/search.go`, `internal/http/mcp.go` | Дедупликация по text similarity: overlap chunks → remove near-duplicates |
| 1B.3 | Contextual chunk headers | `pkg/chunker/types.go`, `pkg/orchestrator/pipeline.go` | Prepend document title + section header к каждому чанку перед embedding |
| 1B.4 | Multi-query retrieval | `pipeline/search.go` (новый метод) | Генерация 2-3 query variants через LLM → search each → RRF merge |
| 1B.5 | Sentence-aware sliding window | `pkg/chunker/sliding.go` | Не резать слова/предложения: snap window boundary к nearest sentence end |
| 1B.6 | Chunk document metadata | `pkg/chunker/types.go`, `pkg/orchestrator/pipeline.go`, `mcp.go` | document_id, document_title, section в chunk metadata для attribution |

---

## 2. Детальный дизайн

### 2.1 Parent-Child Chunks

Ключевая идея: **маленькие чанки для точного поиска, большие для полного контекста.**

**Стратегия:** Два уровня чанков из одного текста:
- **Parent** (2000 chars, merged paragraphs) — записывается в основную коллекцию
- **Child** (256 chars, sentence-based) — записывается в `{collection}_child` коллекцию. Метаданные содержат `parent_id`.

При search:
1. Ищем по `_child` коллекции (высокая precision)
2. Для каждого найденного child → lookup parent_id
3. Возвращаем parent chunk (полный контекст)
4. Dedup parents (один parent может иметь несколько matching children)

**Chunk struct расширение** (`pkg/chunker/types.go`):

```go
type Chunk struct {
    ID           string `json:"id"`
    Text         string `json:"text"`
    Chapter      int    `json:"chapter"`
    ChunkIndex   int    `json:"chunk_index"`
    CutType      string `json:"cut_type,omitempty"`
    ParentID     string `json:"parent_id,omitempty"`     // ID of parent chunk (for child chunks)
    DocumentID   string `json:"document_id,omitempty"`   // source document identifier
    DocumentTitle string `json:"document_title,omitempty"` // document title/filename
    Section      string `json:"section,omitempty"`       // section header (if detected)
}
```

**Новый файл:** `pkg/chunker/parent_child.go`

```go
// ChunkParentChild creates a two-level chunking hierarchy.
// Parents: large merged paragraphs (parentMaxChars).
// Children: small sentence-based chunks (childMaxChars) within each parent.
// Each child has ParentID set to the parent's ID.
//
// Returns (parents, children).
func ChunkParentChild(text string, parentMaxChars, childMaxChars int, documentID string) (parents []Chunk, children []Chunk)
```

**Алгоритм:**
1. Создать parents через `ChunkByParagraphMerged(text, 80, parentMaxChars, documentID)` 
2. Для каждого parent → создать children через `ChunkBySentence(parent.Text, 30, childMaxChars, parent.ID)`
3. Каждый child получает `ParentID = parent.ID`
4. Child IDs: `UUID5(parent.ID + "-child-" + childIndex)`

**Pipeline integration** (`pipeline.go`):

Когда `cfg.ParentChild = true`:
- Embed parents в основную коллекцию (metadata: `{"text":"...", "type":"parent", ...}`)
- Embed children в `{collection}_child` (metadata: `{"text":"...", "parent_id":"...", "type":"child", ...}`)
- BM25 index update для обеих коллекций

**Search integration** (`pipeline/search.go`):

Новый метод:
```go
// SearchByTextParentChild searches child collection for precision,
// then resolves parent chunks for full context.
// Returns parent chunks (deduplicated) ordered by best child match score.
func (p *SearchPipeline) SearchByTextParentChild(ctx context.Context, collection, queryText string, limit int) ([]ScoredResult, error)
```

**MCP API:** Новый параметр `parent_child` в cognify:
```json
{"parent_child": {"type": "boolean", "default": false, 
  "description": "Enable parent-child chunking. Small chunks for search precision, large chunks for context."}}
```

Новый параметр `parent_child` в search (автоматический если коллекция имеет `_child` вариант):
```json
{"parent_child": {"type": "boolean", "default": false,
  "description": "Search child chunks, return parent chunks for context."}}
```

---

### 2.2 Search Result Dedup

При sliding window overlap или parent-child search — дубли неизбежны.

**Файл:** `pipeline/search.go`

```go
// DeduplicateResults removes near-duplicate results by text similarity.
// Uses Jaccard similarity on word sets with threshold (default 0.85).
// Keeps the result with higher score from each duplicate group.
func DeduplicateResults(results []ScoredResult, threshold float64) []ScoredResult
```

**Алгоритм (Jaccard на word sets):**
1. Для каждого result → extract text из metadata → tokenize (split by whitespace + lowercase)
2. Для каждой пары: `jaccard = |intersection| / |union|`
3. Если jaccard > threshold → keep higher-scored, drop lower
4. O(n²) но n = topK (обычно 10-30), так что ~100-900 сравнений

**Почему Jaccard, а не cosine на embeddings:** Embeddings уже вычислены для search, но сравнение embeddings не ловит literal overlap (sliding window chunks идентичны в overlap zone). Jaccard на словах — дешёвый exact-overlap detector.

**Интеграция:** В `toolSearch` handler, после metadata filter, перед return:
```go
if len(results) > topK {
    results = DeduplicateResults(results, 0.85)
}
```

---

### 2.3 Contextual Chunk Headers

**Проблема:** Chunk "Refresh tokens rotated via Redis with TTL" без контекста не связывается с "authentication". Embedding не знает что это про auth.

**Решение:** Prepend document title + detected section header к chunk text **перед** embedding:

```
[Document: MyApp Architecture Guide]
[Section: Authentication]
Refresh tokens rotated via Redis with TTL. Access tokens stored in httpOnly cookies.
```

**Файл:** `pkg/chunker/types.go` — Chunk struct расширен (см. 2.1).

**Section detection** (`pkg/chunker/paragraph.go` — расширение):

```go
// DetectSections scans text for Markdown-style headers (## Section Name),
// HTML headers (<h2>), or title-case lines followed by blank lines.
// Returns map: character offset → section name.
func DetectSections(text string) []SectionBoundary

type SectionBoundary struct {
    Offset int
    Name   string
}
```

Regex patterns:
- `^#{1,4}\s+(.+)$` — Markdown headers
- `^[A-ZА-ЯЁ][A-Za-zА-Яа-яЁё ]{2,60}$` followed by `\n\n` — title-case lines (Chapter, Section names)
- `^Глава \d+` — existing chapter detection (already in code)

**Pipeline integration:**

В Stage 1 (chunking), после создания chunks:
```go
// Detect sections in original text
sections := chunker.DetectSections(text)

// Assign section to each chunk based on character offset
for i := range chunks {
    chunks[i].Section = findSection(sections, chunkOffset(chunks, i))
    chunks[i].DocumentTitle = cfg.DocumentTitle
    chunks[i].DocumentID = docID
}
```

В Stage 4b (embedding), modify metadata формат:
```go
// Prepend contextual header to chunk text before embedding
embeddableText := chunkTexts[i]
if chunk.DocumentTitle != "" {
    embeddableText = "[Document: " + chunk.DocumentTitle + "]\n" + embeddableText
}
if chunk.Section != "" {
    embeddableText = "[Section: " + chunk.Section + "]\n" + embeddableText
}

// Embed with contextual header, but store original text in metadata
```

**Ключевое:** Embedding делается по тексту С header, но в metadata.text хранится ОРИГИНАЛЬНЫЙ текст (без header). Это потому что при retrieval пользователю нужен чистый текст, а embedding должен знать контекст.

---

### 2.4 Multi-Query Retrieval

**Проблема:** Один query может не покрыть все формулировки. "How does auth work?" не найдёт чанк "JWT tokens stored in cookies".

**Решение:** Генерация 2-3 вариантов query через LLM → search каждый → RRF merge.

**Файл:** `pipeline/search.go`

```go
// SearchByTextMultiQuery generates query variants via LLM,
// searches each variant, and merges results via Reciprocal Rank Fusion.
// If LLM is nil, falls back to single-query SearchByText.
// maxVariants: max LLM-generated variants (default 3, capped at 5).
func (p *SearchPipeline) SearchByTextMultiQuery(
    ctx context.Context, collection, queryText string, limit int,
    llmProvider llm.Provider, maxVariants int,
) ([]ScoredResult, error)
```

**LLM prompt:**
```
Generate 3 alternative phrasings of this search query that might match different relevant documents.
Return ONLY a JSON array of strings, no explanation.

Original query: "How does authentication work?"

Alternative queries:
```

**Expected LLM response:**
```json
["JWT token validation process", "login security cookies session", "auth middleware implementation"]
```

**RRF merge:** Reuse existing `bm25.HybridSearch` pattern (Reciprocal Rank Fusion, k=60).

**Fallback:** LLM nil или error → single-query search (no degradation).

**MCP integration:** Новый параметр `multi_query` в search:
```json
{"multi_query": {"type": "boolean", "default": false,
  "description": "Generate query variants via LLM for broader retrieval. Requires LLM."}}
```

---

### 2.5 Sentence-Aware Sliding Window

**Проблема:** `ChunkBySliding` режет по символам: `"...stored in httpOnly co"` + `"okies for security..."`. Слова рвутся.

**Решение:** После вычисления window boundary — snap к ближайшему концу предложения.

**Файл:** `pkg/chunker/sliding.go` — модификация:

```go
// ChunkBySliding splits text into fixed-size windows with overlap.
// If snapToSentence is true, window boundaries snap to nearest sentence end
// (. ! ? followed by space) within ±20% of target position.
func ChunkBySliding(text string, windowChars, overlapChars int, documentID string) []Chunk
```

**Алгоритм snap:**
1. Вычислить target end position: `pos + windowChars`
2. Найти ближайший sentence end в range `[target - 20%, target + 20%]`
3. Если найден — snap к нему
4. Если не найден (длинное слово/код) — snap к ближайшему пробелу
5. Если и пробела нет — оставить как есть (edge case: URL, хеш)

Добавить в Config:
```go
type Config struct {
    // ...existing...
    SnapToSentence bool // For sliding window: snap boundaries to sentence ends
}
```

MCP:
```json
{"snap_to_sentence": {"type": "boolean", "default": true,
  "description": "Snap sliding window boundaries to sentence ends (avoids cutting words)."}}
```

Default `true` — по умолчанию snap включен. Это безопаснее чем резать слова.

---

### 2.6 Chunk Document Metadata

**Проблема:** Чанки не знают из какого документа они пришли. Нельзя ответить "из какого файла эта информация?" и нельзя фильтровать по source.

**Pipeline Config расширение:**
```go
type Config struct {
    // ...existing...
    DocumentTitle string // Human-readable document title (filename, heading)
    DocumentID    string // Stable document identifier (for dedup/update)
}
```

**MCP cognify tool расширение:**
```json
{
    "document_title": {"type": "string", "description": "Document title for attribution (shown in search results)"},
    "document_id": {"type": "string", "description": "Stable document ID for dedup (re-cognify replaces previous chunks)"}
}
```

**Metadata format update** (pipeline.go):
```go
meta := fmt.Sprintf(`{"text":%s,"dataset_id":"%s","document_id":"%s","document_title":"%s","section":"%s","room":%s,"tags":%s}`,
    mustJSON(chunkTexts[i]), cfg.DatasetID,
    mustJSON(cfg.DocumentID), mustJSON(cfg.DocumentTitle),
    mustJSON(chunk.Section),
    mustJSON(cfg.Room), tagsJSON)
```

**Search response enrichment:** metadata уже содержит document_title — caller видит attribution.

---

## 3. Definition of Done (DoD)

### 3.1 Parent-Child Chunks
- [ ] `cognify(parent_child=true)` создаёт 2 коллекции: `{name}` (parents) и `{name}_child` (children)
- [ ] Children имеют `parent_id` в metadata, указывающий на parent chunk ID
- [ ] `search(parent_child=true)` ищет по children, возвращает parents
- [ ] Parents дедуплицированы (один parent при нескольких matching children)
- [ ] Parent text содержит полный контекст (2000 chars default)
- [ ] Child text — sentence-level precision (256 chars default)
- [ ] `search(parent_child=false)` — поведение как раньше (backward compat)
- [ ] Коллекция без `_child` варианта + `parent_child=true` → fallback к обычному search

### 3.2 Result Dedup
- [ ] Sliding window search с overlap → дубли удалены (Jaccard > 0.85)
- [ ] Dedup сохраняет результат с наивысшим score
- [ ] Dedup не удаляет non-overlapping результаты (разный текст)
- [ ] topK=10 после dedup реально возвращает 10 уникальных (overfetch до dedup)
- [ ] Пустые/missing text в metadata → не crash, skip dedup для этого result

### 3.3 Contextual Headers
- [ ] Section detection: Markdown headers (`## Auth`), title-case lines
- [ ] Chunk embedding text = `[Document: title]\n[Section: name]\nchunk text`
- [ ] Chunk metadata.text = ОРИГИНАЛЬНЫЙ текст (без headers)
- [ ] Search "authentication" находит chunk про "JWT tokens" в auth section
- [ ] Без document_title → embedding без `[Document:]` prefix
- [ ] Без section → embedding без `[Section:]` prefix

### 3.4 Multi-Query Retrieval
- [ ] `search(multi_query=true)` генерирует 3 variants + original → 4 searches
- [ ] RRF merge: results ranked by fused score
- [ ] LLM nil → fallback к single query (не ошибка)
- [ ] LLM returns invalid JSON → fallback к single query
- [ ] LLM returns >5 variants → cap at 5
- [ ] Retrieval recall выше чем single query (на тестовых данных)

### 3.5 Sentence-Aware Sliding
- [ ] Default: snap_to_sentence=true → чанки не режут слова
- [ ] Boundary snaps к . ! ? в пределах ±20% от target
- [ ] Если нет sentence end → snap к пробелу
- [ ] Если нет пробела → hard cut (URL, hash)
- [ ] Существующие sliding tests проходят
- [ ] Новые тесты: sentence boundary корректно найден

### 3.6 Document Metadata
- [ ] `cognify(document_title="My Doc")` → все чанки содержат document_title в metadata
- [ ] `cognify(document_id="doc-123")` → все чанки содержат document_id
- [ ] Search results содержат document_title для attribution
- [ ] Без document_title → metadata не содержит пустой document_title field

---

## 4. Риски и митигации

### R1: Parent-child удваивает vector storage
**Риск:** Каждый документ → parents + children. Двойной расход памяти и embed calls.
**Митигация:** parent_child=false по умолчанию. Включается explicitly. При включении — accept trade-off: качество search vs storage. Children обычно меньше (256 chars) → embed дешевле.

### R2: Section detection — false positives
**Риск:** "Chapter 1. The Beginning" detected, но "Redis has features:" — нет.
**Митигация:** Conservative regex: только Markdown headers + lines followed by \n\n. Лучше пропустить section чем hallucinate.

### R3: Multi-query → 4x embed calls + 4x search
**Риск:** Latency grows 4x при multi_query=true.
**Митигация:** 
- Parallel embed (batch all variants in one call)
- Parallel search (goroutines)
- Real latency: embed 1 batch ~5ms + 4 searches ~1ms each = ~9ms vs ~6ms single
- Основной cost — LLM call для variant generation (~500ms). Для latency-sensitive → multi_query=false.

### R4: Jaccard dedup — false negatives
**Риск:** Два чанка с разными словами но одинаковым смыслом — не dedup'аются.
**Митигация:** Jaccard решает конкретную проблему: sliding window overlap (literal text overlap). Semantic dedup — другая задача, решается reranker'ом.

### R5: Contextual headers + embedding dimensions
**Риск:** Prepending "[Document: X]\n[Section: Y]\n" изменяет embedding чанка. Чанки без headers не совместимы с чанками с headers.
**Митигация:** Все чанки в одном cognify run обрабатываются одинаково. Не смешивать: один collection → один формат. При включении headers — re-embed (reembed endpoint уже существует).

### R6: Parent-child search latency
**Риск:** Search children → lookup parents → dedup → return. Дополнительные SQL/map lookups.
**Митигация:** Parent lookup — по ID из metadata (O(1) hash map). Dedup parents — set по ID. Общий overhead: <1ms.

---

## 5. Corner Cases

### 5.1 Parent-Child Chunks
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Пустой текст | "" | 0 parents, 0 children | T1 |
| Короткий текст (< child size) | "Hello" | 1 parent, 1 child (same text) | T2 |
| 1 parent, multiple children | 2000 char paragraph | 1 parent, ~8 children | T3 |
| Multiple parents | Long document | N parents, M*N children (M per parent) | T4 |
| Child parent_id resolves | search child → parent lookup | Parent found, text matches | T5 |
| Multiple children match same parent | 3 children in same parent match | 1 parent returned (deduped) | T6 |
| parent_child=false | Default | No _child collection created | T7 |
| Collection without _child + search parent_child=true | Legacy data | Fallback to normal search | T8 |

### 5.2 Result Dedup
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| No duplicates | 10 unique results | 10 results unchanged | T9 |
| 2 identical texts | Same chunk from overlap | 1 result (higher score kept) | T10 |
| 85% overlap | Sliding window chunks | Deduped | T11 |
| 84% overlap | Different but similar | Both kept (below threshold) | T12 |
| Empty text in metadata | Result without text field | Kept (can't dedup) | T13 |
| All duplicates | 10 copies of same text | 1 result | T14 |

### 5.3 Contextual Headers
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Markdown ## headers | "## Auth\nJWT tokens..." | Section="Auth" | T15 |
| Title-case + blank line | "Authentication\n\nJWT..." | Section="Authentication" | T16 |
| No sections detected | Plain text | Section="" | T17 |
| Multiple sections | Text with 3 headers | Each chunk gets correct section | T18 |
| Кириллические headers | "## Аутентификация" | Section="Аутентификация" | T19 |
| Document title provided | cognify(document_title="X") | All chunks: DocumentTitle="X" | T20 |
| Embedding includes header | "[Document: X]\n..." | Higher relevance for "X" queries | T21 |

### 5.4 Multi-Query
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| LLM available | "auth" → 3 variants | 4 searches merged via RRF | T22 |
| LLM nil | multi_query=true, no LLM | Fallback single query | T23 |
| LLM returns bad JSON | Invalid response | Fallback single query | T24 |
| LLM returns >5 variants | 8 variants | Cap at 5 | T25 |
| LLM returns 0 variants | Empty array | Use original query only | T26 |
| Improved recall | Same query, multi vs single | Multi finds more relevant | T27 |

### 5.5 Sentence-Aware Sliding
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Sentence end in range | "Hello world. Next..." | Snap to "." | T28 |
| No sentence end in range | Long word/URL | Snap to space | T29 |
| No space in range | "aaaaaa...aaaa" | Hard cut at window | T30 |
| Multiple sentence ends | "A. B. C. D." | Snap to nearest to target | T31 |
| Unicode sentence ends | "Привет! Мир." | Correct snap | T32 |
| snap_to_sentence=false | Explicit disable | Pure character cut (old behavior) | T33 |

### 5.6 Document Metadata
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| document_title provided | cognify(document_title="X") | metadata has document_title | T34 |
| document_id provided | cognify(document_id="Y") | metadata has document_id | T35 |
| Neither provided | cognify(data="...") | No document fields in metadata | T36 |
| Search result attribution | search after cognify with title | Results contain title | T37 |

---

## 6. Тесты

### 6.1 Go unit: Parent-Child (`pkg/chunker/parent_child_test.go`)

```
TestChunkParentChild_Empty                 — T1
TestChunkParentChild_ShortText             — T2
TestChunkParentChild_SingleParent          — T3
TestChunkParentChild_MultipleParents       — T4
TestChunkParentChild_ChildHasParentID      — T5
TestChunkParentChild_DeterministicIDs      — IDs stable across calls
TestChunkParentChild_ChildTextInParent     — every child text is substring of its parent
```

### 6.2 Go unit: Result Dedup (`pipeline/dedup_test.go`)

```
TestDedup_NoDuplicates                     — T9
TestDedup_IdenticalTexts                   — T10
TestDedup_HighOverlap                      — T11
TestDedup_BelowThreshold                   — T12
TestDedup_EmptyMetadata                    — T13
TestDedup_AllDuplicates                    — T14
TestDedup_KeepsHigherScore                 — verify higher score survives
TestJaccardSimilarity                      — unit test on word-set similarity
```

### 6.3 Go unit: Section Detection (`pkg/chunker/section_test.go`)

```
TestDetectSections_MarkdownH2              — T15
TestDetectSections_TitleCase               — T16
TestDetectSections_NoSections              — T17
TestDetectSections_MultipleSections        — T18
TestDetectSections_Cyrillic                — T19
TestDetectSections_MixedFormats            — Markdown + title-case
```

### 6.4 Go unit: Multi-Query (`pipeline/multi_query_test.go`)

```
TestMultiQuery_WithLLM                     — mock LLM returns 3 variants
TestMultiQuery_NoLLM                       — T23: fallback single
TestMultiQuery_BadJSON                     — T24: fallback single
TestMultiQuery_TooManyVariants             — T25: cap at 5
TestMultiQuery_EmptyVariants               — T26: original only
TestMultiQuery_RRFMerge                    — verify rank fusion
```

### 6.5 Go unit: Sentence-Aware Sliding (`pkg/chunker/sliding_test.go` — дополнение)

```
TestChunkBySliding_SnapToSentence          — T28
TestChunkBySliding_SnapToSpace             — T29
TestChunkBySliding_HardCutNoSpace          — T30
TestChunkBySliding_NearestSentenceEnd      — T31
TestChunkBySliding_UnicodeSnap             — T32
TestChunkBySliding_SnapDisabled            — T33: old behavior
```

### 6.6 Python MCP integration (`tests/test_mcp_rag_quality.py`)

```python
class TestParentChildChunks:
    test_parent_child_cognify               — creates _child collection
    test_parent_child_search                — search returns parent text
    test_parent_child_dedup_parents         — T6: multiple children → 1 parent
    test_parent_child_disabled_default      — T7: default = no _child

class TestContextualHeaders:
    test_document_title_in_results          — T37: attribution
    test_section_detection_improves_search  — T21: better relevance
    test_no_title_no_headers                — T36: no errors

class TestMultiQuery:
    test_multi_query_search                 — T22: broader results
    test_multi_query_no_llm_fallback        — T23: graceful

class TestResultDedup:
    test_sliding_window_no_duplicates       — overlap search → unique results
    test_dedup_preserves_relevance          — best score kept

class TestSentenceAwareSliding:
    test_sliding_no_broken_words            — T28: words intact
```

---

## 7. Порядок реализации

```
Step 1: pkg/chunker/types.go — расширить Chunk struct
        +ParentID, +DocumentID, +DocumentTitle, +Section
        DoD: go build.

Step 2: pkg/chunker/sliding.go — sentence-aware snap
        Модифицировать ChunkBySliding: snap to sentence/word boundary.
        +SnapToSentence parameter.
        DoD: T28-T33 green + existing sliding tests pass.

Step 3: pkg/chunker/parent_child.go + parent_child_test.go
        ChunkParentChild function.
        DoD: T1-T8 green.

Step 4: pkg/chunker/section.go + section_test.go
        DetectSections function.
        DoD: T15-T19 green.

Step 5: pipeline/dedup.go + dedup_test.go
        DeduplicateResults + JaccardSimilarity.
        DoD: T9-T14 green.

Step 6: pipeline/search.go — MultiQuery + ParentChild search
        SearchByTextMultiQuery, SearchByTextParentChild.
        DoD: T22-T27 green.

Step 7: pkg/orchestrator/pipeline.go — contextual headers + document metadata + parent_child
        Stage 1: section detection, document metadata.
        Stage 4b: contextual header prepend, parent/child dual embed.
        DoD: go build.

Step 8: internal/http/mcp.go — MCP API
        cognify: parent_child, document_title, document_id, snap_to_sentence.
        search: parent_child, multi_query, dedup integration.
        DoD: go build + go vet.

Step 9: tests/test_mcp_rag_quality.py
        Integration tests.
        DoD: all green.

Step 10: Full Phase 1 regression
         Phase 1 + Phase 1B + smoke + palace.
         DoD: 0 new failures.
```

---

## 8. Файлы

**Новые:**
- `pkg/chunker/parent_child.go` + `parent_child_test.go`
- `pkg/chunker/section.go` + `section_test.go`
- `pipeline/dedup.go` + `dedup_test.go`
- `pipeline/multi_query.go` + `multi_query_test.go`
- `tests/test_mcp_rag_quality.py`

**Изменённые:**
- `pkg/chunker/types.go` — Chunk struct расширение
- `pkg/chunker/sliding.go` — sentence-aware snap
- `pkg/chunker/sliding_test.go` — +6 snap tests
- `pipeline/search.go` — SearchByTextParentChild, SearchByTextMultiQuery
- `pkg/orchestrator/pipeline.go` — contextual headers, document metadata, parent-child
- `internal/http/mcp.go` — new parameters
