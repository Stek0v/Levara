package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestVSAAPI_RebuildAndQuery(t *testing.T) {
	db := newVSATestDB(t)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	app := fiber.New()
	RegisterVSAAPI(app, APIConfig{DB: db})

	req := httptest.NewRequest("POST", "/vsa/rebuild", strings.NewReader(`{"dataset_id":"ds-a","dim":128,"shard_size":4}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST /vsa/rebuild: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("POST /vsa/rebuild status=%d, want 200", resp.StatusCode)
	}

	resp, err = app.Test(httptest.NewRequest("GET", "/vsa/query?dataset_id=ds-a&source_id=n1&predicate=KNOWS&top_k=2", nil), -1)
	if err != nil {
		t.Fatalf("GET /vsa/query: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET /vsa/query status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Candidates []vsaQueryResponse `json:"candidates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Candidates) != 1 {
		t.Fatalf("candidates=%v, want one ds-a result", body.Candidates)
	}
	if body.Candidates[0].TargetID != "n2" || body.Candidates[0].TargetName != "Bob" {
		t.Fatalf("candidate=%+v, want Bob/n2", body.Candidates[0])
	}
}

func TestVSAGraphContext_UsesDatasetFilter(t *testing.T) {
	db := newVSATestDB(t)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	if err := vsaStoreForDB(db, 128, 4).RebuildFromGraph(context.Background(), "ds-a"); err != nil {
		t.Fatalf("rebuild ds-a: %v", err)
	}
	if err := vsaStoreForDB(db, 128, 4).RebuildFromGraph(context.Background(), "ds-b"); err != nil {
		t.Fatalf("rebuild ds-b: %v", err)
	}

	ctx := vsaGraphContext(context.Background(), APIConfig{DB: db}, []string{"Alice"}, []string{"ds-a"}, 5)
	if len(ctx) != 1 {
		t.Fatalf("context=%v, want one ds-a line", ctx)
	}
	if !strings.Contains(ctx[0], "Alice") || !strings.Contains(ctx[0], "Bob") || !strings.Contains(ctx[0], "KNOWS") {
		t.Fatalf("context[0]=%q, want Alice/Bob/KNOWS", ctx[0])
	}
	if strings.Contains(ctx[0], "Eve") {
		t.Fatalf("context leaked ds-b target: %q", ctx[0])
	}
}

func newVSATestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/vsa-http.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema := `
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
	valid_until TEXT,
	dataset_id TEXT NOT NULL DEFAULT ''
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create graph schema: %v", err)
	}
	rows := []string{
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('n1', 'Alice', 'Person', 'ds-a')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('n2', 'Bob', 'Person', 'ds-a')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('n3', 'Alice', 'Person', 'ds-b')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('n4', 'Eve', 'Person', 'ds-b')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e1', 'n1', 'n2', 'KNOWS', 'ds-a')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e2', 'n3', 'n4', 'KNOWS', 'ds-b')`,
	}
	for _, stmt := range rows {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed graph: %v", err)
		}
	}
	return db
}
