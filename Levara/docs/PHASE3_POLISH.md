# Phase 3: Production Polish — Implementation Plan

**Цель:** Закрыть все оставшиеся архитектурные пробелы между Levara и production-ready knowledge engine: unified search mode, graph-aware reranking, adaptive router с feedback loop, embedding drift detection, graph pruning, и configurable parameters. После Phase 3 Levara — self-tuning система с полным lifecycle management.

**Принцип:** Никаких моков, заглушек, TODO. Каждый пункт — рабочий код с тестами.

---

## 1. Scope

| # | Фича | Файлы | Описание |
|---|-------|-------|----------|
| 3.1 | Unified search mode | `mcp.go`, `pipeline/search.go` | Параметр `mode` в search: rag (vector only), graph (graph only), full (both), auto (router decides) |
| 3.2 | Graph-aware reranking | `pkg/graphrank/graphrank.go` (новый), `pipeline/search.go` | Hybrid score = α·vector + β·graph_proximity. Graph proximity через hop distance в graph_edges |
| 3.3 | Adaptive router | `pkg/router/adaptive.go` (новый), `router.go` | Feedback loop: собирать (query_pattern, search_type, rating), корректировать signal weights. Persistence в SQLite/PG |
| 3.4 | Embedding drift detection | `pkg/embed/drift.go` (новый), `pipeline.go` | Мониторинг distribution shift: при startup сравнить centroid текущих embeddings с baseline. Warning + optional auto-reembed |
| 3.5 | Graph pruning | `pkg/community/prune.go` (новый), `internal/http/mcp.go` | Удаление superseded edges старше N дней. MCP tool `prune_graph`. Каскадная очистка community_members |
| 3.6 | Community resolution control | `mcp.go`, `pipeline.go` | Параметр `community_resolution` (γ) в cognify. Expose в list_communities. Presets: fine(2.0), medium(1.0), coarse(0.5) |
| 3.7 | Configurable thresholds | `mcp.go`, `pipeline.go` | `dedup_threshold` (semantic dedup, default 0.95), `min_chunk_chars` (override default 80), `max_chunk_chars` (override default 600) |

---

## 2. Детальный дизайн

### 2.1 Unified Search Mode

**Проблема:** cognify имеет `mode=rag/full`, но search не имеет — всегда ищет по всем доступным бэкендам. Если пользователь загрузил данные в RAG mode, graph search всё равно пытается обращаться к пустому графу.

**Решение:** Параметр `mode` в search tool:

```json
{"mode": {"type": "string", "enum": ["rag", "graph", "full", "auto"], "default": "auto",
  "description": "rag: vector/BM25 only. graph: graph traversal only. full: all backends. auto: router decides based on query + capabilities."}}
```

**Семантика:**
- `rag` → допустимые search_types: CHUNKS, HYBRID, CHUNKS_LEXICAL, RAG_COMPLETION. Запрещены: GRAPH_*, COMMUNITY_*, CYPHER, TRIPLET_*
- `graph` → допустимые: GRAPH_COMPLETION, GRAPH_COMPLETION_COT, COMMUNITY_LOCAL, COMMUNITY_GLOBAL, CYPHER, TRIPLET_COMPLETION
- `full` → все типы
- `auto` → router.Route() выбирает (текущее поведение)

**Реализация в `mcp.go` toolSearch:**
```go
searchMode, _ := args["mode"].(string)
if searchMode == "" {
    searchMode = "auto"
}

// Gate search types by mode
allowedTypes := searchTypesForMode(searchMode)
if !allowedTypes[strings.ToUpper(searchType)] {
    // Requested type not allowed in this mode → fallback
    searchType = defaultTypeForMode(searchMode)
}
```

```go
func searchTypesForMode(mode string) map[string]bool {
    switch mode {
    case "rag":
        return map[string]bool{"CHUNKS": true, "HYBRID": true, "CHUNKS_LEXICAL": true, "RAG_COMPLETION": true, "SUMMARIES": true}
    case "graph":
        return map[string]bool{"GRAPH_COMPLETION": true, "GRAPH_COMPLETION_COT": true, "GRAPH_COMPLETION_CONTEXT_EXTENSION": true,
            "COMMUNITY_LOCAL": true, "COMMUNITY_GLOBAL": true, "CYPHER": true, "TRIPLET_COMPLETION": true, "TEMPORAL": true}
    case "full", "auto":
        return nil // nil = all allowed
    default:
        return nil
    }
}
```

**Router integration:** Когда `mode=rag`, router capabilities фильтруются: `HasNeo4j=false`, `HasPostgres=false`, `HasCommunities=false`. Router не выбирает graph-based strategies.

---

### 2.2 Graph-Aware Reranking

**Проблема:** Vector search и graph traversal дают разные сигналы. Чанк может быть семантически далёк от query (low vector score), но entity из этого чанка тесно связан в графе с query entities (high graph proximity). Текущий reranker (cross-encoder) не учитывает graph structure.

**Решение:** Hybrid scoring function:

```
combined_score(result) = α · normalize(vector_score) + β · normalize(graph_proximity)
```

**Новый файл:** `pkg/graphrank/graphrank.go`

```go
package graphrank

// ProximityConfig holds parameters for graph proximity scoring.
type ProximityConfig struct {
    Alpha       float64 // weight for vector score, default 0.7
    Beta        float64 // weight for graph proximity, default 0.3
    MaxHops     int     // max hops to search, default 2
    DecayFactor float64 // score decay per hop, default 0.5
}

func DefaultConfig() ProximityConfig {
    return ProximityConfig{
        Alpha: 0.7, Beta: 0.3,
        MaxHops: 2, DecayFactor: 0.5,
    }
}

// GraphProximity computes proximity score between query entities and a result entity.
// Score = max over all (query_entity, result_entity) pairs of:
//   1.0 if same entity (0 hops)
//   decay^1 if 1-hop neighbor
//   decay^2 if 2-hop neighbor
//   0.0 if not reachable within maxHops
func GraphProximity(ctx context.Context, db *sql.DB, queryEntityIDs, resultEntityIDs []string, cfg ProximityConfig) float64

// RerankWithGraph reranks vector search results using graph proximity.
// For each result: extract entity IDs from metadata → compute graph proximity → blend scores.
func RerankWithGraph(ctx context.Context, db *sql.DB, queryText string, 
    queryEntityIDs []string, results []pipeline.ScoredResult, cfg ProximityConfig) []pipeline.ScoredResult
```

**Graph proximity алгоритм:**

```sql
-- 1-hop: direct neighbors of query entities
SELECT DISTINCT target_id FROM graph_edges
WHERE source_id IN (query_entity_ids)
  AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)

-- 2-hop: neighbors of neighbors
SELECT DISTINCT ge2.target_id FROM graph_edges ge1
JOIN graph_edges ge2 ON ge1.target_id = ge2.source_id
WHERE ge1.source_id IN (query_entity_ids)
  AND (ge1.valid_until IS NULL OR ge1.valid_until > CURRENT_TIMESTAMP)
  AND (ge2.valid_until IS NULL OR ge2.valid_until > CURRENT_TIMESTAMP)
```

Score для result entity:
- В query entities → 1.0
- 1-hop neighbor → `decay^1 = 0.5`
- 2-hop neighbor → `decay^2 = 0.25`
- Not reachable → 0.0

**Normalization:**
- `vector_score` уже в [0, 1] (cosine similarity)
- `graph_proximity` в [0, 1] (max over entity pairs)
- `combined = α * vector_score + β * graph_proximity`

**Pipeline integration (`pipeline/search.go`):**

Новый метод:
```go
func (p *SearchPipeline) SearchByTextWithGraphRerank(
    ctx context.Context, db *sql.DB, collection, queryText string, limit int,
    queryEntityIDs []string, cfg graphrank.ProximityConfig,
) ([]ScoredResult, error)
```

**MCP integration:**
```json
{"graph_rerank": {"type": "boolean", "default": false,
  "description": "Rerank results using graph proximity (entity hop distance). Requires graph data."}}
```

---

### 2.3 Adaptive Router

**Проблема:** Router signal weights (0.85, 0.70, 0.80...) hardcoded. Feedback (1-5 ratings) собирается через `add_feedback` но не влияет на routing.

**Решение:** Feedback-driven weight adjustment.

**Новый файл:** `pkg/router/adaptive.go`

```go
package router

// AdaptiveWeights tracks success rates per search type and adjusts routing scores.
type AdaptiveWeights struct {
    mu           sync.RWMutex
    weights      map[string]float64 // search_type → weight multiplier (default 1.0)
    successCount map[string]int     // search_type → count of rating >= 4
    totalCount   map[string]int     // search_type → total rated queries
    learningRate float64            // how fast weights change, default 0.1
    db           *sql.DB            // persistence
}

// NewAdaptiveWeights loads weights from DB or initializes defaults.
func NewAdaptiveWeights(db *sql.DB, learningRate float64) *AdaptiveWeights

// RecordFeedback updates weights based on new feedback.
// Called by add_feedback MCP tool handler.
func (aw *AdaptiveWeights) RecordFeedback(searchType string, rating int)

// AdjustScore multiplies a candidate score by the adaptive weight.
// If no feedback for this type → returns original score (weight = 1.0).
func (aw *AdaptiveWeights) AdjustScore(searchType string, baseScore float64) float64

// Persist writes current weights to DB.
func (aw *AdaptiveWeights) Persist(ctx context.Context) error

// Load reads weights from DB.
func (aw *AdaptiveWeights) Load(ctx context.Context) error
```

**Weight update formula:**
```
success_rate = success_count / total_count  (rating >= 4 = success)
baseline = 0.5 (neutral)
weight = 1.0 + learning_rate * (success_rate - baseline)

Clamped to [0.5, 1.5] — no search type gets zeroed out or doubled.
```

**DB schema (routing_weights table):**
```sql
CREATE TABLE IF NOT EXISTS routing_weights (
    search_type TEXT PRIMARY KEY,
    weight REAL NOT NULL DEFAULT 1.0,
    success_count INTEGER NOT NULL DEFAULT 0,
    total_count INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**Router integration:**

```go
func Route(query string, caps Capabilities, adaptive *AdaptiveWeights) Decision {
    // ... existing signal detection ...
    
    // Apply adaptive weights
    if adaptive != nil {
        for i := range candidates {
            candidates[i].score = adaptive.AdjustScore(candidates[i].searchType, candidates[i].score)
        }
    }
    
    // Select best candidate
}
```

**Feedback flow:**
1. User calls `add_feedback(query, result_id, search_type, rating)`
2. Handler calls `adaptive.RecordFeedback(searchType, rating)`
3. Next `Route()` call uses adjusted weights
4. Periodically `Persist()` to DB (every 10 feedbacks)

---

### 2.4 Embedding Drift Detection

**Проблема:** При смене embedding модели (e.g., `pplx-embed-context-v1` → `nomic-embed-text-v1.5`) старые векторы в collection становятся incompatible. `reembed` endpoint существует, но нет автоматического детектирования drift.

**Решение:** При startup и периодически — сравнивать embedding model + dimension с collection metadata.

**Новый файл:** `pkg/embed/drift.go`

```go
package embed

// DriftCheckResult reports embedding configuration mismatches.
type DriftCheckResult struct {
    Collection     string
    ExpectedModel  string
    ExpectedDim    int
    ActualModel    string
    ActualDim      int
    IsDrifted      bool
    RecordCount    int
}

// CheckDrift compares current embedding config against all collections.
// Returns list of collections where model or dimension doesn't match.
func CheckDrift(collections *store.CollectionManager, currentModel string, currentDim int) []DriftCheckResult
```

**Алгоритм:**
1. Iterate `collections.List()` → get metadata per collection
2. Compare `meta.EmbeddingModel` vs `currentModel`
3. Compare `meta.EmbeddingDim` vs `currentDim`
4. If mismatch → `IsDrifted = true`
5. Skip internal collections (`_community_summaries`, `_memories_*`)

**Integration points:**

1. **Server startup** (`cmd/server/main.go`):
```go
drifted := embed.CheckDrift(colManager, embedModel, embedDim)
for _, d := range drifted {
    if d.IsDrifted {
        log.Printf("WARNING: Collection %q has model=%s dim=%d, but current config is model=%s dim=%d. Run /api/v1/reembed to fix.",
            d.Collection, d.ActualModel, d.ActualDim, currentModel, currentDim)
    }
}
```

2. **MCP tool `check_drift`:**
```json
{"name": "check_drift", "description": "Check for embedding model drift across collections.",
 "inputSchema": {"type": "object", "properties": {}}}
```

Returns:
```json
[{"collection": "default", "is_drifted": true, "expected_model": "nomic", "actual_model": "pplx", "record_count": 1500}]
```

3. **Optional auto-reembed:** If `AUTO_REEMBED=true` env var → trigger reembed job automatically for drifted collections. Default: warning only.

---

### 2.5 Graph Pruning

**Проблема:** `graph_edges` растёт монотонно. Superseded edges (с `superseded_by != ''` и `valid_until < NOW()`) остаются навсегда. Для long-running instances таблица может стать большой.

**Решение:** Explicit prune tool + configurable retention.

**Новый файл:** `pkg/community/prune.go`

```go
package community

// PruneConfig controls graph edge cleanup.
type PruneConfig struct {
    MaxAgeDays       int  // delete superseded edges older than N days (default 90)
    KeepSuperseding  bool // keep the edge that superseded the deleted one (default true)
    DryRun           bool // report what would be deleted without deleting
}

// PruneResult reports cleanup statistics.
type PruneResult struct {
    EdgesDeleted     int
    EdgesWouldDelete int // for dry-run
    OrphanNodes      int // nodes with no remaining edges
    CommunitiesStale int // communities referencing deleted nodes
}

// PruneGraph deletes old superseded edges and optionally orphaned nodes.
// Also cleans up community_members referencing deleted nodes.
func PruneGraph(ctx context.Context, db *sql.DB, cfg PruneConfig) (PruneResult, error)
```

**SQL:**
```sql
-- Count candidates (dry run or actual)
SELECT COUNT(*) FROM graph_edges
WHERE superseded_by != ''
  AND valid_until IS NOT NULL
  AND valid_until < CURRENT_TIMESTAMP - INTERVAL 'N days'

-- Delete old superseded edges
DELETE FROM graph_edges
WHERE superseded_by != ''
  AND valid_until IS NOT NULL
  AND valid_until < ?

-- Find orphaned nodes (no remaining edges)
SELECT gn.id FROM graph_nodes gn
WHERE NOT EXISTS (
    SELECT 1 FROM graph_edges ge WHERE ge.source_id = gn.id OR ge.target_id = gn.id
)

-- Clean community_members for deleted nodes
DELETE FROM community_members
WHERE node_id NOT IN (SELECT id FROM graph_nodes)
```

**MCP tool `prune_graph`:**
```json
{
    "name": "prune_graph",
    "description": "Clean up old superseded graph edges and orphaned nodes.",
    "inputSchema": {
        "properties": {
            "max_age_days": {"type": "integer", "default": 90},
            "dry_run": {"type": "boolean", "default": true},
            "include_orphan_nodes": {"type": "boolean", "default": false}
        }
    }
}
```

---

### 2.6 Community Resolution Control

**Проблема:** Louvain resolution parameter (γ) hardcoded в DefaultConfig() = 1.0. Пользователь не может контролировать гранулярность community detection.

**Решение:** Expose γ через cognify API.

**cognify tool:**
```json
{"community_resolution": {"type": "number", "default": 1.0,
  "description": "Louvain resolution parameter. >1 = finer granularity (more small communities), <1 = coarser (fewer large communities). Presets: 0.5=coarse, 1.0=medium, 2.0=fine."}}
```

**Pipeline integration:**
```go
if cr, ok := args["community_resolution"].(float64); ok && cr > 0 {
    // Passed to community.Config.Resolution in Stage 5
    pipeCfg.CommunityResolution = cr
}
```

**Config extension:**
```go
type Config struct {
    // ...existing...
    CommunityResolution float64 // γ for Louvain, default 1.0
}
```

**list_communities response** — include resolution used:
```json
{"id": "...", "level": 0, "member_count": 5, "summary": "...", "resolution": 1.0}
```

---

### 2.7 Configurable Thresholds

**Проблема:** Несколько hardcoded thresholds:
- Semantic dedup: 0.95 (pipeline.go line ~408)
- MinChunkChars: 80 (types.go)
- MaxChunkChars: 600 (types.go)

**Решение:** Expose через cognify API.

```json
{
    "dedup_threshold":  {"type": "number", "default": 0.95, "description": "Semantic dedup threshold (0.0-1.0). Higher = stricter."},
    "min_chunk_chars":  {"type": "integer", "default": 80, "description": "Min chunk size in characters."},
    "max_chunk_chars":  {"type": "integer", "default": 600, "description": "Max chunk size in characters."}
}
```

Pipeline Config уже имеет `MinChunkChars` и `MaxChunkChars` — нужно только thread из MCP args.

Для dedup_threshold — добавить `DedupThreshold float64` в Config, использовать вместо hardcoded 0.95.

---

## 3. Definition of Done (DoD)

### 3.1 Unified Search Mode
- [ ] `search(mode="rag")` → только vector/BM25 strategies, graph strategies недоступны
- [ ] `search(mode="graph")` → только graph strategies, vector search недоступен
- [ ] `search(mode="full")` → все strategies
- [ ] `search(mode="auto")` → router выбирает (текущее поведение)
- [ ] `search()` без mode → auto (backward compatible)
- [ ] `search(mode="rag", search_type="GRAPH_COMPLETION")` → fallback к CHUNKS
- [ ] AUTO routing в rag mode не предлагает GRAPH_* strategies

### 3.2 Graph-Aware Reranking
- [ ] `search(graph_rerank=true)` → results reranked по combined score
- [ ] α=0.7, β=0.3 default → vector score dominates, graph proximity augments
- [ ] 0-hop (same entity): proximity = 1.0
- [ ] 1-hop: proximity = 0.5
- [ ] 2-hop: proximity = 0.25
- [ ] Not reachable: proximity = 0.0
- [ ] Без graph data → graceful fallback к vector order
- [ ] graph_rerank + rerank → graph first, then cross-encoder
- [ ] Performance: graph proximity lookup < 10ms для 10 query entities × 30 results

### 3.3 Adaptive Router
- [ ] `add_feedback(search_type="HYBRID", rating=5)` → увеличивает weight для HYBRID
- [ ] Routing weights persistence в `routing_weights` table
- [ ] Weights clamped to [0.5, 1.5] — ни один type не обнуляется
- [ ] learning_rate=0.1 → нужно ~20 feedbacks для заметного сдвига
- [ ] Weights загружаются при startup
- [ ] Weights обновляются при каждом feedback
- [ ] Нет feedbacks → все weights = 1.0 (default)
- [ ] get_feedback_stats включает routing weights

### 3.4 Embedding Drift Detection
- [ ] При startup: log WARNING для drifted collections
- [ ] `check_drift` MCP tool → список drifted collections
- [ ] Пустые collections → не drifted
- [ ] Internal collections (_community_summaries, _memories_*) → skip
- [ ] Одинаковая модель + dimension → not drifted
- [ ] Разная модель + одинаковый dim → drifted (warning)
- [ ] Разный dim → drifted (critical)

### 3.5 Graph Pruning
- [ ] `prune_graph(dry_run=true)` → report count without deleting
- [ ] `prune_graph(max_age_days=30, dry_run=false)` → delete old superseded edges
- [ ] Superseded edges с `valid_until > NOW() - N days` → НЕ удалять (ещё свежие)
- [ ] community_members cleaned up for orphaned nodes
- [ ] Пустой граф → 0 deleted (не ошибка)

### 3.6 Community Resolution
- [ ] `cognify(community_resolution=2.0)` → более мелкие communities
- [ ] `cognify(community_resolution=0.5)` → более крупные communities
- [ ] Default 1.0 → поведение не меняется
- [ ] list_communities показывает resolution
- [ ] Невалидный resolution (0 или отрицательный) → clamp к 0.01

### 3.7 Configurable Thresholds
- [ ] `cognify(dedup_threshold=0.9)` → менее строгий dedup
- [ ] `cognify(min_chunk_chars=50, max_chunk_chars=1000)` → override defaults
- [ ] Без параметров → текущие defaults (backward compat)

### 3.8 Интеграция
- [ ] Phase 1, 1B, 2 тесты проходят (zero regressions)
- [ ] go build / go vet без ошибок
- [ ] Новые параметры видны в tools/list MCP response

---

## 4. Риски и митигации

### R1: Adaptive weights destabilize router
**Риск:** Плохие feedbacks для HYBRID → weight падает → router перестаёт выбирать HYBRID → нет новых feedbacks → weight не восстанавливается (feedback loop death).
**Митигация:**
- Clamp [0.5, 1.5] — минимум 50% базового score
- Decay: weights медленно возвращаются к 1.0 при отсутствии нового feedback (half-life 100 queries)
- Exploration: 10% запросов (random) игнорируют weights → дают шанс "забытым" strategies

### R2: Graph proximity query slow на большом графе
**Риск:** 10K edges, 10 query entities, 30 results → 300 proximity lookups. Каждый = 2-hop SQL query.
**Митигация:**
- Batch query: один SQL запрос для всех query entities → 1-hop set. Второй → 2-hop set.
- Cache: результат proximity valid на время search session (~100ms)
- Total: 2 SQL queries вместо 300. Estimated: <10ms

### R3: Embedding drift false positives
**Риск:** Collection создана с model X, переименовали model → "drifted" но vectors те же.
**Митигация:** Drift detection по model name + dimension. Если dimension совпадает — warning (не critical). User confirmation перед auto-reembed.

### R4: Prune deletes edges needed by communities
**Риск:** Superseded edge deleted → community_members stale → community search returns wrong context.
**Митигация:** 
- Prune каскадно чистит community_members
- После prune — suggest re-running community detection
- Dry-run по умолчанию — user видит impact перед удалением

### R5: Graph rerank + cross-encoder rerank — double cost
**Риск:** `graph_rerank=true` + `rerank=true` → graph proximity lookup + external reranker HTTP call.
**Митигация:** Graph rerank ~5ms (SQL), cross-encoder ~200ms (HTTP). Total ~205ms — приемлемо. Sequence: graph rerank first (cheap, filter), then cross-encoder (expensive, precision).

### R6: Configurable thresholds — user shoots themselves in the foot
**Риск:** `dedup_threshold=0.1` → almost nothing deduped. `min_chunk_chars=1` → millions of tiny chunks.
**Митигация:** Clamp ranges: dedup_threshold [0.5, 1.0], min_chunk_chars [10, 500], max_chunk_chars [100, 10000]. Log warning if extreme values.

---

## 5. Corner Cases

### 5.1 Unified Search Mode
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| mode=rag, type=AUTO | Overview query | Router selects CHUNKS/HYBRID (not GRAPH) | T1 |
| mode=rag, type=GRAPH_COMPLETION | Explicit graph type | Fallback to CHUNKS | T2 |
| mode=graph, type=AUTO | "related to X" | Router selects GRAPH_COMPLETION | T3 |
| mode=graph, type=CHUNKS | Explicit vector type | Fallback to GRAPH_COMPLETION | T4 |
| mode=full, type=AUTO | Any query | All strategies available | T5 |
| mode=auto, no mode param | Default | Same as current behavior | T6 |
| mode=invalid | Typo | Treat as auto | T7 |

### 5.2 Graph-Aware Reranking
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| graph_rerank=true, graph has data | 10 results | Reranked by combined score | T8 |
| graph_rerank=true, empty graph | 10 results | Fallback to vector order | T9 |
| graph_rerank=true, no DB | 10 results | Fallback to vector order | T10 |
| 0-hop match | Query entity = result entity | proximity = 1.0 | T11 |
| 1-hop neighbor | Direct neighbor | proximity = 0.5 | T12 |
| 2-hop neighbor | Neighbor of neighbor | proximity = 0.25 | T13 |
| Not reachable | No path within 2 hops | proximity = 0.0 | T14 |
| Multiple query entities | 3 queries, best match | Max proximity across pairs | T15 |
| graph_rerank + cross-encoder | Both enabled | Graph first, then cross-encoder | T16 |

### 5.3 Adaptive Router
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| No feedbacks | First query | All weights = 1.0 | T17 |
| 10 positive HYBRID | rating=5 ×10 | HYBRID weight > 1.0 | T18 |
| 10 negative HYBRID | rating=1 ×10 | HYBRID weight < 1.0 (but >= 0.5) | T19 |
| Weight persistence | Write → restart → read | Weights restored | T20 |
| Weight clamp | 100 negative feedbacks | Weight = 0.5 (not lower) | T21 |
| Mixed feedbacks | 5 positive + 5 negative | Weight ≈ 1.0 | T22 |
| Unknown search type | Feedback for "FOO" | Ignored (no crash) | T23 |

### 5.4 Embedding Drift
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Same model + dim | Matching config | Not drifted | T24 |
| Different model, same dim | Model renamed | Drifted (warning) | T25 |
| Different dim | 1024 vs 768 | Drifted (critical) | T26 |
| Empty collection | No records | Not drifted | T27 |
| Internal collection | _community_summaries | Skipped | T28 |
| Multiple collections | 2 drifted, 1 OK | Returns 2 drift results | T29 |

### 5.5 Graph Pruning
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Dry run | dry_run=true | Count without deleting | T30 |
| Delete old edges | max_age_days=30 | Old superseded deleted | T31 |
| No superseded edges | Clean graph | 0 deleted | T32 |
| Fresh superseded | valid_until = yesterday | NOT deleted (< max_age) | T33 |
| Cascade to community_members | Edge deleted → node orphaned | community_members cleaned | T34 |
| Empty graph | No edges at all | 0 deleted, no error | T35 |

### 5.6 Community Resolution
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| resolution=2.0 | Same data | More communities than γ=1.0 | T36 |
| resolution=0.5 | Same data | Fewer communities than γ=1.0 | T37 |
| resolution=0 | Invalid | Clamp to 0.01 | T38 |
| Default (no param) | cognify() | γ=1.0 (unchanged) | T39 |

### 5.7 Configurable Thresholds
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| dedup_threshold=0.9 | Similar entities | Less strict dedup | T40 |
| dedup_threshold=0.99 | Same entities | Only exact duplicates merged | T41 |
| min_chunk_chars=50 | Short paragraphs | More chunks (lower threshold) | T42 |
| max_chunk_chars=2000 | Long text | Fewer, larger chunks | T43 |
| Out-of-range values | min=1, max=999999 | Clamped to valid range | T44 |

---

## 6. Тесты

### 6.1 Go unit: Graph Rank (`pkg/graphrank/graphrank_test.go`)

```
TestGraphProximity_SameEntity             — T11: 0-hop = 1.0
TestGraphProximity_OneHop                 — T12: 1-hop = 0.5
TestGraphProximity_TwoHop                 — T13: 2-hop = 0.25
TestGraphProximity_Unreachable            — T14: 0.0
TestGraphProximity_MultipleQueryEntities  — T15: max across pairs
TestGraphProximity_EmptyGraph             — T9: fallback 0.0
TestGraphProximity_NoDB                   — T10: fallback 0.0
TestRerankWithGraph_ReordersResults       — T8: combined score changes order
TestRerankWithGraph_FallbackNoGraph       — graceful when no graph data
```

### 6.2 Go unit: Adaptive Router (`pkg/router/adaptive_test.go`)

```
TestAdaptiveWeights_Default               — T17: all 1.0
TestAdaptiveWeights_PositiveFeedback      — T18: weight increases
TestAdaptiveWeights_NegativeFeedback      — T19: weight decreases
TestAdaptiveWeights_Clamp                 — T21: >= 0.5
TestAdaptiveWeights_MixedFeedback         — T22: ~1.0
TestAdaptiveWeights_UnknownType           — T23: ignored
TestAdaptiveWeights_Persistence           — T20: write/read roundtrip
TestAdaptiveWeights_AdjustScore           — multiplied correctly
```

### 6.3 Go unit: Drift Detection (`pkg/embed/drift_test.go`)

```
TestCheckDrift_NoDrift                    — T24
TestCheckDrift_ModelDrift                 — T25
TestCheckDrift_DimDrift                   — T26
TestCheckDrift_EmptyCollection            — T27
TestCheckDrift_InternalSkipped            — T28
TestCheckDrift_Multiple                   — T29
```

### 6.4 Go unit: Graph Pruning (`pkg/community/prune_test.go`)

```
TestPruneGraph_DryRun                     — T30
TestPruneGraph_DeleteOld                  — T31
TestPruneGraph_NoneToDelete               — T32
TestPruneGraph_FreshNotDeleted            — T33
TestPruneGraph_CascadeCommunityMembers    — T34
TestPruneGraph_EmptyGraph                 — T35
```

### 6.5 Go unit: Search Mode (`pipeline/search_mode_test.go`)

```
TestSearchTypesForMode_RAG                — T1/T2: only vector types
TestSearchTypesForMode_Graph              — T3/T4: only graph types
TestSearchTypesForMode_Full               — T5: nil (all)
TestSearchTypesForMode_Auto               — T6: nil (all)
TestDefaultTypeForMode                    — fallback types correct
```

### 6.6 Go unit: Router Signals (`pkg/router/router_test.go` — дополнение)

```
TestRoute_AdaptiveWeights                 — weights affect routing
TestRoute_ModeRAG_NoGraph                 — mode=rag suppresses graph
TestRoute_ModeFull                        — mode=full allows all
```

### 6.7 Python MCP integration (`tests/test_mcp_phase3.py`)

```python
class TestUnifiedSearchMode:
    test_mode_rag_vector_only              — T1: mode=rag, AUTO → vector types
    test_mode_rag_blocks_graph             — T2: mode=rag, GRAPH_COMPLETION → fallback
    test_mode_auto_default                 — T6: backward compatible
    test_mode_invalid_treated_as_auto      — T7

class TestGraphRerank:
    test_graph_rerank_flag                 — T8: results returned
    test_graph_rerank_no_graph_fallback    — T9: graceful
    test_graph_rerank_plus_cross_encoder   — T16: both work

class TestAdaptiveRouter:
    test_feedback_affects_routing           — T18: positive feedback → weight up
    test_no_feedback_default               — T17: weights = 1.0

class TestDriftDetection:
    test_check_drift_tool                  — returns drift info
    test_check_drift_no_drift              — T24: clean state

class TestGraphPruning:
    test_prune_dry_run                     — T30: counts without deleting
    test_prune_empty_graph                 — T35: no error

class TestCommunityResolution:
    test_resolution_param_accepted         — T39: cognify with resolution
    
class TestConfigurableThresholds:
    test_custom_chunk_sizes                — T42/T43: min/max override

class TestPhase3Regression:
    test_phase1_rag_still_works
    test_phase1b_parent_child_still_works
    test_phase2_communities_still_work
    test_smoke_palace_still_pass
```

### 6.8 Test → Corner Case mapping

| Тест | Corner Case | Тип |
|------|-------------|-----|
| `TestSearchTypesForMode_RAG` | T1, T2 | Go unit |
| `TestSearchTypesForMode_Graph` | T3, T4 | Go unit |
| `TestGraphProximity_SameEntity` | T11 | Go unit |
| `TestGraphProximity_OneHop` | T12 | Go unit |
| `TestGraphProximity_TwoHop` | T13 | Go unit |
| `TestGraphProximity_Unreachable` | T14 | Go unit |
| `TestAdaptiveWeights_Default` | T17 | Go unit |
| `TestAdaptiveWeights_PositiveFeedback` | T18 | Go unit |
| `TestAdaptiveWeights_Clamp` | T21 | Go unit |
| `TestCheckDrift_NoDrift` | T24 | Go unit |
| `TestCheckDrift_DimDrift` | T26 | Go unit |
| `TestPruneGraph_DryRun` | T30 | Go unit |
| `TestPruneGraph_DeleteOld` | T31 | Go unit |
| `test_mode_rag_vector_only` | T1 | MCP integration |
| `test_graph_rerank_flag` | T8 | MCP integration |
| `test_feedback_affects_routing` | T18 | MCP integration |
| `test_check_drift_tool` | T24 | MCP integration |
| `test_prune_dry_run` | T30 | MCP integration |
| `test_resolution_param_accepted` | T39 | MCP integration |

---

## 7. Порядок реализации

```
Step 1: pkg/graphrank/graphrank.go + graphrank_test.go
        Graph proximity scoring. Batch SQL queries for efficiency.
        DoD: T8-T16 green, proximity lookup < 10ms.

Step 2: pkg/router/adaptive.go + adaptive_test.go
        Adaptive weights: RecordFeedback, AdjustScore, Persist/Load.
        routing_weights table DDL.
        DoD: T17-T23 green.

Step 3: pkg/embed/drift.go + drift_test.go
        CheckDrift function.
        DoD: T24-T29 green.

Step 4: pkg/community/prune.go + prune_test.go
        PruneGraph with dry-run + cascade.
        DoD: T30-T35 green.

Step 5: Unified search mode
        searchTypesForMode(), defaultTypeForMode() in mcp.go.
        Mode-aware routing в toolSearch.
        DoD: T1-T7 green.

Step 6: Pipeline integration
        CommunityResolution, DedupThreshold, min/max chunk params в Config.
        Thread from MCP args.
        DoD: T36-T44 green, go build.

Step 7: MCP API
        search: mode, graph_rerank params.
        cognify: community_resolution, dedup_threshold, min/max_chunk_chars.
        New tools: check_drift, prune_graph.
        Router: adaptive weights integration.
        api.go: capabilitiesFromConfig + mode gating.
        DoD: go build + go vet.

Step 8: Integration tests
        tests/test_mcp_phase3.py.
        DoD: all green.

Step 9: Full regression
        ALL phases: 1 + 1B + 2 + 3 + smoke + palace.
        DoD: 0 new failures.
```

---

## 8. Файлы

**Новые:**
- `pkg/graphrank/graphrank.go` + `graphrank_test.go` — graph proximity scoring (~200 LOC)
- `pkg/router/adaptive.go` + `adaptive_test.go` — feedback-driven weights (~200 LOC)
- `pkg/embed/drift.go` + `drift_test.go` — drift detection (~100 LOC)
- `pkg/community/prune.go` + `prune_test.go` — graph pruning (~150 LOC)
- `tests/test_mcp_phase3.py` — 15+ integration tests

**Изменённые:**
- `internal/http/schema.go` — +routing_weights table DDL (PG + SQLite)
- `pkg/orchestrator/pipeline.go` — CommunityResolution, DedupThreshold, min/max chunk in Config
- `pkg/router/router.go` — adaptive weights parameter in Route()
- `pipeline/search.go` — SearchByTextWithGraphRerank
- `internal/http/mcp.go` — mode/graph_rerank/community_resolution/dedup_threshold/check_drift/prune_graph
- `internal/http/api.go` — mode gating + adaptive capabilities
- `cmd/server/main.go` — drift check at startup + adaptive weights init
