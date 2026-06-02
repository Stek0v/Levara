package mcp

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

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
		UNIQUE(key, owner_id)
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
