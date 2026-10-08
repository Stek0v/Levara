package http

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
)

func TestSyncGraphRetirementJoinPreservesKnownWindow(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			left, right := APIConfig{DB: syncConvergenceDB(t, dialect)}, APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			nodes := []syncGraphNode{{ID: "retired-source", Name: "RetiredControl", Type: "Entity", Properties: "{}", DatasetID: "dataset"}, {ID: "retired-target", Name: "Target", Type: "Entity", Properties: "{}", DatasetID: "dataset"}}
			closed := syncGraphEdge{ID: "retired-edge", SourceID: "retired-source", TargetID: "retired-target", RelationshipName: "status_is", DatasetID: "dataset", Properties: `{"payload":"a closed"}`, ValidFrom: "2020-01-01T00:00:00Z", ValidUntil: "2020-03-01T00:00:00Z", Confidence: .7}
			superseded := closed
			superseded.Properties = `{"payload":"z superseded-only"}`
			superseded.ValidFrom = ""
			superseded.ValidUntil = ""
			superseded.SupersededBy = "next-edge"
			laterBoundary := closed
			laterBoundary.Properties = `{"payload":"y later-boundary"}`
			laterBoundary.ValidFrom = "2020-02-01T00:00:00Z"
			laterBoundary.ValidUntil = "2020-04-01T00:00:00Z"
			for index, cfg := range []APIConfig{left, right} {
				edges := []syncGraphEdge{closed, superseded, laterBoundary}
				if index == 1 {
					edges[0], edges[2] = edges[2], edges[0]
				}
				if _, err := importSyncGraph(ctx, cfg, syncGraph{Nodes: nodes, Edges: edges}); err != nil {
					t.Fatal(err)
				}
			}
			var first syncGraph
			for index, cfg := range []APIConfig{left, right} {
				graph, err := exportSyncGraph(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if len(graph.Edges) != 1 {
					t.Fatalf("joined edge count=%d", len(graph.Edges))
				}
				joined := graph.Edges[0]
				if joined.ValidFrom != "2020-02-01T00:00:00Z" || joined.ValidUntil != "2020-03-01T00:00:00Z" || joined.SupersededBy != "next-edge" {
					t.Fatalf("known lifecycle widened or erased: %+v", joined)
				}
				if joined.Properties != superseded.Properties {
					t.Fatalf("deterministic content winner=%q want=%q", joined.Properties, superseded.Properties)
				}
				if index == 0 {
					first = graph
				} else if !reflect.DeepEqual(first, graph) {
					t.Fatalf("opposite-order join diverged: %#v %#v", first, graph)
				}
				// The public tool adapter uses real native SQL. Standalone mode is
				// intentional here: these imported legacy assertions have no native
				// document publication provenance, which authenticated callers require.
				check := func(asOf string, want int) {
					t.Helper()
					args := map[string]any{"name": "RetiredControl", "dataset_id": "dataset"}
					if asOf != "" {
						args["as_of"] = asOf
					}
					result := mcp.ToolQueryEntity(ctx, NewMCPDeps(cfg), args)
					if result.IsError || len(result.Content) != 1 {
						t.Fatalf("query_entity(%q)=%+v", asOf, result)
					}
					var payload struct {
						Edges []json.RawMessage `json:"edges"`
					}
					if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
						t.Fatal(err)
					}
					if len(payload.Edges) != want {
						t.Fatalf("query_entity(%q) edges=%d want=%d", asOf, len(payload.Edges), want)
					}
				}
				check("", 0)
				check("2019-12-01T00:00:00Z", 0)
				check("2020-01-15T00:00:00Z", 0)
				check("2020-02-01T00:00:00Z", 1)
				check("2020-02-15T00:00:00Z", 1)
				check("2020-03-01T00:00:00Z", 0)
				// Known start/end remain monotonic on replay of the wider, incomplete
				// lifecycle payloads, even when their content ranks higher.
				counts, err := importSyncGraph(ctx, cfg, syncGraph{Edges: []syncGraphEdge{superseded, closed, laterBoundary}})
				if err != nil || counts["edges_imported"] != 0 || counts["edges_skipped"] != 3 {
					t.Fatalf("lifecycle replay=%v %v", counts, err)
				}
				after, err := exportSyncGraph(ctx, cfg)
				if err != nil || !reflect.DeepEqual(graph, after) {
					t.Fatalf("replay changed lifecycle: %#v %v", after, err)
				}
				invalid := closed
				invalid.Properties = `{"payload":"zz incompatible chronology"}`
				invalid.ValidFrom = "2020-04-01T00:00:00Z"
				invalid.ValidUntil = "2020-05-01T00:00:00Z"
				invalidCounts, invalidErr := importSyncGraph(ctx, cfg, syncGraph{Edges: []syncGraphEdge{invalid}})
				if invalidErr == nil || invalidCounts["edges_failed"] != 1 || invalidCounts["edges_imported"] != 0 || invalidCounts["edges_skipped"] != 0 {
					t.Fatalf("invalid joined chronology=%v %v", invalidCounts, invalidErr)
				}
				unchanged, err := exportSyncGraph(ctx, cfg)
				if err != nil || !reflect.DeepEqual(graph, unchanged) {
					t.Fatalf("invalid chronology changed persisted edge: %#v %v", unchanged, err)
				}
				if cfg.DB.Stats().InUse != 0 {
					t.Fatal("retirement join/tool leaked pool-one connection")
				}
			}
		})
	}
}

func TestSyncGraphDirectConfidenceSeedExportsFixedPoint(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			left, right := APIConfig{DB: syncConvergenceDB(t, dialect)}, APIConfig{DB: syncConvergenceDB(t, dialect)}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			// Direct SQL intentionally seeds each dialect's native REAL representation,
			// bypassing import normalization so the export seam is independently tested.
			for _, cfg := range []APIConfig{left, right} {
				query, args := QArgs(`INSERT INTO graph_edges(id,source_id,target_id,relationship_name,properties,confidence,dataset_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, "confidence-edge", "source", "target", "related_to", "{}", .7, "dataset")
				if _, err := cfg.DB.ExecContext(ctx, query, args...); err != nil {
					t.Fatal(err)
				}
			}
			want := float64(float32(.7))
			for round := 0; round < 3; round++ {
				leftGraph, err := exportSyncGraph(ctx, left)
				if err != nil {
					t.Fatal(err)
				}
				rightGraph, err := exportSyncGraph(ctx, right)
				if err != nil {
					t.Fatal(err)
				}
				for _, graph := range []syncGraph{leftGraph, rightGraph} {
					if len(graph.Edges) != 1 || graph.Edges[0].Confidence != want {
						t.Fatalf("native confidence export=%+v want %.17g", graph.Edges, want)
					}
				}
				if !reflect.DeepEqual(leftGraph, rightGraph) {
					t.Fatalf("native confidence payload drift: %#v %#v", leftGraph, rightGraph)
				}
				for index, cfg := range []APIConfig{left, right} {
					incoming := rightGraph
					if index == 1 {
						incoming = leftGraph
					}
					counts, err := importSyncGraph(ctx, cfg, incoming)
					if err != nil || counts["edges_imported"] != 0 || counts["edges_skipped"] != 1 {
						t.Fatalf("confidence fixed point=%v %v", counts, err)
					}
					if cfg.DB.Stats().InUse != 0 {
						t.Fatal("confidence replay leaked pool-one connection")
					}
				}
			}
		})
	}
}
