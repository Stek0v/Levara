package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
)

// collections_rename_test.go — HTTP-surface coverage for
// POST /collections/:name/rename. The underlying store.Rename behaviour
// is covered by store/collections_test.go; this file pins the status-code
// mapping the migration runbook relies on (404 → stop, 409 → name clash,
// 400 → bad input, 200 → swapped).

func newRenameApp(t *testing.T) (*fiber.App, *store.CollectionManager, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-http-rename-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	cm, err := store.NewCollectionManager(8, dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewCollectionManager: %v", err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/collections/:name/rename", collectionRenameHandler(APIConfig{Collections: cm}))
	cleanup := func() {
		_ = cm.Close()
		os.RemoveAll(dir)
	}
	return app, cm, cleanup
}

func postRename(t *testing.T, app *fiber.App, name string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if raw, ok := body.([]byte); ok {
		reader = bytes.NewReader(raw)
	} else {
		b, _ := json.Marshal(body)
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest("POST", "/collections/"+name+"/rename", reader)
	r.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(buf, &out)
	return resp.StatusCode, out
}

func TestCollectionRenameHandler_HappyPath(t *testing.T) {
	app, cm, cleanup := newRenameApp(t)
	defer cleanup()

	if err := cm.Create("orig"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	status, body := postRename(t, app, "orig", map[string]string{"new_name": "renamed"})
	if status != 200 {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if name, _ := body["name"].(string); name != "renamed" {
		t.Fatalf("response name=%v, want renamed", body["name"])
	}
	if !cm.Has("renamed") || cm.Has("orig") {
		t.Fatalf("manager state wrong after rename: has renamed=%v has orig=%v",
			cm.Has("renamed"), cm.Has("orig"))
	}
}

func TestCollectionRenameHandler_NotFound(t *testing.T) {
	app, _, cleanup := newRenameApp(t)
	defer cleanup()

	status, _ := postRename(t, app, "missing", map[string]string{"new_name": "whatever"})
	if status != 404 {
		t.Fatalf("status=%d, want 404", status)
	}
}

func TestCollectionRenameHandler_Conflict(t *testing.T) {
	app, cm, cleanup := newRenameApp(t)
	defer cleanup()

	_ = cm.Create("a")
	_ = cm.Create("b")
	status, _ := postRename(t, app, "a", map[string]string{"new_name": "b"})
	if status != 409 {
		t.Fatalf("status=%d, want 409", status)
	}
}

func TestCollectionRenameHandler_IdenticalNames(t *testing.T) {
	app, cm, cleanup := newRenameApp(t)
	defer cleanup()

	_ = cm.Create("same")
	status, _ := postRename(t, app, "same", map[string]string{"new_name": "same"})
	if status != 409 {
		t.Fatalf("status=%d, want 409 for identical names", status)
	}
}

func TestCollectionRenameHandler_InvalidName(t *testing.T) {
	app, cm, cleanup := newRenameApp(t)
	defer cleanup()

	_ = cm.Create("src")
	status, _ := postRename(t, app, "src", map[string]string{"new_name": "../escape"})
	if status != 400 {
		t.Fatalf("status=%d, want 400 for path-traversal name", status)
	}
}

func TestCollectionRenameHandler_MissingNewName(t *testing.T) {
	app, cm, cleanup := newRenameApp(t)
	defer cleanup()

	_ = cm.Create("src")
	status, _ := postRename(t, app, "src", map[string]string{})
	if status != 400 {
		t.Fatalf("status=%d, want 400 for missing new_name", status)
	}
}

func TestCollectionRenameHandler_NoCollections(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/collections/:name/rename", collectionRenameHandler(APIConfig{}))
	r := httptest.NewRequest("POST", "/collections/foo/rename",
		bytes.NewReader([]byte(`{"new_name":"bar"}`)))
	r.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Fatalf("status=%d, want 503 when Collections nil", resp.StatusCode)
	}
}
