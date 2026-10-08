package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/orchestrator"
)

// setupCodifyDB creates a SQLite test DB with graph_nodes and graph_edges tables.
func setupCodifyDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-codify-test-*.db")
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
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT,
			type TEXT,
			description TEXT,
			properties TEXT
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL,
			properties TEXT NOT NULL DEFAULT '{}'
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return &fakeDeps{db: db}
}

// ── ToolCodify ──

func TestToolCodify_MissingArgs(t *testing.T) {
	res := ToolCodify(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when code/filename missing")
	}
	if !strings.Contains(res.Content[0].Text, "'code' and 'filename' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolCodify_MissingFilename(t *testing.T) {
	res := ToolCodify(context.Background(), &fakeDeps{}, map[string]any{
		"code": "package foo",
	})
	if !res.IsError {
		t.Fatal("want IsError when filename missing")
	}
}

func TestToolCodify_ReturnsSummary(t *testing.T) {
	goCode := `package main
func Hello() string { return "hello" }
`
	res := ToolCodify(context.Background(), &fakeDeps{}, map[string]any{
		"code":     goCode,
		"filename": "main.go",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
	if _, ok := out["language"]; !ok {
		t.Error("missing 'language' field")
	}
	if _, ok := out["entities"]; !ok {
		t.Error("missing 'entities' field")
	}
	if _, ok := out["relations"]; !ok {
		t.Error("missing 'relations' field")
	}
}

func TestToolCodify_PipelineOwnsGraphWrites(t *testing.T) {
	deps := setupCodifyDB(t)
	calls := 0
	deps.pipelineFn = func(ctx context.Context, texts []string, cfg orchestrator.Config, updates chan<- orchestrator.Progress) error {
		defer close(updates)
		calls++
		if cfg.Collection != "code_knowledge" || cfg.StaticGraph == nil || len(cfg.StaticGraph.Nodes) != 2 {
			t.Errorf("unexpected static config: %+v", cfg)
		}
		return nil
	}
	args := map[string]any{"code": "package main\nfunc Hello() {}\n", "filename": "main.go"}
	for i := 0; i < 2; i++ {
		if result := ToolCodify(context.Background(), deps, args); result.IsError {
			t.Fatal(result.Content)
		}
	}
	if calls != 2 {
		t.Fatalf("pipeline calls=%d", calls)
	}
	var count int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM graph_nodes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("codify bypassed pipeline with %d direct writes", count)
	}
}

func TestToolCodify_NilDB(t *testing.T) {
	// No DB configured — should still return analysis JSON, just no graph writes.
	goCode := `package main
func Hello() string { return "hello" }
`
	res := ToolCodify(context.Background(), &fakeDeps{}, map[string]any{
		"code":     goCode,
		"filename": "main.go",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError with nil DB: %q", res.Content[0].Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response not JSON: %s", res.Content[0].Text)
	}
}

func TestToolCodify_NoEmbedWhenNotConfigured(t *testing.T) {
	deps := setupCodifyDB(t)
	// No embed endpoint → CollectionInsert should never be called.
	inserted := 0
	deps.embedAvailable = false

	goCode := `package main
func Hello() string { return "hello" }
`
	res := ToolCodify(context.Background(), deps, map[string]any{
		"code":       goCode,
		"filename":   "main.go",
		"collection": "mycode",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}

	// No insertions via CollectionInsert because embed not configured.
	rows := deps.getInserted()
	if len(rows) != inserted {
		t.Errorf("expected 0 vector inserts, got %d", len(rows))
	}
}
