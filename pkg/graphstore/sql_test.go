package graphstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/graphdb"
)

func newSQLGraphStoreTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/graph.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			dataset_id TEXT NOT NULL DEFAULT '',
			created_at TEXT,
			updated_at TEXT
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			dataset_id TEXT NOT NULL DEFAULT '',
			created_at TEXT,
			updated_at TEXT
		);
	`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestSQLGraphStoreWriteGraphUpsertsAndReads(t *testing.T) {
	db := newSQLGraphStoreTestDB(t)
	store := NewSQLGraphStore(db)

	res := store.WriteGraph(context.Background(), "ds-write", []NodeRecord{
		{ID: "n1", Name: "Auth", Type: "Service", Description: "old", Properties: map[string]any{"rank": "1"}},
		{ID: "n2", Properties: map[string]any{"name": "DB", "type": "Service", "description": "database"}},
	}, []EdgeRecord{
		{SourceID: "n1", TargetID: "n2", RelationshipName: "calls", Properties: map[string]any{"weight": "high"}},
	})
	if len(res.Errors) > 0 {
		t.Fatalf("WriteGraph errors: %v", res.Errors)
	}
	if res.NodesWritten != 2 || res.EdgesWritten != 1 {
		t.Fatalf("result=%+v, want 2 nodes/1 edge", res)
	}

	res = store.WriteGraph(context.Background(), "ds-write", []NodeRecord{
		{ID: "n1", Name: "Auth", Type: "Service", Description: "new"},
	}, nil)
	if len(res.Errors) > 0 {
		t.Fatalf("second WriteGraph errors: %v", res.Errors)
	}

	var desc, datasetID string
	if err := db.QueryRow(`SELECT description, dataset_id FROM graph_nodes WHERE id = 'n1'`).Scan(&desc, &datasetID); err != nil {
		t.Fatalf("query node: %v", err)
	}
	if desc != "new" || datasetID != "ds-write" {
		t.Fatalf("desc/dataset=%q/%q, want new/ds-write", desc, datasetID)
	}

	graph, err := store.ReadFullGraph(context.Background())
	if err != nil {
		t.Fatalf("ReadFullGraph: %v", err)
	}
	if len(graph.Nodes) != 2 || len(graph.Edges) != 1 {
		t.Fatalf("graph=%+v, want 2 nodes/1 edge", graph)
	}
	if graph.Nodes[0].Properties["dataset_id"] != "ds-write" {
		t.Fatalf("dataset_id not projected into node properties: %+v", graph.Nodes[0].Properties)
	}
}

func seedSQLGraphPath(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES ('a','Alpha','Service','A','{"custom":"one"}','ds1')`,
		`INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES ('b','Beta','Service','B','{}','ds1')`,
		`INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES ('c','Gamma','Service','C','{}','ds1')`,
		`INSERT INTO graph_nodes(id,name,type,description,properties,dataset_id) VALUES ('d','Delta','Service','D','{}','ds1')`,
		`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,valid_from,valid_until,dataset_id) VALUES ('ab','a','b','calls','{"weight":1}','0',NULL,'ds1')`,
		`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,valid_from,valid_until,dataset_id) VALUES ('bc','b','c','calls','{}','0',NULL,'ds1')`,
		`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,valid_from,valid_until,dataset_id) VALUES ('ac_expired','a','c','old_calls','{}','0','99','ds1')`,
		`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,valid_from,valid_until,dataset_id) VALUES ('cd','c','d','calls','{}','200',NULL,'ds1')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

func TestSQLGraphStoreReadFullGraph(t *testing.T) {
	db := newSQLGraphStoreTestDB(t)
	seedSQLGraphPath(t, db)

	got, err := NewSQLGraphStore(db).ReadFullGraph(context.Background())
	if err != nil {
		t.Fatalf("ReadFullGraph: %v", err)
	}
	if len(got.Nodes) != 4 || len(got.Edges) != 4 {
		t.Fatalf("nodes=%d edges=%d, want 4/4", len(got.Nodes), len(got.Edges))
	}
	if got.Nodes[0].Properties["dataset_id"] != "ds1" {
		t.Fatalf("dataset_id not propagated in node properties: %+v", got.Nodes[0].Properties)
	}
}

func TestSQLGraphStorePathBetweenUsesShortestVisiblePath(t *testing.T) {
	db := newSQLGraphStoreTestDB(t)
	seedSQLGraphPath(t, db)

	got, err := NewSQLGraphStore(db).PathBetween(context.Background(), graphdb.PathQuery{
		From: "a", To: "c", MaxHops: 4, AsOf: 150,
	})
	if err != nil {
		t.Fatalf("PathBetween: %v", err)
	}
	if len(got.Edges) != 2 {
		t.Fatalf("edges=%+v, want two-hop a-b-c because direct edge is expired at as_of=150", got.Edges)
	}
	if got.Edges[0].SourceID != "a" || got.Edges[0].TargetID != "b" || got.Edges[1].SourceID != "b" || got.Edges[1].TargetID != "c" {
		t.Fatalf("unexpected path edges: %+v", got.Edges)
	}
}

func TestSQLGraphStorePathBetweenCorners(t *testing.T) {
	db := newSQLGraphStoreTestDB(t)
	seedSQLGraphPath(t, db)
	store := NewSQLGraphStore(db)

	if _, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a"}); err == nil {
		t.Fatal("missing to should error")
	}
	if _, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a", To: "c", Cursor: "%%%"}); err == nil {
		t.Fatal("invalid cursor should error")
	}
	none, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a", To: "missing"})
	if err != nil {
		t.Fatalf("missing node should not error: %v", err)
	}
	if len(none.Edges) != 0 {
		t.Fatalf("missing node edges=%+v, want empty", none.Edges)
	}
	future, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a", To: "d", AsOf: time.Unix(150, 0).Unix()})
	if err != nil {
		t.Fatalf("future temporal path: %v", err)
	}
	if len(future.Edges) != 0 {
		t.Fatalf("edge c-d valid_from=200 should be hidden at as_of=150: %+v", future.Edges)
	}
}

func TestSQLGraphStorePathBetweenPaginatesFlatEdges(t *testing.T) {
	db := newSQLGraphStoreTestDB(t)
	seedSQLGraphPath(t, db)
	store := NewSQLGraphStore(db)

	first, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a", To: "c", AsOf: 150, Limit: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Edges) != 1 || first.NextCursor == "" {
		t.Fatalf("first page=%+v, want one edge and cursor", first)
	}
	second, err := store.PathBetween(context.Background(), graphdb.PathQuery{From: "a", To: "c", AsOf: 150, Limit: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Edges) != 1 || second.NextCursor != "" {
		t.Fatalf("second page=%+v, want final one edge", second)
	}
}
