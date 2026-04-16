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
	"github.com/stek0v/cognevra/pkg/ingest"
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
	storagePath string   // empty → tool applies its own default
}

func (f *fakeDeps) DB() *sql.DB { return f.db }

// pgPlaceholderRe matches $N placeholder style used by the production
// queries; we rewrite to ? for SQLite.
var pgPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

func (f *fakeDeps) Q(query string) string {
	return pgPlaceholderRe.ReplaceAllString(query, "?")
}

func (f *fakeDeps) HasCollections() bool      { return f.hasColls }
func (f *fakeDeps) ListCollections() []string { return f.collections }
func (f *fakeDeps) StoragePath() string        { return f.storagePath }

// nilDBDeps returns nil for DB() — exercises the guard branch.
// HasCollections defaults to false, matching an unconfigured deployment.
type nilDBDeps struct{}

func (nilDBDeps) DB() *sql.DB               { return nil }
func (nilDBDeps) Q(q string) string         { return q }
func (nilDBDeps) HasCollections() bool      { return false }
func (nilDBDeps) ListCollections() []string { return nil }
func (nilDBDeps) StoragePath() string        { return "" }

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

// setupAddTestDB builds the full schema ingest.MetadataWriter writes
// to. Returns a fake Deps with the DB plus a tempdir StoragePath so the
// ingest side can actually write files.
func setupAddTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-add-test-*.db")
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

	// Flip pkg/ingest into SQLite mode so its $N placeholders and NOW()
	// calls get rewritten. Reset on cleanup — other tests may run in
	// Postgres mode.
	ingest.SetSQLiteMode(true)
	t.Cleanup(func() { ingest.SetSQLiteMode(false) })

	// Schema must carry every column MetadataWriter references. Extra
	// columns (e.g. content_hash, raw_content_hash) matter because the
	// ON CONFLICT DO UPDATE clause reads EXCLUDED.* on conflict.
	stmts := []string{
		`CREATE TABLE datasets (
			id TEXT PRIMARY KEY, name TEXT, owner_id TEXT,
			created_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE data (
			id TEXT PRIMARY KEY, name TEXT, extension TEXT, mime_type TEXT,
			raw_data_location TEXT, original_data_location TEXT,
			content_hash TEXT, raw_content_hash TEXT, owner_id TEXT,
			loader_engine TEXT, pipeline_status TEXT, tags TEXT, room TEXT,
			token_count INTEGER, data_size INTEGER,
			created_at TEXT, updated_at TEXT
		)`,
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT, PRIMARY KEY (dataset_id, data_id))`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	return &fakeDeps{
		db:          db,
		storagePath: t.TempDir(),
		hasColls:    true, // not strictly needed for ToolAdd, but consistent
	}
}

func TestToolAdd_MissingDataIsError(t *testing.T) {
	// 'data' is required; no 'data' key or an empty string → IsError.
	// Must not touch the filesystem either — exit before any ingest call.
	deps := &fakeDeps{storagePath: t.TempDir()}

	got := ToolAdd(context.Background(), deps, map[string]any{})
	if !got.IsError {
		t.Fatalf("IsError = false, want true for missing data")
	}
	if len(got.Content) == 0 || !strings.Contains(got.Content[0].Text, "'data' required") {
		t.Fatalf("content = %+v, want error mentioning 'data' required", got.Content)
	}

	got = ToolAdd(context.Background(), deps, map[string]any{"data": ""})
	if !got.IsError {
		t.Fatalf("empty data: IsError = false, want true")
	}
}

func TestToolAdd_WrongTypeDataIsError(t *testing.T) {
	// Non-string 'data' (e.g. JSON number) → treated as empty → error.
	deps := &fakeDeps{storagePath: t.TempDir()}

	got := ToolAdd(context.Background(), deps, map[string]any{"data": 42})
	if !got.IsError {
		t.Fatalf("int data: IsError = false, want true")
	}
}

func TestToolAdd_IngestSucceedsWithNilDB(t *testing.T) {
	// Nil DB path: ingest writes to disk, metadata phase is skipped,
	// success message mentions the dataset name and item count.
	deps := &fakeDeps{storagePath: t.TempDir()}

	got := ToolAdd(context.Background(), deps, map[string]any{
		"data":         "hello world",
		"dataset_name": "notes",
	})

	if got.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", got.Content)
	}
	if len(got.Content) == 0 {
		t.Fatalf("empty content")
	}
	text := got.Content[0].Text
	if !strings.Contains(text, "dataset 'notes'") {
		t.Errorf("content = %q, want dataset name 'notes'", text)
	}
	if !strings.Contains(text, "items: 1") {
		t.Errorf("content = %q, want 'items: 1'", text)
	}
	if !strings.Contains(text, "dataset_id:") {
		t.Errorf("content = %q, want dataset_id to be mentioned", text)
	}
}

func TestToolAdd_DefaultDatasetName(t *testing.T) {
	// When 'dataset_name' is missing, the tool substitutes 'default'.
	// Verified via the success message, which embeds the name.
	deps := &fakeDeps{storagePath: t.TempDir()}

	got := ToolAdd(context.Background(), deps, map[string]any{"data": "x"})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if !strings.Contains(got.Content[0].Text, "dataset 'default'") {
		t.Errorf("content = %q, want dataset 'default'", got.Content[0].Text)
	}
}

func TestToolAdd_MetadataWriteCommitsToDB(t *testing.T) {
	// End-to-end happy path: a configured DB means ToolAdd must write
	// both a datasets row (matching dataset_name) and a data row
	// (matching the ingest hash).
	deps := setupAddTestDB(t)

	got := ToolAdd(context.Background(), deps, map[string]any{
		"data":         "integration-test payload",
		"dataset_name": "intg",
		"room":         "testroom",
		"tags":         []any{"smoke"},
	})

	if got.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", got.Content)
	}

	var datasetName string
	if err := deps.db.QueryRow("SELECT name FROM datasets WHERE name = 'intg'").Scan(&datasetName); err != nil {
		t.Fatalf("dataset row not found: %v", err)
	}

	var dataCount int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM data").Scan(&dataCount); err != nil {
		t.Fatalf("count data: %v", err)
	}
	if dataCount != 1 {
		t.Fatalf("data count = %d, want 1", dataCount)
	}

	var linkCount int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM dataset_data").Scan(&linkCount); err != nil {
		t.Fatalf("count dataset_data: %v", err)
	}
	if linkCount != 1 {
		t.Errorf("dataset_data link count = %d, want 1", linkCount)
	}

	// Tags stored as JSON list in the text column.
	var tagsText string
	if err := deps.db.QueryRow("SELECT tags FROM data").Scan(&tagsText); err != nil {
		t.Fatalf("select tags: %v", err)
	}
	if !strings.Contains(tagsText, "smoke") {
		t.Errorf("tags = %q, want to contain 'smoke'", tagsText)
	}
}

func TestToolAdd_MetadataErrorIsSwallowed(t *testing.T) {
	// Partial schema (no data table) → MetadataWriter returns an error
	// deep inside the transaction. The tool must still return success
	// because the filesystem ingest has already committed — this locks
	// in the pre-refactor "DB write is best-effort" contract.
	f, _ := os.CreateTemp("", "mcp-add-brokendb-*.db")
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

	ingest.SetSQLiteMode(true)
	t.Cleanup(func() { ingest.SetSQLiteMode(false) })

	// Only datasets exists; data + dataset_data missing so the inner
	// INSERT fails.
	if _, err := db.Exec(`CREATE TABLE datasets (id TEXT, name TEXT, owner_id TEXT, created_at TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create: %v", err)
	}

	deps := &fakeDeps{db: db, storagePath: t.TempDir()}

	got := ToolAdd(context.Background(), deps, map[string]any{"data": "x"})
	if got.IsError {
		t.Errorf("IsError = true, want false (metadata failure should be swallowed); content=%+v", got.Content)
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
