# Phase 2: DAG/GraphRAG Mode — Implementation Plan

**Цель:** Добавить полноценный GraphRAG pipeline: multi-level community detection (Louvain с graph compression), hierarchical summarization communities, и два уровня search — COMMUNITY_LOCAL (context из community) и COMMUNITY_GLOBAL (map-reduce по community summaries). Это закрывает главный архитектурный пробел между Levara и Microsoft GraphRAG.

**Принцип:** Никаких моков, заглушек, TODO. Каждый пункт — рабочий код с тестами. Никаких упрощений «для v1» — делаем production-quality с первого раза.

---

## 1. Scope — что входит в Phase 2

| # | Фича | Файлы | Описание |
|---|-------|-------|----------|
| 2.1 | Multi-level Louvain | `pkg/community/louvain.go` | Полный двухфазный Louvain: Phase 1 (node moves) + Phase 2 (graph compression) → multi-level dendрограмма |
| 2.2 | `graph_communities` таблица | `internal/http/schema.go` | Иерархическая таблица с level, parent_id, member_node_ids |
| 2.3 | Hierarchical LLM summaries | `pkg/community/summarize.go` | Level 0 → summary по entities. Level N → summary по child-community summaries. Embed каждый уровень. |
| 2.4 | COMMUNITY_LOCAL search | `internal/http/graph_search.go` | Query → find community → enrich context from community members → LLM answer |
| 2.5 | COMMUNITY_GLOBAL search | `internal/http/graph_search.go` | Map-reduce: query → top-K community summaries → partial answers → synthesize |
| 2.6 | Stage 5 в cognify pipeline | `pkg/orchestrator/pipeline.go` | Full-graph community detection + hierarchical summarization |
| 2.7 | Resolution parameter | `pkg/community/louvain.go` | γ (gamma) для контроля гранулярности: γ>1 → мелкие, γ<1 → крупные communities |
| 2.8 | Incremental community update | `pkg/community/incremental.go` | При добавлении новых nodes/edges — local re-optimization вместо full recompute |
| 2.9 | MCP API | `internal/http/mcp.go` | `list_communities`, `get_community` tools + COMMUNITY_LOCAL/GLOBAL в search |

**Что НЕ входит:** Leiden refinement phase (Louvain Phase 2 + resolution parameter покрывает 95% use cases), визуализация communities (отдельная фича).

---

## 2. Детальный дизайн

### 2.1 Multi-level Louvain Algorithm

**Новый файл:** `pkg/community/louvain.go`

Полный двухфазный Louvain:

```
Repeat until no improvement:
  Phase 1 (Local moves):
    For each node v (sorted by ID for determinism):
      For each neighbor community c of v:
        ΔQ = modularity gain of moving v to c
      Move v to community with max ΔQ > 0
    Repeat Phase 1 until ΔQ < minGain for full pass
    
  Phase 2 (Graph compression):
    Create super-graph: each community → super-node
    Edge weight between super-nodes = sum of cross-community edges
    Self-loops = sum of intra-community edges (2 * internal weight)
    Record current partition as level L
    
  L += 1
  Graph = super-graph
```

**Модулярность с resolution parameter γ:**
```
Q = (1/2m) Σ [A_ij - γ * (k_i * k_j) / (2m)] * δ(c_i, c_j)

ΔQ(v → c) = [Σ_in + 2*k_v_c] / (2m) - γ * [(Σ_tot + k_v)/(2m)]²
           - [Σ_in / (2m) - γ * (Σ_tot/(2m))² - γ * (k_v/(2m))²]

где:
  γ = resolution parameter (default 1.0)
  m = total edge weight / 2
  k_v = weighted degree of node v
  k_v_c = sum of edge weights from v to nodes in community c
  Σ_in = sum of weights inside community c
  Σ_tot = sum of weights of all edges incident to community c
```

**Структуры:**

```go
package community

// Graph — undirected weighted graph для community detection.
type Graph struct {
    n       int                // node count
    nodeIDs []string           // external node IDs (index-based internally)
    idxOf   map[string]int     // external ID → internal index
    adj     [][]adjEntry       // adjacency list
    degree  []float64          // weighted degree per node
    totalW  float64            // total edge weight (sum of all, not /2)
}

type adjEntry struct {
    target int
    weight float64
}

// Community — one detected community.
type Community struct {
    ID              string   `json:"id"`       // UUID5 from sorted member IDs
    Members         []string `json:"members"`  // external node IDs
    Level           int      `json:"level"`    // hierarchy level (0=leaf)
    ParentID        string   `json:"parent_id"` // parent community ID (""=root)
    InternalWeight  float64  `json:"internal_weight"`  // sum of intra-community edges
    MemberCount     int      `json:"member_count"`
}

// Dendrogram — full hierarchical result.
type Dendrogram struct {
    Levels      [][]Community // levels[0] = finest, levels[N] = coarsest
    Modularity  []float64     // modularity per level
    MaxLevel    int
    TotalNodes  int
    Iterations  int           // total Phase 1 passes across all levels
    Resolution  float64       // γ used
}

// Config — параметры алгоритма.
type Config struct {
    Resolution     float64 // γ, default 1.0. >1 = finer, <1 = coarser
    MinGain        float64 // minimum ΔQ to continue, default 1e-7
    MaxIterations  int     // max Phase 1 passes per level, default 100
    MaxLevels      int     // max hierarchy depth, default 10
    MinCommunity   int     // min nodes per community to keep, default 1
}

func DefaultConfig() Config {
    return Config{
        Resolution:    1.0,
        MinGain:       1e-7,
        MaxIterations: 100,
        MaxLevels:     10,
        MinCommunity:  1,
    }
}
```

**Ключевые функции:**

```go
// Louvain запускает полный multi-level алгоритм.
// Возвращает дендрограмму со всеми уровнями hierarchy.
func Louvain(g *Graph, cfg Config) Dendrogram

// phase1 — local moves до стабилизации. Возвращает partition (node→community).
func phase1(g *Graph, partition []int, cfg Config) ([]int, float64, int)

// phase2 — graph compression. Возвращает super-graph + mapping old→new.
func phase2(g *Graph, partition []int) (*Graph, []int)

// modularity вычисляет Q для текущего partition.
func modularity(g *Graph, partition []int, resolution float64) float64

// deltaQ вычисляет прирост модулярности при перемещении node в target community.
func deltaQ(g *Graph, node int, targetComm int, partition []int, 
    commSumTot []float64, commSumIn []float64, resolution float64) float64
```

**BuildGraphFromSQL — загрузка полного графа:**

```go
// BuildGraphFromSQL загружает ВСЕ активные nodes и edges из SQL.
// Не фильтрует по dataset — community detection на полном графе.
// Edges: only active (valid_until IS NULL OR > NOW()).
func BuildGraphFromSQL(ctx context.Context, db *sql.DB) (*Graph, error)
```

SQL:
```sql
-- Nodes
SELECT id FROM graph_nodes

-- Active edges with weights
SELECT source_id, target_id, confidence
FROM graph_edges
WHERE valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP
```

Граф неориентированный: каждый edge добавляется в обе стороны adjacency list.

**Детерминизм:** Nodes сортируются по ID перед обработкой. Phase 1 обходит nodes в фиксированном порядке. При равном ΔQ — выбирается community с меньшим индексом.

---

### 2.2 `graph_communities` таблица

**Файл:** `internal/http/schema.go`

**PostgreSQL DDL:**
```sql
CREATE TABLE IF NOT EXISTS graph_communities (
    id TEXT PRIMARY KEY,
    level INTEGER NOT NULL DEFAULT 0,
    parent_id TEXT NOT NULL DEFAULT '',
    member_node_ids JSONB NOT NULL DEFAULT '[]',
    member_count INTEGER NOT NULL DEFAULT 0,
    internal_weight REAL NOT NULL DEFAULT 0,
    summary TEXT NOT NULL DEFAULT '',
    summary_embedding_id TEXT NOT NULL DEFAULT '',
    modularity REAL NOT NULL DEFAULT 0,
    resolution REAL NOT NULL DEFAULT 1.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_communities_level ON graph_communities (level);
CREATE INDEX IF NOT EXISTS idx_communities_parent ON graph_communities (parent_id);
CREATE INDEX IF NOT EXISTS idx_communities_member_count ON graph_communities (member_count DESC);
```

**SQLite DDL** — аналогичный с TEXT/CURRENT_TIMESTAMP.

**Mapping community → node:**

Отдельная join-таблица для эффективного поиска "в какой community находится node X":

```sql
CREATE TABLE IF NOT EXISTS community_members (
    community_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    level INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (community_id, node_id)
);
CREATE INDEX IF NOT EXISTS idx_community_members_node ON community_members (node_id, level);
```

Это позволяет запрос `SELECT community_id FROM community_members WHERE node_id = $1 AND level = $2` за O(1) вместо сканирования JSON arrays.

**Поля graph_communities:**
- `id` — UUID5 от sorted member IDs + level
- `level` — 0=leaf (finest), 1=next, etc.
- `parent_id` — community на level+1, в которую эта входит (FK-like, не enforced)
- `member_node_ids` — JSON array для информационных целей + API response
- `member_count` — денормализация
- `internal_weight` — сумма весов intra-community edges
- `summary` — LLM-generated текст
- `summary_embedding_id` — ID записи в vector collection (для удаления при re-detect)
- `modularity` — modularity contribution этого community
- `resolution` — γ использованный при детекции

---

### 2.3 Hierarchical LLM Summaries

**Новый файл:** `pkg/community/summarize.go`

**Два режима summarization:**

**Level 0 (leaf communities):**
```
Input: member entities (name, type, description) + inter-member edges
Prompt:
  "You are summarizing a cluster of related entities from a knowledge graph.
   
   Entities in this group:
   - Einstein (Person): physicist who developed general relativity
   - Bohr (Person): physicist who developed atomic model
   - Princeton (Organization): university and research center
   
   Relationships:
   - Einstein worked_at Princeton
   - Einstein debated_with Bohr
   - Bohr responded_to Einstein
   
   Write a 2-4 sentence summary describing what this group represents,
   the key relationships, and the main topics. Be factual and concise."
```

**Level N (higher levels, community of communities):**
```
Input: child community summaries
Prompt:
  "You are creating a higher-level summary from multiple topic clusters.
   
   Cluster 1 (5 entities): "Einstein and colleagues at Princeton worked on..."
   Cluster 2 (4 entities): "Bohr and the Copenhagen school developed..."
   Cluster 3 (3 entities): "Feynman and Dirac advanced quantum field theory..."
   
   Write a 2-4 sentence overview that captures the broader theme 
   connecting these clusters. Focus on overarching topics, not details."
```

**SummarizeConfig:**

```go
type SummarizeConfig struct {
    LLMProvider    llm.Provider
    EmbedClient    *embed.Client
    Collections    *store.CollectionManager
    DB             *sql.DB
    LLMCache       llmcache.LLMCacher
    Concurrency    int // parallel LLM calls, default 3
    MinMembers     int // min members to summarize, default 3
    MaxContext      int // max entities per prompt, default 50
}
```

**Vector collection:** `_community_summaries`
- Per-level embeddings
- Metadata: `{"community_id":"...", "level":0, "member_count":5, "resolution":1.0}`
- Summary text — полный текст summary в "text" field для reranking

**Large community handling (>MaxContext nodes):**
- Sort members by weighted degree (from graph)
- Take top-MaxContext
- Add note to prompt: "(showing top 50 of 127 entities by connectivity)"

**Hierarchical summarization order:**
1. Level 0: все leaf communities (concurrent, semaphore)
2. Level 1: суммаризация по child summaries
3. ...до MaxLevel

**Graceful degradation:**
- LLM nil → skip summarization, communities сохранены без summary
- Embed nil → summary в SQL, не в vector
- LLM timeout/error на одном community → log warning, продолжить остальные
- LLMCache → повторные cognify не вызывают LLM для уже кэшированных communities

---

### 2.4 COMMUNITY_LOCAL Search

Новый handler. Когда query касается конкретных entities, а не общего обзора.

**Алгоритм:**
1. Vector search → find relevant entities
2. Lookup entity's community via `community_members` table
3. Load community members + edges → build rich context
4. LLM answer with community context

```go
func communityLocalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
    // Step 1: Vector search → entity names
    entityNames := vectorSearchEntities(ctx, cfg, req)
    
    // Step 2: Find communities these entities belong to (level 0)
    communityIDs := lookupCommunities(ctx, cfg.DB, entityNames, 0)
    
    // Step 3: For each community, load ALL member entities + edges
    // This gives broader context than just the matched entities
    var context []string
    for _, commID := range communityIDs {
        members := loadCommunityMembers(ctx, cfg.DB, commID)
        edges := loadCommunityEdges(ctx, cfg.DB, members)
        summary := loadCommunitySummary(ctx, cfg.DB, commID)
        context = append(context, formatCommunityContext(summary, members, edges))
    }
    
    // Step 4: LLM answer
    prompt := fmt.Sprintf(
        "Answer based on these knowledge graph communities:\n\n%s\n\nQuestion: %s",
        strings.Join(context, "\n---\n"), req.QueryText)
    answer := callLLMFromAPI(endpoint, model, prompt, cfg.LLMProvider)
}
```

**Отличие от GRAPH_COMPLETION:** GRAPH_COMPLETION делает 1-hop по edges от найденных entities. COMMUNITY_LOCAL даёт ВСЕ entities из community — более полный контекст, включая entities не напрямую связанные с query.

---

### 2.5 COMMUNITY_GLOBAL Search (Map-Reduce)

По паттерну cotSearch (`graph_search.go:290-409`), но с community summaries вместо sub-questions.

```go
func communityGlobalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
    // Step 1: Vector search in _community_summaries → top-K relevant communities
    // Search highest available level first (broadest context)
    maxLevel := getMaxCommunityLevel(ctx, cfg.DB)
    
    level := maxLevel
    var relevantCommunities []communitySearchHit
    
    for level >= 0 && len(relevantCommunities) < 5 {
        summaryResults := searchSummaries(ctx, sp, req.QueryText, level, 10)
        relevantCommunities = append(relevantCommunities, summaryResults...)
        level--
    }
    // Deduplicate (child and parent may both match)
    relevantCommunities = deduplicateCommunities(relevantCommunities)
    
    if len(relevantCommunities) == 0 {
        // Fallback: no communities → GRAPH_COMPLETION
        return graphCompletionSearch(c, cfg, req)
    }
    
    // Step 2: Map — partial answer per community
    type mapResult struct {
        CommunityID   string
        Summary       string
        MemberCount   int
        Level         int
        PartialAnswer string
    }
    var partials []mapResult
    
    sem := make(chan struct{}, 3) // max 3 concurrent LLM calls
    var mu sync.Mutex
    var wg sync.WaitGroup
    
    for _, comm := range relevantCommunities {
        wg.Add(1)
        go func(comm communitySearchHit) {
            defer wg.Done()
            sem <- struct{}{}
            defer func() { <-sem }()
            
            prompt := fmt.Sprintf(
                "A knowledge graph community contains these related topics:\n\n%s\n\n"+
                "Based on this community's knowledge, provide relevant information for: %s\n"+
                "If this community has no relevant information, respond with 'NOT_RELEVANT'.",
                comm.Summary, req.QueryText)
            partial := callLLMFromAPI(endpoint, model, prompt, cfg.LLMProvider)
            
            if !strings.Contains(partial, "NOT_RELEVANT") {
                mu.Lock()
                partials = append(partials, mapResult{
                    CommunityID: comm.ID, Summary: comm.Summary,
                    MemberCount: comm.MemberCount, Level: comm.Level,
                    PartialAnswer: partial,
                })
                mu.Unlock()
            }
        }(comm)
    }
    wg.Wait()
    
    // Step 3: Reduce — synthesize
    if len(partials) == 0 {
        return c.JSON(fiber.Map{
            "answer": "No relevant communities found for this query.",
            "communities_used": []any{}, "search_type": "COMMUNITY_GLOBAL",
        })
    }
    
    if len(partials) == 1 {
        // Single community — no need for synthesis
        answer = partials[0].PartialAnswer
    } else {
        var partialTexts []string
        for i, p := range partials {
            partialTexts = append(partialTexts, fmt.Sprintf(
                "Community %d (%d entities, level %d):\n%s",
                i+1, p.MemberCount, p.Level, p.PartialAnswer))
        }
        synthesizePrompt := fmt.Sprintf(
            "Multiple knowledge communities provided these perspectives on '%s':\n\n%s\n\n"+
            "Synthesize a comprehensive answer combining all relevant perspectives. "+
            "Resolve any contradictions. Be thorough but concise.",
            req.QueryText, strings.Join(partialTexts, "\n\n---\n\n"))
        answer = callLLMFromAPI(endpoint, model, synthesizePrompt, cfg.LLMProvider)
    }
}
```

**Response:**
```json
{
    "answer": "...",
    "communities_used": [
        {"community_id": "...", "level": 1, "member_count": 12, "summary": "...", "partial_answer": "..."}
    ],
    "total_communities_searched": 5,
    "search_type": "COMMUNITY_GLOBAL"
}
```

---

### 2.6 Stage 5 в cognify pipeline

**Файл:** `pkg/orchestrator/pipeline.go`

**Вставка:** После `writeWg.Wait()`, перед финальным Progress.

```go
writeWg.Wait()

// --- Stage 5: Community Detection + Hierarchical Summarization ---
// Runs on FULL graph (not per-dataset) to capture cross-document relationships.
// Skipped in RAG mode (no graph) or when DB is nil.
communitiesDetected := 0
if !cfg.SkipGraph && cfg.DB != nil && len(dedupResult.Nodes) >= 3 {
    progressCh <- Progress{Stage: "communities", Message: "detecting communities", ElapsedMs: ms(start)}
    
    // Build full graph (all active nodes/edges, not just current dataset)
    g, err := community.BuildGraphFromSQL(ctx, cfg.DB)
    if err != nil {
        log.Printf("[pipeline] community graph build: %v", err)
    } else if g.NodeCount() >= 3 {
        // Detect with default resolution
        commCfg := community.DefaultConfig()
        dendro := community.Louvain(g, commCfg)
        
        log.Printf("[pipeline] communities: %d levels, %d leaf communities (modularity %.4f, γ=%.2f, %d iterations)",
            dendro.MaxLevel+1, len(dendro.Levels[0]), dendro.Modularity[0], dendro.Resolution, dendro.Iterations)
        
        // Clear old communities and write new
        if err := community.ReplaceCommunities(ctx, cfg.DB, dendro); err != nil {
            log.Printf("[pipeline] community write: %v", err)
        } else {
            for _, level := range dendro.Levels {
                communitiesDetected += len(level)
            }
        }
        
        // Hierarchical summarization (optional, needs LLM + embed)
        if cfg.LLMProvider != nil && cfg.EmbedEndpoint != "" {
            progressCh <- Progress{
                Stage: "communities", 
                Message: fmt.Sprintf("summarizing %d communities", communitiesDetected),
                ElapsedMs: ms(start),
            }
            sumCfg := community.SummarizeConfig{
                LLMProvider: cfg.LLMProvider,
                EmbedClient: embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3),
                Collections: cfg.Collections,
                DB:          cfg.DB,
                LLMCache:    cfg.LLMCache,
                Concurrency: 3,
                MinMembers:  3,
                MaxContext:   50,
            }
            if err := community.SummarizeHierarchy(ctx, dendro, g, sumCfg); err != nil {
                log.Printf("[pipeline] community summarize: %v", err)
            }
        }
    }
}
```

**Полный граф vs per-dataset:**
- Community detection на **полном графе** — entities из разных cognify runs связаны через graph_edges
- Каждый cognify run **пересчитывает все communities** с нуля (ReplaceCommunities)
- Для больших графов (>10K nodes) это дорого → в 2.8 добавим incremental update

---

### 2.7 Resolution Parameter

Доступен через:

1. **Config** в `community.DefaultConfig().Resolution = 1.0`
2. **MCP cognify tool** — новый параметр `community_resolution`:
   ```json
   {"community_resolution": {"type": "number", "default": 1.0, 
     "description": "Louvain resolution. >1 = finer granularity, <1 = coarser."}}
   ```
3. **Хранится в graph_communities.resolution** для диагностики

**Семантика:**
- γ = 1.0 — standard modularity (default)
- γ = 2.0 — в 2 раза мельче (больше маленьких communities)
- γ = 0.5 — в 2 раза крупнее (меньше больших communities)
- γ = 0.0 — невалидно, clamp к 0.01

---

### 2.8 Incremental Community Update

**Новый файл:** `pkg/community/incremental.go`

При добавлении новых nodes/edges (новый cognify run) — не пересчитываем всё, а:

1. Загрузить текущий partition из `community_members` таблицы
2. Добавить новые nodes в singleton communities
3. Запустить **только Phase 1** (local moves) с текущим partition как стартовой точкой
4. Новые nodes будут перемещены в оптимальные communities
5. Если модулярность улучшилась > threshold — запустить Phase 2 (compression)
6. Обновить изменённые communities в SQL

```go
// IncrementalUpdate обновляет community structure после добавления новых nodes/edges.
// Загружает текущий partition из DB, добавляет новые nodes как singletons,
// запускает Phase 1 (local moves only) для оптимизации.
// Если улучшение > minGain, запускает Phase 2 + полный пересчёт.
func IncrementalUpdate(ctx context.Context, db *sql.DB, newNodeIDs []string, cfg Config) (*Dendrogram, error)
```

**Условие full recompute:**
- Если новых nodes > 20% от существующих → full recompute (слишком много изменений для incremental)
- Если partition загрузилась пустая → full recompute (первый run)

**Условие incremental:**
- Новых nodes <= 20% существующих
- Существующий partition valid

---

### 2.9 MCP API Changes

**Новый tool: `list_communities`:**
```json
{
    "name": "list_communities",
    "description": "List detected communities (entity clusters) with summaries. Communities are detected by Louvain algorithm during cognify.",
    "inputSchema": {
        "properties": {
            "level": {"type": "integer", "description": "Hierarchy level (0=finest). Omit for all levels."},
            "min_members": {"type": "integer", "default": 2},
            "limit": {"type": "integer", "default": 20},
            "include_members": {"type": "boolean", "default": false, "description": "Include member node names in response"}
        }
    }
}
```

**Новый tool: `get_community`:**
```json
{
    "name": "get_community",
    "description": "Get detailed info about a specific community: members, edges, summary, child communities.",
    "inputSchema": {
        "properties": {
            "community_id": {"type": "string"},
            "include_edges": {"type": "boolean", "default": true}
        },
        "required": ["community_id"]
    }
}
```

**search tool** — добавить `COMMUNITY_LOCAL` и `COMMUNITY_GLOBAL` в search_type enum.

**cognify tool** — добавить `community_resolution` parameter.

**Router** — добавить `HasCommunities` в Capabilities:
```go
type Capabilities struct {
    // ... existing ...
    HasCommunities bool // graph_communities table has data
}
```

---

## 3. Definition of Done (DoD)

### 3.1 Louvain Algorithm
- [ ] Multi-level: Phase 1 (local moves) + Phase 2 (graph compression) → Dendrogram
- [ ] Resolution parameter γ работает: γ=2.0 даёт больше communities чем γ=1.0 на том же графе
- [ ] Детерминизм: одинаковый граф + одинаковый γ → одинаковый результат
- [ ] Barbell graph (2 cliques по 5 + bridge) → 2 communities на level 0
- [ ] Karate club graph (Zachary) → 2-4 communities, modularity > 0.35
- [ ] 1K nodes / 5K edges < 50ms; 10K nodes / 50K edges < 500ms
- [ ] Пустой граф → 0 communities, не panic
- [ ] 1 node → 1 singleton, modularity 0
- [ ] Self-loops ignored, duplicate edges merged (weights summed)
- [ ] Graph compression корректна: super-graph modularity ≥ pre-compression modularity

### 3.2 Таблицы
- [ ] graph_communities + community_members создаются в MigrateSchema
- [ ] PG + SQLite оба работают
- [ ] Повторный migrate — idempotent
- [ ] CRUD roundtrip: write → read → delete → read(empty)
- [ ] community_members index позволяет O(1) lookup node → community

### 3.3 Hierarchical Summaries
- [ ] Level 0: summary по entities + edges
- [ ] Level 1+: summary по child summaries
- [ ] Summaries embedded в `_community_summaries` с level в metadata
- [ ] Без LLM → communities сохранены, summary="" (не ошибка)
- [ ] Без embed → summary в SQL, не в vector (не ошибка)
- [ ] Community > 50 members → top-50 by degree, prompt содержит "(top 50 of N)"
- [ ] LLM timeout на 1 community → остальные продолжают
- [ ] LLMCache используется — повторный cognify не re-summarize unchanged communities

### 3.4 COMMUNITY_LOCAL search
- [ ] Находит community для entity, возвращает enriched context
- [ ] Если entity не в community → fallback к GRAPH_COMPLETION
- [ ] Response содержит community context + LLM answer

### 3.5 COMMUNITY_GLOBAL search
- [ ] Map-reduce: partial answers → synthesis
- [ ] Если нет communities → fallback к GRAPH_COMPLETION
- [ ] Если нет LLM → fallback к CHUNKS
- [ ] Concurrent map phase (semaphore=3)
- [ ] NOT_RELEVANT filter: LLM partial answers с "NOT_RELEVANT" отбрасываются
- [ ] Single community match → answer без reduce step
- [ ] Response содержит `communities_used`, `total_communities_searched`

### 3.6 Router
- [ ] "give me an overview" → COMMUNITY_GLOBAL (при HasCommunities + HasLLM)
- [ ] "what entities are related to X" → COMMUNITY_LOCAL
- [ ] "find function X" → НЕ community search
- [ ] HasCommunities=false → router не выбирает community strategies

### 3.7 Incremental
- [ ] Второй cognify run с <20% новых nodes → incremental (не full recompute)
- [ ] Второй cognify run с >20% новых nodes → full recompute
- [ ] Первый cognify run → full compute (no existing partition)
- [ ] Incremental корректен: communities содержат новые nodes

### 3.8 Интеграция
- [ ] Phase 1 тесты проходят (zero regressions)
- [ ] go build / go vet без ошибок
- [ ] cognify_status показывает stage="communities" во время Stage 5

---

## 4. Риски и митигации

### R1: Louvain на полном графе при каждом cognify
**Риск:** Graph 10K+ nodes → Louvain ~500ms + summarization ~30s (LLM calls). Cognify становится медленным.
**Митигация:**
- Incremental update (2.8) — Phase 1 only для <20% новых nodes
- Stage 5 context timeout: 60s. Если timeout → log warning, pipeline COMPLETED без communities
- Progress reporting: user видит "detecting communities" / "summarizing 5/12" в cognify_status
- Summarization concurrent (semaphore=3): 12 communities = 4 batches × ~3s = ~12s вместо 36s

### R2: Modularity = 0 для маленьких графов
**Риск:** 5 nodes, 4 edges → modularity near 0, все в 1 community. Бесполезный результат.
**Митигация:** Минимальный порог: если nodes < 6, skip community detection. Документировать: "community detection полезен при 10+ entities".

### R3: Hierarchical summary quality
**Риск:** Level 1+ summaries (summary of summaries) теряют конкретику.
**Митигация:**
- Level 0 prompt: конкретные entities + edges
- Level 1+ prompt: child summaries + ключевые entities из крупнейшего child
- Max 3 levels (больше → слишком абстрактно)

### R4: community_members таблица может стать большой
**Риск:** 10K nodes × 3 levels = 30K rows в community_members.
**Митигация:** Индекс `(node_id, level)` — O(1) lookup. 30K rows — тривиально для SQLite/PG. Cleanup при ReplaceCommunities.

### R5: Concurrent summarization + LLM rate limits
**Риск:** 3 concurrent LLM calls могут превысить rate limit (Ollama, OpenAI).
**Митигация:** Semaphore=3 (configurable). LLM provider уже обрабатывает 429 retries (если реализовано). Fallback: sequential при ошибках.

### R6: Stale communities after node deletion
**Риск:** Node удалён через delete, но остаётся в community_members.
**Митигация:** ReplaceCommunities при следующем cognify пересчитает всё. Для manual cleanup: `PRUNE` tool уже чистит graph_nodes → добавить каскадную очистку community_members.

### R7: _community_summaries коллекция не синхронизирована
**Риск:** Communities пересчитаны, но старые embeddings в vector collection.
**Митигация:** ReplaceCommunities удаляет все записи из `_community_summaries` перед записью новых. Используем `summary_embedding_id` для точечного удаления.

---

## 5. Corner Cases

### 5.1 Louvain Algorithm
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Пустой граф | 0 nodes, 0 edges | Dendrogram{Levels: nil}, modularity 0 | T1 |
| 1 node, 0 edges | Isolated node | 1 singleton community, level 0 | T2 |
| 2 nodes, 1 edge | A—B | 1 community {A,B}, modularity > 0 | T3 |
| 2 disconnected | A, B no edge | 2 singletons, modularity 0 | T4 |
| Triangle | A—B, B—C, A—C | 1 community | T5 |
| Barbell (2×5+bridge) | Two 5-cliques connected by 1 edge | 2 communities | T6 |
| Zachary karate club | 34 nodes, 78 edges | 2-4 communities, Q > 0.35 | T7 |
| Star | Hub + 10 spokes | 1 community (hub pulls all) | T8 |
| Self-loop | A—A | Ignored, same as no edges | T9 |
| Duplicate edges | A—B twice, weight 1 each | Merged to weight 2 | T10 |
| Weighted | A—B(10), B—C(1) | A,B together; C may separate | T11 |
| 1K nodes performance | Random graph | < 50ms | T12 |
| 10K nodes performance | Random graph | < 500ms | T13 |
| Deterministic | Same graph × 2 | Identical dendrograms | T14 |
| Resolution γ=2.0 | Barbell graph | More communities than γ=1.0 | T15 |
| Resolution γ=0.5 | Barbell graph | Fewer communities than γ=1.0 | T16 |
| Multi-level | 3-level graph (3 groups of 3 cliques) | Dendrogram with 2+ levels | T17 |
| Phase 2 correctness | After compression, Q ≥ Q before | Modularity non-decreasing | T18 |
| All-connected (complete graph) | K₁₀ | 1 community, modularity ~0 | T19 |
| Chain graph | A—B—C—D—E—F—G | 1-3 communities depending on γ | T20 |

### 5.2 Hierarchical Summaries
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| LLM nil | Communities detected | summary="" for all, communities saved | T21 |
| Embed nil | LLM OK | Summary in SQL, not in vector | T22 |
| Community > 50 members | 100-node community | Top-50, prompt has "(top 50 of 100)" | T23 |
| Community < MinMembers | 2-node community | Not summarized | T24 |
| Empty descriptions | Nodes without description | Summary from names only | T25 |
| Level 1 summary | 3 child communities with summaries | Summary of summaries | T26 |
| LLM timeout on 1 community | 5 communities, 1 timeout | 4 summarized, 1 empty | T27 |
| LLMCache hit | Re-cognify same data | No new LLM calls for existing summaries | T28 |

### 5.3 COMMUNITY_LOCAL search
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| Entity in community | "how does X work" | Community context + answer | T29 |
| Entity NOT in community | New entity | Fallback to GRAPH_COMPLETION | T30 |
| Entity in multiple levels | Level 0 and level 1 | Use level 0 (finest) | T31 |

### 5.4 COMMUNITY_GLOBAL search
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| No community summaries | Empty _community_summaries | Fallback GRAPH_COMPLETION | T32 |
| No LLM | search_type=COMMUNITY_GLOBAL | Fallback CHUNKS | T33 |
| 1 relevant community | Query matches 1 | Answer without reduce | T34 |
| 5 relevant communities | Query matches 5 | Map-reduce synthesized | T35 |
| All NOT_RELEVANT | 3 communities, all irrelevant | "No relevant communities" | T36 |
| Concurrent partial answers | 5 communities | sem=3 → 2 batches | T37 |

### 5.5 Router
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| "overview of all topics" | HasCommunities=true | COMMUNITY_GLOBAL | T38 |
| "overview of all topics" | HasCommunities=false | SUMMARIES (no communities) | T39 |
| "how does auth work" | HasCommunities=true | COMMUNITY_LOCAL or GRAPH_COMPLETION | T40 |
| "find function X" | HasCommunities=true | NOT community | T41 |

### 5.6 Pipeline integration
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| mode=rag | cognify mode=rag | Stage 5 skipped | T42 |
| <6 entities | Very short text | Stage 5 skipped | T43 |
| Stage 5 timeout | LLM hangs during summarization | Communities saved, summaries partial | T44 |
| cognify_status | During Stage 5 | Stage = "communities" | T45 |
| Re-cognify | Second cognify on same data | Communities replaced, summaries refreshed | T46 |

### 5.7 Incremental
| Case | Input | Expected | Тест |
|------|-------|----------|------|
| First cognify (empty DB) | No existing partition | Full compute | T47 |
| <20% new nodes | 100 existing, 15 new | Incremental Phase 1 only | T48 |
| >20% new nodes | 100 existing, 50 new | Full recompute | T49 |
| New node merges into community | New entity linked to existing cluster | Appears in correct community | T50 |

---

## 6. Тесты

### 6.1 Go unit: Louvain (`pkg/community/louvain_test.go`)

```
TestLouvain_EmptyGraph                     — T1
TestLouvain_SingleNode                     — T2
TestLouvain_TwoNodesOneEdge               — T3
TestLouvain_TwoDisconnected               — T4
TestLouvain_Triangle                       — T5
TestLouvain_Barbell                        — T6
TestLouvain_ZacharyKarateClub             — T7: reference dataset, Q > 0.35
TestLouvain_Star                           — T8
TestLouvain_SelfLoopIgnored               — T9
TestLouvain_DuplicateEdgesMerged          — T10
TestLouvain_WeightedEdges                 — T11
TestLouvain_1K_Performance                — T12: < 50ms
TestLouvain_10K_Performance               — T13: < 500ms
TestLouvain_Deterministic                 — T14
TestLouvain_ResolutionHigher              — T15: γ=2.0 → more communities
TestLouvain_ResolutionLower               — T16: γ=0.5 → fewer communities
TestLouvain_MultiLevel                    — T17: dendrogram с 2+ levels
TestLouvain_Phase2_ModularityNonDecreasing — T18
TestLouvain_CompleteGraph                 — T19: K₁₀ → 1 community
TestLouvain_ChainGraph                    — T20
TestModularity_Known                      — verify Q computation against hand-calculated values
TestDeltaQ_Correctness                    — verify ΔQ matches actual Q difference
TestBuildGraph_Undirected                 — edges added both ways
TestBuildGraph_WeightMerging              — parallel edges merged
```

### 6.2 Go unit: Summarize (`pkg/community/summarize_test.go`)

```
TestWriteCommunities_Roundtrip            — write → read, all fields
TestWriteCommunities_Replace              — old deleted, new inserted
TestWriteCommunities_CommunityMembers     — join table populated
TestSummarize_NoLLM                       — T21
TestSummarize_NoEmbed                     — T22
TestSummarize_LargeCommunity              — T23
TestSummarize_MinMembers                  — T24
TestSummarize_HierarchicalLevel1          — T26
TestSummarize_LLMTimeout                  — T27: httptest slow server
TestSummarize_CacheHit                    — T28: cached LLM response
TestLookupCommunities                     — node → community ID via community_members
```

### 6.3 Go unit: Incremental (`pkg/community/incremental_test.go`)

```
TestIncremental_FirstRun_FullCompute      — T47
TestIncremental_SmallDelta_PhaseOnly      — T48
TestIncremental_LargeDelta_FullRecompute  — T49
TestIncremental_NewNodeMerged             — T50
```

### 6.4 Go unit: Router (`pkg/router/router_test.go` — дополнение)

```
TestRoute_OverviewWithCommunities         — T38
TestRoute_OverviewWithoutCommunities      — T39
TestRoute_SpecificWithCommunities         — T40
TestRoute_CodeQueryNotCommunity           — T41
```

### 6.5 Go benchmark (`pkg/community/louvain_bench_test.go`)

```
BenchmarkLouvain_100
BenchmarkLouvain_1000
BenchmarkLouvain_10000
BenchmarkBuildGraphFromSlice_1000
BenchmarkModularity_1000
```

### 6.6 Python MCP integration (`tests/test_mcp_communities.py`)

```python
class TestCommunityDetection:
    test_cognify_creates_communities        — T45: Stage 5 runs, list_communities non-empty
    test_list_communities_levels            — multiple levels returned
    test_list_communities_with_members      — include_members=true
    test_get_community_detail               — specific community with edges
    test_rag_mode_no_communities            — T42: mode=rag → no communities
    test_small_text_no_communities          — T43: <6 entities → skipped

class TestCommunityGlobalSearch:
    test_global_explicit                    — T35: map-reduce answer
    test_global_fallback_no_summaries       — T32: fallback
    test_global_fallback_no_llm             — T33: fallback to CHUNKS
    test_global_auto_routing                — T38: AUTO → COMMUNITY_GLOBAL

class TestCommunityLocalSearch:
    test_local_explicit                     — T29: community context answer
    test_local_fallback                     — T30: entity not in community

class TestCommunityRegression:
    test_phase1_rag_mode_still_works        — Phase 1 unaffected
    test_phase1_sliding_still_works         — Phase 1 unaffected
    test_phase1_rerank_still_works          — Phase 1 unaffected
    test_smoke_palace_still_pass            — Existing tests unaffected
```

### 6.7 Test data

```python
PHYSICS_TEXT = """
Einstein worked at the Institute for Advanced Study in Princeton from 1933 until his death in 1955.
He collaborated with Nathan Rosen on the EPR paradox paper, and with Boris Podolsky.
Niels Bohr responded to the EPR paper with his own interpretation of quantum mechanics.
Max Planck, who originated quantum theory, awarded Einstein the Max Planck Medal in 1929.
Werner Heisenberg developed the uncertainty principle and matrix mechanics at the University of Copenhagen.
Erwin Schrodinger proposed wave mechanics as an alternative formulation while at the University of Zurich.
Paul Dirac unified quantum mechanics with special relativity in the Dirac equation at Cambridge.
Richard Feynman later developed quantum electrodynamics at Caltech, building on Dirac's work.
The Copenhagen interpretation, championed by Bohr at the Niels Bohr Institute, became standard.
John Bell proposed Bell's theorem to test local hidden variables at CERN in 1964.
"""
```

~10 person entities, ~6 organization entities, ~15 edges → достаточно для 2-4 communities (physicists cluster by collaboration/institution).

**Zachary karate club graph** (для Go unit test) — hardcoded 34 nodes, 78 edges, reference modularity Q=0.38-0.42.

---

## 7. Порядок реализации

```
Step 1: pkg/community/louvain.go + louvain_test.go + louvain_bench_test.go
        Полный Louvain: Phase 1 + Phase 2 + multi-level + resolution.
        Graph struct + BuildGraph + modularity + deltaQ.
        DoD: T1-T20 green, benchmarks < 50ms/500ms.

Step 2: internal/http/schema.go — graph_communities + community_members DDL
        PG + SQLite варианты. MigrateSchema.
        DoD: таблицы создаются, idempotent.

Step 3: pkg/community/summarize.go + summarize_test.go
        WriteCommunities + ReplaceCommunities + SummarizeCommunities +
        SummarizeHierarchy + LookupCommunities.
        DoD: T21-T28 green.

Step 4: pkg/community/incremental.go + incremental_test.go
        IncrementalUpdate — Phase 1 only for small deltas.
        DoD: T47-T50 green.

Step 5: pkg/orchestrator/pipeline.go — Stage 5
        Community detection + summarization в cognify pipeline.
        DoD: go build, cognify → communities created.

Step 6: internal/http/graph_search.go — communityLocalSearch + communityGlobalSearch
        Два новых search handler.
        DoD: T29-T37 green.

Step 7: pkg/router/router.go — COMMUNITY_LOCAL + COMMUNITY_GLOBAL signals
        HasCommunities capability + routing heuristics.
        DoD: T38-T41 green.

Step 8: internal/http/mcp.go + api.go — MCP API
        list_communities + get_community tools.
        COMMUNITY_LOCAL + COMMUNITY_GLOBAL в search dispatch.
        community_resolution в cognify.
        DoD: go build + go vet.

Step 9: tests/test_mcp_communities.py
        Integration tests.
        DoD: все MCP tests green.

Step 10: Full regression
         Phase 1 + Phase 2 + smoke + palace.
         DoD: 0 new failures.
```

---

## 8. Файлы (итог)

**Новые:**
- `pkg/community/louvain.go` — ~300 LOC (Graph, Louvain, Phase 1, Phase 2, modularity)
- `pkg/community/louvain_test.go` — 20+ unit tests + Zachary karate club
- `pkg/community/louvain_bench_test.go` — benchmarks
- `pkg/community/summarize.go` — ~250 LOC (Write, Replace, Summarize, Hierarchy)
- `pkg/community/summarize_test.go` — 10+ tests
- `pkg/community/incremental.go` — ~100 LOC
- `pkg/community/incremental_test.go` — 4 tests
- `tests/test_mcp_communities.py` — 15+ integration tests

**Изменённые:**
- `internal/http/schema.go` — +graph_communities + community_members DDL
- `pkg/orchestrator/pipeline.go` — Stage 5
- `internal/http/graph_search.go` — communityLocalSearch + communityGlobalSearch
- `pkg/router/router.go` — HasCommunities + COMMUNITY_LOCAL/GLOBAL signals
- `internal/http/mcp.go` — list_communities + get_community + search_type + cognify params
- `internal/http/api.go` — case "COMMUNITY_LOCAL", "COMMUNITY_GLOBAL" в switch
