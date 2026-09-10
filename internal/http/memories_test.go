package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/memoryindex"
)

func TestSaveMemorySQLiteUpsertUsesCollectionScopedKey(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "levara.db")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	SetDBProvider(DBSQLite)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterMemoryAPI(app.Group("/api/v1"), APIConfig{DB: db})

	body := map[string]string{
		"key":             "ui-memory",
		"value":           "first",
		"type":            "fact",
		"owner_id":        "owner-1",
		"collection_name": "levara",
	}
	postMemory(t, app, body, 201)
	body["value"] = "second"
	postMemory(t, app, body, 201)

	var count int
	var value string
	if err := db.QueryRow(`SELECT COUNT(*), MAX(value) FROM memories WHERE key = 'ui-memory' AND owner_id = 'owner-1' AND collection_name = 'levara'`).Scan(&count, &value); err != nil {
		t.Fatalf("query memory: %v", err)
	}
	if count != 1 || value != "second" {
		t.Fatalf("memory count=%d value=%q, want one updated row", count, value)
	}
}

func postMemory(t *testing.T, app *fiber.App, body any, want int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/memories", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("status=%d, want %d", resp.StatusCode, want)
	}
}

func TestDeleteMemoryByIDRemovesOnlySelectedIdentity(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	outbox, err := memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	collections, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "alice")
		return c.Next()
	})
	RegisterMemoryAPI(app.Group("/api/v1"), APIConfig{DB: db, MemoryIndexOutbox: outbox, Collections: collections})
	selectedID := "selected % +"
	for _, args := range [][]any{
		{selectedID, "dup", "selected", "alice", "one"},
		{"sibling", "dup", "sibling", "alice", "two"},
		{"shared", "dup", "shared", "", "one"},
		{"foreign", "dup", "foreign", "bob", "one"},
		{"id-as-key", selectedID, "control", "alice", "one"},
	} {
		if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES(?,?,?,?,?)`, args...); err != nil {
			t.Fatal(err)
		}
	}

	resp, body := deleteMemoryRequest(t, app, "/api/v1/memories/by-id/"+url.PathEscape(selectedID))
	if resp != http.StatusOK || !strings.Contains(body, `"id":"selected % +"`) {
		t.Fatalf("status=%d body=%s", resp, body)
	}
	var controls, selected, jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id IN ('sibling','shared','foreign','id-as-key')`).Scan(&controls); err != nil || controls != 4 {
		t.Fatalf("controls=%d err=%v", controls, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id=?`, selectedID).Scan(&selected); err != nil || selected != 0 {
		t.Fatalf("selected=%d err=%v", selected, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs WHERE memory_id=? AND operation='delete_vector' AND collection_name='one' AND owner_id='alice' AND digest=?`, selectedID, "delete:"+selectedID).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
}

func TestDeleteMemoryLegacyAmbiguityAndSharedNoop(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	outbox, err := memoryindex.NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "alice")
		return c.Next()
	})
	RegisterMemoryAPI(app.Group("/api/v1"), APIConfig{DB: db, MemoryIndexOutbox: outbox})
	for _, args := range [][]any{
		{"one", "dup", "one", "alice", "one"},
		{"two", "dup", "two", "alice", "two"},
		{"shared", "shared-only", "shared", "", "one"},
	} {
		if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES(?,?,?,?,?)`, args...); err != nil {
			t.Fatal(err)
		}
	}
	if status, body := deleteMemoryRequest(t, app, "/api/v1/memories/dup"); status != http.StatusConflict || !strings.Contains(body, "ambiguous") {
		t.Fatalf("ambiguous status=%d body=%s", status, body)
	}
	if status, body := deleteMemoryRequest(t, app, "/api/v1/memories/missing"); status != http.StatusOK || !strings.Contains(body, `"deleted":true`) {
		t.Fatalf("missing status=%d body=%s", status, body)
	}
	if status, _ := deleteMemoryRequest(t, app, "/api/v1/memories/shared-only"); status != http.StatusOK {
		t.Fatalf("shared key status=%d", status)
	}
	var rows, jobs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&rows); err != nil || rows != 3 {
		t.Fatalf("legacy request changed rows: rows=%d err=%v", rows, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_index_jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("legacy request queued jobs: jobs=%d err=%v", jobs, err)
	}
}

func TestDeleteMemoryByIDHidesForeignAndMissing(t *testing.T) {
	db := newMCPMemoryBehaviorDB(t)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "alice")
		return c.Next()
	})
	RegisterMemoryAPI(app.Group("/api/v1"), APIConfig{DB: db})
	if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name) VALUES('foreign','secret','value','bob','one')`); err != nil {
		t.Fatal(err)
	}
	foreignStatus, foreignBody := deleteMemoryRequest(t, app, "/api/v1/memories/by-id/foreign")
	missingStatus, missingBody := deleteMemoryRequest(t, app, "/api/v1/memories/by-id/missing")
	if foreignStatus != http.StatusNotFound || missingStatus != http.StatusNotFound || foreignBody != missingBody {
		t.Fatalf("foreign=(%d,%s) missing=(%d,%s)", foreignStatus, foreignBody, missingStatus, missingBody)
	}
}

func deleteMemoryRequest(t *testing.T, app *fiber.App, path string) (int, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(http.MethodDelete, path, nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}
