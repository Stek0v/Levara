package graphstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stek0v/levara/pkg/graphdb"
)

// These fixtures delete only their own random node IDs. No global graph cleanup
// or schema/backfill operation is needed to test the read adapter.
func neighborhoodWriter(t *testing.T) *graphdb.Writer {
	t.Helper()
	url := os.Getenv("NEO4J_TEST_URL")
	if url == "" {
		t.Skip("NEO4J_TEST_URL not set")
	}
	user, pass, database := os.Getenv("NEO4J_TEST_USER"), os.Getenv("NEO4J_TEST_PASSWORD"), os.Getenv("NEO4J_TEST_DATABASE")
	if user == "" {
		user = "neo4j"
	}
	if pass == "" {
		pass = "test"
	}
	if database == "" {
		database = "neo4j"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := graphdb.NewWriter(ctx, url, user, pass, database)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close(context.Background()) })
	return w
}

func neighborhoodWrite(t *testing.T, w *graphdb.Writer, query string, params map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := graphdb.RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		r, err := tx.Run(ctx, query, params)
		if err != nil {
			return struct{}{}, err
		}
		_, err = r.Consume(ctx)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatalf("fixture write: %v", err)
	}
}

func neighborhoodSeed(t *testing.T, w *graphdb.Writer, nodes []NodeRecord, edges []EdgeRecord) {
	t.Helper()
	rows := make([]map[string]any, 0, len(nodes))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		// Empty type deliberately exercises the dynamic label fallback.
		var typ any = n.Type
		if n.Type == "" {
			typ = nil
		}
		rows = append(rows, map[string]any{"id": n.ID, "name": n.Name, "type": typ})
		ids = append(ids, n.ID)
	}
	t.Cleanup(func() {
		neighborhoodWrite(t, w, "MATCH (n:__Node__) WHERE n.id IN $ids DETACH DELETE n", map[string]any{"ids": ids})
	})
	neighborhoodWrite(t, w, "UNWIND $rows AS row CREATE (n:__Node__:Fallback) SET n.id=row.id,n.name=row.name,n.type=row.type", map[string]any{"rows": rows})
	relations := make([]map[string]any, 0, len(edges))
	for _, e := range edges {
		relations = append(relations, map[string]any{"source": e.SourceID, "target": e.TargetID, "id": e.ID})
	}
	// Real endpoints are the only source of identity; no source_node_id or
	// target_node_id property is seeded.
	neighborhoodWrite(t, w, "UNWIND $rows AS row MATCH (a:__Node__ {id:row.source}),(b:__Node__ {id:row.target}) CREATE (a)-[r:LINK]->(b) SET r.id=row.id", map[string]any{"rows": relations})
}

func neighborhoodNativeDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	if dialect == "sqlite" {
		return newSQLGraphStoreTestDB(t)
	}
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	schema := "neighborhood_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
			t.Errorf("cleanup schema: %v", err)
		}
	})
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE graph_nodes (
		id TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',type TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',properties TEXT NOT NULL DEFAULT '{}',
		dataset_id TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ);
		CREATE TABLE graph_edges (
		id TEXT PRIMARY KEY,source_id TEXT NOT NULL,target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',properties TEXT NOT NULL DEFAULT '{}',
		valid_from TIMESTAMPTZ,valid_until TIMESTAMPTZ,dataset_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ,updated_at TIMESTAMPTZ)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func neighborhoodSorted(in []GraphContext) []GraphContext {
	out := append([]GraphContext(nil), in...)
	sort.Slice(out, func(i, j int) bool { return fmt.Sprintf("%q", out[i]) < fmt.Sprintf("%q", out[j]) })
	return out
}

func TestNeo4jNeighborhoodDepthAndNativeParity(t *testing.T) {
	w := neighborhoodWriter(t)
	p := "nh-" + uuid.NewString() + "-"
	node := func(id, name, typ string) NodeRecord { return NodeRecord{ID: p + id, Name: p + name, Type: typ} }
	nodes := []NodeRecord{node("a", "A", "Service"), node("b", "B", ""), node("c", "C", "Service"), node("d", "D", "Service"), node("x", "X", "Service"), node("y", "Y", "Service"), node("a2", "A", "Service"), node("b2", "B", "")}
	edge := func(id, from, to string) EdgeRecord {
		return EdgeRecord{ID: p + id, SourceID: p + from, TargetID: p + to, RelationshipName: "LINK"}
	}
	edges := []EdgeRecord{edge("ab", "a", "b"), edge("ab-copy", "a", "b"), edge("bc", "b", "c"), edge("cd", "c", "d"), edge("xa", "x", "a"), edge("cy", "c", "y"), edge("a2b2", "a2", "b2"), edge("aa", "a", "a"), edge("bx", "b", "x")}
	neighborhoodSeed(t, w, nodes, edges)
	store := NewNeo4jGraphStore(w)
	gc := func(from, to, sourceType, targetType string) GraphContext {
		return GraphContext{SourceName: p + from, SourceType: sourceType, Relationship: "LINK", TargetName: p + to, TargetType: targetType}
	}
	one := []GraphContext{gc("A", "B", "Service", "Fallback"), gc("A", "B", "Service", "Fallback"), gc("A", "X", "Service", "Service"), gc("A", "A", "Service", "Service")}
	two := append(append([]GraphContext{}, one...), gc("B", "A", "Fallback", "Service"), gc("B", "A", "Fallback", "Service"), gc("B", "C", "Fallback", "Service"), gc("X", "A", "Service", "Service"), gc("B", "X", "Fallback", "Service"), gc("X", "B", "Service", "Fallback"))
	three := append(append([]GraphContext{}, two...), gc("C", "B", "Service", "Fallback"), gc("C", "D", "Service", "Service"), gc("C", "Y", "Service", "Service"))
	cases := []struct {
		hops int
		want []GraphContext
	}{{1, one}, {2, two}, {3, three}, {0, one}, {-7, one}}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.hops), func(t *testing.T) {
			got, err := store.QueryNHop(context.Background(), []string{strings.ToLower(p + "A"), p + "A"}, tc.hops)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(neighborhoodSorted(got), neighborhoodSorted(tc.want)) {
				t.Fatalf("got=%+v want=%+v", got, tc.want)
			}
			for i := 0; i < 3; i++ {
				repeated, err := store.QueryNHop(context.Background(), []string{p + "A"}, tc.hops)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, repeated) {
					t.Fatalf("unstable order: %+v / %+v", got, repeated)
				}
			}
		})
	}
	if got, err := store.Query1Hop(context.Background(), []string{p + "A"}); err != nil || !reflect.DeepEqual(neighborhoodSorted(got), neighborhoodSorted(one)) {
		t.Fatalf("Query1Hop=%+v err=%v", got, err)
	}
	if got, err := store.Query2Hop(context.Background(), []string{p + "A"}); err != nil || !reflect.DeepEqual(neighborhoodSorted(got), neighborhoodSorted(two)) {
		t.Fatalf("Query2Hop=%+v err=%v", got, err)
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := neighborhoodNativeDB(t, dialect)
			sqlNodes := append([]NodeRecord(nil), nodes...)
			for i := range sqlNodes {
				if sqlNodes[i].Type == "" {
					sqlNodes[i].Type = "Fallback"
				}
			}
			native := NewSQLGraphStore(db)
			if res := native.WriteGraph(context.Background(), p, sqlNodes, edges); len(res.Errors) > 0 {
				t.Fatal(res.Errors)
			}
			for _, hops := range []int{1, 2, 3, 8, 99, 0, -7} {
				want, err := native.QueryNHop(context.Background(), []string{strings.ToLower(p + "A")}, hops)
				if err != nil {
					t.Fatal(err)
				}
				got, err := store.QueryNHop(context.Background(), []string{strings.ToLower(p + "A")}, hops)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(neighborhoodSorted(got), neighborhoodSorted(want)) {
					t.Fatalf("hops %d: neo4j=%+v %s=%+v", hops, got, dialect, want)
				}
			}
		})
	}
	for _, names := range [][]string{nil, {}, {p + "missing"}} {
		if got, err := store.QueryNHop(context.Background(), names, 2); err != nil || len(got) != 0 {
			t.Fatalf("empty/missing got=%+v err=%v", got, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := store.QueryNHop(ctx, []string{p + "A"}, 2); err == nil || len(got) != 0 {
		t.Fatalf("cancelled read got=%+v err=%v", got, err)
	}
	if _, err := w.ReadSubgraph(context.Background(), "", []string{p + "A"}); err == nil {
		t.Fatal("external empty label validation weakened")
	}
}

func TestNeo4jNeighborhoodClampAndLimit(t *testing.T) {
	w := neighborhoodWriter(t)
	p := "nh-" + uuid.NewString() + "-"
	var nodes []NodeRecord
	var edges []EdgeRecord
	for i := 0; i <= 10; i++ {
		id := fmt.Sprintf("%schain-%02d", p, i)
		nodes = append(nodes, NodeRecord{ID: id, Name: id, Type: "Service"})
		if i > 0 {
			edges = append(edges, EdgeRecord{ID: id + "-edge", SourceID: fmt.Sprintf("%schain-%02d", p, i-1), TargetID: id, RelationshipName: "LINK"})
		}
	}
	neighborhoodSeed(t, w, nodes, edges)
	store := NewNeo4jGraphStore(w)
	for _, hops := range []int{8, 9, 1000} {
		got, err := store.QueryNHop(context.Background(), []string{p + "chain-00"}, hops)
		if err != nil {
			t.Fatal(err)
		}
		// Frontier 0..7 contributes 8 forward and 7 reverse contexts.
		if len(got) != 15 {
			t.Fatalf("hops=%d contexts=%d want15: %+v", hops, len(got), got)
		}
		for _, g := range got {
			if strings.HasSuffix(g.SourceName, "08") || strings.HasSuffix(g.TargetName, "09") {
				t.Fatalf("beyond eight hops: %+v", g)
			}
		}
	}
	nodes = []NodeRecord{{ID: p + "hub", Name: p + "hub", Type: "Service"}}
	edges = nil
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("%sleaf-%03d", p, i)
		nodes = append(nodes, NodeRecord{ID: id, Name: id, Type: "Service"})
		edges = append(edges, EdgeRecord{ID: id + "-edge", SourceID: p + "hub", TargetID: id, RelationshipName: "LINK"})
	}
	neighborhoodSeed(t, w, nodes, edges)
	var first []GraphContext
	for i := 0; i < 3; i++ {
		got, err := store.Query1Hop(context.Background(), []string{p + "hub"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 100 {
			t.Fatalf("cap contexts=%d want100", len(got))
		}
		for j, g := range got {
			if g.SourceName != p+"hub" || g.TargetName != fmt.Sprintf("%sleaf-%03d", p, j) {
				t.Fatalf("cap ordering[%d]=%+v", j, g)
			}
		}
		if i == 0 {
			first = got
		} else if !reflect.DeepEqual(first, got) {
			t.Fatal("unstable capped order")
		}
	}
}

func TestNeo4jNeighborhoodAbsentStore(t *testing.T) {
	for _, store := range []*Neo4jGraphStore{nil, NewNeo4jGraphStore(nil)} {
		if got, err := store.QueryNHop(context.Background(), []string{"A"}, 2); err != nil || len(got) != 0 {
			t.Fatalf("absent store=%+v err=%v", got, err)
		}
	}
}

func TestNeo4jNeighborhoodBackendFailure(t *testing.T) {
	w := neighborhoodWriter(t)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := NewNeo4jGraphStore(w).Query1Hop(context.Background(), []string{"any"}); err == nil || len(got) != 0 {
		t.Fatalf("closed backend got=%+v err=%v", got, err)
	}
}
