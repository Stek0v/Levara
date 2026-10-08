package http

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Independent native stores share no state. MaxOpenConns(1) makes accidental
// nested SQL access fail rather than hiding it behind a spare connection.
func syncConvergenceDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	if dialect == "postgres" {
		return syncTruthPostgresDB(t)
	}
	SetDBProvider(DBSQLite)
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "convergence.db")+"?_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close(); SetDBProvider(DBPostgres) })
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSyncCanonicalFixedPointIndependentStores(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			left, right := APIConfig{DB: syncConvergenceDB(t, dialect)}, APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			low := syncInteraction{ID: "same", SessionID: "session", UserID: "owner", Query: "a", Response: "first", CreatedAt: "2026-09-05T03:00:00+03:00"}
			high := low
			high.Query = "z"
			high.Response = "deterministic winner"
			high.CreatedAt = "2026-09-05T00:00:00Z"
			if counts, err := importSyncInteractions(ctx, left, []syncInteraction{low, high}); err != nil || counts["imported"] != 2 {
				t.Fatalf("left interactions=%v %v", counts, err)
			}
			if counts, err := importSyncInteractions(ctx, right, []syncInteraction{high, low}); err != nil || counts["imported"] != 1 || counts["skipped"] != 1 {
				t.Fatalf("right interactions=%v %v", counts, err)
			}
			node := syncGraphNode{ID: "node", Name: "a", Type: "component", Properties: `{"large":9007199254740993,"n":1.0}`, DatasetID: "dataset"}
			betterNode := node
			betterNode.Name = "z"
			betterNode.Description = "winner"
			betterNode.Properties = `{"n":1e0,"large":9007199254740993}`
			active := syncGraphEdge{ID: "edge", SourceID: "node", TargetID: "target", RelationshipName: "status_is", Properties: `{"value":"z active"}`, ValidFrom: "2026-09-01T03:00:00+03:00", Confidence: .7, DatasetID: "dataset"}
			retired := active
			retired.Properties = `{"value":"a retired"}`
			retired.ValidUntil = "2026-09-05T00:00:00Z"
			retired.SupersededBy = "replacement"
			for index, cfg := range []APIConfig{left, right} {
				nodes := []syncGraphNode{node, betterNode}
				edges := []syncGraphEdge{active, retired}
				if index == 1 {
					nodes[0], nodes[1] = nodes[1], nodes[0]
					edges[0], edges[1] = edges[1], edges[0]
				}
				counts, err := importSyncGraph(ctx, cfg, syncGraph{Nodes: nodes, Edges: edges})
				if err != nil {
					t.Fatal(err)
				}
				if index == 1 && (counts["nodes_skipped"] != 1 || counts["edges_skipped"] != 1) {
					t.Fatalf("losing graph rows not skipped: %v", counts)
				}
			}
			for round := 0; round < 3; round++ {
				exportedLeft, err := exportSyncInteractions(ctx, left, "")
				if err != nil {
					t.Fatal(err)
				}
				exportedRight, err := exportSyncInteractions(ctx, right, "")
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(exportedLeft, exportedRight) || len(exportedLeft) != 1 || exportedLeft[0].Query != "z" || exportedLeft[0].CreatedAt != "2026-09-05T00:00:00Z" {
					t.Fatalf("interaction divergence: %#v %#v", exportedLeft, exportedRight)
				}
				graphLeft, err := exportSyncGraph(ctx, left)
				if err != nil {
					t.Fatal(err)
				}
				graphRight, err := exportSyncGraph(ctx, right)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(graphLeft, graphRight) {
					t.Fatalf("graph divergence: %#v %#v", graphLeft, graphRight)
				}
				if len(graphLeft.Edges) != 1 || graphLeft.Edges[0].ValidUntil != "2026-09-05T00:00:00Z" || graphLeft.Edges[0].SupersededBy != "replacement" || graphLeft.Edges[0].ValidFrom != "2026-09-01T00:00:00Z" || graphLeft.Edges[0].Confidence != float64(float32(.7)) {
					t.Fatalf("retired metadata lost: %#v", graphLeft)
				}
				if graphLeft.Nodes[0].Name != "z" || !strings.Contains(graphLeft.Nodes[0].Properties, "9007199254740993") {
					t.Fatalf("node payload lost: %#v", graphLeft)
				}
				for index, cfg := range []APIConfig{left, right} {
					interactions, graph := exportedRight, graphRight
					if index == 1 {
						interactions, graph = exportedLeft, graphLeft
					}
					counts, err := importSyncInteractions(ctx, cfg, interactions)
					if err != nil || counts["imported"] != 0 || counts["skipped"] != 1 {
						t.Fatalf("repeat interactions=%v %v", counts, err)
					}
					graphCounts, err := importSyncGraph(ctx, cfg, graph)
					if err != nil || graphCounts["nodes_imported"] != 0 || graphCounts["edges_imported"] != 0 || graphCounts["nodes_skipped"] != 1 || graphCounts["edges_skipped"] != 1 {
						t.Fatalf("repeat graph=%v %v", graphCounts, err)
					}
					if syncAcceptedCount(graphCounts) != 2 {
						t.Fatalf("skips not acknowledged=%v", graphCounts)
					}
				}
			}
			// Legacy active payloads cannot reopen an already retired edge.
			counts, err := importSyncGraph(ctx, left, syncGraph{Edges: []syncGraphEdge{active}})
			if err != nil || counts["edges_imported"] != 0 || counts["edges_skipped"] != 1 {
				t.Fatalf("legacy resurrection=%v %v", counts, err)
			}
			for _, cfg := range []APIConfig{left, right} {
				if cfg.DB.Stats().InUse != 0 {
					t.Fatal("convergence leaked pool-one connection")
				}
			}
		})
	}
}

func TestSyncGraphSourceIdentityConflictFailsClosed(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx := context.Background()
			node := syncGraphNode{ID: "node", Name: "original", Properties: "{}", DatasetID: "dataset"}
			edge := syncGraphEdge{ID: "edge", SourceID: "node", TargetID: "target", RelationshipName: "related_to", Properties: "{}", Confidence: 1, DatasetID: "dataset"}
			if _, err := importSyncGraph(ctx, cfg, syncGraph{Nodes: []syncGraphNode{node}, Edges: []syncGraphEdge{edge}}); err != nil {
				t.Fatal(err)
			}
			before, err := exportSyncGraph(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, scope := range []string{"other", ""} {
				conflictNode := node
				conflictNode.DatasetID = scope
				conflictNode.Name = "z"
				conflictEdge := edge
				conflictEdge.DatasetID = scope
				counts, err := importSyncGraph(ctx, cfg, syncGraph{Nodes: []syncGraphNode{conflictNode}, Edges: []syncGraphEdge{conflictEdge}})
				if err == nil || counts["nodes_failed"] != 1 || counts["edges_failed"] != 1 || syncAcceptedCount(counts) != 0 {
					t.Fatalf("scope conflict=%v %v", counts, err)
				}
			}
			for _, field := range []string{"source", "target", "relationship"} {
				conflict := edge
				switch field {
				case "source":
					conflict.SourceID = "other"
				case "target":
					conflict.TargetID = "other"
				case "relationship":
					conflict.RelationshipName = "other"
				}
				counts, err := importSyncGraph(ctx, cfg, syncGraph{Edges: []syncGraphEdge{conflict}})
				if err == nil || counts["edges_failed"] != 1 {
					t.Fatalf("endpoint conflict=%v %v", counts, err)
				}
			}
			after, err := exportSyncGraph(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("source identity conflict changed persisted graph")
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("source conflict leaked transaction")
			}
		})
	}
}

func TestSyncConcurrentAbsentIDImportsConverge(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := syncConvergenceDB(t, dialect)
			cfg := APIConfig{DB: db}
			// An independent connection can race first admission. Keep this race fixture
			// at two connections; other convergence checks exercise pool-one behavior.
			db.SetMaxOpenConns(2)
			db.SetMaxIdleConns(2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			// InUse includes pending opens. Reserve both native connections before
			// the race so the final zero assertion measures leaked work, not the opener.
			first, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			second, err := db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			if err := second.Close(); err != nil {
				t.Fatal(err)
			}
			if stats := db.Stats(); stats.OpenConnections != 2 || stats.Idle != 2 || stats.InUse != 0 {
				t.Fatalf("native race pool not ready: %+v", stats)
			}
			start := make(chan struct{})
			failures := make(chan error, 8)
			var workers sync.WaitGroup
			for i := 0; i < 8; i++ {
				workers.Add(1)
				go func(i int) {
					defer workers.Done()
					<-start
					payload := syncInteraction{ID: "race", Query: fmt.Sprintf("%02d", i), CreatedAt: "2026-09-05T00:00:00Z"}
					_, err := importSyncInteractions(ctx, cfg, []syncInteraction{payload})
					if err == nil {
						_, err = importSyncGraph(ctx, cfg, syncGraph{Nodes: []syncGraphNode{{ID: "race-node", Name: fmt.Sprintf("%02d", i), Properties: "{}", DatasetID: "dataset"}}, Edges: []syncGraphEdge{{ID: "race-edge", SourceID: "race-node", TargetID: "target", RelationshipName: "related_to", Properties: fmt.Sprintf("{\"value\":%d}", i), DatasetID: "dataset", Confidence: 1}}})
					}
					failures <- err
				}(i)
			}
			close(start)
			workers.Wait()
			close(failures)
			for err := range failures {
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := exportSyncInteractions(ctx, cfg, "")
			if err != nil || len(rows) != 1 || rows[0].Query != "07" {
				t.Fatalf("absent-row race=%#v %v", rows, err)
			}
			graph, err := exportSyncGraph(ctx, cfg)
			if err != nil || len(graph.Nodes) != 1 || len(graph.Edges) != 1 || graph.Nodes[0].Name != "07" || graph.Edges[0].Properties != `{"value":7}` {
				t.Fatalf("graph absent-row race=%#v %v", graph, err)
			}
			if db.Stats().InUse != 0 {
				t.Fatal("concurrent import leaked connection")
			}
		})
	}
}

func TestSyncExportsDoNotSilentlyTruncate(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			cfg := APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			for _, fixture := range []struct {
				table, columns, values string
				count                  int
			}{
				{"interactions", "id,query,created_at", "CAST(n AS TEXT),'complete','2026-09-05T00:00:00Z'", 10001},
				{"memories", "id,key,value,type,owner_id,collection_name,created_at,updated_at", "CAST(n AS TEXT),CAST(n AS TEXT),'complete','project','','export','2026-09-05T00:00:00Z','2026-09-05T00:00:00Z'", 10001},
				{"graph_nodes", "id,name,type,properties,dataset_id", "CAST(n AS TEXT),'complete','component','{}','dataset'", 50001},
				{"graph_edges", "id,source_id,target_id,relationship_name,properties,dataset_id", "CAST(n AS TEXT),'source','target','related_to','{}','dataset'", 50001},
			} {
				prefix := fmt.Sprintf("WITH RECURSIVE numbers(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM numbers WHERE n<%d) ", fixture.count)
				if dialect == "postgres" {
					prefix = fmt.Sprintf("WITH numbers(n) AS (SELECT generate_series(1,%d)) ", fixture.count)
				}
				query := prefix + "INSERT INTO " + fixture.table + "(" + fixture.columns + ") SELECT " + fixture.values + " FROM numbers"
				if _, err := cfg.DB.ExecContext(ctx, query); err != nil {
					t.Fatal(err)
				}
			}
			interactions, err := exportSyncInteractions(ctx, cfg, "")
			if err != nil || len(interactions) != 10001 {
				t.Fatalf("interaction export=%d %v", len(interactions), err)
			}
			memories, err := exportSyncMemories(ctx, cfg, "")
			if err != nil || len(memories) != 10001 {
				t.Fatalf("memory export=%d %v", len(memories), err)
			}
			if filtered, err := exportSyncInteractions(ctx, cfg, "2026-09-01T00:00:00Z"); err != nil || len(filtered) != 10001 {
				t.Fatalf("since interaction export=%d %v", len(filtered), err)
			}
			if filtered, err := exportSyncMemories(ctx, cfg, "2026-09-01T00:00:00Z"); err != nil || len(filtered) != 10001 {
				t.Fatalf("since memory export=%d %v", len(filtered), err)
			}
			graph, err := exportSyncGraph(ctx, cfg)
			if err != nil || len(graph.Nodes) != 50001 || len(graph.Edges) != 50001 {
				t.Fatalf("graph export=%d/%d %v", len(graph.Nodes), len(graph.Edges), err)
			}
			if cfg.DB.Stats().InUse != 0 {
				t.Fatal("complete export leaked connection")
			}
		})
	}
}
