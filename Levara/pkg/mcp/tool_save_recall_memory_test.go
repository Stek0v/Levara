package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// setupSaveRecallMemoryDB builds the memories schema with the
// UNIQUE(key, owner_id) constraint the upsert depends on.
func setupSaveRecallMemoryDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-save-recall-test-*.db")
	path := f.Name()
	f.Close()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		os.Remove(path)
	})

	stmt := `CREATE TABLE memories (
		id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT, owner_id TEXT,
		collection_name TEXT, room TEXT, hall TEXT,
		is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
		created_at TEXT, updated_at TEXT,
		superseded_by TEXT DEFAULT '',
		UNIQUE(key, owner_id)
	)`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create: %v", err)
	}
	return &fakeDeps{db: db}
}

// ── ToolSaveMemory ──

func TestToolSaveMemory_RequiresKeyAndValue(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	bad := []map[string]any{
		{},
		{"key": "k"},
		{"value": "v"},
		{"key": "", "value": "v"},
		{"key": "k", "value": ""},
	}
	for i, args := range bad {
		got := ToolSaveMemory(context.Background(), deps, args)
		if !got.IsError {
			t.Errorf("case %d args=%+v: IsError = false, want true", i, args)
		}
		if !strings.Contains(got.Content[0].Text, "'key' and 'value' required") {
			t.Errorf("case %d: content = %q", i, got.Content[0].Text)
		}
	}
}

func TestToolSaveMemory_NilDBIsError(t *testing.T) {
	got := ToolSaveMemory(context.Background(), nilDBDeps{}, map[string]any{
		"key": "k", "value": "v",
	})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
}

func TestToolSaveMemory_InvalidHallIsError(t *testing.T) {
	// hall vocabulary is enforced at pkg/mcp.IsValidHall. An invalid
	// value returns a helpful error listing valid halls.
	deps := setupSaveRecallMemoryDB(t)
	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": "v", "hall": "bogus",
	})
	if !got.IsError {
		t.Fatalf("bogus hall: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "invalid hall 'bogus'") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolSaveMemory_DefaultsToProjectType(t *testing.T) {
	// Missing 'type' → "project" default.
	deps := setupSaveRecallMemoryDB(t)
	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": "v",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "type: project") {
		t.Errorf("content = %q, want 'type: project'", got.Content[0].Text)
	}
}

func TestToolSaveMemory_InsertsRowWithPinFlags(t *testing.T) {
	// 'pin' + 'pin_priority' propagate to the row.
	deps := setupSaveRecallMemoryDB(t)

	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "alpha", "value": "first",
		"type": "fact", "pin": true, "pin_priority": float64(7),
		"room": "auth", "hall": "decision",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	var key, value, typ, room, hall string
	var pinned, prio int
	err := deps.db.QueryRow(`SELECT key, value, type, room, hall, is_pinned, pin_priority FROM memories`).Scan(
		&key, &value, &typ, &room, &hall, &pinned, &prio)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if key != "alpha" || value != "first" || typ != "fact" {
		t.Errorf("row = %s/%s/%s, want alpha/first/fact", key, value, typ)
	}
	if room != "auth" || hall != "decision" {
		t.Errorf("room/hall = %s/%s, want auth/decision", room, hall)
	}
	if pinned != 1 || prio != 7 {
		t.Errorf("pinned/prio = %d/%d, want 1/7", pinned, prio)
	}
}

func TestToolSaveMemory_UpsertOnConflict(t *testing.T) {
	// Same key, same owner → UPDATE (value replaced, row count stays 1).
	deps := setupSaveRecallMemoryDB(t)

	ToolSaveMemory(context.Background(), deps, map[string]any{"key": "k", "value": "v1"})
	ToolSaveMemory(context.Background(), deps, map[string]any{"key": "k", "value": "v2"})

	var value string
	var count int
	deps.db.QueryRow(`SELECT value FROM memories WHERE key = 'k'`).Scan(&value)
	deps.db.QueryRow(`SELECT COUNT(*) FROM memories WHERE key = 'k'`).Scan(&count)
	if count != 1 {
		t.Errorf("row count = %d, want 1 (upsert)", count)
	}
	if value != "v2" {
		t.Errorf("value = %q, want v2 (update applied)", value)
	}
}

func TestToolSaveMemory_TruncatesValueInMessage(t *testing.T) {
	// Long values are truncated in the success message but the row
	// carries the full value.
	deps := setupSaveRecallMemoryDB(t)

	long := strings.Repeat("x", 500)
	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": long,
	})
	if strings.Contains(got.Content[0].Text, long) {
		t.Errorf("success message contains full 500-char value; expected Truncate")
	}

	var storedValue string
	deps.db.QueryRow(`SELECT value FROM memories WHERE key = 'k'`).Scan(&storedValue)
	if storedValue != long {
		t.Errorf("DB value was truncated; row length = %d, want 500", len(storedValue))
	}
}

func TestToolSaveMemory_EmbedPathFiresGoroutine(t *testing.T) {
	// With EmbedAvailable=true, a background goroutine calls Embed and
	// CollectionInsert. The tool returns immediately; the indexing
	// happens off the critical path. We poll briefly for the side effect.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	var embedCalled atomic.Bool
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		embedCalled.Store(true)
		return []float32{1, 2, 3}, nil
	}

	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key":        "topic",
		"value":      "content",
		"collection": "levara",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	// Poll up to 1s for the goroutine to complete.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if embedCalled.Load() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !embedCalled.Load() {
		t.Fatalf("Embed goroutine never ran")
	}

	// Give the Insert a moment to complete after Embed returns.
	// Must use getInserted() — direct insertedRows access races with
	// the goroutine writing via CollectionInsert.
	deadline = time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if len(deps.getInserted()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	inserted := deps.getInserted()
	if len(inserted) != 1 {
		t.Fatalf("CollectionInsert called %d times, want 1", len(inserted))
	}
	// The collection name reflects the pinned collection arg.
	if inserted[0].collection != "_memories_levara" {
		t.Errorf("insert collection = %q, want _memories_levara", inserted[0].collection)
	}
}

func TestToolSaveMemory_ReSaveReusesStableVectorID(t *testing.T) {
	// P1.4 regression: a re-save of the same (key, owner) must index the
	// vector under the SAME id both times — the canonical SQL row id — so
	// the second Insert overwrites the first vector in place instead of
	// leaving an orphan in HNSW. Pre-fix, ToolSaveMemory minted a fresh
	// uuid per call and indexed under it, so each re-save accreted a stale
	// vector (vectors > SQL rows).
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1, 2, 3}, nil
	}

	ctx := context.Background()
	ToolSaveMemory(ctx, deps, map[string]any{"key": "k", "value": "v1", "collection": "levara"})
	ToolSaveMemory(ctx, deps, map[string]any{"key": "k", "value": "v2", "collection": "levara"})

	// Poll for both background Inserts to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(deps.getInserted()) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	inserted := deps.getInserted()
	if len(inserted) != 2 {
		t.Fatalf("CollectionInsert called %d times, want 2", len(inserted))
	}
	if inserted[0].id != inserted[1].id {
		t.Errorf("re-save indexed under different vector ids %q != %q; orphan vector left in HNSW",
			inserted[0].id, inserted[1].id)
	}

	// The stable id must be the canonical SQL row id, so vector recall can
	// hydrate back to a live memories row.
	var rowID string
	if err := deps.db.QueryRow(`SELECT id FROM memories WHERE key = 'k'`).Scan(&rowID); err != nil {
		t.Fatalf("scan row id: %v", err)
	}
	if inserted[1].id != rowID {
		t.Errorf("vector id %q != canonical SQL row id %q", inserted[1].id, rowID)
	}
}

func TestToolSaveMemory_NoEmbedSkipsGoroutine(t *testing.T) {
	// With EmbedAvailable=false, no Embed call happens. The tool still
	// returns success — the vector-index is optional.
	deps := setupSaveRecallMemoryDB(t)
	// embedAvailable left as false.
	var embedCalled atomic.Bool
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		embedCalled.Store(true)
		return []float32{1, 2, 3}, nil
	}

	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": "v",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	// Wait a tick to confirm the goroutine isn't merely slow.
	time.Sleep(50 * time.Millisecond)
	if embedCalled.Load() {
		t.Errorf("Embed called despite EmbedAvailable=false")
	}
}

// ── ToolRecallMemory ──

func TestToolRecallMemory_RequiresQuery(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	got := ToolRecallMemory(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing query: IsError = false, want true")
	}
}

func TestToolRecallMemory_NilDBReturnsEmpty(t *testing.T) {
	got := ToolRecallMemory(context.Background(), nilDBDeps{}, map[string]any{"query": "anything"})
	if got.IsError {
		t.Fatalf("nil DB: IsError = true, want false")
	}
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolRecallMemory_SQLFallbackLikeMatch(t *testing.T) {
	// No embedding available → SQL LIKE path. Matches key or value
	// substring, scoped to owner_id (empty owner for test).
	// All nullable columns seeded with empty strings to match
	// ToolSaveMemory's production contract (it always writes defaults).
	deps := setupSaveRecallMemoryDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m1', 'auth', 'OAuth2 flow', 'fact', '', '', '', '', ?, ?)`, now, now)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m2', 'deploy', 'staging pipeline', 'fact', '', '', '', '', ?, ?)`, now, now)

	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "OAuth"})
	var items []map[string]any
	if err := json.Unmarshal([]byte(got.Content[0].Text), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 || items[0]["key"] != "auth" {
		t.Errorf("got %+v, want single 'auth' (matched via value)", items)
	}
}

func TestToolRecallMemory_SQLFallbackNoMatch(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "zzz"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "No memories found") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolRecallMemory_RoomFilterSkipsVectorPath(t *testing.T) {
	// With a room filter, vector search is skipped (room/hall aren't
	// indexed in vector metadata). This is the correctness guarantee
	// for structural filters — only SQL path runs.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	var embedCalled atomic.Bool
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		embedCalled.Store(true)
		return []float32{1, 2, 3}, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m1', 'k1', 'v matching', 'fact', '', '', 'auth', '', ?, ?)`, now, now)

	got := ToolRecallMemory(context.Background(), deps, map[string]any{
		"query": "matching",
		"room":  "auth",
	})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	if embedCalled.Load() {
		t.Errorf("Embed called despite room filter; vector path must be skipped")
	}
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 {
		t.Errorf("got %d items from SQL path, want 1", len(items))
	}
}

func TestToolRecallMemory_VectorPathReturnsMetaItems(t *testing.T) {
	// No structural filter + embed available → vector path. Search
	// results are unmarshalled from the Data field and returned.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.searchFn = func(collection string, q []float32, k int) ([]SearchResult, error) {
		meta, _ := json.Marshal(map[string]string{
			"key": "hit", "value": "semantic match", "type": "fact",
		})
		return []SearchResult{{ID: "id1", Score: 0.9, Data: meta}}, nil
	}

	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "anything"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	var items []map[string]string
	if err := json.Unmarshal([]byte(got.Content[0].Text), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 || items[0]["key"] != "hit" {
		t.Errorf("got %+v, want single 'hit'", items)
	}
}

func TestToolRecallMemory_VectorEmptyFallsThroughToSQL(t *testing.T) {
	// When vector search returns zero results, fall through to the
	// SQL path rather than returning "[]" — pre-refactor behavior.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.searchFn = func(collection string, q []float32, k int) ([]SearchResult, error) {
		return nil, nil // no hits
	}

	now := time.Now().UTC().Format(time.RFC3339)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m1', 'target', 'content', 'fact', '', '', '', '', ?, ?)`, now, now)

	got := ToolRecallMemory(context.Background(), deps, map[string]any{"query": "target"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(got.Content[0].Text), &items); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, got.Content[0].Text)
	}
	if len(items) != 1 || items[0]["key"] != "target" {
		t.Errorf("SQL fallback: got %+v, want single 'target'", items)
	}
}

func TestToolRecallMemory_CollectionFilterPersists(t *testing.T) {
	// collection_name filter narrows SQL-path results.
	deps := setupSaveRecallMemoryDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m1', 'a', 'match', 'fact', '', 'levara', '', '', ?, ?)`, now, now)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m2', 'b', 'match', 'fact', '', 'other', '', '', ?, ?)`, now, now)

	got := ToolRecallMemory(context.Background(), deps, map[string]any{
		"query":      "match",
		"collection": "levara",
	})
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 || items[0]["key"] != "a" {
		t.Errorf("got %+v, want single 'a'", items)
	}
}

func TestToolRecallMemory_HidesSuperseded(t *testing.T) {
	deps := setupSaveRecallMemoryDB(t)
	ctx := context.Background()

	ToolSaveMemory(ctx, deps, map[string]any{"key": "active", "value": "potion sidecar fact"})
	ToolSaveMemory(ctx, deps, map[string]any{"key": "old", "value": "potion sidecar fact"})
	if _, err := deps.db.Exec(`UPDATE memories SET superseded_by = 'x' WHERE key = 'old'`); err != nil {
		t.Fatalf("mark superseded: %v", err)
	}

	got := ToolRecallMemory(ctx, deps, map[string]any{"query": "potion sidecar"})
	if got.IsError {
		t.Fatalf("recall errored: %s", got.Content[0].Text)
	}
	if strings.Contains(got.Content[0].Text, "old") {
		t.Errorf("recall returned superseded row 'old': %s", got.Content[0].Text)
	}
	if !strings.Contains(got.Content[0].Text, "active") {
		t.Errorf("recall dropped active row: %s", got.Content[0].Text)
	}
}
