package http

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestDatasetGraph_SQLFallbackFiltersDataset(t *testing.T) {
	db := newDatasetGraphSQLiteDB(t)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/datasets/:id/graph", DatasetGraph(GraphVisualizationConfig{DB: db}))

	resp, err := app.Test(httptest.NewRequest("GET", "/datasets/ds-a/graph", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var got GraphDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("nodes=%+v, want only ds-a nodes", got.Nodes)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("edges=%+v, want only ds-a edge whose endpoints are included", got.Edges)
	}
	for _, n := range got.Nodes {
		if n.Properties["dataset_id"] != "ds-a" {
			t.Fatalf("node leaked from another dataset: %+v", n)
		}
	}
}

func newDatasetGraphSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/viz.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES
			('a1','Alice','Person','ds-a'),
			('a2','Acme','Org','ds-a'),
			('b1','Bob','Person','ds-b');
		INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id) VALUES
			('a-edge','a1','a2','works_at','ds-a'),
			('cross-edge','a1','b1','knows','ds-a'),
			('b-edge','b1','a2','mentions','ds-b');
	`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	return db
}
