package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// setupQueryEntityDB builds the graph_nodes + graph_edges schema
// ToolQueryEntity reads. Columns match production shape; only the
// ones the tool scans matter for assertions.
func setupQueryEntityDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-queryentity-test-*.db")
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

	stmts := []string{
		`CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY, name TEXT, type TEXT, updated_at TEXT
		)`,
		`CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT,
			relationship_name TEXT, properties TEXT,
			valid_from TEXT, valid_until TEXT, superseded_by TEXT,
			confidence REAL, updated_at TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	return &fakeDeps{db: db}
}

func TestToolQueryEntity_NilDBIsError(t *testing.T) {
	got := ToolQueryEntity(context.Background(), nilDBDeps{}, map[string]any{"name": "x"})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
}

func TestToolQueryEntity_RequiresName(t *testing.T) {
	deps := setupQueryEntityDB(t)
	got := ToolQueryEntity(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing name: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'name' required") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolQueryEntity_UnknownNameIsNotError(t *testing.T) {
	// Unknown entity → specific message, IsError=false. Querying a
	// non-existent name is a reasonable read, not a client fault.
	deps := setupQueryEntityDB(t)
	got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "ghost"})
	if got.IsError {
		t.Fatalf("IsError = true, want false for unknown name")
	}
	if !strings.Contains(got.Content[0].Text, "No entity found with name 'ghost'") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolQueryEntity_ReturnsActiveEdgesByDefault(t *testing.T) {
	// Without as_of: only edges whose valid_until is NULL or in the
	// future. An expired edge must not appear.
	deps := setupQueryEntityDB(t)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'db', 'service', '2026-01-01T00:00:00Z')`)
	// Active edge (valid_until NULL).
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('e1', 'n1', 'n2', 'calls', '{}', '2026-01-01T00:00:00Z', NULL, '', 0.9, '2026-03-01T00:00:00Z')`)
	// Expired edge (valid_until in the past).
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('e2', 'n1', 'n2', 'old_rel', '{}', '2020-01-01T00:00:00Z', '2021-01-01T00:00:00Z', '', 0.5, '2021-01-01T00:00:00Z')`)

	got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "auth"})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	var resp map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &resp)

	if resp["entity"] != "auth" {
		t.Errorf("entity = %v, want auth", resp["entity"])
	}
	edges := resp["edges"].([]any)
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (expired edge must be filtered)", len(edges))
	}
	if edges[0].(map[string]any)["id"] != "e1" {
		t.Errorf("edge id = %v, want e1", edges[0].(map[string]any)["id"])
	}
}

func TestToolQueryEntity_AsOfIncludesHistoricallyValid(t *testing.T) {
	// With as_of supplied, edges whose validity window (valid_from
	// through valid_until) includes the timestamp are returned —
	// even if they've since expired.
	deps := setupQueryEntityDB(t)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'db', 'service', '2026-01-01T00:00:00Z')`)
	// Edge valid only in 2020.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('historic', 'n1', 'n2', 'called', '{}', '2020-01-01T00:00:00Z', '2021-01-01T00:00:00Z', '', 0.5, '2021-01-01T00:00:00Z')`)

	// Snapshot at 2020-06-01 — should see the historic edge.
	got := ToolQueryEntity(context.Background(), deps, map[string]any{
		"name":  "auth",
		"as_of": "2020-06-01T00:00:00Z",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	var resp map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &resp)
	edges := resp["edges"].([]any)
	if len(edges) != 1 || edges[0].(map[string]any)["id"] != "historic" {
		t.Errorf("got %+v, want single 'historic' edge", edges)
	}
	if resp["as_of"] != "2020-06-01T00:00:00Z" {
		t.Errorf("as_of in response = %v, want echoed back", resp["as_of"])
	}

	// Snapshot before the edge — should be excluded (valid_from > as_of).
	got = ToolQueryEntity(context.Background(), deps, map[string]any{
		"name":  "auth",
		"as_of": "2019-01-01T00:00:00Z",
	})
	json.Unmarshal([]byte(got.Content[0].Text), &resp)
	edges, _ = resp["edges"].([]any)
	if len(edges) != 0 {
		t.Errorf("got %d edges at 2019, want 0 (valid_from is 2020)", len(edges))
	}
}

func TestToolQueryEntity_MatchesSourceOrTarget(t *testing.T) {
	// An edge counts if the entity's node is on EITHER side.
	deps := setupQueryEntityDB(t)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'db', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n3', 'api', 'service', '2026-01-01T00:00:00Z')`)
	// auth as source.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('e1', 'n1', 'n2', 'calls', '{}', NULL, NULL, '', 1.0, '2026-03-01T00:00:00Z')`)
	// auth as target.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('e2', 'n3', 'n1', 'invokes', '{}', NULL, NULL, '', 1.0, '2026-03-02T00:00:00Z')`)
	// unrelated edge.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('e3', 'n2', 'n3', 'reads', '{}', NULL, NULL, '', 1.0, '2026-03-01T00:00:00Z')`)

	got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "auth"})
	var resp map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &resp)
	edges := resp["edges"].([]any)
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (source + target)", len(edges))
	}
	// Results are ordered by updated_at DESC: e2 (Mar 2) then e1 (Mar 1).
	ids := []string{
		edges[0].(map[string]any)["id"].(string),
		edges[1].(map[string]any)["id"].(string),
	}
	if ids[0] != "e2" || ids[1] != "e1" {
		t.Errorf("edge order = %v, want [e2, e1] (updated_at DESC)", ids)
	}
}

func TestToolQueryEntity_LimitBounds(t *testing.T) {
	// The "limit" arg caps the edge count returned.
	deps := setupQueryEntityDB(t)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'db', 'service', '2026-01-01T00:00:00Z')`)
	// Seed 5 edges.
	for i := 0; i < 5; i++ {
		deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
			VALUES (?, 'n1', 'n2', 'rel', '{}', NULL, NULL, '', 1.0, '2026-03-01T00:00:00Z')`,
			"e"+string(rune('a'+i)))
	}

	got := ToolQueryEntity(context.Background(), deps, map[string]any{
		"name":  "auth",
		"limit": float64(2),
	})
	var resp map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &resp)
	edges := resp["edges"].([]any)
	if len(edges) != 2 {
		t.Errorf("got %d edges with limit=2, want 2", len(edges))
	}
}

func TestToolQueryEntity_ResolvesMultipleNodesByName(t *testing.T) {
	// Same entity name pointing to two nodes (imported from different
	// sources) — both contribute to the edge search.
	deps := setupQueryEntityDB(t)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'auth', 'module',  '2026-01-01T00:00:00Z')`)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n3', 'db',   'service', '2026-01-01T00:00:00Z')`)
	// Edge touching n1 only.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('eA', 'n1', 'n3', 'a', '{}', NULL, NULL, '', 1.0, '2026-03-01T00:00:00Z')`)
	// Edge touching n2 only.
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, valid_until, superseded_by, confidence, updated_at)
		VALUES ('eB', 'n2', 'n3', 'b', '{}', NULL, NULL, '', 1.0, '2026-03-02T00:00:00Z')`)

	got := ToolQueryEntity(context.Background(), deps, map[string]any{"name": "auth"})
	var resp map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &resp)
	nodeIDs := resp["node_ids"].([]any)
	if len(nodeIDs) != 2 {
		t.Errorf("node_ids = %v, want 2 resolutions", nodeIDs)
	}
	edges := resp["edges"].([]any)
	if len(edges) != 2 {
		t.Errorf("got %d edges, want 2 (one per node)", len(edges))
	}
}
