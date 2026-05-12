package http

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

func newSyncMemoryTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE memories (
			id TEXT PRIMARY KEY,
			key TEXT NOT NULL,
			value TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'project',
			owner_id TEXT NOT NULL DEFAULT '',
			collection_name TEXT NOT NULL DEFAULT '',
			room TEXT NOT NULL DEFAULT '',
			hall TEXT NOT NULL DEFAULT '',
			is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
			pin_priority INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(key, owner_id)
		);
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	return db, func() {
		_ = db.Close()
		SetDBProvider(DBPostgres)
	}
}

func TestSyncExportMemoriesPreservesRoomHallPins(t *testing.T) {
	db, cleanup := newSyncMemoryTestDB(t)
	defer cleanup()
	if _, err := db.Exec(`
		INSERT INTO memories (
			id, key, value, type, owner_id, collection_name, room, hall,
			is_pinned, pin_priority, created_at, updated_at
		) VALUES (
			'm1', 'deploy.freeze', 'freeze on 2026-05-10', 'project', 'user-1',
			'levara', 'deploy', 'event', TRUE, 9, '2026-05-10T00:00:00Z', '2026-05-10T01:00:00Z'
		)
	`); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{DB: db})
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/sync/export/memories", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var memories []syncMemory
	if err := json.NewDecoder(resp.Body).Decode(&memories); err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Fatalf("memories len=%d, want 1", len(memories))
	}
	got := memories[0]
	if got.Room != "deploy" || got.Hall != "event" || !got.IsPinned || got.PinPriority != 9 {
		t.Fatalf("exported memory lost palace fields: %+v", got)
	}
}

func TestSyncImportMemoriesPreservesRoomHallPins(t *testing.T) {
	db, cleanup := newSyncMemoryTestDB(t)
	defer cleanup()

	app := fiber.New()
	RegisterSyncAPI(app, APIConfig{DB: db})
	payload, _ := json.Marshal([]syncMemory{{
		ID:             "m1",
		Key:            "style",
		Value:          "terse russian",
		Type:           "preference",
		OwnerID:        "user-1",
		CollectionName: "levara",
		Room:           "agent",
		Hall:           "preference",
		IsPinned:       true,
		PinPriority:    10,
		CreatedAt:      "2026-05-10T00:00:00Z",
		UpdatedAt:      "2026-05-10T01:00:00Z",
	}})
	req := httptest.NewRequest(http.MethodPost, "/sync/import/memories", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}

	var room, hall string
	var pinned bool
	var prio int
	if err := db.QueryRow(`
		SELECT room, hall, is_pinned, pin_priority
		FROM memories WHERE key = 'style' AND owner_id = 'user-1'
	`).Scan(&room, &hall, &pinned, &prio); err != nil {
		t.Fatal(err)
	}
	if room != "agent" || hall != "preference" || !pinned || prio != 10 {
		t.Fatalf("imported fields: room=%q hall=%q pinned=%v prio=%d", room, hall, pinned, prio)
	}
}
