package http

import (
	"bytes"
	"context"
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
			superseded_by TEXT NOT NULL DEFAULT '', valid_until TEXT,
            supersedes_memory_id TEXT NOT NULL DEFAULT '', supersession_reason TEXT NOT NULL DEFAULT '',
			source_task_id TEXT NOT NULL DEFAULT '', source_receipt_ids TEXT NOT NULL DEFAULT '[]',
			verification_status TEXT NOT NULL DEFAULT 'unverified',
 tier TEXT NOT NULL DEFAULT 'raw', consolidated_from TEXT NOT NULL DEFAULT '', consolidation_run_id TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(key, owner_id, collection_name)
		);
        CREATE TABLE memory_sync_deletions(memory_id TEXT NOT NULL,key TEXT NOT NULL,owner_id TEXT NOT NULL,collection_name TEXT NOT NULL,deleted_at TEXT NOT NULL,PRIMARY KEY(memory_id,owner_id,collection_name));
	`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	SetDBProvider(DBSQLite)
	if err := migrateMemorySyncGenerations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
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

	var batch syncMemoryBatch
	if err := json.NewDecoder(resp.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	memories := batch.Memories
	if err := batch.validate(); err != nil {
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
	payload, _ := json.Marshal(syncMemoryFixtureBatch([]syncMemory{{
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
	}}))
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

// syncMemoryFixtureBatch describes independent first-generation test rows.
func syncMemoryFixtureBatch(memories []syncMemory) syncMemoryBatch {
	b := syncMemoryBatch{ProtocolVersion: syncMemoryProtocolVersion, Memories: memories, Deletions: []syncMemoryDeletion{}, Incarnations: []syncMemoryIncarnation{}, Aliases: []syncMemoryAlias{}}
	for _, m := range memories {
		state := "active"
		if m.ValidUntil != "" || m.SupersededBy != "" {
			state = "retired"
		}
		b.Incarnations = append(b.Incarnations, syncMemoryIncarnation{MemoryID: m.ID, OwnerID: m.OwnerID, CollectionName: m.CollectionName, LogicalKey: m.Key, OriginalKeyResolved: 1, State: state})
		b.Aliases = append(b.Aliases, syncMemoryAlias{AliasID: m.ID, MemoryID: m.ID})
	}
	return b
}
