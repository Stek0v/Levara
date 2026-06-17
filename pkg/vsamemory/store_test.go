package vsamemory

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func newVSATestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/vsa.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO graph_edges(id, source_id, target_id, relationship_name, valid_until, dataset_id) VALUES
			('e1', 'auth', 'postgres', 'calls', NULL, 'ds-a'),
			('e2', 'auth', 'redis', 'calls', NULL, 'ds-a'),
			('e3', 'api',  'auth', 'calls', NULL, 'ds-a'),
			('e4', 'auth', 'alice', 'owned_by', NULL, 'ds-a'),
			('e5', 'auth', 'mysql', 'calls', '2026-01-01T00:00:00Z', 'ds-a'),
			('e6', 'auth', 'tenant-b-db', 'calls', NULL, 'ds-b');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return db
}

func TestStoreRebuildFromGraphCreatesPredicateShards(t *testing.T) {
	ctx := context.Background()
	db := newVSATestDB(t)
	store := NewStore(db, Config{Dim: 256, ShardSize: 2, Dialect: DialectSQLite})

	if err := store.RebuildFromGraph(ctx, "ds-a"); err != nil {
		t.Fatalf("RebuildFromGraph: %v", err)
	}
	var callsShards int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vsa_fact_shards WHERE dataset_id = 'ds-a' AND predicate = 'calls'`).Scan(&callsShards); err != nil {
		t.Fatalf("count shards: %v", err)
	}
	if callsShards != 2 {
		t.Fatalf("calls shards=%d, want 2 with shard size 2 and 3 active calls facts", callsShards)
	}
	var expiredIndexed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM vsa_fact_members WHERE edge_id = 'e5'`).Scan(&expiredIndexed); err != nil {
		t.Fatalf("count expired: %v", err)
	}
	if expiredIndexed != 0 {
		t.Fatalf("expired edge indexed count=%d, want 0", expiredIndexed)
	}
}

func TestStoreQueryObjectRanksGraphMembersAndIsolatesDataset(t *testing.T) {
	ctx := context.Background()
	db := newVSATestDB(t)
	store := NewStore(db, Config{Dim: 1024, ShardSize: 12, Dialect: DialectSQLite})
	if err := store.RebuildFromGraph(ctx, "ds-a"); err != nil {
		t.Fatalf("RebuildFromGraph ds-a: %v", err)
	}
	if err := store.RebuildFromGraph(ctx, "ds-b"); err != nil {
		t.Fatalf("RebuildFromGraph ds-b: %v", err)
	}

	got, err := store.QueryObject(ctx, "ds-a", "auth", "calls", 10)
	if err != nil {
		t.Fatalf("QueryObject: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candidates=%+v, want postgres and redis only", got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.TargetID] = true
		if c.DatasetID != "ds-a" || c.Predicate != "calls" {
			t.Fatalf("bad candidate metadata: %+v", c)
		}
	}
	if !seen["postgres"] || !seen["redis"] || seen["tenant-b-db"] {
		t.Fatalf("candidate isolation failed: %+v", got)
	}
}

func TestStoreQueryObjectTopK(t *testing.T) {
	ctx := context.Background()
	db := newVSATestDB(t)
	store := NewStore(db, Config{Dim: 512, ShardSize: 12, Dialect: DialectSQLite})
	if err := store.RebuildFromGraph(ctx, "ds-a"); err != nil {
		t.Fatalf("RebuildFromGraph: %v", err)
	}
	got, err := store.QueryObject(ctx, "ds-a", "auth", "calls", 1)
	if err != nil {
		t.Fatalf("QueryObject: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("topK result len=%d, want 1", len(got))
	}
}
