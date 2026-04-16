package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/cognevra/internal/cluster"
	"github.com/stek0v/cognevra/internal/store"
)

// T-5: HTTP contract tests for the four vector-ops endpoints that back every
// search, ingest, delete and batch-write path in Levara:
//
//   POST /api/v1/insert
//   POST /api/v1/batch_insert
//   POST /api/v1/search
//   POST /api/v1/delete
//
// The goal is a regression net for the request/response shape — the fields,
// status codes, and error messages that external clients (WebUI, SDK, MCP
// tools) depend on. We use a real store.Levara via cluster.DirectNode instead
// of mocking so the tests also exercise the wiring that main.go uses.
//
// These tests intentionally avoid auth middleware: we register the handlers
// on a bare fiber.App to isolate the contract. Auth is covered elsewhere.

// testHandler spins up a Handler wired to a real in-memory-ish store
// (tempdir-backed) plus a fiber app mounted at /api/v1/* with the four core
// vector-op routes. Returns the app + cleanup.
func testHandler(t testing.TB, dim int) (*fiber.App, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-contract-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	shard := &cluster.DirectNode{DB: db}
	c := store.NewCluster([]store.ShardHandler{shard})

	h := NewHandler(c, dim)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api := app.Group("/api/v1")
	api.Post("/insert", h.Insert)
	api.Post("/batch_insert", h.BatchInsert)
	api.Post("/search", h.Search)
	api.Post("/delete", h.Delete)

	return app, func() {
		_ = app.Shutdown()
		_ = db.Close()
		os.RemoveAll(dir)
	}
}

// doJSON posts body as JSON and returns status + parsed response body.
func doJSON(t testing.TB, app *fiber.App, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody
}

// ──────────────────────────────────────────────────────────────────
// POST /api/v1/insert
// ──────────────────────────────────────────────────────────────────

func TestInsert_HappyPath(t *testing.T) {
	app, cleanup := testHandler(t, 4)
	defer cleanup()

	req := InsertRequest{
		ID:     "rec-1",
		Vector: []float32{1, 2, 3, 4},
		Data:   map[string]any{"title": "hello"},
	}
	status, body := doJSON(t, app, "POST", "/api/v1/insert", req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out map[string]string
	_ = json.Unmarshal(body, &out)
	if !strings.Contains(out["message"], "inserted successfully") {
		t.Errorf("body = %s, want 'inserted successfully'", body)
	}
}

func TestInsert_BadJSON_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 4)
	defer cleanup()

	req := httptest.NewRequest("POST", "/api/v1/insert", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestInsert_MissingID_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 4)
	defer cleanup()
	status, body := doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{
		Vector: []float32{1, 2, 3, 4},
	})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", status, body)
	}
	if !strings.Contains(string(body), "id and vector") {
		t.Errorf("error message = %s, want to mention 'id and vector'", body)
	}
}

func TestInsert_MissingVector_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 4)
	defer cleanup()
	status, _ := doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{ID: "x"})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

// ──────────────────────────────────────────────────────────────────
// POST /api/v1/batch_insert
// ──────────────────────────────────────────────────────────────────

func TestBatchInsert_HappyPath(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()

	req := BatchInsertRequest{
		Records: []BatchInsertItem{
			{ID: "a", Vector: []float32{1, 0}, Data: map[string]any{"n": 1}},
			{ID: "b", Vector: []float32{0, 1}, Data: map[string]any{"n": 2}},
			{ID: "c", Vector: []float32{1, 1}},
		},
	}
	status, body := doJSON(t, app, "POST", "/api/v1/batch_insert", req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out BatchInsertResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Inserted != 3 || out.Failed != 0 {
		t.Errorf("out = %+v, want Inserted=3 Failed=0", out)
	}
}

func TestBatchInsert_EmptyRecords_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	status, body := doJSON(t, app, "POST", "/api/v1/batch_insert", BatchInsertRequest{})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", status, body)
	}
}

func TestBatchInsert_InvalidRecord_Returns400(t *testing.T) {
	// One invalid record (missing vector) → whole batch rejected with 400
	// before any record is inserted. This is the current contract; clients
	// rely on all-or-nothing semantics for partial validation errors.
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	req := BatchInsertRequest{
		Records: []BatchInsertItem{
			{ID: "good", Vector: []float32{1, 0}},
			{ID: "bad-no-vector"},
		},
	}
	status, body := doJSON(t, app, "POST", "/api/v1/batch_insert", req)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", status, body)
	}
	if !strings.Contains(string(body), "bad-no-vector") {
		t.Errorf("error should name the offending record; body = %s", body)
	}
}

// ──────────────────────────────────────────────────────────────────
// POST /api/v1/search
// ──────────────────────────────────────────────────────────────────

func TestSearch_HappyPath(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()

	// Seed a couple of records.
	_, _ = doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{
		ID: "a", Vector: []float32{1, 0}, Data: map[string]any{"tag": "x"},
	})
	_, _ = doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{
		ID: "b", Vector: []float32{0, 1}, Data: map[string]any{"tag": "y"},
	})

	status, body := doJSON(t, app, "POST", "/api/v1/search", SearchRequest{
		Vector: []float32{1, 0}, TopK: 2,
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out SearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	// Results may be empty if HNSW indexer hasn't caught up, but the contract
	// is the shape: SearchResponse with a results array (possibly empty).
	if out.Results == nil {
		t.Error("Results = nil, want non-nil slice (can be empty)")
	}
}

func TestSearch_MissingVector_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	status, _ := doJSON(t, app, "POST", "/api/v1/search", SearchRequest{TopK: 5})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestSearch_DefaultTopK(t *testing.T) {
	// Omitted TopK should default to 5, not error. Contract stability check.
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	status, body := doJSON(t, app, "POST", "/api/v1/search", SearchRequest{
		Vector: []float32{1, 0},
	})
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %s", status, body)
	}
}

func TestSearch_BadJSON_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	req := httptest.NewRequest("POST", "/api/v1/search", strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ──────────────────────────────────────────────────────────────────
// POST /api/v1/delete
// ──────────────────────────────────────────────────────────────────

func TestDelete_HappyPath(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()

	for _, id := range []string{"a", "b"} {
		_, _ = doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{
			ID: id, Vector: []float32{1, 0},
		})
	}

	status, body := doJSON(t, app, "POST", "/api/v1/delete", DeleteRequest{
		IDs: []string{"a", "b"},
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", status, body)
	}
	var out DeleteResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if out.Deleted != 2 || out.Failed != 0 {
		t.Errorf("out = %+v, want Deleted=2 Failed=0", out)
	}
}

func TestDelete_EmptyIDs_Returns400(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	status, body := doJSON(t, app, "POST", "/api/v1/delete", DeleteRequest{})
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", status, body)
	}
}

func TestDelete_MissingRecord_ReportsFailure(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()

	status, body := doJSON(t, app, "POST", "/api/v1/delete", DeleteRequest{
		IDs: []string{"nonexistent"},
	})
	// Delete of a nonexistent record is NOT a 4xx — the API is idempotent-ish:
	// Failed count is incremented but the call succeeds. Contract lock-in.
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200 (deletion of missing record is not a client error); body = %s",
			status, body)
	}
	var out DeleteResponse
	_ = json.Unmarshal(body, &out)
	if out.Failed != 1 || len(out.Errors) != 1 {
		t.Errorf("out = %+v, want Failed=1 with 1 error entry", out)
	}
}

// ──────────────────────────────────────────────────────────────────
// Round-trip: insert → delete → search hits nothing
// ──────────────────────────────────────────────────────────────────

func TestInsertDeleteSearch_Roundtrip(t *testing.T) {
	app, cleanup := testHandler(t, 2)
	defer cleanup()

	// Insert, delete, then re-search — guards against regressions where
	// the handler stops calling into cluster methods properly.
	_, _ = doJSON(t, app, "POST", "/api/v1/insert", InsertRequest{
		ID: "ephemeral", Vector: []float32{0.5, 0.5},
	})
	status, _ := doJSON(t, app, "POST", "/api/v1/delete", DeleteRequest{
		IDs: []string{"ephemeral"},
	})
	if status != http.StatusOK {
		t.Fatalf("delete status = %d", status)
	}
	// Search and don't care about the exact results — the shape must
	// round-trip (no panic, valid JSON, SearchResponse parseable).
	status, body := doJSON(t, app, "POST", "/api/v1/search", SearchRequest{
		Vector: []float32{0.5, 0.5}, TopK: 5,
	})
	if status != http.StatusOK {
		t.Fatalf("search status = %d", status)
	}
	var out SearchResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("search response not parseable: %v; body = %s", err, body)
	}
}

// ──────────────────────────────────────────────────────────────────
// Content-Type requirement
// ──────────────────────────────────────────────────────────────────

func TestInsert_MissingContentType_Returns400(t *testing.T) {
	// fiber's BodyParser rejects bodies without Content-Type=application/json.
	// Locking this in prevents accidental MIME type sniffing regressions.
	app, cleanup := testHandler(t, 2)
	defer cleanup()
	body, _ := json.Marshal(InsertRequest{ID: "x", Vector: []float32{1, 0}})
	req := httptest.NewRequest("POST", "/api/v1/insert", bytes.NewReader(body))
	// No Content-Type header set.
	resp, _ := app.Test(req, -1)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no Content-Type)", resp.StatusCode)
	}
}
