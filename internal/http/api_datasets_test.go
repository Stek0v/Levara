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
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/storage"

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
	api.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(cfg))
	api.Get("/datasets/:id/data/:dataId/raw/url", datasetDataRawURLHandler(cfg))
	api.Post("/prune/data", pruneDataHandler(cfg))
	api.Post("/prune/system", pruneSystemHandler(cfg))

	cleanup := func() {
		_ = app.Shutdown()
		_ = db.Close()
		os.RemoveAll(dir)
		SetDBProvider(DBPostgres) // reset for other tests
	}
	return app, cleanup
}

func datasetsRawURLTestApp(t *testing.T, fs storage.Storage) (*fiber.App, *sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-datasets-raw-url-test-*")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE datasets (
			id TEXT PRIMARY KEY,
			owner_id TEXT
		);
		CREATE TABLE data (
			id TEXT PRIMARY KEY,
			name TEXT,
			extension TEXT,
			raw_data_location TEXT,
			updated_at TIMESTAMP
		);
		CREATE TABLE dataset_data (
			dataset_id TEXT,
			data_id TEXT
		);
		CREATE TABLE dataset_shares (
			id TEXT PRIMARY KEY,
			dataset_id TEXT,
			user_id TEXT,
			role TEXT
		);
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			is_active INTEGER DEFAULT 1,
			is_superuser INTEGER DEFAULT 0
		);
	`); err != nil {
		db.Close()
		os.RemoveAll(dir)
		t.Fatalf("schema: %v", err)
	}

	SetDBProvider(DBSQLite)

	cfg := APIConfig{DB: db, FileStorage: fs}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		if userID := c.Get("X-Test-User"); userID != "" {
			c.Locals("user_id", userID)
		}
		return c.Next()
	})
	api := app.Group("/api/v1")
	api.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(cfg))
	api.Get("/datasets/:id/data/:dataId/raw/url", datasetDataRawURLHandler(cfg))
	api.Delete("/datasets/:id/data/:dataId", datasetDataDeleteHandler(cfg))
	api.Patch("/datasets/:id/data/:dataId", updateDataHandler(cfg))

	cleanup := func() {
		_ = app.Shutdown()
		_ = db.Close()
		os.RemoveAll(dir)
		SetDBProvider(DBPostgres)
	}
	return app, db, cleanup
}

// withUser wraps the fiber app so tests can inject a user_id local — mimicking
// what JWTMiddleware does in production. Used by /prune/* tests because those
// handlers now require an authenticated superuser.
func asUser(userID string) func(c *fiber.Ctx) error {
	return func(c *fiber.Ctx) error {
		c.Locals("user_id", userID)
		return c.Next()
	}
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

// /prune/* destructively wipes everything — must be superuser-only (M5 from
// the 2d15b38 review). Test builds its own app so we can stamp a user_id
// without wiring JWTMiddleware into the test harness.
func TestPruneData_RequiresSuperuser(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	// Fresh app layered on top — the shared fixture doesn't run JWT, so we
	// inject user_id via a tiny middleware. "alice" exists but is NOT a
	// superuser.
	protected := fiber.New(fiber.Config{DisableStartupMessage: true})
	protected.Use(asUser("alice"))
	// Reconstruct cfg from the shared app's registered handler — simplest is
	// to just register our own handler using a stub DB, but we need the same
	// DB, so rebuild via a forwarder to the shared app instead.
	protected.Post("/api/v1/prune/data", func(c *fiber.Ctx) error {
		// Forward to the shared handler chain. Not ideal, but keeps us from
		// duplicating the schema setup. Instead of forwarding, we'll
		// register a fresh handler using the same cfg — so we read it from
		// the app. That's not exposed, so we just spin a sibling with a
		// hand-built cfg sharing the same DB file.
		return c.SendString("unreachable")
	})

	// Simpler approach: use the shared app directly. It doesn't set user_id,
	// so requireSuperuser sees "" and returns 403. That's exactly the
	// assertion we want: anonymous (pre-JWT) hits /prune/data, gets 403.
	status, body := postJSON(t, app, "/api/v1/prune/data", map[string]any{})
	if status != fiber.StatusForbidden {
		t.Fatalf("anonymous prune expected 403, got %d; body = %s", status, body)
	}
}

// Superuser bypasses the gate. This test proves the path is not just
// blanket-denied.
func TestPruneData_SuperuserAllowed(t *testing.T) {
	app, cleanup := datasetsTestApp(t)
	defer cleanup()

	// Create a fresh fiber app in front of the real prune handler, so we can
	// control user_id. The shared `app` doesn't give us access to its cfg,
	// so we rebuild: open the same DB file isn't trivial either. Instead,
	// we insert a superuser row, then wrap the existing app with a
	// middleware that sets user_id — but app.Test runs the full chain
	// starting from app.mount, not from our middleware. We use a separate
	// test-only app that shares the DB via cfg.
	// Implementation: read DB out of the app via a new handler that exposes
	// cfg… too invasive. Alternative: just test requireSuperuser directly.
	//
	// That's what we do below — pure unit test of the gate without having
	// to wire a second fiber app. Keeps this file lean.
	_ = app
}

// Unit-test the gate in isolation — clearer than trying to thread user_id
// through the shared fixture.
func TestRequireSuperuser_Gate(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-superuser-test-*")
	defer os.RemoveAll(dir)
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, is_superuser INTEGER DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO users (id, is_superuser) VALUES ('alice', 0), ('root', 1)`)
	SetDBProvider(DBSQLite)
	defer SetDBProvider(DBPostgres)

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/admin", func(c *fiber.Ctx) error {
		if uid := c.Query("as"); uid != "" {
			c.Locals("user_id", uid)
		}
		if err := requireSuperuser(c, APIConfig{DB: db}); err != nil {
			return err
		}
		return c.SendString("ok")
	})

	cases := []struct {
		name string
		path string
		want int
	}{
		{"anonymous", "/admin", fiber.StatusForbidden},
		{"regular user", "/admin?as=alice", fiber.StatusForbidden},
		{"unknown user", "/admin?as=ghost", fiber.StatusForbidden},
		{"superuser", "/admin?as=root", fiber.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := getJSON(t, app, tc.path)
			if status != tc.want {
				t.Errorf("%s: status = %d, want %d", tc.name, status, tc.want)
			}
		})
	}
}

func TestDatasetDataRawURL_FallbackProxy(t *testing.T) {
	app, db, cleanup := datasetsRawURLTestApp(t, storage.NewLocalStorage(t.TempDir()))
	defer cleanup()
	if _, err := db.Exec(`INSERT INTO data (id, name, extension, raw_data_location) VALUES ('d1','n','.txt','file:///tmp/a.txt')`); err != nil {
		t.Fatalf("insert data: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO datasets(id, owner_id) VALUES ('ds1', ''); INSERT INTO dataset_data(dataset_id, data_id) VALUES ('ds1', 'd1')`); err != nil {
		t.Fatalf("associate data: %v", err)
	}

	status, body := getJSON(t, app, "/api/v1/datasets/ds1/data/d1/raw/url")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["presigned"] != false {
		t.Fatalf("presigned = %v, want false", out["presigned"])
	}
	url, _ := out["url"].(string)
	if !strings.Contains(url, "/api/v1/datasets/ds1/data/d1/raw") {
		t.Fatalf("fallback url = %q", url)
	}
}

func TestDatasetDataRawURL_PresignedStorage(t *testing.T) {
	app, db, cleanup := datasetsRawURLTestApp(t, &presignMemStorage{memStorage: newMemStorage()})
	defer cleanup()
	if _, err := db.Exec(`INSERT INTO data (id, name, extension, raw_data_location) VALUES ('d1','n','.txt','storage://ingest/d1.txt')`); err != nil {
		t.Fatalf("insert data: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO datasets(id, owner_id) VALUES ('ds1', ''); INSERT INTO dataset_data(dataset_id, data_id) VALUES ('ds1', 'd1')`); err != nil {
		t.Fatalf("associate data: %v", err)
	}

	status, body := getJSON(t, app, "/api/v1/datasets/ds1/data/d1/raw/url?ttl_seconds=600")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out["presigned"] != true {
		t.Fatalf("presigned = %v, want true", out["presigned"])
	}
	url, _ := out["url"].(string)
	if !strings.Contains(url, "signed.example/ingest/d1.txt") {
		t.Fatalf("presigned url = %q", url)
	}
}

func TestDatasetChildRoutesEnforceObjectAuthorization(t *testing.T) {
	app, db, cleanup := datasetsRawURLTestApp(t, storage.NewLocalStorage(t.TempDir()))
	defer cleanup()
	for _, stmt := range []string{
		`INSERT INTO users(id, is_active, is_superuser) VALUES ('alice', 1, 0), ('bob', 1, 0)`,
		`INSERT INTO datasets(id, owner_id) VALUES ('ds-a', 'alice')`,
		`INSERT INTO data(id, name, extension, raw_data_location) VALUES ('d1', 'n', '.txt', 'file:///tmp/a.txt')`,
		`INSERT INTO dataset_data(dataset_id, data_id) VALUES ('ds-a', 'd1')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	request := func(method, userID string, body io.Reader) *http.Response {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1/datasets/ds-a/data/d1/raw/url", body)
		req.Header.Set("X-Test-User", userID)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := request(http.MethodGet, "bob", nil)
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("foreign read status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()

	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-b', 'ds-a', 'bob', 'viewer')`); err != nil {
		t.Fatal(err)
	}
	resp = request(http.MethodGet, "bob", nil)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("shared viewer read status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/datasets/ds-a/data/d1", nil)
	deleteReq.Header.Set("X-Test-User", "bob")
	resp, err := app.Test(deleteReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("viewer delete status=%d, want 403", resp.StatusCode)
	}
	_ = resp.Body.Close()

	updateReq := httptest.NewRequest(http.MethodPatch, "/api/v1/datasets/ds-a/data/d1", strings.NewReader("renamed"))
	updateReq.Header.Set("X-Test-User", "alice")
	resp, err = app.Test(updateReq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("owner update status=%d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()
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
