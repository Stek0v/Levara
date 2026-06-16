package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/llm"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// countingProvider counts ChatCompletion calls and returns a fixed, faithful
// summary. The test seeds lowercase number-free sources so the coverage guard
// never rejects — each abstract cluster therefore costs exactly one counted
// call, making the count a clean proxy for sweep-wide LLM spend.
type countingProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *countingProvider) ChatCompletion(_ context.Context, _ llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &llm.CompletionResponse{Content: "consolidated summary"}, nil
}

func (p *countingProvider) Name() string { return "counting" }

// seedDupInColl inserts a raw, non-superseded, unpinned memory row in an
// explicit collection (seedDup hardcodes 'levara').
func seedDupInColl(t *testing.T, deps *fakeDeps, id, collection, value, created string) {
	t.Helper()
	_, err := deps.db.Exec(
		`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, created_at, updated_at, superseded_by, tier)
		 VALUES (?, ?, ?, 'project', '', ?, '', '', 0, ?, ?, '', 'raw')`,
		id, "k-"+id, value, collection, created, created)
	if err != nil {
		t.Fatalf("seed %s/%s: %v", collection, id, err)
	}
}

// TestConsolidationRunner_SweepWideLLMBudget guards the per-sweep LLM cap: the
// janitor runs consolidate.Run on every collection with a PER-collection budget
// (DefaultConfig.MaxLLMCalls=24), so an N-collection sweep can fan out up to
// N×24 DeepSeek calls. With a sweep-wide cap of 2 across three abstract-only
// collections, the total must hold at 2 (one collection skipped) — without the
// cap the default budget would let all three fire.
func TestConsolidationRunner_SweepWideLLMBudget(t *testing.T) {
	deps := setupConsolidateDB(t)
	colls := []string{"c1", "c2", "c3"}
	for _, c := range colls {
		seedDupInColl(t, deps, c+"-a", c, "alpha apple", "2026-01-01T00:00:00Z")
		seedDupInColl(t, deps, c+"-b", c, "alpha apricot", "2026-01-02T00:00:00Z")
	}
	deps.collections = colls
	deps.hasColls = true
	deps.embedAvailable = true
	deps.embedFn = func(_ context.Context, _ string) ([]float32, error) {
		return []float32{1, 0, 0}, nil
	}
	// Each sidecar returns its own pair at an abstract-range score (0.92 ∈
	// [TauLow, TauHigh)), so every collection yields one abstract cluster.
	deps.searchFn = func(collection string, _ []float32, _ int) ([]SearchResult, error) {
		c := strings.TrimPrefix(collection, "_memories_")
		return []SearchResult{{ID: c + "-a", Score: 0.92}, {ID: c + "-b", Score: 0.92}}, nil
	}
	prov := &countingProvider{}
	deps.llmProvider = prov

	r := NewConsolidationRunner(deps, 2) // sweep cap = 2 LLM calls
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if prov.calls != 2 {
		t.Errorf("LLM calls across sweep = %d, want 2 (sweep-wide cap); per-collection default would allow 3", prov.calls)
	}
}

// TestConsolidationRunner_SweepBudgetZeroUnbounded: a zero/unset sweep cap keeps
// the legacy per-collection-only behaviour — every abstract collection fires.
func TestConsolidationRunner_SweepBudgetZeroUnbounded(t *testing.T) {
	deps := setupConsolidateDB(t)
	colls := []string{"c1", "c2", "c3"}
	for _, c := range colls {
		seedDupInColl(t, deps, c+"-a", c, "alpha apple", "2026-01-01T00:00:00Z")
		seedDupInColl(t, deps, c+"-b", c, "alpha apricot", "2026-01-02T00:00:00Z")
	}
	deps.collections = colls
	deps.hasColls = true
	deps.embedAvailable = true
	deps.embedFn = func(_ context.Context, _ string) ([]float32, error) {
		return []float32{1, 0, 0}, nil
	}
	deps.searchFn = func(collection string, _ []float32, _ int) ([]SearchResult, error) {
		c := strings.TrimPrefix(collection, "_memories_")
		return []SearchResult{{ID: c + "-a", Score: 0.92}, {ID: c + "-b", Score: 0.92}}, nil
	}
	prov := &countingProvider{}
	deps.llmProvider = prov

	r := NewConsolidationRunner(deps, 0) // 0 = unbounded sweep
	if err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if prov.calls != 3 {
		t.Errorf("LLM calls = %d, want 3 (no sweep cap → all collections fire)", prov.calls)
	}
}

// parseRunID extracts the run ID from a consolidate result string like
// "consolidate applied: run=<id> candidates=...".
func parseRunID(t *testing.T, text string) string {
	t.Helper()
	m := regexp.MustCompile(`run=(\S+)`).FindStringSubmatch(text)
	if len(m) < 2 {
		t.Fatalf("parseRunID: no run=<id> token in %q", text)
	}
	return m[1]
}

// setupConsolidateDB builds a memories table that includes the
// consolidation columns the tool reads/writes (tier, valid_until,
// consolidated_from, consolidation_run_id) plus the base/NOT-NULL
// columns the INSERT path touches.
func setupConsolidateDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-consolidate-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})

	stmt := `CREATE TABLE memories (
		id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT, owner_id TEXT DEFAULT '',
		collection_name TEXT DEFAULT '', room TEXT DEFAULT '', hall TEXT DEFAULT '',
		is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
		created_at TEXT, updated_at TEXT,
		superseded_by TEXT DEFAULT '',
		valid_until TEXT,
		consolidated_from TEXT DEFAULT '',
		consolidation_run_id TEXT DEFAULT '',
		tier TEXT DEFAULT 'raw',
		UNIQUE(key, owner_id, collection_name)
	)`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create: %v", err)
	}
	return &fakeDeps{db: db}
}

// seedDup inserts a raw, non-superseded, unpinned memory row.
func seedDup(t *testing.T, deps *fakeDeps, id, value, created string) {
	t.Helper()
	_, err := deps.db.Exec(
		`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, created_at, updated_at, superseded_by, tier)
		 VALUES (?, ?, ?, 'project', '', 'levara', '', '', 0, ?, ?, '', 'raw')`,
		id, "k-"+id, value, created, created)
	if err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func newConsolidateDeps(t *testing.T) *fakeDeps {
	deps := setupConsolidateDB(t)
	// older survives-to-newer: "a" older, "b" newer → survivor is "b".
	seedDup(t, deps, "a", "Pi runs potion sidecar on port 9101.", "2026-01-01T00:00:00Z")
	seedDup(t, deps, "b", "Pi runs potion sidecar on port 9101.", "2026-01-02T00:00:00Z")

	deps.embedAvailable = true
	deps.embedFn = func(_ context.Context, _ string) ([]float32, error) {
		return []float32{1, 0, 0}, nil
	}
	// Each candidate's neighbor is the OTHER row at cosine 0.99 (>= TauHigh)
	// so the cluster is a deterministic MERGE (no LLM needed).
	deps.searchFn = func(_ string, _ []float32, _ int) ([]SearchResult, error) {
		return []SearchResult{
			{ID: "a", Score: 0.99},
			{ID: "b", Score: 0.99},
		}, nil
	}
	return deps
}

// TestEdges_EmbedsKeyPlusValue guards F-quality.1: the edge-builder must embed
// the same key+" "+value text that indexMemorySync indexes, otherwise the
// value-only query vector is asymmetric against the stored key+value vectors and
// under-clusters records whose key carries text.
func TestEdges_EmbedsKeyPlusValue(t *testing.T) {
	var embedded []string
	deps := &fakeDeps{
		embedAvailable: true,
		embedFn: func(_ context.Context, text string) ([]float32, error) {
			embedded = append(embedded, text)
			return []float32{1, 0, 0}, nil
		},
		searchFn: func(_ string, _ []float32, _ int) ([]SearchResult, error) {
			return nil, nil
		},
	}
	n := &collectionNeighbors{deps: deps, collection: "_memories_levara"}
	recs := []consolidate.MemoryRecord{
		{ID: "a", Key: "pi-potion-sidecar", Value: "runs on port 9101"},
	}
	if _, err := n.Edges(context.Background(), recs, consolidate.DefaultConfig()); err != nil {
		t.Fatalf("Edges: %v", err)
	}
	want := "pi-potion-sidecar runs on port 9101"
	if len(embedded) != 1 || embedded[0] != want {
		t.Errorf("embedded = %q, want exactly [%q] (key+\" \"+value, matching indexMemorySync)", embedded, want)
	}
}

// TestToolConsolidate_SurfacesDimIncompatible guards P1.2: a collection whose
// vectors are a different dimension than the server embedder makes every
// CollectionSearch fail with store.ErrDimMismatch. The tool must surface that
// as a visible note (not a silent clusters=0) while still returning a clean,
// non-error result so a multi-collection sweep / the janitor keep going.
func TestToolConsolidate_SurfacesDimIncompatible(t *testing.T) {
	deps := newConsolidateDeps(t)
	deps.searchFn = func(_ string, _ []float32, _ int) ([]SearchResult, error) {
		return nil, fmt.Errorf("%w: query dim 768 != collection \"_memories_levara\" dim 256", store.ErrDimMismatch)
	}

	got := ToolConsolidate(context.Background(), deps, map[string]any{
		"collection": "levara", "dry_run": true,
	})
	if got.IsError {
		t.Fatalf("dim-incompat must degrade cleanly, got IsError: %q", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "dim") || !strings.Contains(strings.ToLower(got.Content[0].Text), "incompat") {
		t.Errorf("text = %q, want a dim-incompatible note", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "clusters=0") {
		t.Errorf("text = %q, want clusters=0 (no edges built)", got.Content[0].Text)
	}
}

func TestSummaryMaxTokens_ScalesAndClamps(t *testing.T) {
	if got := summaryMaxTokens([]string{"short"}); got != 512 {
		t.Errorf("small sources -> %d, want floor 512", got)
	}
	if got := summaryMaxTokens([]string{strings.Repeat("a", 20000)}); got != 4096 {
		t.Errorf("huge sources -> %d, want cap 4096", got)
	}
	if got := summaryMaxTokens([]string{strings.Repeat("a", 3000)}); got != 1256 {
		t.Errorf("mid sources (3000 chars) -> %d, want 3000/3+256=1256", got)
	}
}

func TestToolConsolidate_RequiresCollection(t *testing.T) {
	deps := setupConsolidateDB(t)
	got := ToolConsolidate(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing collection: IsError = false, want true")
	}
}

// seedBaseDup inserts a base-store memory row (collection_name=”) — the rows
// that live in the unprefixed _memories vector collection.
func seedBaseDup(t *testing.T, deps *fakeDeps, id, value, created string) {
	t.Helper()
	_, err := deps.db.Exec(
		`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, created_at, updated_at, superseded_by, tier)
		 VALUES (?, ?, ?, 'project', '', '', '', '', 0, ?, ?, '', 'raw')`,
		id, "k-"+id, value, created, created)
	if err != nil {
		t.Fatalf("seed base %s: %v", id, err)
	}
}

// TestToolConsolidate_TargetsBaseStore guards P2.2: the base memory store keeps
// its rows at collection_name=” and indexes them in the unprefixed '_memories'
// vector collection. It was unreachable via on-demand consolidate because the
// tool rejects an empty 'collection' arg. Callers must be able to target it
// explicitly by its vector-collection name ('_memories'), which maps to the
// empty SQL collection filter — not to a literal collection_name='_memories'
// (which matches nothing).
func TestToolConsolidate_TargetsBaseStore(t *testing.T) {
	deps := setupConsolidateDB(t)
	seedBaseDup(t, deps, "a", "Pi runs potion sidecar on port 9101.", "2026-01-01T00:00:00Z")
	seedBaseDup(t, deps, "b", "Pi runs potion sidecar on port 9101.", "2026-01-02T00:00:00Z")
	deps.embedAvailable = true
	deps.embedFn = func(_ context.Context, _ string) ([]float32, error) {
		return []float32{1, 0, 0}, nil
	}
	deps.searchFn = func(_ string, _ []float32, _ int) ([]SearchResult, error) {
		return []SearchResult{{ID: "a", Score: 0.99}, {ID: "b", Score: 0.99}}, nil
	}

	got := ToolConsolidate(context.Background(), deps, map[string]any{
		"collection": "_memories", "dry_run": false,
	})
	if got.IsError {
		t.Fatalf("base-store consolidate errored: %q", got.Content[0].Text)
	}

	var superseded string
	var n int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by != ''`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("base-store merge superseded %d rows, want exactly 1 (the base store must be reachable)", n)
	}
	if err := deps.db.QueryRow(`SELECT superseded_by FROM memories WHERE superseded_by != ''`).Scan(&superseded); err != nil {
		t.Fatalf("scan superseded: %v", err)
	}
	if superseded != "b" {
		t.Errorf("survivor = %q, want b (newest)", superseded)
	}
}

func TestToolConsolidate_DryRunDoesNotApply(t *testing.T) {
	deps := newConsolidateDeps(t)
	got := ToolConsolidate(context.Background(), deps, map[string]any{
		"collection": "levara", "dry_run": true,
	})
	if got.IsError {
		t.Fatalf("IsError = true: %q", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "clusters=") || !strings.Contains(got.Content[0].Text, "actions=") {
		t.Errorf("text = %q, want clusters/actions counts", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "dry_run") {
		t.Errorf("text = %q, want dry_run mode", got.Content[0].Text)
	}

	var n int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by != ''`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry run superseded %d rows, want 0", n)
	}
}

func TestToolConsolidate_AppliesMerge(t *testing.T) {
	deps := newConsolidateDeps(t)
	got := ToolConsolidate(context.Background(), deps, map[string]any{
		"collection": "levara", "dry_run": false,
	})
	if got.IsError {
		t.Fatalf("IsError = true: %q", got.Content[0].Text)
	}

	var superseded, runID string
	if err := deps.db.QueryRow(
		`SELECT superseded_by, consolidation_run_id FROM memories WHERE superseded_by != ''`).
		Scan(&superseded, &runID); err != nil {
		t.Fatalf("scan superseded row: %v", err)
	}
	if superseded != "b" {
		t.Errorf("survivor = %q, want b (newest)", superseded)
	}
	if runID == "" {
		t.Errorf("consolidation_run_id empty, want set")
	}

	var n int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE superseded_by != ''`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("superseded %d rows, want exactly 1", n)
	}
}

func TestToolConsolidationRevert_RestoresState(t *testing.T) {
	deps := newConsolidateDeps(t)
	ctx := context.Background()

	got := ToolConsolidate(ctx, deps, map[string]any{"collection": "levara", "dry_run": false})
	if got.IsError {
		t.Fatalf("consolidate errored: %s", got.Content[0].Text)
	}
	runID := parseRunID(t, got.Content[0].Text)

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

func TestConsolidationSkipCategory(t *testing.T) {
	cases := []struct {
		reason string
		want   string
	}{
		{"cluster too large for abstraction (7 > 6)", "oversized"},
		{"LLM call budget exhausted (24 calls)", "llm_budget"},
		{"consolidate: summary dropped source number \"256\"", "coverage_guard"},
		{"consolidate: summary invented number \"99\"", "coverage_guard"},
		{"consolidate: summary dropped 3/4 source entities (75% > 10%): [Pi Levara]", "coverage_guard"},
		{"consolidate: empty summary", "coverage_guard"},
		{"consolidate: no sources", "coverage_guard"},
		{"some unexpected backend failure", "other"},
		{"", "other"},
	}
	for _, c := range cases {
		if got := consolidationSkipCategory(c.reason); got != c.want {
			t.Errorf("consolidationSkipCategory(%q) = %q, want %q", c.reason, got, c.want)
		}
	}
}

// Integration: a real merge consolidation must record one char_density
// observation under kind=merge (guards the handler's Densities[i] wiring).
func TestToolConsolidate_ObservesCharDensity(t *testing.T) {
	deps := newConsolidateDeps(t)
	obs := metrics.ConsolidationCharDensity.WithLabelValues("merge")
	before := histSampleCount(t, obs)

	got := ToolConsolidate(context.Background(), deps, map[string]any{"collection": "levara", "dry_run": false})
	if got.IsError {
		t.Fatalf("consolidate errored: %s", got.Content[0].Text)
	}
	if after := histSampleCount(t, obs); after != before+1 {
		t.Errorf("char_density{merge} sample count = %d, want %d (one merge action)", after, before+1)
	}
}

func histSampleCount(t *testing.T, obs prometheus.Observer) uint64 {
	t.Helper()
	m, ok := obs.(prometheus.Metric)
	if !ok {
		t.Fatalf("observer %T is not a prometheus.Metric", obs)
	}
	var dm dto.Metric
	if err := m.Write(&dm); err != nil {
		t.Fatalf("metric write: %v", err)
	}
	return dm.GetHistogram().GetSampleCount()
}
