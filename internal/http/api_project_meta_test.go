package http

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"database/sql"
	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Unit coverage for project context/activity endpoints (block ③).

func projectMetaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT DEFAULT '')`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT DEFAULT '', owner_id TEXT, created_at TEXT DEFAULT '')`,
		`CREATE TABLE dataset_shares (id TEXT PRIMARY KEY, dataset_id TEXT, user_id TEXT, role TEXT, granted_by TEXT DEFAULT '', created_at TEXT DEFAULT '')`,
		`CREATE TABLE data (id TEXT PRIMARY KEY, name TEXT DEFAULT '', pipeline_status TEXT DEFAULT '', created_at TEXT DEFAULT '')`,
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT)`,
		`CREATE TABLE memories (id TEXT PRIMARY KEY, key TEXT DEFAULT '', value TEXT DEFAULT '', type TEXT DEFAULT '', collection_name TEXT DEFAULT '', owner_id TEXT DEFAULT '', superseded_by TEXT DEFAULT '', valid_until TEXT, created_at TEXT DEFAULT '')`,
		`INSERT INTO users(id, email) VALUES ('u-alice', 'alice@example.com')`,
		`INSERT INTO datasets(id, name, owner_id, created_at) VALUES ('ds-1', 'proj-x', 'u-alice', '2026-09-04T10:00:00Z')`,
		`INSERT INTO data(id, name, pipeline_status, created_at) VALUES ('d-1', 'spec.pdf', 'completed', '2026-09-04T10:05:00Z')`,
		`INSERT INTO dataset_data(dataset_id, data_id) VALUES ('ds-1', 'd-1')`,
		`INSERT INTO dataset_shares(id, dataset_id, user_id, role, created_at) VALUES ('sh-1', 'ds-1', 'u-alice', 'viewer', '2026-09-04T11:00:00Z')`,
		`INSERT INTO memories(id, key, value, type, collection_name, created_at) VALUES ('m-1', 'arch', 'monolith', 'decision', 'proj-x', '2026-09-04T12:00:00Z')`,
		`INSERT INTO memories(id, key, value, type, collection_name, created_at, superseded_by) VALUES ('m-2', 'old', 'x', 'fact', 'proj-x', '2026-09-04T12:01:00Z', 'm-1')`,
		`INSERT INTO memories(id, key, value, type, collection_name, created_at, valid_until) VALUES ('m-3', 'expired', 'x', 'fact', 'proj-x', '2026-09-04T12:02:00Z', '2026-09-04T13:00:00Z')`,
		`INSERT INTO memories(id, key, value, type, collection_name, created_at) VALUES ('m-4', 'other', 'x', 'fact', 'proj-y', '2026-09-04T12:03:00Z')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

func metaApp(t *testing.T) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Get("/datasets/:id/context", datasetContextHandler(APIConfig{DB: projectMetaDB(t)}))
	app.Get("/datasets/:id/activity", datasetActivityHandler(APIConfig{DB: projectMetaDB(t)}))
	return app
}

func getMetaJSON(t *testing.T, app *fiber.App, url string) []map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	var out []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDatasetContextReturnsActiveMemories(t *testing.T) {
	app := metaApp(t)
	items := getMetaJSON(t, app, "/datasets/ds-1/context")
	// m-2 superseded, m-3 expired, m-4 other collection — only m-1 remains.
	if len(items) != 1 {
		t.Fatalf("context items = %d, want 1", len(items))
	}
	if items[0]["key"] != "arch" {
		t.Fatalf("key = %v, want arch", items[0]["key"])
	}
}

func TestDatasetActivityMergesUploadsSharesContext(t *testing.T) {
	app := metaApp(t)
	items := getMetaJSON(t, app, "/datasets/ds-1/activity")
	types := map[string]int{}
	for _, it := range items {
		types[it["type"].(string)]++
	}
	if types["upload"] != 1 || types["share_granted"] != 1 || types["context_add"] != 1 {
		t.Fatalf("activity types = %v, want 1 of each", types)
	}
	// Sorted descending by created_at: m-1 (12:00) last of the three.
	first, _ := items[0]["created_at"].(string)
	last, _ := items[len(items)-1]["created_at"].(string)
	if first != "" && last != "" && first < last {
		t.Fatalf("activity not sorted desc: first=%s last=%s", first, last)
	}
}

func TestDatasetContextMissingDatasetIs404(t *testing.T) {
	app := fiber.New()
	app.Get("/datasets/:id/context", datasetContextHandler(APIConfig{DB: projectMetaDB(t)}))
	req := httptest.NewRequest("GET", "/datasets/nope/context", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
