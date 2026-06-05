# Memory Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a reversible background "System2" memory-consolidation layer to Levara that collapses clusters of near-duplicate / closely-related memory-palace records into a single record, raising density and reducing recall noise.

**Architecture:** A pure, unit-testable engine package `pkg/consolidate` plans the work (cluster → classify → merge/abstract) over small interfaces (Embedder, Neighbors, Summarizer, Store). MCP handlers in `pkg/mcp` adapt the existing `Deps` to those interfaces and apply the plan. Originals are never deleted — they get `superseded_by` + `valid_until` set (the same temporal pattern already used on graph edges) and are hidden from recall but physically retained, so a run can be fully reverted. A background janitor (built on `pkg/runreg`) and two MCP tools (`consolidate`, `consolidation_revert`) drive it.

**Tech Stack:** Go, Postgres/SQLite (`memories` table), existing `pkg/embed`, `pkg/llm`, `pkg/runreg`, `internal/metrics` (Prometheus), `pkg/mcp` tool framework. Tests: standard `testing`, table-driven, no testify, `fakeDeps` + temp SQLite (matches existing `pkg/mcp/tool_save_recall_memory_test.go`).

**Spec:** `docs/superpowers/specs/2026-06-01-memory-consolidation-design.md`

---

## File Structure

**New files:**
- `Levara/pkg/consolidate/types.go` — engine data types (`MemoryRecord`, `SimEdge`, `Cluster`, `Action`, `Config`).
- `Levara/pkg/consolidate/cluster.go` — union-find connected-components over thresholded similarity edges.
- `Levara/pkg/consolidate/cluster_test.go`
- `Levara/pkg/consolidate/plan.go` — `Plan()`: classify clusters into mechanical-merge vs LLM-abstract actions.
- `Levara/pkg/consolidate/plan_test.go`
- `Levara/pkg/consolidate/abstract.go` — LLM abstraction + anti-hallucination coverage guard.
- `Levara/pkg/consolidate/abstract_test.go`
- `Levara/pkg/mcp/tool_consolidate.go` — `ToolConsolidate`, `ToolConsolidationRevert`, Deps→engine adapters.
- `Levara/pkg/mcp/tool_consolidate_test.go`

**Modified files:**
- `Levara/internal/http/schema.go` — add consolidation columns to `memories` (migration).
- `Levara/pkg/mcp/tool_save_recall_memory.go` — hide superseded records in recall (default).
- `Levara/internal/http/memories.go` — hide superseded records in list/wake_up (default).
- `Levara/pkg/mcp/tools.go` — register `consolidate` + `consolidation_revert` descriptors.
- `Levara/internal/http/mcp.go` — dispatch + handler wrappers for the two tools.
- `Levara/internal/metrics/telemetry.go` — consolidation metrics.
- `Levara/cmd/server/main.go` — start the consolidation janitor (background loop).

---

## Conventions

- All `go` / `go test` commands run from `Levara/` (the module root).
- Cosine: the `_memories` vector collection stores L2-normalized vectors; `CollectionSearch` returns `SearchResult` whose `Score` is cosine similarity in `[-1, 1]` (higher = more similar). **Task 5 Step 0 verifies this** before relying on it.
- Thresholds (config defaults): `TauLow = 0.85` (cluster edge), `TauHigh = 0.97` (mechanical-merge gate).

---

## Task 1: Schema — consolidation columns on `memories`

**Files:**
- Modify: `Levara/internal/http/schema.go` (the `memories` `CREATE TABLE` near line 264, plus migration section)

- [ ] **Step 1: Read the current schema + migration mechanism**

Read `Levara/internal/http/schema.go`. Confirm: (a) the `memories` `CREATE TABLE IF NOT EXISTS` block (≈ line 264), and (b) how the file applies additive column migrations to existing tables (search for `ADD COLUMN` in the file). Match whatever idempotent pattern already exists. If the file already does `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` for other tables, follow that exactly.

- [ ] **Step 2: Add columns to the base `CREATE TABLE memories`**

Add these five columns to the `memories` create statement (so fresh DBs have them):

```sql
superseded_by TEXT NOT NULL DEFAULT '',
valid_until TIMESTAMPTZ,
consolidated_from TEXT NOT NULL DEFAULT '',
consolidation_run_id TEXT NOT NULL DEFAULT '',
tier TEXT NOT NULL DEFAULT 'raw'
```

Semantics:
- `superseded_by` — id of the record that replaced this one (`''` = active).
- `valid_until` — when this record stopped being current (NULL = open-ended).
- `consolidated_from` — JSON array of source ids on a generated semantic record (`''` otherwise).
- `consolidation_run_id` — run id stamped on BOTH generated records and superseded sources (drives revert).
- `tier` — `raw` | `consolidated` | `semantic`.

- [ ] **Step 3: Add an idempotent migration for existing DBs**

Following the pattern confirmed in Step 1, add additive migrations. For Postgres these are safe and idempotent:

```sql
ALTER TABLE memories ADD COLUMN IF NOT EXISTS superseded_by TEXT NOT NULL DEFAULT '';
ALTER TABLE memories ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS consolidated_from TEXT NOT NULL DEFAULT '';
ALTER TABLE memories ADD COLUMN IF NOT EXISTS consolidation_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE memories ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'raw';
```

SQLite has no `ADD COLUMN IF NOT EXISTS`; if the file supports SQLite, guard each `ALTER` by checking `PRAGMA table_info(memories)` first (or reuse the file's existing helper for this). Use `deps.Q()`-style rewriting if the file already routes DDL through it.

- [ ] **Step 4: Build to verify it compiles**

Run: `go build ./...`
Expected: success, no errors.

- [ ] **Step 5: Commit**

```bash
git add Levara/internal/http/schema.go
git commit -m "feat(consolidate): add supersede/tier columns to memories table"
```

---

## Task 2: Engine types

**Files:**
- Create: `Levara/pkg/consolidate/types.go`

- [ ] **Step 1: Write the types**

```go
// Package consolidate plans and applies memory-consolidation: collapsing
// clusters of near-duplicate / related memory records into one, reversibly.
package consolidate

import "time"

// MemoryRecord is the minimal projection of a memories row the engine needs.
type MemoryRecord struct {
	ID        string
	Key       string
	Value     string
	Room      string
	Hall      string
	CreatedAt time.Time
}

// SimEdge is an undirected similarity edge between two candidate records.
type SimEdge struct {
	A, B  string
	Score float64 // cosine similarity in [-1, 1]
}

// Cluster is a connected component of candidate record ids plus its internal edges.
type Cluster struct {
	IDs   []string
	Edges []SimEdge
}

// ActionKind discriminates the two consolidation strategies.
type ActionKind string

const (
	ActionMerge    ActionKind = "merge"    // deterministic: keep newest, supersede rest
	ActionAbstract ActionKind = "abstract" // LLM: synthesize a new semantic record, supersede all
)

// Action is one planned consolidation operation over a cluster.
type Action struct {
	Kind       ActionKind
	SurvivorID string   // merge: the kept (newest) record id; abstract: "" (a new record is created)
	NewValue   string   // abstract: synthesized text; merge: ""
	SourceIDs  []string // records to supersede
	Room       string
	Hall       string
}

// Config holds tunable thresholds.
type Config struct {
	TauLow  float64 // cluster edge threshold
	TauHigh float64 // mechanical-merge gate
	TopK    int     // neighbors fetched per candidate when building the graph
}

// DefaultConfig returns the production defaults.
func DefaultConfig() Config {
	return Config{TauLow: 0.85, TauHigh: 0.97, TopK: 8}
}
```

- [ ] **Step 2: Build**

Run: `go build ./pkg/consolidate/`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add Levara/pkg/consolidate/types.go
git commit -m "feat(consolidate): engine types"
```

---

## Task 3: Clustering (union-find connected components)

**Files:**
- Create: `Levara/pkg/consolidate/cluster.go`
- Test: `Levara/pkg/consolidate/cluster_test.go`

- [ ] **Step 1: Write the failing test**

```go
package consolidate

import (
	"sort"
	"testing"
)

func sortedIDs(c Cluster) []string {
	out := append([]string(nil), c.IDs...)
	sort.Strings(out)
	return out
}

func TestClusterComponents_GroupsByThreshold(t *testing.T) {
	edges := []SimEdge{
		{A: "a", B: "b", Score: 0.99}, // a-b tight
		{A: "b", B: "c", Score: 0.90}, // b-c above tau_low
		{A: "d", B: "e", Score: 0.80}, // below tau_low -> dropped
	}
	clusters := ClusterComponents(edges, 0.85)

	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1 (only a,b,c connected)", len(clusters))
	}
	got := sortedIDs(clusters[0])
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("cluster members = %v, want %v", got, want)
	}
	// Only the two surviving edges belong to the cluster.
	if len(clusters[0].Edges) != 2 {
		t.Fatalf("cluster edges = %d, want 2", len(clusters[0].Edges))
	}
}

func TestClusterComponents_DropsSingletons(t *testing.T) {
	edges := []SimEdge{{A: "x", B: "y", Score: 0.50}} // below threshold
	clusters := ClusterComponents(edges, 0.85)
	if len(clusters) != 0 {
		t.Fatalf("got %d clusters, want 0 (no edge survives)", len(clusters))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/consolidate/ -run TestClusterComponents -v`
Expected: FAIL — `undefined: ClusterComponents`.

- [ ] **Step 3: Write the implementation**

```go
package consolidate

// ClusterComponents keeps edges with Score >= tauLow and returns the connected
// components (size >= 2) as clusters, each carrying its surviving internal edges.
func ClusterComponents(edges []SimEdge, tauLow float64) []Cluster {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	var kept []SimEdge
	for _, e := range edges {
		if e.Score < tauLow {
			continue
		}
		union(e.A, e.B)
		kept = append(kept, e)
	}

	idsByRoot := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range kept {
		for _, id := range []string{e.A, e.B} {
			if !seen[id] {
				seen[id] = true
				r := find(id)
				idsByRoot[r] = append(idsByRoot[r], id)
			}
		}
	}
	edgesByRoot := map[string][]SimEdge{}
	for _, e := range kept {
		r := find(e.A)
		edgesByRoot[r] = append(edgesByRoot[r], e)
	}

	var clusters []Cluster
	for root, ids := range idsByRoot {
		if len(ids) < 2 {
			continue
		}
		clusters = append(clusters, Cluster{IDs: ids, Edges: edgesByRoot[root]})
	}
	return clusters
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/consolidate/ -run TestClusterComponents -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add Levara/pkg/consolidate/cluster.go Levara/pkg/consolidate/cluster_test.go
git commit -m "feat(consolidate): union-find clustering"
```

---

## Task 4: Plan — classify clusters into merge/abstract actions

**Files:**
- Create: `Levara/pkg/consolidate/plan.go`
- Test: `Levara/pkg/consolidate/plan_test.go`

The `Plan` function is pure: given records, their similarity clusters, and config, it produces `[]Action`. Abstraction text is filled later (it needs the LLM), so `Plan` marks abstract actions with empty `NewValue` and the caller fills them.

- [ ] **Step 1: Write the failing test**

```go
package consolidate

import (
	"testing"
	"time"
)

func recsByID() map[string]MemoryRecord {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return map[string]MemoryRecord{
		"a": {ID: "a", Value: "Pi runs potion sidecar on 9101", Room: "infra", Hall: "fact", CreatedAt: t0},
		"b": {ID: "b", Value: "Pi runs potion sidecar on 9101", Room: "infra", Hall: "fact", CreatedAt: t0.Add(time.Hour)},
		"c": {ID: "c", Value: "potion model is 256-dim", Room: "infra", Hall: "fact", CreatedAt: t0.Add(2 * time.Hour)},
	}
}

func TestPlan_TightClusterMerges_NewestSurvives(t *testing.T) {
	recs := recsByID()
	clusters := []Cluster{{
		IDs:   []string{"a", "b"},
		Edges: []SimEdge{{A: "a", B: "b", Score: 0.985}}, // >= TauHigh
	}}
	actions := Plan(recs, clusters, DefaultConfig())

	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	act := actions[0]
	if act.Kind != ActionMerge {
		t.Fatalf("kind = %s, want merge", act.Kind)
	}
	if act.SurvivorID != "b" {
		t.Fatalf("survivor = %s, want b (newest)", act.SurvivorID)
	}
	if len(act.SourceIDs) != 1 || act.SourceIDs[0] != "a" {
		t.Fatalf("sources = %v, want [a]", act.SourceIDs)
	}
}

func TestPlan_LooseClusterAbstracts(t *testing.T) {
	recs := recsByID()
	clusters := []Cluster{{
		IDs:   []string{"a", "c"},
		Edges: []SimEdge{{A: "a", B: "c", Score: 0.88}}, // between TauLow and TauHigh
	}}
	actions := Plan(recs, clusters, DefaultConfig())

	if len(actions) != 1 || actions[0].Kind != ActionAbstract {
		t.Fatalf("actions = %+v, want one abstract action", actions)
	}
	if len(actions[0].SourceIDs) != 2 {
		t.Fatalf("abstract sources = %v, want both a,c", actions[0].SourceIDs)
	}
	if actions[0].NewValue != "" {
		t.Fatalf("NewValue should be empty until LLM fills it, got %q", actions[0].NewValue)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/consolidate/ -run TestPlan -v`
Expected: FAIL — `undefined: Plan`.

- [ ] **Step 3: Write the implementation**

```go
package consolidate

import "sort"

// Plan classifies each cluster into a consolidation Action.
// A cluster whose every internal edge >= TauHigh is a mechanical merge
// (keep newest record, supersede the rest). Otherwise it is an LLM abstraction
// (supersede all sources; NewValue is left empty for the caller to fill).
func Plan(recs map[string]MemoryRecord, clusters []Cluster, cfg Config) []Action {
	var actions []Action
	for _, c := range clusters {
		if len(c.IDs) < 2 {
			continue
		}
		ids := append([]string(nil), c.IDs...)
		sort.Strings(ids) // deterministic ordering

		if allTight(c.Edges, cfg.TauHigh) {
			survivor := newest(ids, recs)
			var sources []string
			for _, id := range ids {
				if id != survivor {
					sources = append(sources, id)
				}
			}
			actions = append(actions, Action{
				Kind:       ActionMerge,
				SurvivorID: survivor,
				SourceIDs:  sources,
				Room:       recs[survivor].Room,
				Hall:       recs[survivor].Hall,
			})
			continue
		}

		actions = append(actions, Action{
			Kind:      ActionAbstract,
			SourceIDs: ids,
			Room:      dominantRoom(ids, recs),
			Hall:      "semantic",
		})
	}
	return actions
}

func allTight(edges []SimEdge, tauHigh float64) bool {
	if len(edges) == 0 {
		return false
	}
	for _, e := range edges {
		if e.Score < tauHigh {
			return false
		}
	}
	return true
}

func newest(ids []string, recs map[string]MemoryRecord) string {
	best := ids[0]
	for _, id := range ids[1:] {
		if recs[id].CreatedAt.After(recs[best].CreatedAt) {
			best = id
		}
	}
	return best
}

func dominantRoom(ids []string, recs map[string]MemoryRecord) string {
	count := map[string]int{}
	for _, id := range ids {
		count[recs[id].Room]++
	}
	best, bestN := "", -1
	for room, n := range count {
		if n > bestN {
			best, bestN = room, n
		}
	}
	return best
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/consolidate/ -run TestPlan -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add Levara/pkg/consolidate/plan.go Levara/pkg/consolidate/plan_test.go
git commit -m "feat(consolidate): classify clusters into merge/abstract actions"
```

---

## Task 5: LLM abstraction + anti-hallucination coverage guard

**Files:**
- Create: `Levara/pkg/consolidate/abstract.go`
- Test: `Levara/pkg/consolidate/abstract_test.go`

The guard rejects a synthesized record that drops any number or capitalized entity token present in the sources, or that introduces a number absent from all sources. On rejection the cluster stays raw.

- [ ] **Step 1: Write the failing test**

```go
package consolidate

import (
	"context"
	"errors"
	"testing"
)

// fakeSummarizer returns a canned summary regardless of input.
type fakeSummarizer struct {
	out string
	err error
}

func (f fakeSummarizer) Summarize(_ context.Context, _ []string) (string, error) {
	return f.out, f.err
}

func TestAbstractValue_AcceptsFaithfulSummary(t *testing.T) {
	sources := []string{"Pi runs potion sidecar on 9101", "potion model is 256-dim"}
	s := fakeSummarizer{out: "Pi runs the potion sidecar on 9101; the model is 256-dim."}

	got, err := AbstractValue(context.Background(), s, sources)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == "" {
		t.Fatal("got empty summary")
	}
}

func TestAbstractValue_RejectsDroppedNumber(t *testing.T) {
	sources := []string{"Pi runs potion sidecar on 9101", "potion model is 256-dim"}
	s := fakeSummarizer{out: "Pi runs the potion sidecar."} // drops 9101 and 256

	_, err := AbstractValue(context.Background(), s, sources)
	if err == nil {
		t.Fatal("err = nil, want coverage failure (dropped numbers)")
	}
}

func TestAbstractValue_RejectsHallucinatedNumber(t *testing.T) {
	sources := []string{"potion model is 256-dim"}
	s := fakeSummarizer{out: "potion model is 256-dim and runs on port 9999."} // 9999 invented

	_, err := AbstractValue(context.Background(), s, sources)
	if err == nil {
		t.Fatal("err = nil, want hallucination failure (invented 9999)")
	}
}

func TestAbstractValue_PropagatesLLMError(t *testing.T) {
	s := fakeSummarizer{err: errors.New("llm down")}
	_, err := AbstractValue(context.Background(), s, []string{"x"})
	if err == nil {
		t.Fatal("err = nil, want propagated llm error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/consolidate/ -run TestAbstractValue -v`
Expected: FAIL — `undefined: AbstractValue` / `undefined: Summarizer`.

- [ ] **Step 3: Write the implementation**

```go
package consolidate

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Summarizer turns a cluster's source values into one consolidated statement.
type Summarizer interface {
	Summarize(ctx context.Context, sources []string) (string, error)
}

var (
	numberRe = regexp.MustCompile(`\d+`)
	// Capitalized multi-char tokens: crude entity proxy (Pi, Levara, DeepSeek...).
	entityRe = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]+\b`)
)

// AbstractValue calls the Summarizer and enforces the coverage guard:
//   - every number present in the sources must appear in the output;
//   - every number in the output must appear in some source (no invented numbers).
// On any violation (or LLM error) it returns an error and the caller leaves the
// cluster untouched.
func AbstractValue(ctx context.Context, s Summarizer, sources []string) (string, error) {
	out, err := s.Summarize(ctx, sources)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("consolidate: empty summary")
	}

	srcNums := tokenSet(numberRe, sources...)
	outNums := tokenSet(numberRe, out)

	for n := range srcNums {
		if !outNums[n] {
			return "", fmt.Errorf("consolidate: summary dropped source number %q", n)
		}
	}
	for n := range outNums {
		if !srcNums[n] {
			return "", fmt.Errorf("consolidate: summary invented number %q", n)
		}
	}
	return out, nil
}

func tokenSet(re *regexp.Regexp, texts ...string) map[string]bool {
	set := map[string]bool{}
	for _, t := range texts {
		for _, m := range re.FindAllString(t, -1) {
			set[m] = true
		}
	}
	return set
}
```

Note: `entityRe` is defined for a future entity-coverage check; numbers are the MVP guard. (If `go vet`/lint flags `entityRe` as unused, add a TODO-referencing test or remove it — keep the build clean.) To keep it used now, add an entity-coverage check mirroring numbers:

```go
	srcEnts := tokenSet(entityRe, sources...)
	outEnts := tokenSet(entityRe, out)
	for e := range srcEnts {
		if !outEnts[e] {
			return "", fmt.Errorf("consolidate: summary dropped source entity %q", e)
		}
	}
```

Insert that block before `return out, nil`. Update the faithful-summary test's canned output if needed so every capitalized source token (`Pi`) survives — the provided `out` already contains "Pi", so it passes.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/consolidate/ -run TestAbstractValue -v`
Expected: PASS (all four cases).

- [ ] **Step 5: Commit**

```bash
git add Levara/pkg/consolidate/abstract.go Levara/pkg/consolidate/abstract_test.go
git commit -m "feat(consolidate): LLM abstraction with coverage guard"
```

---

## Task 6: Hide superseded records in recall / list / wake_up

**Files:**
- Modify: `Levara/pkg/mcp/tool_save_recall_memory.go` (the SQL-LIKE recall query)
- Modify: `Levara/internal/http/memories.go` (list / wake_up queries)
- Test: `Levara/pkg/mcp/tool_save_recall_memory_test.go` (add a case)

- [ ] **Step 1: Write the failing test**

Add to `Levara/pkg/mcp/tool_save_recall_memory_test.go`. First update `setupSaveRecallMemoryDB` to include the new column in its `CREATE TABLE` (add `superseded_by TEXT DEFAULT ''` to the statement). Then:

```go
func TestToolRecallMemory_HidesSuperseded(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ctx := context.Background()

	// Two rows with the same searchable text; one is superseded.
	ToolSaveMemory(ctx, deps, map[string]any{"key": "active", "value": "potion sidecar fact"})
	ToolSaveMemory(ctx, deps, map[string]any{"key": "old", "value": "potion sidecar fact"})
	if _, err := deps.db.Exec(`UPDATE memories SET superseded_by = 'x' WHERE key = 'old'`); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}

	got := ToolRecallMemory(ctx, deps, map[string]any{"query": "potion sidecar"})
	if got.IsError {
		t.Fatalf("recall errored: %s", got.Content[0].Text)
	}
	if strings.Contains(got.Content[0].Text, "old") {
		t.Errorf("recall returned superseded row 'old': %s", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "active") {
		t.Errorf("recall dropped active row: %s", got.Content[0].Text)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mcp/ -run TestToolRecallMemory_HidesSuperseded -v`
Expected: FAIL — superseded row `old` still appears.

- [ ] **Step 3: Add the filter to the recall SQL**

In `Levara/pkg/mcp/tool_save_recall_memory.go`, find the SQL-LIKE recall query (the `WHERE (key LIKE ... OR value LIKE ...)` clause). Add `AND superseded_by = ''` to the `WHERE`. If the handler accepts an `include_superseded` arg, only append the clause when it is not true:

```go
includeSuperseded, _ := args["include_superseded"].(bool)
if !includeSuperseded {
	where += " AND superseded_by = ''"
}
```

Mirror the same `AND superseded_by = ''` default into the list and wake_up queries in `Levara/internal/http/memories.go` (pinned wake_up rows are never superseded, but add the clause for consistency).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/mcp/ -run TestToolRecallMemory -v`
Expected: PASS (new case + existing recall cases still green).

- [ ] **Step 5: Run the full mcp + http test packages**

Run: `go test ./pkg/mcp/ ./internal/http/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add Levara/pkg/mcp/tool_save_recall_memory.go Levara/pkg/mcp/tool_save_recall_memory_test.go Levara/internal/http/memories.go
git commit -m "feat(consolidate): hide superseded memories from recall/list/wake_up"
```

---

## Task 7: Engine driver + Deps adapters (`ToolConsolidate`)

This task wires the pure engine to live data. The engine driver `Run` is exercised with fakes; the MCP handler adapts `Deps`.

**Files:**
- Create: `Levara/pkg/consolidate/run.go` (driver + Store/Embedder/Neighbors interfaces)
- Create: `Levara/pkg/consolidate/run_test.go`
- Create: `Levara/pkg/mcp/tool_consolidate.go` (Deps adapters + `ToolConsolidate`)
- Create: `Levara/pkg/mcp/tool_consolidate_test.go`

- [ ] **Step 0: Verify the `SearchResult.Score` semantics**

Read `Levara/pkg/mcp/deps.go` (`SearchResult` definition) and `Levara/internal/store/hnsw.go` `Search`. Confirm `SearchResult.Score` is cosine similarity where higher = more similar. Record the exact field name/type; the adapter in Step 6 depends on it. If the score is a distance (lower = closer) instead, invert it in the adapter (`score = 1 - distance`).

- [ ] **Step 1: Write the failing test for the driver**

```go
package consolidate

import (
	"context"
	"testing"
	"time"
)

type fakeStore struct {
	recs      []MemoryRecord
	applied   []Action
	runID     string
}

func (f *fakeStore) Candidates(_ context.Context, _, _, _ string) ([]MemoryRecord, error) {
	return f.recs, nil
}
func (f *fakeStore) Apply(_ context.Context, runID string, actions []Action) error {
	f.runID = runID
	f.applied = append(f.applied, actions...)
	return nil
}

// fakeNeighbors returns a fixed edge set regardless of query.
type fakeNeighbors struct{ edges []SimEdge }

func (f fakeNeighbors) Edges(_ context.Context, _ []MemoryRecord, _ Config) ([]SimEdge, error) {
	return f.edges, nil
}

func TestRun_DryRunDoesNotApply(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "x", CreatedAt: t0},
		{ID: "b", Value: "x", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.99}}}

	res, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "x"},
		Cfg: DefaultConfig(), RunID: "run1", DryRun: true,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.Actions) != 1 || res.Actions[0].Kind != ActionMerge {
		t.Fatalf("planned actions = %+v, want one merge", res.Actions)
	}
	if len(store.applied) != 0 {
		t.Fatalf("dry run applied %d actions, want 0", len(store.applied))
	}
}

func TestRun_AppliesWhenNotDryRun(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &fakeStore{recs: []MemoryRecord{
		{ID: "a", Value: "x", CreatedAt: t0},
		{ID: "b", Value: "x", CreatedAt: t0.Add(time.Hour)},
	}}
	neigh := fakeNeighbors{edges: []SimEdge{{A: "a", B: "b", Score: 0.99}}}

	_, err := Run(context.Background(), Params{
		Store: store, Neighbors: neigh, Summarizer: fakeSummarizer{out: "x"},
		Cfg: DefaultConfig(), RunID: "run2", DryRun: false,
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if store.runID != "run2" || len(store.applied) != 1 {
		t.Fatalf("applied=%d runID=%q, want 1 action under run2", len(store.applied), store.runID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/consolidate/ -run TestRun -v`
Expected: FAIL — `undefined: Run` / `undefined: Params`.

- [ ] **Step 3: Write the driver**

```go
package consolidate

import "context"

// Store loads candidate records and applies/reverts consolidation actions.
type Store interface {
	// Candidates returns active (non-superseded, non-pinned) records in scope.
	Candidates(ctx context.Context, collection, room, hall string) ([]MemoryRecord, error)
	// Apply writes the actions under runID (creates semantic records, supersedes sources).
	Apply(ctx context.Context, runID string, actions []Action) error
}

// NeighborSource builds the similarity-edge set for the candidates.
type NeighborSource interface {
	Edges(ctx context.Context, recs []MemoryRecord, cfg Config) ([]SimEdge, error)
}

// Params bundles the inputs for one consolidation run.
type Params struct {
	Store      Store
	Neighbors  NeighborSource
	Summarizer Summarizer
	Cfg        Config
	Collection string
	Room       string
	Hall       string
	RunID      string
	DryRun     bool
}

// Result reports what a run planned (and, when not dry, applied).
type Result struct {
	Candidates int
	Clusters   int
	Actions    []Action
	Skipped    int // clusters dropped because abstraction failed the guard
}

// Run executes the full pipeline: load → edges → cluster → plan → (fill LLM) → apply.
func Run(ctx context.Context, p Params) (Result, error) {
	recs, err := p.Store.Candidates(ctx, p.Collection, p.Room, p.Hall)
	if err != nil {
		return Result{}, err
	}
	byID := make(map[string]MemoryRecord, len(recs))
	for _, r := range recs {
		byID[r.ID] = r
	}

	edges, err := p.Neighbors.Edges(ctx, recs, p.Cfg)
	if err != nil {
		return Result{}, err
	}
	clusters := ClusterComponents(edges, p.Cfg.TauLow)
	actions := Plan(byID, clusters, p.Cfg)

	res := Result{Candidates: len(recs), Clusters: len(clusters)}
	var final []Action
	for _, a := range actions {
		if a.Kind == ActionAbstract {
			sources := make([]string, 0, len(a.SourceIDs))
			for _, id := range a.SourceIDs {
				sources = append(sources, byID[id].Value)
			}
			val, err := AbstractValue(ctx, p.Summarizer, sources)
			if err != nil {
				res.Skipped++ // guard/LLM failure -> leave cluster raw
				continue
			}
			a.NewValue = val
		}
		final = append(final, a)
	}
	res.Actions = final

	if !p.DryRun && len(final) > 0 {
		if err := p.Store.Apply(ctx, p.RunID, final); err != nil {
			return res, err
		}
	}
	return res, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/consolidate/ -run TestRun -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit the engine**

```bash
git add Levara/pkg/consolidate/run.go Levara/pkg/consolidate/run_test.go
git commit -m "feat(consolidate): run driver over Store/Neighbors/Summarizer"
```

- [ ] **Step 6: Write the Deps adapters + `ToolConsolidate`**

Create `Levara/pkg/mcp/tool_consolidate.go`. The adapters implement the engine interfaces over `Deps`:
- `sqlStore` — `Candidates` runs `SELECT id,key,value,room,hall,created_at FROM memories WHERE collection_name=? AND superseded_by='' AND is_pinned=0 [AND room=?] [AND hall=?]`; `Apply` runs, per action, the writes in Step 7's helper.
- `collectionNeighbors` — for each record, `deps.Embed(value)` then `deps.CollectionSearch(memCollection, vec, cfg.TopK+1)`, emitting a `SimEdge` for each neighbor != self with `Score` from `SearchResult` (apply the inversion decided in Step 0 if needed). De-dupe undirected pairs.
- `llmSummarizer` — builds a `pkg/llm` provider from server config and calls `ChatCompletion` with a strict prompt:
  > "Combine the following memory notes into ONE concise statement. Preserve every fact, number, name, and port exactly. Do NOT add any information not present below. Notes:\n- ...".

```go
func ToolConsolidate(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return errResult("'collection' required")
	}
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	dryRun := true
	if v, ok := args["dry_run"].(bool); ok {
		dryRun = v
	}
	runID := newRunID() // reuse the uuid helper already used by save_memory

	res, err := consolidate.Run(ctx, consolidate.Params{
		Store:      &sqlStore{deps: deps},
		Neighbors:  &collectionNeighbors{deps: deps, collection: memCollectionName(collection)},
		Summarizer: &llmSummarizer{deps: deps},
		Cfg:        consolidate.DefaultConfig(),
		Collection: collection, Room: room, Hall: hall,
		RunID: runID, DryRun: dryRun,
	})
	if err != nil {
		metrics.ConsolidationRuns.WithLabelValues("error").Inc()
		return errResult("consolidate: " + err.Error())
	}
	metrics.ConsolidationRuns.WithLabelValues("ok").Inc()
	metrics.ConsolidationClusters.Add(float64(res.Clusters))
	metrics.ConsolidationActions.Add(float64(len(res.Actions)))

	mode := "applied"
	if dryRun {
		mode = "dry_run"
	}
	return okResult(fmt.Sprintf(
		"consolidate %s: run=%s candidates=%d clusters=%d actions=%d skipped=%d",
		mode, runID, res.Candidates, res.Clusters, len(res.Actions), res.Skipped))
}
```

Use the existing helpers for `errResult`/`okResult`/uuid that other tools in `pkg/mcp` already use (match their names — read `tool_save_recall_memory.go`). `memCollectionName(c)` returns `"_memories_"+c` (or `"_memories"` for the default) — match the naming used by `indexMemoryAsync` in `tool_save_recall_memory.go`.

- [ ] **Step 7: Write `sqlStore.Apply` (the supersede/write helper)**

```go
func (s *sqlStore) Apply(ctx context.Context, runID string, actions []consolidate.Action) error {
	for _, a := range actions {
		switch a.Kind {
		case consolidate.ActionMerge:
			// Supersede each source -> survivor.
			for _, src := range a.SourceIDs {
				if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
					`UPDATE memories SET superseded_by=?, valid_until=?, consolidation_run_id=? WHERE id=?`),
					a.SurvivorID, nowTS(), runID, src); err != nil {
					return err
				}
			}
		case consolidate.ActionAbstract:
			newID := newRunID()
			from, _ := json.Marshal(a.SourceIDs)
			if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
				`INSERT INTO memories (id,key,value,type,room,hall,tier,consolidated_from,consolidation_run_id,created_at,updated_at)
				 VALUES (?,?,?,?,?,?, 'semantic', ?, ?, ?, ?)`),
				newID, "consolidated:"+newID, a.NewValue, "project", a.Room, a.Hall,
				string(from), runID, nowTS(), nowTS()); err != nil {
				return err
			}
			for _, src := range a.SourceIDs {
				if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
					`UPDATE memories SET superseded_by=?, valid_until=?, consolidation_run_id=? WHERE id=?`),
					newID, nowTS(), runID, src); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
```

`nowTS()` returns an RFC3339 string (match how `tool_save_recall_memory.go` writes `created_at`). Reuse its exact timestamp format.

- [ ] **Step 8: Write the `ToolConsolidate` integration test (temp SQLite)**

In `Levara/pkg/mcp/tool_consolidate_test.go`, build a `fakeDeps` whose `memories` table includes the consolidation columns and whose `CollectionSearch`/`Embed` are stubbed so two rows are near-identical. Assert:
- `dry_run=true` returns an action summary but leaves all rows active (`superseded_by=''`).
- `dry_run=false` supersedes the older row (`superseded_by != ''`, `consolidation_run_id != ''`).

Follow the `setupSaveRecallMemoryDB` + `fakeDeps` pattern from `tool_save_recall_memory_test.go`. Stub `Embed` to return a fixed vector and `CollectionSearch` to return the sibling id with `Score=0.99`.

- [ ] **Step 9: Register the tool**

In `Levara/pkg/mcp/tools.go` add a descriptor to `ToolDescriptors()`:

```go
{
	Name:        "consolidate",
	Description:  "Consolidate near-duplicate/related memories in a collection: cluster, then merge (deterministic) or abstract (LLM). Reversible. dry_run defaults true.",
	OutputSchema: statusMessageSchema(),
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"collection": map[string]any{"type": "string", "description": "Collection to consolidate."},
			"room":       map[string]any{"type": "string", "description": "Optional room scope."},
			"hall":       map[string]any{"type": "string", "description": "Optional hall scope."},
			"dry_run":    map[string]any{"type": "boolean", "description": "Preview only, no writes. Default true."},
		},
		"required": []string{"collection"},
	},
},
```

In `Levara/internal/http/mcp.go` add to `executeToolInner` the collection-injection case (add `"consolidate"` to the switch that injects `sess.DefaultCollection`), a dispatch case `case "consolidate": return h.toolConsolidate(ctx, args)`, and the wrapper:

```go
func (h *mcpHandler) toolConsolidate(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolConsolidate(ctx, h, args)
}
```

- [ ] **Step 10: Run tests**

Run: `go test ./pkg/consolidate/ ./pkg/mcp/ -v`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add Levara/pkg/consolidate/ Levara/pkg/mcp/tool_consolidate.go Levara/pkg/mcp/tool_consolidate_test.go Levara/pkg/mcp/tools.go Levara/internal/http/mcp.go
git commit -m "feat(consolidate): consolidate MCP tool + Deps adapters"
```

---

## Task 8: `consolidation_revert` tool

**Files:**
- Modify: `Levara/pkg/mcp/tool_consolidate.go` (add `ToolConsolidationRevert` + `sqlStore.Revert`)
- Modify: `Levara/pkg/mcp/tool_consolidate_test.go` (round-trip test)
- Modify: `Levara/pkg/mcp/tools.go`, `Levara/internal/http/mcp.go` (register/dispatch)

- [ ] **Step 1: Write the failing round-trip test**

Add to `tool_consolidate_test.go`: run `ToolConsolidate` with `dry_run=false`, capture the `run=...` id from the result text, then call `ToolConsolidationRevert` with that id. Assert every previously-active row is active again (`superseded_by=''`) and no `tier='semantic'` rows remain for that run.

```go
func TestToolConsolidationRevert_RestoresState(t *testing.T) {
	deps := setupConsolidateDB(t) // helper from Task 7 Step 8
	ctx := context.Background()
	// ... seed two near-dup rows (see Task 7 Step 8) ...

	got := ToolConsolidate(ctx, deps, map[string]any{"collection": "proj", "dry_run": false})
	runID := parseRunID(t, got.Content[0].Text) // small test helper: regex `run=(\S+)`

	rev := ToolConsolidationRevert(ctx, deps, map[string]any{"run_id": runID})
	if rev.IsError {
		t.Fatalf("revert errored: %s", rev.Content[0].Text)
	}

	var active, semantic int
	deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by=''`).Scan(&active)
	deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE tier='semantic' AND consolidation_run_id=?`, runID).Scan(&semantic)
	if active != 2 {
		t.Errorf("active rows after revert = %d, want 2", active)
	}
	if semantic != 0 {
		t.Errorf("semantic rows after revert = %d, want 0", semantic)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/mcp/ -run TestToolConsolidationRevert -v`
Expected: FAIL — `undefined: ToolConsolidationRevert`.

- [ ] **Step 3: Implement revert**

```go
func (s *sqlStore) Revert(ctx context.Context, runID string) error {
	// 1. Reactivate superseded sources from this run.
	if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
		`UPDATE memories SET superseded_by='', valid_until=NULL, consolidation_run_id=''
		 WHERE consolidation_run_id=? AND superseded_by<>''`), runID); err != nil {
		return err
	}
	// 2. Delete generated semantic records from this run.
	if _, err := s.deps.DB().ExecContext(ctx, s.deps.Q(
		`DELETE FROM memories WHERE consolidation_run_id=? AND tier='semantic'`), runID); err != nil {
		return err
	}
	return nil
}

func ToolConsolidationRevert(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return errResult("'run_id' required")
	}
	if err := (&sqlStore{deps: deps}).Revert(ctx, runID); err != nil {
		return errResult("revert: " + err.Error())
	}
	metrics.ConsolidationRuns.WithLabelValues("revert").Inc()
	return okResult("consolidation reverted: run=" + runID)
}
```

- [ ] **Step 4: Register + dispatch**

Add a `consolidation_revert` descriptor to `ToolDescriptors()` (input: required `run_id` string), a dispatch case in `executeToolInner`, and the wrapper `toolConsolidationRevert` in `internal/http/mcp.go` (same shape as `toolConsolidate`).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/mcp/ -run 'TestToolConsolidat' -v`
Expected: PASS (consolidate + revert round-trip).

- [ ] **Step 6: Commit**

```bash
git add Levara/pkg/mcp/tool_consolidate.go Levara/pkg/mcp/tool_consolidate_test.go Levara/pkg/mcp/tools.go Levara/internal/http/mcp.go
git commit -m "feat(consolidate): consolidation_revert tool (reversible runs)"
```

---

## Task 9: Prometheus metrics

**Files:**
- Modify: `Levara/internal/metrics/telemetry.go`

- [ ] **Step 1: Add metric declarations**

In the `var (...)` block of `telemetry.go`, following the existing `promauto` pattern:

```go
ConsolidationRuns = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "levara_consolidation_runs_total",
	Help: "Consolidation runs by outcome (ok/error/revert).",
}, []string{"outcome"})

ConsolidationClusters = promauto.NewCounter(prometheus.CounterOpts{
	Name: "levara_consolidation_clusters_total",
	Help: "Clusters discovered across consolidation runs.",
})

ConsolidationActions = promauto.NewCounter(prometheus.CounterOpts{
	Name: "levara_consolidation_actions_total",
	Help: "Consolidation actions planned/applied (merge + abstract).",
})
```

These are referenced in Tasks 7–8. (The before/after density gauges from the spec are deferred to the janitor task where a full-collection count is available — see Task 10 Step 3.)

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add Levara/internal/metrics/telemetry.go
git commit -m "feat(consolidate): prometheus metrics"
```

---

## Task 10: Background janitor

**Files:**
- Create: `Levara/pkg/consolidate/janitor.go`
- Create: `Levara/pkg/consolidate/janitor_test.go`
- Modify: `Levara/cmd/server/main.go` (start the janitor)

- [ ] **Step 1: Write the failing test**

```go
package consolidate

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingRunner struct {
	mu sync.Mutex
	n  int
}

func (c *countingRunner) RunOnce(_ context.Context) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return nil
}

func TestJanitor_TicksThenStops(t *testing.T) {
	r := &countingRunner{}
	stop := StartJanitor(context.Background(), r, 10*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	stop()
	r.mu.Lock()
	n := r.n
	r.mu.Unlock()
	if n < 2 {
		t.Fatalf("janitor ticked %d times in 35ms@10ms, want >= 2", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/consolidate/ -run TestJanitor -v`
Expected: FAIL — `undefined: StartJanitor` / `undefined: Runner`.

- [ ] **Step 3: Implement the janitor**

```go
package consolidate

import (
	"context"
	"time"
)

// Runner performs one consolidation sweep (typically over all collections).
type Runner interface {
	RunOnce(ctx context.Context) error
}

// StartJanitor ticks the Runner every interval until the returned stop() is called.
// Mirrors pkg/runreg.StartJanitor's stop-func contract.
func StartJanitor(ctx context.Context, r Runner, interval time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = r.RunOnce(ctx) // errors are surfaced via metrics inside RunOnce
			}
		}
	}()
	return func() { close(done) }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/consolidate/ -run TestJanitor -v`
Expected: PASS.

- [ ] **Step 5: Wire a `Runner` over `Deps` and start it in main**

In `Levara/pkg/mcp/tool_consolidate.go` add a `Runner` that iterates `deps.ListCollections()` and calls `consolidate.Run(... DryRun:false)` per collection (skipping internal `_memories*` collections), recording before/after active-row counts into the deferred density gauges if you choose to add them. In `Levara/cmd/server/main.go`, after services are constructed, start it behind an env flag so it's opt-in:

```go
if os.Getenv("CONSOLIDATION_INTERVAL") != "" {
	d, err := time.ParseDuration(os.Getenv("CONSOLIDATION_INTERVAL"))
	if err == nil && d > 0 {
		stop := consolidate.StartJanitor(context.Background(), newConsolidationRunner(deps), d)
		defer stop()
	}
}
```

Document `CONSOLIDATION_INTERVAL` (unset = janitor off; e.g. `30m`) in `CLAUDE.md`'s env table as part of this step.

- [ ] **Step 6: Build + full test**

Run: `go build ./... && go test ./pkg/consolidate/ ./pkg/mcp/ ./internal/http/`
Expected: success / PASS.

- [ ] **Step 7: Commit**

```bash
git add Levara/pkg/consolidate/janitor.go Levara/pkg/consolidate/janitor_test.go Levara/pkg/mcp/tool_consolidate.go Levara/cmd/server/main.go Levara/CLAUDE.md
git commit -m "feat(consolidate): background janitor (opt-in via CONSOLIDATION_INTERVAL)"
```

---

## Task 11: End-to-end smoke + docs

**Files:**
- Modify: `CLAUDE.md` (root) — document the two MCP tools in the tools list and the env table.

- [ ] **Step 1: Full module test + vet**

Run: `go test ./... && go vet ./...`
Expected: PASS, no vet warnings (resolve any unused-symbol issues from Task 5's `entityRe`).

- [ ] **Step 2: Document the tools**

Update the project `CLAUDE.md`: add `consolidate` and `consolidation_revert` to the MCP tools list (Memory section), bump the tool count, and confirm `CONSOLIDATION_INTERVAL` is in the env table (from Task 10).

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "docs(consolidate): document consolidate/revert tools + env"
```

---

## Self-Review notes (for the implementer)

- **Spec coverage:** 5-stage pipeline → Tasks 3–7; data-model additions → Task 1; recall hides superseded → Task 6; dry_run default + tools → Tasks 7–8; revert → Task 8; metrics → Task 9; janitor → Task 10; safety (pinned excluded via `is_pinned=0` in `Candidates`; anti-hallucination guard in Task 5; atomic per-cluster — each Action applied independently) → Tasks 5,7.
- **Deferred from spec, intentionally:** Louvain (`pkg/community`) — the design offered "connected-components / Louvain"; this plan uses union-find connected components (simpler, deterministic, sufficient for near-dup merging). `char_density_before/after` gauges are noted as deferred in Task 9/10. Per-run LLM budget cap is not in the MVP loop (the janitor is opt-in and scoped per collection); add it when the janitor runs unattended at scale.
- **Idempotency:** `Candidates` excludes `superseded_by<>''` and `tier='semantic'` rows are not re-clustered with raw ones only if you also exclude them — add `AND tier='raw'` to the `Candidates` query so a second sweep is a no-op. (Apply this in Task 7 Step 6.)
```
