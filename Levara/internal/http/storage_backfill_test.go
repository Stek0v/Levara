package http

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestBackfillRawLocationsToStorage_MigratesFileURI(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "bf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE data (
			id TEXT PRIMARY KEY,
			extension TEXT,
			raw_data_location TEXT,
			updated_at TIMESTAMP
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}

	artifact := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(artifact, []byte("hello backfill"), 0o644); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO data (id, extension, raw_data_location) VALUES ('d1', '.txt', ?)`, "file://"+artifact); err != nil {
		t.Fatalf("insert data row: %v", err)
	}

	SetDBProvider(DBSQLite)
	defer SetDBProvider(DBPostgres)

	store := newMemStorage()
	report, err := BackfillRawLocationsToStorage(context.Background(), APIConfig{
		DB:          db,
		FileStorage: store,
	}, 100)
	if err != nil {
		t.Fatalf("BackfillRawLocationsToStorage: %v", err)
	}
	if report.Scanned != 1 || report.Migrated != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if got := string(store.objects["ingest/d1.txt"]); got != "hello backfill" {
		t.Fatalf("stored object = %q, want %q", got, "hello backfill")
	}

	var location string
	if err := db.QueryRow(`SELECT raw_data_location FROM data WHERE id = 'd1'`).Scan(&location); err != nil {
		t.Fatalf("select migrated location: %v", err)
	}
	if location != "storage://ingest/d1.txt" {
		t.Fatalf("raw_data_location = %q, want storage://ingest/d1.txt", location)
	}
}

func TestBackfillRawLocationsToStorage_MissingFile(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "bf-missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`
		CREATE TABLE data (
			id TEXT PRIMARY KEY,
			extension TEXT,
			raw_data_location TEXT,
			updated_at TIMESTAMP
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO data (id, extension, raw_data_location) VALUES ('d1', '.txt', 'file:///tmp/definitely-missing.txt')`); err != nil {
		t.Fatalf("insert data row: %v", err)
	}

	SetDBProvider(DBSQLite)
	defer SetDBProvider(DBPostgres)

	report, err := BackfillRawLocationsToStorage(context.Background(), APIConfig{
		DB:          db,
		FileStorage: newMemStorage(),
	}, 100)
	if err != nil {
		t.Fatalf("BackfillRawLocationsToStorage: %v", err)
	}
	if report.Scanned != 1 || report.Missing != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}

	var location string
	if err := db.QueryRow(`SELECT raw_data_location FROM data WHERE id = 'd1'`).Scan(&location); err != nil {
		t.Fatalf("select location: %v", err)
	}
	if !strings.HasPrefix(location, "file://") {
		t.Fatalf("location changed on missing source: %q", location)
	}
}
