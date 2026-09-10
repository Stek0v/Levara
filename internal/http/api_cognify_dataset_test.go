package http

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// cognifyDatasetDB opens an in-memory-ish sqlite DB with just the datasets
// schema resolveCognifyDataset reads, and pins the dialect to SQLite for Q().
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

func TestResolveCognifyDatasetDoesNotCreateBeforeAuthorizedSource(t *testing.T) {
	db := cognifyDatasetDB(t)
	ctx := context.Background()

	id, name, err := resolveCognifyDataset(ctx, db, "alice", "docs", "run-1")
	if err != nil || id != "run-1" || name != "__cognify__:alice:docs" {
		t.Fatalf("id=%q name=%q err=%v", id, name, err)
	}

	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM datasets").Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("resolver created %d dataset rows before authorization", rows)
	}
}

func TestResolveCognifyDatasetReusesOnlyOwnerRow(t *testing.T) {
	db := cognifyDatasetDB(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO datasets(id,name,owner_id) VALUES('owned','__cognify__:alice:docs','alice'),('foreign','__cognify__:bob:docs','mallory')`); err != nil {
		t.Fatal(err)
	}
	id, _, err := resolveCognifyDataset(ctx, db, "alice", "docs", "new")
	if err != nil || id != "owned" {
		t.Fatalf("owner row id=%q err=%v", id, err)
	}
	if _, _, err := resolveCognifyDataset(ctx, db, "bob", "docs", "new"); !errors.Is(err, accesspkg.ErrDocumentForbidden) {
		t.Fatalf("foreign preclaim err=%v", err)
	}
}
