package http

import (
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
)

// collections_record_delete_test.go — HTTP-surface coverage for
// DELETE /collections/:name/records/:id. This is the per-vector delete
// primitive the P1.4 orphan-vector GC relies on (remove vectors whose
// memory_id no longer exists in the SQL memories table). The underlying
// store.Delete behaviour is covered in store/; this file pins the
// status-code contract: 204 on success, 404 when the id is absent.

func newRecordDeleteApp(t *testing.T) (*fiber.App, *store.CollectionManager, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-http-recdel-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	cm, err := store.NewCollectionManager(8, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewCollectionManager: %v", err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Delete("/collections/:name/records/:id", collectionRecordDeleteHandler(APIConfig{Collections: cm}))
	cleanup := func() {
		_ = cm.Close()
		os.RemoveAll(dir)
	}
	return app, cm, cleanup
}

func deleteRecord(t *testing.T, app *fiber.App, name, id string) int {
	t.Helper()
	r := httptest.NewRequest("DELETE", "/collections/"+name+"/records/"+id, nil)
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

func TestCollectionRecordDeleteHandler_HappyPath(t *testing.T) {
	app, cm, cleanup := newRecordDeleteApp(t)
	defer cleanup()

	if err := cm.Create("c"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cm.Insert("c", "vec-1", []float32{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8}, map[string]string{"memory_id": "vec-1"}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	if status := deleteRecord(t, app, "c", "vec-1"); status != 204 {
		t.Errorf("delete existing record: status = %d, want 204", status)
	}
	// Second delete of the same id is now a 404 — the vector is gone.
	if status := deleteRecord(t, app, "c", "vec-1"); status != 404 {
		t.Errorf("delete missing record: status = %d, want 404", status)
	}
}

func TestCollectionRecordDeleteHandler_MissingCollection(t *testing.T) {
	app, _, cleanup := newRecordDeleteApp(t)
	defer cleanup()

	// Unknown collection → store surfaces "not found" → 404.
	if status := deleteRecord(t, app, "nope", "x"); status != 404 {
		t.Errorf("delete from unknown collection: status = %d, want 404", status)
	}
}
