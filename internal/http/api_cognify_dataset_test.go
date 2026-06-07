package http

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// cognifyDatasetDB opens an in-memory-ish sqlite DB with just the datasets
// schema ensureCognifyDataset writes (name UNIQUE so ON CONFLICT(name) has an
// index), and pins the dialect to SQLite for Q() placeholder rewriting.
func cognifyDatasetDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "cog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	if _, err := db.Exec(`CREATE TABLE datasets (
		id TEXT PRIMARY KEY,
		name TEXT UNIQUE,
		owner_id TEXT,
		created_at TIMESTAMP,
		updated_at TIMESTAMP
	)`); err != nil {
		t.Fatalf("create datasets: %v", err)
	}
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(DBPostgres) })
	return db
}

// ensureCognifyDataset is the REST mirror of the MCP helper: it must
// get-or-create exactly one caller-owned datasets row per (owner,collection)
// so search's RBAC gate doesn't drop the chunks this run stamps. Without it,
// an unregistered ephemeral runID is in no caller's allowed-set and every
// cognified chunk reads back empty (the P2.1/Issue-2 regression).
func TestEnsureCognifyDataset_GetOrCreateIsIdempotent(t *testing.T) {
	db := cognifyDatasetDB(t)
	ctx := context.Background()

	id1 := ensureCognifyDataset(ctx, db, "alice", "docs", "run-1")
	id2 := ensureCognifyDataset(ctx, db, "alice", "docs", "run-2")

	if id1 != "run-1" {
		t.Errorf("first id = %q, want fallback run-1 (created the row)", id1)
	}
	if id2 != id1 {
		t.Errorf("second id = %q, want %q (reuse, not a fresh runID)", id2, id1)
	}

	var rows int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM datasets WHERE name = '__cognify__:alice:docs'").Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("datasets rows = %d, want exactly 1 (no per-run accretion)", rows)
	}
}

func TestEnsureCognifyDataset_ScopesByOwnerAndCollection(t *testing.T) {
	db := cognifyDatasetDB(t)
	ctx := context.Background()

	alice := ensureCognifyDataset(ctx, db, "alice", "docs", "r-a")
	bob := ensureCognifyDataset(ctx, db, "bob", "docs", "r-b")
	aliceOther := ensureCognifyDataset(ctx, db, "alice", "notes", "r-a2")

	if alice == bob {
		t.Errorf("alice and bob share dataset id %q; owner scoping broken", alice)
	}
	if alice == aliceOther {
		t.Errorf("alice's docs and notes share id %q; collection scoping broken", alice)
	}

	var owner string
	if err := db.QueryRow("SELECT owner_id FROM datasets WHERE id = ?", alice).Scan(&owner); err != nil {
		t.Fatalf("select owner: %v", err)
	}
	if owner != "alice" {
		t.Errorf("owner_id = %q, want alice (RBAC gate keys on ownership)", owner)
	}
}
