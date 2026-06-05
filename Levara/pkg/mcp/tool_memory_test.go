package mcp

import (
	"context"
	"encoding/json"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// setupMemoryTestDB builds the memories schema used by the palace
// tools. Columns match the production schema's shape; only the ones
// the four tools under test touch matter here.
func setupMemoryTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-memory-test-*.db")
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

	stmts := []string{
		`CREATE TABLE memories (
			id TEXT PRIMARY KEY, key TEXT, value TEXT, type TEXT, owner_id TEXT,
			collection_name TEXT, room TEXT, hall TEXT,
			is_pinned INTEGER DEFAULT 0, pin_priority INTEGER DEFAULT 0,
			created_at TEXT, updated_at TEXT,
			superseded_by TEXT DEFAULT ''
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	return &fakeDeps{db: db}
}

// seedMemory is a compact helper for inserting memory rows in tests.
// Ignores errors since test setup should never hit real failures.
func seedMemory(t *testing.T, db *sql.DB, id, key, value, typ, owner, coll, room, hall string, pinned int, prio int) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO memories (
		id, key, value, type, owner_id, collection_name, room, hall,
		is_pinned, pin_priority, created_at, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, key, value, typ, owner, coll, room, hall, pinned, prio, now, now)
	if err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// ── ToolListMemories ──

func TestToolListMemories_NilDBReturnsEmpty(t *testing.T) {
	// No DB configured → "[]" (not an error), so MCP clients that only
	// read memory still work in deployments without the palace schema.
	got := ToolListMemories(context.Background(), nilDBDeps{}, map[string]any{})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolListMemories_NoRowsReturnsEmpty(t *testing.T) {
	// Empty DB (schema present but no rows) must still return "[]"
	// rather than "null" — some JSON consumers choke on null arrays.
	deps := setupMemoryTestDB(t)

	got := ToolListMemories(context.Background(), deps, map[string]any{})
	if got.Content[0].Text != "[]" {
		t.Errorf("content = %q, want []", got.Content[0].Text)
	}
}

func TestToolListMemories_TypeFilter(t *testing.T) {
	// "type" filter narrows the result set by the memory type column.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "k1", "v1", "project", "", "", "", "", 0, 0)
	seedMemory(t, deps.db, "m2", "k2", "v2", "preference", "", "", "", "", 0, 0)
	seedMemory(t, deps.db, "m3", "k3", "v3", "project", "", "", "", "", 0, 0)

	got := ToolListMemories(context.Background(), deps, map[string]any{"type": "project"})
	var items []map[string]any
	if err := json.Unmarshal([]byte(got.Content[0].Text), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d project items, want 2", len(items))
	}
	for _, it := range items {
		if it["type"] != "project" {
			t.Errorf("item type = %v, want project", it["type"])
		}
	}
}

func TestToolListMemories_CombinedFilters(t *testing.T) {
	// Multiple filters AND together. Seeding 4 rows with different
	// combinations ensures the filter is applied on every axis.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "a", "v", "project", "", "levara", "auth", "decision", 0, 0)
	seedMemory(t, deps.db, "m2", "b", "v", "project", "", "levara", "auth", "fact", 0, 0)
	seedMemory(t, deps.db, "m3", "c", "v", "project", "", "other", "auth", "decision", 0, 0)
	seedMemory(t, deps.db, "m4", "d", "v", "feedback", "", "levara", "auth", "decision", 0, 0)

	// Only m1 matches all three: levara collection, auth room, decision hall.
	got := ToolListMemories(context.Background(), deps, map[string]any{
		"type":       "project",
		"collection": "levara",
		"room":       "auth",
		"hall":       "decision",
	})
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 || items[0]["key"] != "a" {
		t.Errorf("got %+v, want single m1/a", items)
	}
}

func TestToolListMemories_FormatsPinnedAsBool(t *testing.T) {
	// is_pinned is stored as INTEGER (0/1) in SQL but returned as bool
	// in the JSON output. Locks in the pre-refactor shape that clients
	// rely on.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "k", "v", "project", "", "", "", "", 1, 10)

	got := ToolListMemories(context.Background(), deps, map[string]any{})
	var items []map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &items)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if items[0]["is_pinned"] != true {
		t.Errorf("is_pinned = %v (type %T), want bool true", items[0]["is_pinned"], items[0]["is_pinned"])
	}
	if int(items[0]["pin_priority"].(float64)) != 10 {
		t.Errorf("pin_priority = %v, want 10", items[0]["pin_priority"])
	}
}

// ── ToolPinMemory ──

func TestToolPinMemory_RequiresKey(t *testing.T) {
	// 'key' arg is required.
	deps := setupMemoryTestDB(t)
	got := ToolPinMemory(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing key: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'key' required") {
		t.Errorf("content = %q, want 'key' required", got.Content[0].Text)
	}
}

func TestToolPinMemory_NilDBIsError(t *testing.T) {
	// No DB → hard error, matches the pin/unpin contract for a
	// write-only tool.
	got := ToolPinMemory(context.Background(), nilDBDeps{}, map[string]any{"key": "k"})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "database not configured") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolPinMemory_NoMatchingRowIsError(t *testing.T) {
	// Pinning a non-existent key is IsError (unlike Delete/Prune which
	// are best-effort). Pin is a state-change request; the caller
	// needs to know it had no effect.
	deps := setupMemoryTestDB(t)

	got := ToolPinMemory(context.Background(), deps, map[string]any{"key": "ghost"})
	if !got.IsError {
		t.Fatalf("missing row: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "No memory matched key ghost") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolPinMemory_HappyPathUpdatesRow(t *testing.T) {
	// Pin sets is_pinned=1 + pin_priority=requested + updated_at.
	// Message echoes the key and priority.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "config", "v", "fact", "", "", "", "", 0, 0)

	got := ToolPinMemory(context.Background(), deps, map[string]any{
		"key":      "config",
		"priority": float64(8),
	})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Pinned config (priority=8)") {
		t.Errorf("content = %q", got.Content[0].Text)
	}

	var pinned, prio int
	deps.db.QueryRow("SELECT is_pinned, pin_priority FROM memories WHERE key = 'config'").Scan(&pinned, &prio)
	if pinned != 1 {
		t.Errorf("is_pinned = %d, want 1", pinned)
	}
	if prio != 8 {
		t.Errorf("pin_priority = %d, want 8", prio)
	}
}

func TestToolPinMemory_DefaultPriorityIsOne(t *testing.T) {
	// Missing 'priority' arg → priority=1 (pre-refactor default).
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "config", "v", "fact", "", "", "", "", 0, 0)

	got := ToolPinMemory(context.Background(), deps, map[string]any{"key": "config"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	if !strings.Contains(got.Content[0].Text, "priority=1") {
		t.Errorf("content = %q, want priority=1 default", got.Content[0].Text)
	}
}

// ── ToolUnpinMemory ──

func TestToolUnpinMemory_RequiresKey(t *testing.T) {
	deps := setupMemoryTestDB(t)
	got := ToolUnpinMemory(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing key: IsError = false, want true")
	}
}

func TestToolUnpinMemory_NilDBIsError(t *testing.T) {
	got := ToolUnpinMemory(context.Background(), nilDBDeps{}, map[string]any{"key": "k"})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
}

func TestToolUnpinMemory_MissingRowIsNotError(t *testing.T) {
	// Unpin is idempotent — asking to unpin a non-existent row is fine,
	// the target state (unpinned) is already satisfied. This is the
	// opposite of Pin's strict semantics.
	deps := setupMemoryTestDB(t)

	got := ToolUnpinMemory(context.Background(), deps, map[string]any{"key": "ghost"})
	if got.IsError {
		t.Errorf("missing row: IsError = true, want false (unpin is idempotent)")
	}
	if !strings.Contains(got.Content[0].Text, "Unpinned ghost") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolUnpinMemory_ClearsFlagsOnRealRow(t *testing.T) {
	// Unpin flips is_pinned=0 and pin_priority=0 regardless of prior
	// priority. Tests lock in both, not just the flag.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "config", "v", "fact", "", "", "", "", 1, 10)

	got := ToolUnpinMemory(context.Background(), deps, map[string]any{"key": "config"})
	if got.IsError {
		t.Fatalf("IsError = true")
	}

	var pinned, prio int
	deps.db.QueryRow("SELECT is_pinned, pin_priority FROM memories WHERE key = 'config'").Scan(&pinned, &prio)
	if pinned != 0 || prio != 0 {
		t.Errorf("after unpin: is_pinned=%d pin_priority=%d, want 0/0", pinned, prio)
	}
}

// ── ToolDeleteMemory ──

func TestToolDeleteMemory_RequiresKey(t *testing.T) {
	deps := setupMemoryTestDB(t)
	got := ToolDeleteMemory(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing key: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'key' required") {
		t.Errorf("content = %q, want 'key' required", got.Content[0].Text)
	}
}

func TestToolDeleteMemory_NilDBIsError(t *testing.T) {
	got := ToolDeleteMemory(context.Background(), nilDBDeps{}, map[string]any{"key": "k"})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "database not configured") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolDeleteMemory_NoMatchingRowIsError(t *testing.T) {
	// Deleting a non-existent key is IsError (like pin, unlike the
	// idempotent unpin) — delete is a state change and the caller needs
	// to know it had no effect.
	deps := setupMemoryTestDB(t)

	got := ToolDeleteMemory(context.Background(), deps, map[string]any{"key": "ghost"})
	if !got.IsError {
		t.Fatalf("missing row: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "No memory matched key ghost") {
		t.Errorf("content = %q", got.Content[0].Text)
	}
}

func TestToolDeleteMemory_HappyPathRemovesRow(t *testing.T) {
	// Delete removes the SQL row and reports the count.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "stale", "v", "fact", "", "", "", "", 0, 0)

	got := ToolDeleteMemory(context.Background(), deps, map[string]any{"key": "stale"})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Deleted stale (1 record(s))") {
		t.Errorf("content = %q", got.Content[0].Text)
	}

	var n int
	deps.db.QueryRow("SELECT COUNT(*) FROM memories WHERE key = 'stale'").Scan(&n)
	if n != 0 {
		t.Errorf("rows after delete = %d, want 0", n)
	}
}

func TestToolDeleteMemory_OwnershipScoped(t *testing.T) {
	// Caller deletes only its own rows and shared (empty-owner) rows;
	// another user's row with the same key survives. Three rows with the
	// same key but distinct owner_ids is a valid production state under
	// UNIQUE(key, owner_id).
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "secret", "mine", "fact", "alice", "", "", "", 0, 0)
	seedMemory(t, deps.db, "m2", "secret", "theirs", "fact", "bob", "", "", "", 0, 0)
	seedMemory(t, deps.db, "m3", "secret", "shared", "fact", "", "", "", "", 0, 0)

	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	got := ToolDeleteMemory(ctx, deps, map[string]any{"key": "secret"})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Deleted secret (2 record(s))") {
		t.Errorf("content = %q, want 2 records", got.Content[0].Text)
	}

	// Only bob's row remains.
	var remaining string
	if err := deps.db.QueryRow("SELECT owner_id FROM memories WHERE key = 'secret'").Scan(&remaining); err != nil {
		t.Fatalf("expected bob's row to survive: %v", err)
	}
	if remaining != "bob" {
		t.Errorf("surviving owner = %q, want bob", remaining)
	}
}

func TestToolDeleteMemory_CollectionFilterNarrows(t *testing.T) {
	// An optional collection narrows the delete to one pinned-context
	// shard. Two same-key rows with distinct owner_ids (valid under
	// UNIQUE(key, owner_id)) sit in different collections; the filter
	// keeps the non-matching one.
	deps := setupMemoryTestDB(t)
	seedMemory(t, deps.db, "m1", "dup", "in-levara", "fact", "", "levara", "", "", 0, 0)
	seedMemory(t, deps.db, "m2", "dup", "in-other", "fact", "alice", "other", "", "", 0, 0)

	ctx := context.WithValue(context.Background(), UserIDKey, "alice")
	got := ToolDeleteMemory(ctx, deps, map[string]any{"key": "dup", "collection": "levara"})
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "Deleted dup (1 record(s))") {
		t.Errorf("content = %q, want 1 record", got.Content[0].Text)
	}

	var coll string
	if err := deps.db.QueryRow("SELECT collection_name FROM memories WHERE key = 'dup'").Scan(&coll); err != nil {
		t.Fatalf("expected the 'other' row to survive: %v", err)
	}
	if coll != "other" {
		t.Errorf("surviving collection = %q, want other", coll)
	}
}

func TestToolDeleteMemory_DropsVectorSidecar(t *testing.T) {
	// When collections are configured, the matching vector sidecar entry
	// is dropped so the record stops surfacing in recall. The sidecar name
	// derives from the row's collection_name via memoryCollectionName.
	deps := setupMemoryTestDB(t)
	deps.hasColls = true
	seedMemory(t, deps.db, "m1", "base", "v", "fact", "", "", "", "", 0, 0)
	seedMemory(t, deps.db, "m2", "shard", "v", "fact", "", "levara", "", "", 0, 0)
	// Pretend both vectors were previously indexed.
	deps.CollectionInsert("_memories", "m1", []float32{1}, nil)
	deps.CollectionInsert("_memories_levara", "m2", []float32{1}, nil)

	if got := ToolDeleteMemory(context.Background(), deps, map[string]any{"key": "base"}); got.IsError {
		t.Fatalf("delete base IsError; content=%+v", got.Content)
	}
	if got := ToolDeleteMemory(context.Background(), deps, map[string]any{"key": "shard"}); got.IsError {
		t.Fatalf("delete shard IsError; content=%+v", got.Content)
	}

	deleted := deps.getDeleted()
	want := map[string]string{"_memories": "m1", "_memories_levara": "m2"}
	if len(deleted) != 2 {
		t.Fatalf("CollectionDelete calls = %d, want 2 (%+v)", len(deleted), deleted)
	}
	for _, d := range deleted {
		if want[d.collection] != d.id {
			t.Errorf("unexpected sidecar delete %s/%s", d.collection, d.id)
		}
	}
	// And the records no longer verify as present.
	if deps.CollectionHasRecord("_memories", "m1") {
		t.Errorf("_memories/m1 still present after delete")
	}
}

func TestToolDeleteMemory_NoVectorWhenCollectionsAbsent(t *testing.T) {
	// With collections unconfigured the SQL delete still succeeds and no
	// vector delete is attempted (HasCollections gate).
	deps := setupMemoryTestDB(t) // hasColls=false
	seedMemory(t, deps.db, "m1", "k", "v", "fact", "", "", "", "", 0, 0)

	if got := ToolDeleteMemory(context.Background(), deps, map[string]any{"key": "k"}); got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	if d := deps.getDeleted(); len(d) != 0 {
		t.Errorf("CollectionDelete called %d times, want 0 when collections absent", len(d))
	}
}

// ── ToolWakeUp ──

// setupWakeUpTestDB builds both memories and graph tables so ToolWakeUp
// can exercise the pinned + entities branches end-to-end.
func setupWakeUpTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	deps := setupMemoryTestDB(t)

	stmts := []string{
		`CREATE TABLE graph_nodes (id TEXT PRIMARY KEY, name TEXT, type TEXT, updated_at TEXT)`,
		`CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT,
			valid_until TEXT, updated_at TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := deps.db.Exec(s); err != nil {
			t.Fatalf("create: %v", err)
		}
	}
	return deps
}

func TestToolWakeUp_NilDBIsError(t *testing.T) {
	got := ToolWakeUp(context.Background(), nilDBDeps{}, map[string]any{})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
}

func TestToolWakeUp_EmptyDBReturnsEmptyBundle(t *testing.T) {
	// Fresh DB (schema but no rows) → bundle with empty pinned +
	// empty entities, not an error.
	deps := setupWakeUpTestDB(t)

	got := ToolWakeUp(context.Background(), deps, map[string]any{})
	if got.IsError {
		t.Fatalf("IsError = true")
	}
	var bundle map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &bundle)
	if bundle["pinned"] != nil {
		t.Errorf("pinned = %v, want nil/empty", bundle["pinned"])
	}
}

func TestToolWakeUp_ReturnsPinnedOrderedByPriority(t *testing.T) {
	// Pinned memories come back in pin_priority DESC order. updated_at
	// is the tiebreaker — covered by the default seed timestamps.
	deps := setupWakeUpTestDB(t)
	seedMemory(t, deps.db, "m1", "low", "v1", "fact", "", "", "", "", 1, 1)
	seedMemory(t, deps.db, "m2", "high", "v2", "fact", "", "", "", "", 1, 10)
	seedMemory(t, deps.db, "m3", "unpinned", "v3", "fact", "", "", "", "", 0, 0)

	got := ToolWakeUp(context.Background(), deps, map[string]any{})
	var bundle map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &bundle)

	pinned := bundle["pinned"].([]any)
	if len(pinned) != 2 {
		t.Fatalf("got %d pinned, want 2 (unpinned row must be excluded)", len(pinned))
	}
	first := pinned[0].(map[string]any)
	if first["key"] != "high" {
		t.Errorf("first pinned = %v, want 'high' (priority 10)", first["key"])
	}
}

func TestToolWakeUp_CollectionScopingFiltersPinned(t *testing.T) {
	// Passing a "collection" arg restricts pinned to that collection_name.
	deps := setupWakeUpTestDB(t)
	seedMemory(t, deps.db, "m1", "a", "v", "fact", "", "levara", "", "", 1, 5)
	seedMemory(t, deps.db, "m2", "b", "v", "fact", "", "other", "", "", 1, 5)

	got := ToolWakeUp(context.Background(), deps, map[string]any{"collection": "levara"})
	var bundle map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &bundle)

	pinned := bundle["pinned"].([]any)
	if len(pinned) != 1 {
		t.Fatalf("got %d pinned, want 1 (other collection must be excluded)", len(pinned))
	}
	if pinned[0].(map[string]any)["key"] != "a" {
		t.Errorf("pinned key = %v, want 'a'", pinned[0].(map[string]any)["key"])
	}
}

func TestToolWakeUp_TrimsToMaxTokens(t *testing.T) {
	// Seeding 30 pinned rows then passing max_tokens=20 (= 80 chars)
	// forces the trim loop. Entities drop first; then pinned pops from
	// the tail until the encoded bundle fits.
	deps := setupWakeUpTestDB(t)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < 30; i++ {
		_, _ = deps.db.Exec(`INSERT INTO memories (
			id, key, value, type, owner_id, collection_name, room, hall,
			is_pinned, pin_priority, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			"m"+string(rune('a'+i)), "key"+string(rune('a'+i)),
			strings.Repeat("x", 50), "fact", "", "", "", "",
			1, 30-i, now, now)
	}

	got := ToolWakeUp(context.Background(), deps, map[string]any{
		"max_tokens": float64(20), // 80 chars budget
	})
	// Success — the bundle still encodes, even if it's near-empty.
	if got.IsError {
		t.Fatalf("IsError = true; content=%+v", got.Content)
	}
	// Size is bounded (some slack for JSON overhead of the wrapper).
	if len(got.Content[0].Text) > 200 {
		t.Errorf("trim ineffective: output length %d bytes for max_tokens=20 (budget 80 chars)", len(got.Content[0].Text))
	}
}

func TestToolWakeUp_EntityDegreeCountsActiveEdges(t *testing.T) {
	// Graph entities section counts ACTIVE edges per node. Expired
	// edges (valid_until in the past) are excluded from the degree.
	deps := setupWakeUpTestDB(t)
	past := "2000-01-01T00:00:00Z"
	// Nodes.
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n1', 'auth', 'service', ?)`, past)
	deps.db.Exec(`INSERT INTO graph_nodes (id, name, type, updated_at) VALUES ('n2', 'deploy', 'service', ?)`, past)
	// Active edges touching n1 (x2), expired edge touching n2 (x1).
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, valid_until, updated_at) VALUES ('e1', 'n1', 'n2', NULL, ?)`, past)
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, valid_until, updated_at) VALUES ('e2', 'n1', 'n2', NULL, ?)`, past)
	deps.db.Exec(`INSERT INTO graph_edges (id, source_id, target_id, valid_until, updated_at) VALUES ('e3', 'n2', 'n1', ?, ?)`, past, past)

	got := ToolWakeUp(context.Background(), deps, map[string]any{})
	var bundle map[string]any
	json.Unmarshal([]byte(got.Content[0].Text), &bundle)

	entities := bundle["top_entities"].([]any)
	if len(entities) == 0 {
		t.Fatalf("no entities returned")
	}
	// n1 has 2 active edges (e1, e2), plus e3 (expired) which touches
	// it as target. The expired-edge check is against e3.valid_until
	// (past), so e3 should be excluded → n1 degree = 2.
	top := entities[0].(map[string]any)
	if top["name"] != "auth" {
		t.Errorf("top entity = %v, want 'auth'", top["name"])
	}
	if int(top["degree"].(float64)) != 2 {
		t.Errorf("auth degree = %v, want 2 (expired edge excluded)", top["degree"])
	}
}
