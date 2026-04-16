package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// fakeDeps is the minimum Deps implementation for unit-testing tool
// functions: a real *sql.DB (in-memory sqlite) plus a Q() that mirrors
// the internal/http Postgres→SQLite rewrite (ncruces sqlite does not
// accept $N placeholders). collections is optional — leave nil for
// tests that exercise the "no vector engine" branch.
type fakeDeps struct {
	db          *sql.DB
	collections []string // nil → HasCollections() false (not configured)
	hasColls    bool     // explicit flag so empty-but-configured is possible
}

func (f *fakeDeps) DB() *sql.DB { return f.db }

// pgPlaceholderRe matches $N placeholder style used by the production
// queries; we rewrite to ? for SQLite.
var pgPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

func (f *fakeDeps) Q(query string) string {
	return pgPlaceholderRe.ReplaceAllString(query, "?")
}

func (f *fakeDeps) HasCollections() bool     { return f.hasColls }
func (f *fakeDeps) ListCollections() []string { return f.collections }

// nilDBDeps returns nil for DB() — exercises the guard branch.
// HasCollections defaults to false, matching an unconfigured deployment.
type nilDBDeps struct{}

func (nilDBDeps) DB() *sql.DB              { return nil }
func (nilDBDeps) Q(q string) string        { return q }
func (nilDBDeps) HasCollections() bool     { return false }
func (nilDBDeps) ListCollections() []string { return nil }

func setupDepsTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-deps-test-*.db")
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

	if _, err := db.Exec(`CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return &fakeDeps{db: db}
}

func TestToolDelete_MissingIDReturnsError(t *testing.T) {
	// Missing / empty 'dataset_id' is a client error; tool must surface it
	// via IsError=true rather than silently returning "deleted."
	deps := nilDBDeps{}

	got := ToolDelete(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("IsError = false, want true")
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "dataset_id") {
		t.Fatalf("content = %+v, want error mentioning 'dataset_id'", got.Content)
	}

	got = ToolDelete(context.Background(), deps, map[string]any{"dataset_id": ""})
	if !got.IsError {
		t.Fatalf("empty dataset_id: IsError = false, want true")
	}
}

func TestToolDelete_WrongTypeIsError(t *testing.T) {
	// Non-string dataset_id (e.g. JSON number) must hit the same "required"
	// error path — the type assertion failure zeroes the string, not a panic.
	got := ToolDelete(context.Background(), nilDBDeps{}, map[string]any{"dataset_id": 42})
	if !got.IsError {
		t.Fatalf("int dataset_id: IsError = false, want true")
	}
}

func TestToolDelete_NilDBNoPanic(t *testing.T) {
	// No Postgres configured → Deps.DB() returns nil. Tool must treat
	// this as a no-op and still return a success message, matching the
	// pre-refactor behavior.
	got := ToolDelete(context.Background(), nilDBDeps{}, map[string]any{"dataset_id": "abc"})
	if got.IsError {
		t.Fatalf("IsError = true, want false (nil DB should be a silent no-op)")
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "abc") {
		t.Fatalf("content = %+v, want message containing 'abc'", got.Content)
	}
}

func TestToolDelete_HappyPathDeletesRow(t *testing.T) {
	// With a working DB, the DELETE must actually remove the row. The
	// $1 placeholder is rewritten via Deps.Q() so the sqlite backend
	// accepts it.
	deps := setupDepsTestDB(t)
	deps.db.Exec("INSERT INTO datasets (id, name) VALUES ('ds1', 'alpha')")
	deps.db.Exec("INSERT INTO datasets (id, name) VALUES ('ds2', 'beta')")

	got := ToolDelete(context.Background(), deps, map[string]any{"dataset_id": "ds1"})
	if got.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", got.Content)
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "ds1") {
		t.Fatalf("content = %+v, want message mentioning 'ds1'", got.Content)
	}

	var count int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM datasets WHERE id = 'ds1'").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("ds1 row count = %d after delete, want 0", count)
	}

	// Other rows untouched.
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM datasets WHERE id = 'ds2'").Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Fatalf("ds2 row count = %d after unrelated delete, want 1", count)
	}
}

func TestToolDelete_MissingRowNoError(t *testing.T) {
	// Deleting a non-existent id is best-effort: the tool returns the
	// standard success message rather than 404-ing. This matches the
	// pre-refactor contract MCP clients rely on.
	deps := setupDepsTestDB(t)

	got := ToolDelete(context.Background(), deps, map[string]any{"dataset_id": "nope"})
	if got.IsError {
		t.Fatalf("IsError = true, want false on missing row")
	}
}

// setupPruneTestDB builds a sqlite DB with every table ToolPrune touches
// (see pruneTables). Rows are pre-seeded so the happy-path test can
// assert post-prune counts.
func setupPruneTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-prune-test-*.db")
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

	// Same column lists used by the real schema; only the bits ToolPrune
	// reads (table existence) matter here, so minimal schemas are fine.
	stmts := []string{
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT)`,
		`CREATE TABLE data (id TEXT PRIMARY KEY)`,
		`CREATE TABLE datasets (id TEXT PRIMARY KEY)`,
		`CREATE TABLE graph_nodes (id TEXT PRIMARY KEY)`,
		`CREATE TABLE graph_edges (id TEXT PRIMARY KEY)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// Seed one row per table so the post-prune count=0 assertion is
	// meaningful (counting empty tables would pass trivially).
	seeds := []string{
		`INSERT INTO dataset_data (dataset_id, data_id) VALUES ('ds1', 'd1')`,
		`INSERT INTO data (id) VALUES ('d1')`,
		`INSERT INTO datasets (id) VALUES ('ds1')`,
		`INSERT INTO graph_nodes (id) VALUES ('n1')`,
		`INSERT INTO graph_edges (id) VALUES ('e1')`,
	}
	for _, s := range seeds {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return &fakeDeps{db: db}
}

func TestToolPrune_NilDBNoPanic(t *testing.T) {
	// nil DB path mirrors ToolDelete: silent no-op, success message.
	got := ToolPrune(context.Background(), nilDBDeps{})
	if got.IsError {
		t.Fatalf("IsError = true, want false (nil DB should be a silent no-op)")
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "pruned") {
		t.Fatalf("content = %+v, want 'pruned' message", got.Content)
	}
}

func TestToolPrune_ClearsEveryKnownTable(t *testing.T) {
	// End-to-end: with one row per prune target, a single ToolPrune call
	// must leave each table empty. This is the regression net against
	// future pruneTables edits that forget to add a new table.
	deps := setupPruneTestDB(t)

	got := ToolPrune(context.Background(), deps)
	if got.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", got.Content)
	}

	for _, table := range pruneTables {
		var n int
		if err := deps.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("table %s: count = %d after prune, want 0", table, n)
		}
	}
}

func TestToolPrune_MissingTableIsBestEffort(t *testing.T) {
	// If a table is missing (e.g. schema drift in a minority deployment),
	// ToolPrune must still succeed for the tables that do exist. This
	// locks in the "errors are swallowed" contract from pre-refactor.
	f, _ := os.CreateTemp("", "mcp-prune-partial-*.db")
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

	// Only datasets exists; the other four pruneTables are absent.
	if _, err := db.Exec(`CREATE TABLE datasets (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO datasets (id) VALUES ('x')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := ToolPrune(context.Background(), &fakeDeps{db: db})
	if got.IsError {
		t.Fatalf("IsError = true, want false on partial schema")
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&n); err != nil {
		t.Fatalf("count datasets: %v", err)
	}
	if n != 0 {
		t.Errorf("datasets count = %d after prune, want 0 even when other tables are missing", n)
	}
}

// setupListDataTestDB builds the schema ToolListData reads: datasets
// (id, name, created_at) and data (id, name, extension, room, tags,
// created_at). Seeded with rows that exercise both paths.
func setupListDataTestDB(t *testing.T) *sql.DB {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-listdata-test-*.db")
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
		`CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT, created_at TEXT)`,
		`CREATE TABLE data (
			id TEXT PRIMARY KEY, name TEXT, extension TEXT,
			room TEXT, tags TEXT, created_at TEXT
		)`,
		// Two datasets, newest first by created_at.
		`INSERT INTO datasets VALUES ('ds2', 'newer', '2026-02-01')`,
		`INSERT INTO datasets VALUES ('ds1', 'older', '2026-01-01')`,
		// Data rows: mix of rooms and tag sets.
		`INSERT INTO data VALUES ('d1', 'auth-doc', 'md', 'auth', '["security","docs"]', '2026-03-01')`,
		`INSERT INTO data VALUES ('d2', 'deploy-log', 'txt', 'deploy', '["ops"]', '2026-03-02')`,
		`INSERT INTO data VALUES ('d3', 'auth-test', 'go', 'auth', '["tests"]', '2026-03-03')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %v", s, err)
		}
	}
	return db
}

// parseListDataContent pulls the JSON array out of a ToolResult so
// assertions can inspect individual items.
func parseListDataContent(t *testing.T, res ToolResult) []map[string]any {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", res.Content)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("content = %+v, want single text item", res.Content)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &items); err != nil {
		t.Fatalf("json unmarshal %q: %v", res.Content[0].Text, err)
	}
	return items
}

func TestToolListData_NoCollectionsReturnsEmpty(t *testing.T) {
	// When no collection manager is configured (HasCollections false),
	// the tool must short-circuit to "[]" without touching the DB. This
	// matches deployments that run without the vector engine.
	got := ToolListData(context.Background(), nilDBDeps{}, map[string]any{})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if len(got.Content) != 1 || got.Content[0].Text != "[]" {
		t.Fatalf("content = %+v, want single text \"[]\"", got.Content)
	}
}

func TestToolListData_UnfilteredListsCollectionsAndDatasets(t *testing.T) {
	// With no room/tags filter: emit one vector_collection item per
	// registered collection, then datasets ordered by created_at DESC.
	deps := &fakeDeps{
		db:          setupListDataTestDB(t),
		collections: []string{"levara", "other"},
		hasColls:    true,
	}

	items := parseListDataContent(t, ToolListData(context.Background(), deps, map[string]any{}))

	// Expect 2 collections + 2 datasets = 4 items.
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4; items=%+v", len(items), items)
	}
	// First two are collections in registration order.
	for i, name := range []string{"levara", "other"} {
		if items[i]["type"] != "vector_collection" {
			t.Errorf("item %d type = %v, want vector_collection", i, items[i]["type"])
		}
		if items[i]["collection"] != name {
			t.Errorf("item %d collection = %v, want %s", i, items[i]["collection"], name)
		}
	}
	// Last two are datasets, newest first.
	if items[2]["type"] != "dataset" || items[2]["id"] != "ds2" {
		t.Errorf("items[2] = %+v, want dataset ds2 (newest)", items[2])
	}
	if items[3]["type"] != "dataset" || items[3]["id"] != "ds1" {
		t.Errorf("items[3] = %+v, want dataset ds1", items[3])
	}
}

func TestToolListData_RoomFilterHitsDataTable(t *testing.T) {
	// "room=auth" must skip the collections listing and return only
	// data rows whose room column matches.
	deps := &fakeDeps{
		db:          setupListDataTestDB(t),
		collections: []string{"levara"},
		hasColls:    true,
	}

	items := parseListDataContent(t, ToolListData(context.Background(), deps,
		map[string]any{"room": "auth"}))

	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 auth rows; items=%+v", len(items), items)
	}
	for _, it := range items {
		if it["type"] != "data" {
			t.Errorf("item type = %v, want data (filter path skips collections)", it["type"])
		}
		if it["room"] != "auth" {
			t.Errorf("item room = %v, want auth", it["room"])
		}
	}
}

func TestToolListData_TagsFilterMatchesAnyTag(t *testing.T) {
	// "tags=[security]" hits d1 only (the tags string contains "security").
	// Confirms the LIKE '%"tag"%' pattern works against JSON-as-text.
	deps := &fakeDeps{
		db:          setupListDataTestDB(t),
		collections: []string{"levara"},
		hasColls:    true,
	}

	items := parseListDataContent(t, ToolListData(context.Background(), deps,
		map[string]any{"tags": []any{"security"}}))

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1; items=%+v", len(items), items)
	}
	if items[0]["id"] != "d1" {
		t.Errorf("item id = %v, want d1", items[0]["id"])
	}
}

func TestToolListData_CombinedFilterAndsConditions(t *testing.T) {
	// room=auth AND tags=[tests] → only d3 matches (d1 is auth but has
	// security+docs, not tests; d2 is deploy/ops). ANDing is important
	// because tag-only or room-only would return different rows.
	deps := &fakeDeps{
		db:          setupListDataTestDB(t),
		collections: []string{"levara"},
		hasColls:    true,
	}

	items := parseListDataContent(t, ToolListData(context.Background(), deps,
		map[string]any{"room": "auth", "tags": []any{"tests"}}))

	if len(items) != 1 || items[0]["id"] != "d3" {
		t.Fatalf("got %+v, want single d3 item", items)
	}
}

func TestToolListData_EmptyTagStringsIgnored(t *testing.T) {
	// Clients sometimes send `{"tags": [""]}` — these must not become a
	// LIKE '%""%' that matches every row. The tool strips empty strings
	// before building the filter.
	deps := &fakeDeps{
		db:          setupListDataTestDB(t),
		collections: []string{"levara"},
		hasColls:    true,
	}

	// tags=[""] with no room → treated as no filter → collections + datasets.
	items := parseListDataContent(t, ToolListData(context.Background(), deps,
		map[string]any{"tags": []any{""}}))
	// Should match unfiltered output: 1 collection + 2 datasets = 3.
	if len(items) != 3 {
		t.Fatalf("got %d items with empty-tag filter, want 3 (same as unfiltered)", len(items))
	}
}

func TestToolListData_CollectionsConfiguredButEmpty(t *testing.T) {
	// HasCollections=true but ListCollections returns zero names (fresh
	// deployment). The tool still proceeds to the DB section rather than
	// short-circuiting — short-circuit is only for "not configured."
	deps := &fakeDeps{
		db:       setupListDataTestDB(t),
		hasColls: true, // configured, just no collections yet
	}

	items := parseListDataContent(t, ToolListData(context.Background(), deps, map[string]any{}))
	// Zero collections + 2 datasets = 2 items.
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 dataset rows; items=%+v", len(items), items)
	}
	for _, it := range items {
		if it["type"] != "dataset" {
			t.Errorf("item type = %v, want dataset", it["type"])
		}
	}
}
