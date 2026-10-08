package mcp

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/orchestrator"
)

type projectAggregateDeps struct {
	*fakeDeps
	aggregates ProjectContextAggregates
	err        error
}

func (d *projectAggregateDeps) ProjectContextAggregates(context.Context, string) (ProjectContextAggregates, error) {
	return d.aggregates, d.err
}

// testBaseCfg returns an orchestrator.Config with just EmbedModel set
// so ToolCheckDrift can compare models without a real LLM stack.
func testBaseCfg(model string, dim int) orchestrator.Config {
	return orchestrator.Config{EmbedModel: model}
}

// setupProjectDB creates a SQLite test DB with the tables that
// ToolGetProjectContext needs.
func setupProjectDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-project-test-*.db")
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
		CREATE TABLE memories (
			id TEXT PRIMARY KEY,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'fact',
			owner_id TEXT NOT NULL DEFAULT '',
			collection_name TEXT NOT NULL DEFAULT '',
			superseded_by TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT,
			type TEXT,
			description TEXT,
			properties TEXT
		);
		CREATE TABLE interactions (
			id TEXT PRIMARY KEY,
			query TEXT NOT NULL DEFAULT '',
			response TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	if err != nil {
		t.Fatalf("create tables: %v", err)
	}
	return &fakeDeps{db: db}
}

// ── ToolGetProjectContext ──

func TestToolGetProjectContext_MissingCollection(t *testing.T) {
	res := ToolGetProjectContext(context.Background(), &fakeDeps{}, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when collection missing")
	}
	if !strings.Contains(res.Content[0].Text, "'collection' required") {
		t.Errorf("wrong error text: %q", res.Content[0].Text)
	}
}

func TestToolGetProjectContext_NoVectors(t *testing.T) {
	deps := setupProjectDB(t)
	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection": "test",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Collection Stats") {
		t.Error("missing Collection Stats section")
	}
	if !strings.Contains(text, "unavailable") || !strings.Contains(text, "no memories") {
		t.Errorf("expected explicit unavailable and empty memory summary; got %q", text)
	}
}

func TestToolGetProjectContext_ScopedAggregates(t *testing.T) {
	base := setupProjectDB(t)
	deps := &projectAggregateDeps{fakeDeps: base, aggregates: ProjectContextAggregates{
		PublishedDocuments: 2,
		EntityTypes:        map[string]int{"Service": 3},
		RecentInteractions: []ProjectContextInteraction{{Query: "where", Response: "there"}},
	}}
	res := ToolGetProjectContext(context.Background(), deps, map[string]any{"collection": "project"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "Accessible published documents: 2") || !strings.Contains(res.Content[0].Text, "Service: 3") || !strings.Contains(res.Content[0].Text, "where → there") {
		t.Fatalf("unexpected aggregate result: %+v", res)
	}
	deps.err = errors.New("aggregate unavailable")
	res = ToolGetProjectContext(context.Background(), deps, map[string]any{"collection": "project"})
	if !res.IsError || res.StructuredContent != nil {
		t.Fatalf("aggregate error returned partial result: %+v", res)
	}
}

func TestToolGetProjectContext_WithVectors(t *testing.T) {
	deps := setupProjectDB(t)
	deps.collectionMetas = map[string]CollectionInfo{
		"myproj": {Name: "myproj", Records: 42, Dim: 768, Metric: "cosine"},
	}
	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection": "myproj",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "unavailable") || strings.Contains(text, "Records: 42") {
		t.Errorf("unguarded vector statistics must be unavailable; text=%q", text)
	}
}

func TestToolGetProjectContext_MemoriesListed(t *testing.T) {
	deps := setupProjectDB(t)
	db := deps.db

	db.Exec(`INSERT INTO memories (id, key, value, type, collection_name) VALUES
		('m1', 'arch', 'HNSW vector DB', 'fact', 'proj'),
		('m2', 'deploy', 'docker compose', 'event', 'proj')`)

	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection": "proj",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "HNSW vector DB") {
		t.Errorf("memory value not in output; text=%q", text)
	}
	if !strings.Contains(text, "docker compose") {
		t.Errorf("second memory not in output; text=%q", text)
	}
}

func TestToolGetProjectContext_EntitiesListed(t *testing.T) {
	deps := setupProjectDB(t)
	db := deps.db

	db.Exec(`INSERT INTO graph_nodes (id, name, type) VALUES
		('n1', 'Auth', 'module'),
		('n2', 'DB', 'module'),
		('n3', 'User', 'entity')`)

	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection": "proj",
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Key Entity Types") {
		t.Error("missing Key Entity Types section")
	}
	if !strings.Contains(text, "unavailable") || strings.Contains(text, "module") {
		t.Errorf("unscoped graph entities must be unavailable; text=%q", text)
	}
}

func TestToolGetProjectContext_NilDB(t *testing.T) {
	// Without SQL there is no authoritative scoped context to return.
	deps := &fakeDeps{}
	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection": "test",
	})
	if !res.IsError || res.StructuredContent != nil {
		t.Fatalf("expected database error without partial success: %+v", res)
	}
}

func TestToolGetProjectContext_IncludeRelated(t *testing.T) {
	deps := setupProjectDB(t)
	deps.collectionMetas = map[string]CollectionInfo{
		"related": {Name: "related", Records: 10, Dim: 768, Metric: "l2"},
	}

	res := ToolGetProjectContext(context.Background(), deps, map[string]any{
		"collection":      "main",
		"include_related": []any{"related"},
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Related Projects") {
		t.Errorf("missing Related Projects section; text=%q", text)
	}
	if !strings.Contains(text, "related") {
		t.Errorf("related collection not shown; text=%q", text)
	}
}

// ── ToolCheckDrift ──

func TestToolCheckDrift_NoDrift(t *testing.T) {
	deps := &fakeDeps{
		collections: []string{"col1", "col2"},
		baseCfg:     testBaseCfg("nomic-v2", 768),
		collectionMetas: map[string]CollectionInfo{
			"col1": {EmbedModel: "nomic-v2", Dim: 768, Records: 10},
			"col2": {EmbedModel: "nomic-v2", Dim: 768, Records: 5},
		},
	}
	res := ToolCheckDrift(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"drifted": []`) {
		t.Errorf("expected empty drifted array, got %q", res.Content[0].Text)
	}
}

func TestToolCheckDrift_ModelMismatch(t *testing.T) {
	deps := &fakeDeps{
		collections: []string{"old"},
		baseCfg:     testBaseCfg("nomic-v2", 768),
		collectionMetas: map[string]CollectionInfo{
			"old": {EmbedModel: "nomic-v1", Dim: 768, Records: 5},
		},
	}
	res := ToolCheckDrift(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "is_drifted") {
		t.Errorf("expected drift result; got %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "nomic-v1") {
		t.Errorf("actual_model not in response; got %q", res.Content[0].Text)
	}
}

func TestToolCheckDrift_EmptyCollectionSkipped(t *testing.T) {
	deps := &fakeDeps{
		collections: []string{"empty"},
		baseCfg:     testBaseCfg("nomic-v2", 768),
		collectionMetas: map[string]CollectionInfo{
			"empty": {EmbedModel: "old-model", Dim: 512, Records: 0}, // 0 records → skip
		},
	}
	res := ToolCheckDrift(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"drifted": []`) {
		t.Errorf("empty collection should be skipped; got %q", res.Content[0].Text)
	}
}

func TestToolCheckDrift_InternalCollectionSkipped(t *testing.T) {
	deps := &fakeDeps{
		collections: []string{"_internal"},
		baseCfg:     testBaseCfg("nomic-v2", 768),
		collectionMetas: map[string]CollectionInfo{
			"_internal": {EmbedModel: "old-model", Dim: 512, Records: 10},
		},
	}
	res := ToolCheckDrift(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, `"drifted": []`) {
		t.Errorf("internal collection should be skipped; got %q", res.Content[0].Text)
	}
}

func TestToolCheckDrift_DimMismatch(t *testing.T) {
	// Two collections: "reference" establishes currentDim=768; "narrow"
	// was indexed with dim=512 → should report as drifted.
	deps := &fakeDeps{
		collections: []string{"reference", "narrow"},
		baseCfg:     testBaseCfg("nomic-v2", 768),
		collectionMetas: map[string]CollectionInfo{
			"reference": {EmbedModel: "nomic-v2", Dim: 768, Records: 10},
			"narrow":    {EmbedModel: "nomic-v2", Dim: 512, Records: 3},
		},
	}
	res := ToolCheckDrift(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "is_drifted") {
		t.Errorf("dim mismatch should produce drift result; got %q", res.Content[0].Text)
	}
}
