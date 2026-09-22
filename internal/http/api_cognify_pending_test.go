package http

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func pendingDocsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "pending.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE data (id TEXT PRIMARY KEY, name TEXT, raw_data_location TEXT)`,
		`CREATE TABLE dataset_data (dataset_id TEXT, data_id TEXT, PRIMARY KEY (dataset_id, data_id))`,
		`CREATE TABLE document_index_publications (dataset_id TEXT, data_id TEXT)`,
		`INSERT INTO data VALUES ('d1','one.md','loc1'), ('d2','two.md','loc2'), ('d3','three.md','loc3')`,
		`INSERT INTO dataset_data VALUES ('ds','d1'), ('ds','d2'), ('ds','d3')`,
		`INSERT INTO document_index_publications VALUES ('ds','d2')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(DBPostgres) })
	return db
}

func TestPendingDatasetDocumentsSkipsPublished(t *testing.T) {
	db := pendingDocsDB(t)
	docs, err := pendingDatasetDocuments(context.Background(), db, "ds")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 {
		t.Fatalf("pending = %d docs, want 2 (published d2 excluded): %+v", len(docs), docs)
	}
	for _, d := range docs {
		if d.documentID == "d2" {
			t.Fatalf("published document re-picked: %+v", d)
		}
	}
	if docs[0].documentID != "d1" || docs[1].documentID != "d3" {
		t.Fatalf("order changed: %+v", docs)
	}
	// A dataset without publications returns everything.
	docs, err = pendingDatasetDocuments(context.Background(), db, "other")
	if err != nil || len(docs) != 0 {
		t.Fatalf("foreign dataset: %d %v", len(docs), err)
	}
}
