package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
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

func TestToolSaveMemory_DivergenceOnMissingAfterInsert(t *testing.T) {
	// When the vector does not verify present after insert (CollectionHasRecord
	// stays false), the write path must NOT stay silent: it retries the
	// insert once and then emits a "memory_index_divergence" heartbeat with
	// reason=missing_after_insert. SQL row is the source of truth and stays
	// committed regardless.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1, 2, 3}, nil
	}
	deps.hasRecordFn = func(collection, id string) bool { return false } // never verifies

	var mu sync.Mutex
	var diverged []map[string]any
	deps.heartbeatFn = func(eventType string, payload any) {
		if eventType != "memory_index_divergence" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if m, ok := payload.(map[string]any); ok {
			diverged = append(diverged, m)
		}
	}

	ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": "v", "collection": "levara",
	})

	// Poll for the divergence heartbeat.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(diverged)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(diverged) != 1 {
		t.Fatalf("memory_index_divergence emitted %d times, want 1", len(diverged))
	}
	if diverged[0]["reason"] != "missing_after_insert" {
		t.Errorf("reason = %v, want missing_after_insert", diverged[0]["reason"])
	}
	// Retry-once means two insert attempts were made.
	if n := len(deps.getInserted()); n != memoryIndexMaxAttempts {
		t.Errorf("CollectionInsert called %d times, want %d (retry-once)", n, memoryIndexMaxAttempts)
	}
}

func TestToolSaveMemory_DivergenceOnEmbedFail(t *testing.T) {
	// An embed failure must surface as a divergence heartbeat (reason
	// embed_failed) and skip the insert entirely — not vanish silently.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return nil, errors.New("embed service down")
	}

	var mu sync.Mutex
	var reason string
	deps.heartbeatFn = func(eventType string, payload any) {
		if eventType != "memory_index_divergence" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if m, ok := payload.(map[string]any); ok {
			reason, _ = m["reason"].(string)
		}
	}

	got := ToolSaveMemory(context.Background(), deps, map[string]any{
		"key": "k", "value": "v",
	})
	if got.IsError {
		t.Fatalf("save returned IsError on embed failure; SQL write must still succeed")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		r := reason
		mu.Unlock()
		if r != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if reason != "embed_failed" {
		t.Errorf("divergence reason = %q, want embed_failed", reason)
	}
	if n := len(deps.getInserted()); n != 0 {
		t.Errorf("CollectionInsert called %d times on embed failure, want 0", n)
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

func TestToolRecallMemory_RoomFilterFallsBackToSQLLike(t *testing.T) {
	// Under a room filter, vector search still runs (its hits are hydrated
	// through SQL so the filter applies authoritatively). When the vector
	// path yields no usable hit, recall falls back to the SQL LIKE path and
	// the literal substring match is still returned.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1, 2, 3}, nil
	}
	// searchFn left nil → CollectionSearch returns no hits → SQL fallback.

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
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 || items[0]["key"] != "k1" {
		t.Errorf("got %+v from SQL fallback, want single 'k1'", items)
	}
}

func TestToolRecallMemory_RoomFilterUsesVectorHydration(t *testing.T) {
	// Regression: a room filter must NOT downgrade recall to literal
	// substring matching. A semantic query that is not a substring of
	// key/value still finds the row via vector search, with the room
	// filter applied authoritatively against SQL (and other-room vector
	// hits excluded).
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1, 2, 3}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Neither value contains the query phrase — only a vector hit can find it.
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m1', 'potion-fact', 'model2vec sidecar on loopback', 'fact', '', '', 'embed', 'fact', ?, ?)`, now, now)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('m2', 'other', 'unrelated note', 'fact', '', '', 'auth', 'fact', ?, ?)`, now, now)
	deps.searchFn = func(collection string, q []float32, k int) ([]SearchResult, error) {
		return []SearchResult{{ID: "m1", Score: 0.9}, {ID: "m2", Score: 0.8}}, nil
	}

	got := ToolRecallMemory(context.Background(), deps, map[string]any{
		"query": "embedding model service", // not a substring of any row
		"room":  "embed",
	})
	if got.IsError {
		t.Fatalf("IsError = true: %s", got.Content[0].Text)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(got.Content[0].Text), &items); err != nil {
		t.Fatalf("unmarshal: %v (content=%q)", err, got.Content[0].Text)
	}
	if len(items) != 1 || items[0]["key"] != "potion-fact" {
		t.Fatalf("got %+v, want single 'potion-fact' (vector hit, room=embed)", items)
	}
}

func TestToolRecallMemory_HallFilterUsesVectorHydration(t *testing.T) {
	// Same guarantee on the hall axis: a hall filter keeps semantic recall
	// and excludes vector hits whose hall differs.
	deps := setupSaveRecallMemoryDB(t)
	deps.embedAvailable = true
	deps.embedFn = func(ctx context.Context, text string) ([]float32, error) {
		return []float32{1, 2, 3}, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('d1', 'decided-x', 'chose sqlite over postgres', 'project', '', '', 'db', 'decision', ?, ?)`, now, now)
	deps.db.Exec(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
		VALUES ('f1', 'fact-x', 'chose sqlite over postgres', 'project', '', '', 'db', 'fact', ?, ?)`, now, now)
	deps.searchFn = func(collection string, q []float32, k int) ([]SearchResult, error) {
		return []SearchResult{{ID: "d1", Score: 0.9}, {ID: "f1", Score: 0.85}}, nil
	}

	got := ToolRecallMemory(context.Background(), deps, map[string]any{
		"query": "storage engine selection",
		"hall":  "decision",
	})
	if got.IsError {
		t.Fatalf("IsError = true: %s", got.Content[0].Text)
	}
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 || items[0]["key"] != "decided-x" {
		t.Fatalf("got %+v, want single 'decided-x' (hall=decision)", items)
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
