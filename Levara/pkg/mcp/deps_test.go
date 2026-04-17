package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/cognevra/pipeline"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/router"
	"github.com/stek0v/cognevra/pkg/runreg"
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

	// Embedding + vector-store stubs for tests that exercise the AI
	// path. All zero-valued by default — EmbedAvailable() returns false
	// unless embedAvailable is explicitly set, so tests that don't need
	// vector behavior stay on the SQL fallback path.
	embedAvailable bool
	embedFn        func(ctx context.Context, text string) ([]float32, error)
	searchFn       func(collection string, q []float32, k int) ([]SearchResult, error)

	// insertedRows is guarded by insertedMu because ToolSaveMemory
	// writes via a goroutine; tests that read it must use getInserted.
	insertedMu   sync.Mutex
	insertedRows []insertedRow

	// Cognify stubs. Tests that exercise ToolCognify install:
	//   runs        — a *runreg.Registry to observe status transitions
	//   baseCfg     — the orchestrator.Config returned by BaseCognifyConfig
	//                 (zero value is fine when tests just check registry state)
	//   ontologyFn  — override for OntologyPromptSuffix
	//   persistFn   — observe PersistPipelineStatus calls
	//   heartbeatFn — observe LogHeartbeat calls
	//   pipelineFn  — stub for RunPipeline; when nil the default emits one
	//                 Progress event, closes progressCh, and returns nil.
	runs        *runreg.Registry
	baseCfg     orchestrator.Config
	ontologyFn  func(collection string) string
	persistFn   func(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64)
	heartbeatFn func(eventType string, payload any)
	pipelineFn  func(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error

	// Search stubs.
	//   searchPipelineFn — returns the SearchPipeline (or nil). Tests
	//     that exercise ToolSearch install a fakeSearchPipeline here.
	//   llmProvider / llmModel — returned by LLMProvider / LLMModel.
	//   capabilities          — returned by SearchCapabilities.
	searchPipelineFn func(doRerank bool) SearchPipeline
	llmProvider      llm.Provider
	llmModel         string
	capabilities     router.Capabilities

	// collectionMetas is keyed by collection name. Tests that exercise
	// toolGetProjectContext or toolCheckDrift populate this map.
	collectionMetas map[string]CollectionInfo
}

// insertedRow records a single CollectionInsert call so tests can
// assert on the fire-and-forget side effect.
type insertedRow struct {
	collection string
	id         string
	vec        []float32
	meta       any
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
func (f *fakeDeps) StoragePath() string       { return f.storagePath }

func (f *fakeDeps) CollectionExists(name string) bool {
	if !f.hasColls {
		return false
	}
	for _, c := range f.collections {
		if c == name {
			return true
		}
	}
	return false
}

func (f *fakeDeps) EmbedAvailable() bool { return f.embedAvailable }

func (f *fakeDeps) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.embedFn != nil {
		return f.embedFn(ctx, text)
	}
	// Default stub: a tiny vector derived from text length so different
	// strings produce different vectors — lets search tests pair queries
	// to seeded inserts without needing a real embedder.
	return []float32{float32(len(text)), 0.5, -0.5}, nil
}

func (f *fakeDeps) CollectionInsert(collection, id string, vec []float32, meta any) error {
	f.insertedMu.Lock()
	defer f.insertedMu.Unlock()
	f.insertedRows = append(f.insertedRows, insertedRow{collection, id, vec, meta})
	return nil
}

// getInserted returns a snapshot of observed CollectionInsert calls.
// Tests that exercise the async index-path MUST read through this
// helper — direct access to insertedRows would race with the
// goroutine writing via CollectionInsert.
func (f *fakeDeps) getInserted() []insertedRow {
	f.insertedMu.Lock()
	defer f.insertedMu.Unlock()
	out := make([]insertedRow, len(f.insertedRows))
	copy(out, f.insertedRows)
	return out
}

func (f *fakeDeps) CollectionSearch(collection string, q []float32, k int) ([]SearchResult, error) {
	if f.searchFn != nil {
		return f.searchFn(collection, q, k)
	}
	return nil, nil
}

// Runs lazily initializes a real *runreg.Registry on first access so
// tests that touch the cognify path don't have to construct one
// explicitly. Registry is goroutine-safe.
func (f *fakeDeps) Runs() *runreg.Registry {
	if f.runs == nil {
		f.runs = runreg.New()
	}
	return f.runs
}

func (f *fakeDeps) BaseCognifyConfig() orchestrator.Config { return f.baseCfg }

func (f *fakeDeps) OntologyPromptSuffix(collection string) string {
	if f.ontologyFn != nil {
		return f.ontologyFn(collection)
	}
	return ""
}

func (f *fakeDeps) PersistPipelineStatus(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64) {
	if f.persistFn != nil {
		f.persistFn(datasetID, collection, status, chunks, entities, edges, elapsedMs)
	}
}

func (f *fakeDeps) LogHeartbeat(eventType string, payload any) {
	if f.heartbeatFn != nil {
		f.heartbeatFn(eventType, payload)
	}
}

func (f *fakeDeps) RunPipeline(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
	if f.pipelineFn != nil {
		return f.pipelineFn(ctx, texts, cfg, progress)
	}
	// Default: one progress event, then clean close. Emulates a trivial
	// "chunking done" pipeline so status progresses to COMPLETED without
	// hitting any real service.
	progress <- orchestrator.Progress{
		Stage:   "done",
		Message: "stub pipeline",
	}
	close(progress)
	return nil
}

func (f *fakeDeps) NewSearchPipeline(doRerank bool) SearchPipeline {
	if f.searchPipelineFn != nil {
		return f.searchPipelineFn(doRerank)
	}
	return nil
}

func (f *fakeDeps) LLMProvider() llm.Provider               { return f.llmProvider }
func (f *fakeDeps) LLMModel() string                         { return f.llmModel }
func (f *fakeDeps) SearchCapabilities() router.Capabilities  { return f.capabilities }
func (f *fakeDeps) CollectionMeta(name string) CollectionInfo {
	if f.collectionMetas == nil {
		return CollectionInfo{}
	}
	return f.collectionMetas[name]
}

// fakeSearchPipeline is a programmable SearchPipeline stub. Each method
// consults a matching function field; when nil, returns empty results
// and nil error. Tests that only need to exercise one branch leave the
// rest as no-ops.
type fakeSearchPipeline struct {
	byText            func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error)
	byTextParentChild func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error)
	byTextMultiQuery  func(ctx context.Context, coll, query string, topK int, p llm.Provider, model string, n int) ([]pipeline.ScoredResult, error)
	byTextWithRerank  func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, bool, error)
	rerankEnabled     bool
}

func (p *fakeSearchPipeline) SearchByText(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
	if p.byText != nil {
		return p.byText(ctx, coll, query, topK)
	}
	return nil, nil
}

func (p *fakeSearchPipeline) SearchByTextParentChild(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
	if p.byTextParentChild != nil {
		return p.byTextParentChild(ctx, coll, query, topK)
	}
	return nil, nil
}

func (p *fakeSearchPipeline) SearchByTextMultiQuery(ctx context.Context, coll, query string, topK int, provider llm.Provider, model string, n int) ([]pipeline.ScoredResult, error) {
	if p.byTextMultiQuery != nil {
		return p.byTextMultiQuery(ctx, coll, query, topK, provider, model, n)
	}
	return nil, nil
}

func (p *fakeSearchPipeline) SearchByTextWithRerank(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, bool, error) {
	if p.byTextWithRerank != nil {
		return p.byTextWithRerank(ctx, coll, query, topK)
	}
	return nil, false, nil
}

func (p *fakeSearchPipeline) RerankEnabled() bool { return p.rerankEnabled }

// nilDBDeps returns nil for DB() — exercises the guard branch.
// HasCollections defaults to false, matching an unconfigured deployment.
type nilDBDeps struct{}

func (nilDBDeps) DB() *sql.DB                                                  { return nil }
func (nilDBDeps) Q(q string) string                                            { return q }
func (nilDBDeps) HasCollections() bool                                         { return false }
func (nilDBDeps) ListCollections() []string                                    { return nil }
func (nilDBDeps) StoragePath() string                                          { return "" }
func (nilDBDeps) CollectionExists(string) bool                                 { return false }
func (nilDBDeps) EmbedAvailable() bool                                         { return false }
func (nilDBDeps) Embed(context.Context, string) ([]float32, error)             { return nil, nil }
func (nilDBDeps) CollectionInsert(string, string, []float32, any) error        { return nil }
func (nilDBDeps) CollectionSearch(string, []float32, int) ([]SearchResult, error) {
	return nil, nil
}
func (nilDBDeps) Runs() *runreg.Registry                { return runreg.New() }
func (nilDBDeps) BaseCognifyConfig() orchestrator.Config { return orchestrator.Config{} }
func (nilDBDeps) OntologyPromptSuffix(string) string     { return "" }
func (nilDBDeps) PersistPipelineStatus(string, string, string, int, int, int, int64) {}
func (nilDBDeps) LogHeartbeat(string, any)                                           {}
func (nilDBDeps) RunPipeline(context.Context, []string, orchestrator.Config, chan<- orchestrator.Progress) error {
	return nil
}
func (nilDBDeps) NewSearchPipeline(bool) SearchPipeline          { return nil }
func (nilDBDeps) LLMProvider() llm.Provider                      { return nil }
func (nilDBDeps) LLMModel() string                               { return "" }
func (nilDBDeps) SearchCapabilities() router.Capabilities        { return router.Capabilities{} }
func (nilDBDeps) CollectionMeta(string) CollectionInfo           { return CollectionInfo{} }

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

// setupFeedbackTestDB builds the search_feedback schema. Columns match
// the real production schema's shape; only the ones ToolAddFeedback
// writes and ToolGetFeedbackStats reads matter for these tests.
func setupFeedbackTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "mcp-feedback-test-*.db")
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

	stmt := `CREATE TABLE search_feedback (
		id TEXT PRIMARY KEY, query TEXT, result_id TEXT, collection TEXT,
		search_type TEXT, rating INTEGER, comment TEXT, user_id TEXT
	)`
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create search_feedback: %v", err)
	}
	return &fakeDeps{db: db}
}

func TestToolAddFeedback_MissingQueryIsError(t *testing.T) {
	// 'query' is required — missing or empty → IsError with specific message.
	deps := setupFeedbackTestDB(t)

	got := ToolAddFeedback(context.Background(), deps, map[string]any{"rating": float64(3)})
	if !got.IsError {
		t.Fatalf("missing query: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'query' required") {
		t.Errorf("content = %q, want error mentioning 'query' required", got.Content[0].Text)
	}
}

func TestToolAddFeedback_InvalidRatingRange(t *testing.T) {
	// Rating must be 1..5 inclusive. Zero (missing / unparseable), negative,
	// and >5 all reject with the same error.
	deps := setupFeedbackTestDB(t)
	bad := []any{nil, float64(0), float64(6), float64(-1), "three"}
	for _, r := range bad {
		args := map[string]any{"query": "q"}
		if r != nil {
			args["rating"] = r
		}
		got := ToolAddFeedback(context.Background(), deps, args)
		if !got.IsError {
			t.Errorf("rating=%v: IsError = false, want true", r)
			continue
		}
		if !strings.Contains(got.Content[0].Text, "'rating' must be 1-5") {
			t.Errorf("rating=%v: content = %q, want 1-5 error", r, got.Content[0].Text)
		}
	}
}

func TestToolAddFeedback_NilDBIsError(t *testing.T) {
	// Unlike other tools, feedback treats a missing DB as a hard error —
	// there's no useful degraded mode when the feedback table is
	// unreachable.
	got := ToolAddFeedback(context.Background(), nilDBDeps{}, map[string]any{
		"query":  "q",
		"rating": float64(3),
	})
	if !got.IsError {
		t.Fatalf("nil DB: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "database not configured") {
		t.Errorf("content = %q, want 'database not configured'", got.Content[0].Text)
	}
}

func TestToolAddFeedback_HappyPathWritesRow(t *testing.T) {
	// Valid input → row inserted, success message echoes rating + query
	// (truncated for long queries).
	deps := setupFeedbackTestDB(t)

	got := ToolAddFeedback(context.Background(), deps, map[string]any{
		"query":       "why is search slow",
		"rating":      float64(4),
		"result_id":   "res-1",
		"collection":  "levara",
		"search_type": "hybrid",
		"comment":     "took 2s",
	})
	if got.IsError {
		t.Fatalf("IsError = true, want false; content=%+v", got.Content)
	}
	if !strings.Contains(got.Content[0].Text, "rating=4") {
		t.Errorf("content = %q, want 'rating=4'", got.Content[0].Text)
	}

	var count int
	if err := deps.db.QueryRow("SELECT COUNT(*) FROM search_feedback WHERE rating = 4").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1", count)
	}
}

func TestToolAddFeedback_LongQueryTruncatedInMessage(t *testing.T) {
	// Messages echo the query text, truncated at feedbackQueryLogMaxLen.
	// DB row still carries the full query (not asserted here — owned by
	// pkg/ingest tests); we only verify the user-visible echo is bounded.
	deps := setupFeedbackTestDB(t)

	long := strings.Repeat("a", 200)
	got := ToolAddFeedback(context.Background(), deps, map[string]any{
		"query":  long,
		"rating": float64(1),
	})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if strings.Contains(got.Content[0].Text, long) {
		t.Errorf("success message contains full 200-char query; expected Truncate to kick in")
	}
	if !strings.Contains(got.Content[0].Text, "...") {
		t.Errorf("success message missing ellipsis; content = %q", got.Content[0].Text)
	}
}

func TestToolGetFeedbackStats_NilDBReturnsZero(t *testing.T) {
	// Absent DB → return a concrete `{"total":0}` payload, not an error.
	// This matches pre-refactor behavior that lets feedback be optional
	// in downstream clients.
	got := ToolGetFeedbackStats(context.Background(), nilDBDeps{}, map[string]any{})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if got.Content[0].Text != `{"total":0}` {
		t.Errorf("content = %q, want `{\"total\":0}`", got.Content[0].Text)
	}
}

func TestToolGetFeedbackStats_AggregatesAll(t *testing.T) {
	// Seed 3 feedback rows (ratings 1, 3, 5) → total=3, avg=3, worst
	// query is the rating=1 row.
	deps := setupFeedbackTestDB(t)
	rows := []struct{ id, q string; r int }{
		{"f1", "bad search", 1},
		{"f2", "ok search", 3},
		{"f3", "great search", 5},
	}
	for _, r := range rows {
		deps.db.Exec("INSERT INTO search_feedback (id, query, rating, collection) VALUES (?,?,?,?)",
			r.id, r.q, r.r, "levara")
	}

	res := ToolGetFeedbackStats(context.Background(), deps, map[string]any{})
	if res.IsError {
		t.Fatalf("IsError = true, want false")
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if int(stats["total"].(float64)) != 3 {
		t.Errorf("total = %v, want 3", stats["total"])
	}
	if stats["avg_rating"].(float64) != 3.0 {
		t.Errorf("avg_rating = %v, want 3.0", stats["avg_rating"])
	}
	if stats["worst_query"] != "bad search" {
		t.Errorf("worst_query = %v, want 'bad search'", stats["worst_query"])
	}
}

func TestToolGetFeedbackStats_CollectionFilterScopes(t *testing.T) {
	// With a "collection" filter, stats are scoped to that collection.
	// Rows in other collections must not contribute.
	deps := setupFeedbackTestDB(t)
	deps.db.Exec("INSERT INTO search_feedback (id, query, rating, collection) VALUES ('a','x',5,'levara')")
	deps.db.Exec("INSERT INTO search_feedback (id, query, rating, collection) VALUES ('b','y',1,'other')")

	res := ToolGetFeedbackStats(context.Background(), deps, map[string]any{"collection": "levara"})
	var stats map[string]any
	json.Unmarshal([]byte(res.Content[0].Text), &stats)
	if int(stats["total"].(float64)) != 1 {
		t.Errorf("total = %v, want 1 (filtered)", stats["total"])
	}
	if stats["avg_rating"].(float64) != 5.0 {
		t.Errorf("avg_rating = %v, want 5.0", stats["avg_rating"])
	}
}

func TestToolSetContext_MissingCollectionIsError(t *testing.T) {
	// 'collection' arg is required.
	sess := &Session{}
	got := ToolSetContext(sess, nilDBDeps{}, map[string]any{})
	if !got.IsError {
		t.Fatalf("missing collection: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "'collection' required") {
		t.Errorf("content = %q, want error", got.Content[0].Text)
	}
	if sess.DefaultCollection != "" {
		t.Errorf("DefaultCollection mutated to %q on error path", sess.DefaultCollection)
	}
}

func TestToolSetContext_NilSessionIsError(t *testing.T) {
	// No session → caller forgot to send initialize; return a specific
	// error rather than panicking.
	got := ToolSetContext(nil, nilDBDeps{}, map[string]any{"collection": "levara"})
	if !got.IsError {
		t.Fatalf("nil session: IsError = false, want true")
	}
	if !strings.Contains(got.Content[0].Text, "no active session") {
		t.Errorf("content = %q, want session error", got.Content[0].Text)
	}
}

func TestToolSetContext_ExistingCollection(t *testing.T) {
	// Collection registered → session gets the default, message says "set".
	sess := &Session{}
	deps := &fakeDeps{hasColls: true, collections: []string{"levara", "other"}}

	got := ToolSetContext(sess, deps, map[string]any{"collection": "levara"})
	if got.IsError {
		t.Fatalf("IsError = true, want false")
	}
	if sess.DefaultCollection != "levara" {
		t.Errorf("DefaultCollection = %q, want levara", sess.DefaultCollection)
	}
	// "set" but NOT the "not yet created" tail.
	if strings.Contains(got.Content[0].Text, "not yet created") {
		t.Errorf("content = %q, should not mention 'not yet created' for existing coll", got.Content[0].Text)
	}
}

func TestToolSetContext_UnknownCollectionIsAllowed(t *testing.T) {
	// Unknown collection → session still updated, but message flags
	// "will be created when data is added." This lets clients pre-set
	// a context before the first write.
	sess := &Session{}
	deps := &fakeDeps{hasColls: true, collections: []string{"levara"}}

	got := ToolSetContext(sess, deps, map[string]any{"collection": "new-coll"})
	if got.IsError {
		t.Fatalf("IsError = true, want false for unknown coll")
	}
	if sess.DefaultCollection != "new-coll" {
		t.Errorf("DefaultCollection = %q, want new-coll (session updated even for unknown)", sess.DefaultCollection)
	}
	if !strings.Contains(got.Content[0].Text, "not yet created") {
		t.Errorf("content = %q, want 'not yet created' warning", got.Content[0].Text)
	}
}
