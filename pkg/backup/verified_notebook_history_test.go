package backup

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/ingest"
)

// The administrative backup and restore run only against owned disposable
// stores. Notebook tables stay in the native schema after execution retires.
func TestVerifiedNotebookHistoryRoundTrip(t *testing.T) {
	for _, provider := range []string{"sqlite", "postgres"} {
		t.Run(provider, func(t *testing.T) {
			ctx := context.Background()
			o := verifiedFixture(t)
			o.Timeout = 3 * time.Minute
			prior := httpapi.GetDBProvider()
			defer httpapi.SetDBProvider(prior)
			defer ingest.SetSQLiteMode(false)
			dbPath := filepath.Join(o.DataDir, "levara.db")
			if err := os.Remove(dbPath); err != nil {
				t.Fatal(err)
			}
			driver, source := "sqlite3", dbPath
			if provider == "postgres" {
				dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("isolated test PostgreSQL DSN required")
				}
				admin, err := sql.Open("pgx", dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer admin.Close()
				name := fmt.Sprintf("backup_notebooks_%d", time.Now().UnixNano())
				if _, err := admin.Exec("CREATE DATABASE " + quoteSQL(name)); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := admin.Exec("DROP DATABASE " + quoteSQL(name) + " WITH (FORCE)"); err != nil {
						t.Errorf("drop fixture: %v", err)
					}
				}()
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal(err)
				}
				u.Path = "/" + name
				source, driver = u.String(), "pgx"
				o.DBProvider, o.PostgresDSN, o.PostgresBinDir = "postgres", source, os.Getenv("LEVARA_TEST_POSTGRES_BIN")
				httpapi.SetDBProvider(httpapi.DBPostgres)
			} else {
				httpapi.SetDBProvider(httpapi.DBSQLite)
			}
			ingest.SetSQLiteMode(provider == "sqlite")
			db, err := sql.Open(driver, source)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)
			if err := httpapi.MigrateSchema(db); err != nil {
				t.Fatal(err)
			}
			created := time.Date(2024, 1, 2, 3, 4, 5, 123000000, time.UTC)
			updated := created.Add(time.Hour)
			for i, owner := range []string{"private-owner", "other-owner", ""} {
				id := fmt.Sprintf("history-%d", i)
				if _, err := db.Exec(httpapi.Q("INSERT INTO notebooks(id,title,owner_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5)"),
					id, "История "+id, owner, created, updated); err != nil {
					t.Fatal(err)
				}
				for j, kind := range []string{"code", "markdown", "search"} {
					if _, err := db.Exec(httpapi.Q("INSERT INTO notebook_cells(id,notebook_id,cell_type,source,output,position,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)"),
						fmt.Sprintf("%s-cell-%d", id, j), id, kind, "source\nλ", "{\"saved\":\"result\"}", 10-j, created, updated); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Native fields are NOT NULL; an extra default-valued cell pins empty
			// source/output and the default cell type/position as well.
			if _, err := db.Exec("INSERT INTO notebook_cells(id,notebook_id,created_at,updated_at) VALUES('empty-cell','history-0',$1,$2)", created, updated); err != nil {
				t.Fatal(err)
			}
			opts, err := verifiedDefaults(o)
			if err != nil {
				t.Fatal(err)
			}
			beforeSchema, before, err := sqlProofs(ctx, db, provider, nil, opts.MaxRows)
			if err != nil {
				t.Fatal(err)
			}
			if before["notebooks"].Rows != 3 || before["notebook_cells"].Rows != 10 {
				t.Fatalf("vacuous history fixture: %+v", before)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			receipt, err := CreateVerifiedBackup(ctx, o)
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
				if err := os.Rename(root, root+"-unavailable"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(root); !os.IsNotExist(err) {
					t.Fatalf("original root remains available: %s %v", root, err)
				}
			}
			if _, err := VerifyArchive(ctx, receipt.Archive, o); err != nil {
				t.Fatal(err)
			}
			sandbox := t.TempDir()
			if _, _, _, err := extractVerified(ctx, receipt.Archive, sandbox, opts); err != nil {
				t.Fatal(err)
			}
			filename := "sqlite.db"
			if provider == "postgres" {
				filename = "postgres.dump"
			}
			restored, closeRestored, err := snapshotSQLForProof(ctx, opts, filepath.Join(sandbox, "sql", filename))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := closeRestored(); err != nil {
					t.Errorf("close restored SQL: %v", err)
				}
			}()
			afterSchema, after, err := sqlProofs(ctx, restored, provider, nil, opts.MaxRows)
			if err != nil {
				t.Fatal(err)
			}
			if beforeSchema != afterSchema {
				t.Fatal("restored native schema/index/constraint proof changed")
			}
			for _, table := range []string{"notebooks", "notebook_cells"} {
				if !sameJSON(before[table], after[table]) {
					t.Fatalf("restored complete %s history changed: before=%+v after=%+v", table, before[table], after[table])
				}
			}
			// SQLite connection-level enforcement is enabled explicitly, while
			// the preceding schema proof pins the restored FK definition.
			if provider == "sqlite" {
				if _, err := restored.Exec("PRAGMA foreign_keys=ON"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := restored.Exec(httpapi.Q("INSERT INTO notebook_cells(id,notebook_id) VALUES($1,$2)"), "orphan", "missing-notebook"); err == nil {
				t.Fatal("restored foreign key accepted an orphan")
			}
		})
	}
}
