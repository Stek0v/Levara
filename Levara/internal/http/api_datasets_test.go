package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// datasetsTestApp wires up a real Fiber router with the datasets handlers
// backed by an in-memory SQLite instance pinned to the datasets schema.
// Unlike handler_contract_test.go (which uses a real vector store via
// DirectNode), these endpoints are pure SQL — the test fixture swaps in
// SQLite so we don't need Postgres on the test machine.
func datasetsTestApp(t *testing.T) (*fiber.App, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-datasets-test-*")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}

	// Minimal schema — api_datasets handlers only touch these tables and
	// columns. Bringing in the full MigrateSchema would pull in auth,
	// RBAC, sessions, ontologies, etc.; the goal here is to exercise the
	// CRUD paths, not the migration.
	if _, err := db.Exec(`
		CREATE TABLE datasets (
			id TEXT PRIMARY KEY,
			name TEXT UNIQUE,
			owner_id TEXT,
			created_at TIMESTAMP,
			updated_at TIMESTAMP
		);
		CREATE TABLE data (
			id TEXT PRIMARY KEY,
			name TEXT,
			extension TEXT,
			mime_type TEXT,
			raw_data_location TEXT,
			data_size INTEGER,
			pipeline_status TEXT,
			tags TEXT,
			created_at TIMESTAMP
		);
		CREATE TABLE dataset_data (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dataset_id TEXT,
			data_id TEXT
		);
		CREATE TABLE dataset_shares (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			dataset_id TEXT,
			user_id TEXT,
			role TEXT
		);
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			is_superuser INTEGER DEFAULT 0
		);
	`); err != nil {
		db.Close()
		os.RemoveAll(dir)
		t.Fatalf("schema: %v", err)
	}

	// Route the handlers through the test-specific dialect rewriter.
	SetDBProvider(DBSQLite)

	cfg := APIConfig{DB: db}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api := app.Group("/api/v1")
	api.Get("/datasets", datasetsListHandler(cfg))
	api.Post("/datasets", datasetCreateHandler(cfg))
	api.Delete("/datasets/:id", datasetDeleteHandler(cfg))
	api.Get("/datasets/:id/data", datasetDataHandler(cfg))

	cleanup := func() {
		_ = app.Shutdown()
		_ = db.Close()
		os.RemoveAll(dir)
		SetDBProvider(DBPostgres) // reset for other tests
	}
	return app, cleanup
}

func TestDatasetsList_EmptyReturnsEmptyArray(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	status, body := getJSON(t, app, "/api/v1/datasets")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	// Empty datasets table — response must be [] (not null).
	trimmed := bytes.TrimSpace(body)
	if !bytes.Equal(trimmed, []byte("[]")) {
		t.Errorf("body = %q, want empty JSON array", body)
	}
}

func TestDatasetsCreate_HappyPath(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	status, body := postJSON(t, app, "/api/v1/datasets", map[string]any{"name": "alpha"})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", status, body)
	}
	var out DatasetDTO
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v; body = %s", err, body)
	}
	if out.ID == "" || out.Name != "alpha" {
		t.Errorf("out = %+v, want ID!='' and Name='alpha'", out)
	}

	// Round-trip: GET should return the created dataset.
	_, listBody := getJSON(t, app, "/api/v1/datasets")
	var list []DatasetDTO
	_ = json.Unmarshal(listBody, &list)
	if len(list) != 1 || list[0].Name != "alpha" {
		t.Errorf("list = %+v, want single 'alpha' entry", list)
	}
}

func TestDatasetsCreate_MissingName_Returns400(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	status, body := postJSON(t, app, "/api/v1/datasets", map[string]any{})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", status, body)
	}
}

func TestDatasetsDelete_HappyPath(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	_, createBody := postJSON(t, app, "/api/v1/datasets", map[string]any{"name": "to-delete"})
	var created DatasetDTO
	_ = json.Unmarshal(createBody, &created)

	status, body := deleteReq(t, app, "/api/v1/datasets/"+created.ID)
	if status != http.StatusOK {
		t.Fatalf("delete status = %d; body = %s", status, body)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if out["deleted"] != true {
		t.Errorf("body = %s, want {\"deleted\":true}", body)
	}

	// List is empty again.
	_, listBody := getJSON(t, app, "/api/v1/datasets")
	if trimmed := bytes.TrimSpace(listBody); !bytes.Equal(trimmed, []byte("[]")) {
		t.Errorf("after delete list = %q, want []", listBody)
	}
}

func TestDatasetsDelete_NonexistentIsIdempotent(t *testing.T) {
	// Deleting an ID that doesn't exist returns 200 + deleted:true. This
	// mirrors the contract set by handler_contract_test for vector delete
	// (missing records don't 4xx). Regression lock — clients rely on
	// delete being safely retriable.
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	status, body := deleteReq(t, app, "/api/v1/datasets/does-not-exist")
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", status, body)
	}
}

func TestDatasetData_EmptyReturnsEmptyArray(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	_, createBody := postJSON(t, app, "/api/v1/datasets", map[string]any{"name": "ds1"})
	var created DatasetDTO
	_ = json.Unmarshal(createBody, &created)

	status, body := getJSON(t, app, "/api/v1/datasets/"+created.ID+"/data")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	if trimmed := bytes.TrimSpace(body); !bytes.Equal(trimmed, []byte("[]")) {
		t.Errorf("body = %q, want []", body)
	}
}

// Tiny shared helpers to keep the test bodies compact.

func getJSON(t *testing.T, app *fiber.App, path string) (int, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", path, nil), -1)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func postJSON(t *testing.T, app *fiber.App, path string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, buf
}

func deleteReq(t *testing.T, app *fiber.App, path string) (int, []byte) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("DELETE", path, nil), -1)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
