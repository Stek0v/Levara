package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
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
