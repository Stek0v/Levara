package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestMetadataConsistency(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgres"} {
		t.Run(provider, func(t *testing.T) {
			driver, dsn := "sqlite3", "file:"+filepath.Join(t.TempDir(), "metadata.db")+"?_pragma=foreign_keys(1)"
			if provider == "postgres" {
				driver, dsn = "pgx", os.Getenv("LEVARA_TEST_INGEST_PG_DSN")
				if dsn == "" {
					t.Skip("LEVARA_TEST_INGEST_PG_DSN is not configured")
				}
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if provider == "postgres" {
				schema := fmt.Sprintf("ingest_%s", filepath.Base(t.TempDir()))
				// TempDir's last component is numeric, and this schema is local to this test connection.
				if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
					t.Fatal(err)
				}
				defer db.Exec("DROP SCHEMA " + schema + " CASCADE")
				if _, err := db.Exec("SET search_path TO " + schema); err != nil {
					t.Fatal(err)
				}
			}
			SetSQLiteMode(provider == "sqlite")
			defer SetSQLiteMode(false)
			_, err = db.Exec(`
			CREATE TABLE datasets (id TEXT PRIMARY KEY, name TEXT UNIQUE, owner_id TEXT, created_at TIMESTAMP, updated_at TIMESTAMP);
			CREATE TABLE data (id TEXT PRIMARY KEY, name TEXT, extension TEXT, mime_type TEXT, raw_data_location TEXT,
			 original_data_location TEXT, content_hash TEXT, raw_content_hash TEXT, owner_id TEXT, loader_engine TEXT,
			 pipeline_status TEXT, tags TEXT, room TEXT, token_count INTEGER, data_size BIGINT, created_at TIMESTAMP, updated_at TIMESTAMP);
			CREATE TABLE dataset_data (dataset_id TEXT REFERENCES datasets(id), data_id TEXT REFERENCES data(id),
			 PRIMARY KEY(dataset_id,data_id), CHECK(dataset_id <> 'reject-link'));`)
			if err != nil {
				t.Fatal(err)
			}
			writer := NewMetadataWriterFromDB(db)
			ctx := context.Background()
			count := func(table string) int {
				t.Helper()
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			// A blob surviving a failed earlier transaction is not an existing SQL row.
			result := Result{ID: "document", Name: "document.txt", FilePath: "file:///test/blob", AlreadyExists: true}
			if _, err := writer.WriteMetadata(ctx, []Result{result}, "alice", "allowed", "allowed"); err != nil {
				t.Fatal(err)
			}
			if count("data") != 1 || count("dataset_data") != 1 {
				t.Error("existing blob did not produce metadata and association")
			}
			// A repeated source may produce new text after parser/schema changes.
			if _, err := db.Exec(`UPDATE data SET pipeline_status='{"docs":{"status":"COMPLETED"}}',raw_content_hash='old' WHERE id='document'`); err != nil {
				t.Fatal(err)
			}
			result.ContentHash = "old"
			if _, err := writer.WriteMetadata(ctx, []Result{result}, "alice", "allowed", "allowed"); err != nil {
				t.Fatal(err)
			}
			var status string
			if err := db.QueryRow("SELECT pipeline_status FROM data WHERE id='document'").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status == "{}" {
				t.Error("identical extraction lost completed status")
			}
			result.ContentHash = "new"
			if _, err := writer.WriteMetadata(ctx, []Result{result}, "alice", "allowed", "allowed"); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT pipeline_status FROM data WHERE id='document'").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "{}" {
				t.Errorf("changed extraction kept completed status: %s", status)
			}
			beforeData, beforeDatasets := count("data"), count("datasets")
			bad := Result{ID: "rejected", Name: "bad.txt"}
			if _, err := writer.WriteMetadata(ctx, []Result{bad}, "alice", "reject-link", "reject-link"); err == nil {
				t.Error("association failure was swallowed")
			}
			if count("data") != beforeData || count("datasets") != beforeDatasets {
				t.Error("association failure left partial metadata")
			}
			if _, err := writer.WriteMetadata(ctx, []Result{bad}, "bob", "other", "allowed"); err == nil {
				t.Error("conflicting dataset name created phantom metadata")
			}
			if count("data") != beforeData || count("datasets") != beforeDatasets {
				t.Error("dataset conflict left partial metadata")
			}
		})
	}
}
