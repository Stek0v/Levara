package http

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func uploadDatasetDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(dir, "upload.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		os.RemoveAll(dir)
	})
	if _, err := db.Exec(`CREATE TABLE datasets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		owner_id TEXT
	)`); err != nil {
		t.Fatalf("create datasets: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE dataset_shares (
		id TEXT PRIMARY KEY,
		dataset_id TEXT,
		user_id TEXT,
		role TEXT
	)`); err != nil {
		t.Fatalf("create dataset_shares: %v", err)
	}
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(DBPostgres) })
	return db
}

func TestLookupUploadDatasetID_PrefersOwnerMatch(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES
		('legacy-default', 'default', ''),
		('alice-default', 'default', 'alice')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "alice-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want alice-default", got)
	}
}

func TestLookupUploadDatasetID_FallsBackForNoAuth(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('legacy-default', 'default', '')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "")
	if got != "legacy-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want legacy-default", got)
	}
}

func TestLookupUploadDatasetID_DoesNotFallbackToOtherOwner(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-default', 'default', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "" {
		t.Fatalf("lookupUploadDatasetID = %q, want empty for another owner's dataset", got)
	}
}

func TestLookupUploadDatasetID_AuthenticatedUserCanUsePublicFallback(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('public-default', 'default', '')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	got := lookupUploadDatasetID(context.Background(), db, "default", "alice")
	if got != "public-default" {
		t.Fatalf("lookupUploadDatasetID = %q, want public-default", got)
	}
}

func TestValidateUploadDatasetID_DeniesOtherOwner(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-ds', 'bob-data', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if ok {
		t.Fatal("validateUploadDatasetID allowed another owner's dataset")
	}
}

func TestValidateUploadDatasetID_AllowsSharedDataset(t *testing.T) {
	db := uploadDatasetDB(t)
	if _, err := db.Exec(`INSERT INTO datasets(id, name, owner_id) VALUES ('bob-ds', 'bob-data', 'bob')`); err != nil {
		t.Fatalf("seed datasets: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_shares(id, dataset_id, user_id, role) VALUES ('share-1', 'bob-ds', 'alice', 'viewer')`); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	ok, err := validateUploadDatasetID(context.Background(), db, "bob-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if !ok {
		t.Fatal("validateUploadDatasetID denied shared dataset")
	}
}

func TestValidateUploadDatasetID_AllowsMissingDatasetForCreate(t *testing.T) {
	db := uploadDatasetDB(t)

	ok, err := validateUploadDatasetID(context.Background(), db, "new-ds", "alice")
	if err != nil {
		t.Fatalf("validateUploadDatasetID: %v", err)
	}
	if !ok {
		t.Fatal("validateUploadDatasetID denied missing dataset id")
	}
}
