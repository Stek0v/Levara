package community

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/sqlcompat"
)

func pruneRegressionDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	previous := sqlcompat.CurrentProvider()
	t.Cleanup(func() { sqlcompat.SetProvider(previous) })
	var db *sql.DB
	var err error
	stampType := "TEXT"
	if dialect == "postgres" {
		sqlcompat.SetProvider(sqlcompat.Postgres)
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
		}
		db, err = sql.Open("pgx", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		schema := fmt.Sprintf("community_prune_%d", time.Now().UnixNano())
		if _, err = db.Exec(`CREATE SCHEMA ` + schema); err != nil {
			db.Close()
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); db.Close() })
		if _, err = db.Exec(`SET search_path TO ` + schema); err != nil {
			t.Fatal(err)
		}
		stampType = "TIMESTAMPTZ"
	} else {
		sqlcompat.SetProvider(sqlcompat.SQLite)
		db, err = sql.Open("sqlite3", t.TempDir()+"/prune.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
	}
	for _, q := range []string{
		`CREATE TABLE graph_nodes(id TEXT PRIMARY KEY)`,
		fmt.Sprintf(`CREATE TABLE graph_edges(id TEXT PRIMARY KEY,source_id TEXT,target_id TEXT,superseded_by TEXT,valid_until %s)`, stampType),
		`CREATE TABLE community_members(id TEXT PRIMARY KEY,node_id TEXT)`,
		`INSERT INTO graph_nodes VALUES ('old-source'),('old-target'),('current-source'),('current-target'),('orphan')`,
		`INSERT INTO graph_edges VALUES ('expired','old-source','old-target','replacement','2000-01-01'),('current','current-source','current-target','',NULL)`,
		`INSERT INTO community_members VALUES ('old','old-source'),('current','current-source'),('dangling','absent')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPruneRegressionDialectAndRollback(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			t.Run("preview-and-delete", func(t *testing.T) {
				db := pruneRegressionDB(t, dialect)
				cfg := PruneConfig{MaxAgeDays: 90, DryRun: true, IncludeOrphans: true}
				got, err := PruneGraph(context.Background(), db, cfg)
				if err != nil || got.EdgesWouldDelete != 1 {
					t.Fatalf("preview=%+v err=%v", got, err)
				}
				var n int
				if err = db.QueryRow(`SELECT COUNT(*) FROM graph_edges`).Scan(&n); err != nil || n != 2 {
					t.Fatalf("preview mutated rows=%d err=%v", n, err)
				}
				// Preview includes nodes orphaned by deleting the candidate edges.
				if got.OrphanNodes != 3 {
					t.Errorf("preview resulting orphans=%d", got.OrphanNodes)
				}
				cfg.DryRun = false
				got, err = PruneGraph(context.Background(), db, cfg)
				if err != nil || got.EdgesDeleted != 1 || got.OrphanNodes != 3 || got.MembersCleanedUp != 2 {
					t.Fatalf("delete=%+v err=%v", got, err)
				}
				if err = db.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE id='current'`).Scan(&n); err != nil || n != 1 {
					t.Fatalf("current edge lost count=%d err=%v", n, err)
				}
			})
			for _, table := range []string{"graph_edges", "graph_nodes", "community_members"} {
				t.Run("error-"+table, func(t *testing.T) {
					db := pruneRegressionDB(t, dialect)
					if _, err := db.Exec(`ALTER TABLE ` + table + ` RENAME TO unavailable`); err != nil {
						t.Fatal(err)
					}
					cfg := PruneConfig{MaxAgeDays: 90, DryRun: true, IncludeOrphans: true}
					if table != "community_members" {
						if result, err := PruneGraph(context.Background(), db, cfg); err == nil {
							t.Errorf("preview hid SQL error: %+v", result)
						}
					}
					cfg.DryRun = false
					if result, err := PruneGraph(context.Background(), db, cfg); err == nil {
						t.Errorf("mutation hid SQL error: %+v", result)
					} else if result.EdgesDeleted != 0 || result.OrphanNodes != 0 || result.MembersCleanedUp != 0 {
						t.Errorf("rolled-back operations reported committed: %+v", result)
					}
					if _, err := db.Exec(`ALTER TABLE unavailable RENAME TO ` + table); err != nil {
						t.Fatal(err)
					}
					for table, want := range map[string]int{"graph_edges": 2, "graph_nodes": 5, "community_members": 3} {
						var n int
						if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != want {
							t.Errorf("partial deletion table=%s count=%d want=%d err=%v", table, n, want, err)
						}
					}
				})
			}
			t.Run("timestamp-formats", func(t *testing.T) {
				db := pruneRegressionDB(t, dialect)
				cutoff := time.Now().UTC().AddDate(0, 0, -90)
				// Same-instant comparisons must account for offsets and both common
				// SQLite timestamp encodings, including rows on the cutoff day.
				for id, stamp := range map[string]string{
					"older-sql":    cutoff.Add(-time.Hour).Format("2006-01-02 15:04:05"),
					"older-offset": cutoff.Add(-time.Hour).In(time.FixedZone("plus14", 14*3600)).Format(time.RFC3339),
					"newer-offset": cutoff.Add(time.Hour).In(time.FixedZone("minus12", -12*3600)).Format(time.RFC3339),
					"newer-utc":    cutoff.Add(time.Hour).Format(time.RFC3339),
				} {
					if _, err := db.Exec(`INSERT INTO graph_edges VALUES ($1,'current-source','current-target','replacement',$2)`, id, stamp); err != nil {
						t.Fatal(err)
					}
				}
				result, err := PruneGraph(context.Background(), db, PruneConfig{MaxAgeDays: 90, DryRun: true})
				if err != nil || result.EdgesWouldDelete != 3 {
					t.Fatalf("timestamps preview=%+v err=%v", result, err)
				}
				result, err = PruneGraph(context.Background(), db, PruneConfig{MaxAgeDays: 90})
				if err != nil || result.EdgesDeleted != 3 {
					t.Fatalf("timestamps deletion=%+v err=%v", result, err)
				}
			})
		})
	}
}

func TestPruneRegressionPreviewPreservesUnknownSupersession(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := pruneRegressionDB(t, dialect)
			for _, q := range []string{
				`INSERT INTO graph_nodes VALUES('unknown')`,
				`INSERT INTO graph_edges VALUES('unknown','unknown','unknown',NULL,'2000-01-01')`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			preview, err := PruneGraph(context.Background(), db, PruneConfig{DryRun: true, IncludeOrphans: true})
			if err != nil || preview.EdgesWouldDelete != 1 || preview.OrphanNodes != 3 {
				t.Fatalf("preview=%+v err=%v", preview, err)
			}
			applied, err := PruneGraph(context.Background(), db, PruneConfig{IncludeOrphans: true})
			if err != nil || applied.EdgesDeleted != preview.EdgesWouldDelete || applied.OrphanNodes != preview.OrphanNodes {
				t.Fatalf("preview=%+v apply=%+v err=%v", preview, applied, err)
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM graph_nodes WHERE id='unknown'`).Scan(&n); err != nil || n != 1 {
				t.Errorf("NULL supersession orphaned a kept node: count=%d err=%v", n, err)
			}
		})
	}
}
