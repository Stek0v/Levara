package mcp

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// fakeDeps is the minimum Deps implementation for unit-testing tool
// functions: a real *sql.DB (in-memory sqlite) plus a Q() that mirrors
// the internal/http Postgres→SQLite rewrite (ncruces sqlite does not
// accept $N placeholders).
type fakeDeps struct {
	db *sql.DB
}

func (f *fakeDeps) DB() *sql.DB { return f.db }

// pgPlaceholderRe matches $N placeholder style used by the production
// queries; we rewrite to ? for SQLite.
var pgPlaceholderRe = regexp.MustCompile(`\$(\d+)`)

func (f *fakeDeps) Q(query string) string {
	return pgPlaceholderRe.ReplaceAllString(query, "?")
}

// nilDBDeps returns nil for DB() — exercises the guard branch.
type nilDBDeps struct{}

func (nilDBDeps) DB() *sql.DB       { return nil }
func (nilDBDeps) Q(q string) string { return q }

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
