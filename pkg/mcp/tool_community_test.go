package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// setupCommunityDB creates a SQLite test DB with the tables that
// ToolListCommunities and ToolPruneGraph need.
func setupCommunityDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-community-test-*.db")
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

	_, err = db.Exec(`
		CREATE TABLE graph_communities (
			id TEXT PRIMARY KEY,
			level INTEGER NOT NULL DEFAULT 0,
			parent_id TEXT NOT NULL DEFAULT '',
			member_count INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL,
			properties TEXT NOT NULL DEFAULT '{}',
			superseded_by TEXT NOT NULL DEFAULT '',
			valid_until TEXT
		);
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT,
			type TEXT,
			description TEXT,
			properties TEXT
		);
		CREATE TABLE community_members (
			id TEXT PRIMARY KEY,
			community_id TEXT NOT NULL,
			node_id TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return &fakeDeps{db: db}
}

// ── ToolListCommunities ──

func TestToolListCommunities_NilDB(t *testing.T) {
	res := ToolListCommunities(context.Background(), nilDBDeps{}, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if res.Content[0].Text != "[]" {
		t.Errorf("want []  got %q", res.Content[0].Text)
	}
}

func TestToolListCommunities_EmptyTable(t *testing.T) {
	deps := setupCommunityDB(t)
	res := ToolListCommunities(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array, got %d items", len(out))
	}
}

func TestToolListCommunities_ReturnsRows(t *testing.T) {
	deps := setupCommunityDB(t)
	db := deps.db

	db.Exec(`INSERT INTO graph_communities (id, level, parent_id, member_count, summary)
		VALUES ('c1', 0, '', 5, 'auth'), ('c2', 1, 'c1', 3, 'users')`)

	res := ToolListCommunities(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 rows, got %d", len(out))
	}
	// Spot-check first result has all expected fields.
	if _, ok := out[0]["id"]; !ok {
		t.Error("missing 'id' field")
	}
	if _, ok := out[0]["member_count"]; !ok {
		t.Error("missing 'member_count' field")
	}
}

func TestToolListCommunities_LevelFilter(t *testing.T) {
	deps := setupCommunityDB(t)
	db := deps.db

	db.Exec(`INSERT INTO graph_communities (id, level, parent_id, member_count, summary)
		VALUES ('c1', 0, '', 5, 'L0'), ('c2', 1, 'c1', 3, 'L1'), ('c3', 0, '', 4, 'L0b')`)

	res := ToolListCommunities(context.Background(), deps, map[string]any{
		"level": float64(0),
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	// Only level=0 rows (c1, c3).
	if len(out) != 2 {
		t.Errorf("level filter: expected 2, got %d", len(out))
	}
}

func TestToolListCommunities_MinMembersFilter(t *testing.T) {
	deps := setupCommunityDB(t)
	db := deps.db

	db.Exec(`INSERT INTO graph_communities (id, level, parent_id, member_count, summary)
		VALUES ('c1', 0, '', 5, 'big'), ('c2', 0, '', 1, 'tiny')`)

	res := ToolListCommunities(context.Background(), deps, map[string]any{
		"min_members": float64(3),
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if len(out) != 1 {
		t.Errorf("min_members filter: expected 1, got %d", len(out))
	}
}

// ── ToolPruneGraph ──

func TestToolPruneGraph_NilDB(t *testing.T) {
	res := ToolPruneGraph(context.Background(), nilDBDeps{}, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "edges_deleted") {
		t.Errorf("expected edges_deleted in response, got %q", res.Content[0].Text)
	}
}

func TestToolPruneGraph_DryRunDefault(t *testing.T) {
	deps := setupCommunityDB(t)

	res := ToolPruneGraph(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	// Default is dry_run=true → edges_would_delete present, edges_deleted=0.
	if out["edges_deleted"] != float64(0) {
		t.Errorf("edges_deleted=%v, want 0", out["edges_deleted"])
	}
}

func TestToolPruneGraph_HeartbeatFired(t *testing.T) {
	deps := setupCommunityDB(t)
	fired := false
	deps.heartbeatFn = func(eventType string, payload any) {
		if eventType == "prune" {
			fired = true
		}
	}

	ToolPruneGraph(context.Background(), deps, map[string]any{})
	if !fired {
		t.Error("heartbeat 'prune' not fired")
	}
}
