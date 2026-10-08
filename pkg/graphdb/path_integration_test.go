package graphdb

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Uses the opt-in disposable database selected by NEO4J_TEST_*.
// newLiveWriter deletes all nodes; these cases must never target a shared database.
func seedTemporalPath(t *testing.T, w *Writer, edges []map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	nodeSet := map[string]bool{}
	for _, edge := range edges {
		nodeSet[edge["source"].(string)] = true
		nodeSet[edge["target"].(string)] = true
	}
	nodes := make([]string, 0, len(nodeSet))
	for id := range nodeSet {
		nodes = append(nodes, id)
	}
	sort.Strings(nodes)
	_, err := RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		res, err := tx.Run(ctx, "UNWIND $nodes AS id CREATE (n:__Node__ {id:id})", map[string]any{"nodes": nodes})
		if err != nil {
			return struct{}{}, err
		}
		if _, err := res.Consume(ctx); err != nil {
			return struct{}{}, err
		}
		res, err = tx.Run(ctx, `UNWIND $edges AS edge
		 MATCH (a:__Node__ {id:edge.source}), (b:__Node__ {id:edge.target})
		 CREATE (a)-[r:LINK]->(b) SET r += edge.properties
		 RETURN count(r) AS count`, map[string]any{"edges": edges})
		if err != nil {
			return struct{}{}, err
		}
		if !res.Next(ctx) || res.Record().Values[0] != int64(len(edges)) {
			return struct{}{}, fmt.Errorf("temporal seed count mismatch: %v", res.Err())
		}
		_, err = res.Consume(ctx)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func temporalPathEdge(id, source, target string, from, until any) map[string]any {
	return map[string]any{"source": source, "target": target, "properties": map[string]any{"id": id, "valid_from": from, "valid_until": until}}
}

func temporalPathIDs(edges []PathEdge) []string {
	ids := make([]string, 0, len(edges))
	for _, edge := range edges {
		ids = append(ids, edge.Properties["id"].(string))
	}
	return ids
}

func TestTemporalPathSnapshotSelection(t *testing.T) {
	live := []map[string]any{
		temporalPathEdge("ab", "a", "b", int64(100), int64(200)),
		temporalPathEdge("bc", "b", "c", int64(100), int64(200)),
		temporalPathEdge("expired-shortcut", "a", "c", int64(100), int64(120)),
	}
	for _, tc := range []struct {
		name  string
		edges []map[string]any
		asOf  int64
		hops  int
		want  []string
	}{
		{"expired_shortcut_longer_route", live, 150, 4, []string{"ab", "bc"}},
		{"inclusive_lower_bound", live, 100, 4, []string{"expired-shortcut"}},
		{"inclusive_upper_bound", live, 200, 4, []string{"ab", "bc"}},
		{"before_lower_bound", live, 99, 4, []string{}},
		{"after_upper_bound", live, 201, 4, []string{}},
		{"AsOf0_history", live, 0, 4, []string{"expired-shortcut"}},
		{"hop_limit", live, 150, 1, []string{}},
		{"legacy_epoch_open_end", []map[string]any{temporalPathEdge("legacy", "a", "c", nil, nil)}, 1, 4, []string{"legacy"}},
		{"future_shortcut", []map[string]any{
			temporalPathEdge("ab", "a", "b", int64(100), nil),
			temporalPathEdge("bc", "b", "c", int64(100), nil),
			temporalPathEdge("future", "a", "c", int64(200), nil),
		}, 150, 4, []string{"ab", "bc"}},
		{"invalid_route_has_no_fragment", []map[string]any{
			temporalPathEdge("ab", "a", "b", int64(100), nil),
			temporalPathEdge("expired-bc", "b", "c", int64(100), int64(120)),
		}, 150, 4, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newLiveWriter(t)
			seedTemporalPath(t, w, tc.edges)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := w.PathBetween(ctx, PathQuery{From: "a", To: "c", AsOf: tc.asOf, MaxHops: tc.hops})
			if err != nil || got.AsOf != tc.asOf || !reflect.DeepEqual(temporalPathIDs(got.Edges), tc.want) || got.NextCursor != "" {
				t.Fatalf("snapshot path=%+v err=%v want=%v", got, err, tc.want)
			}
			for _, edge := range got.Edges {
				if tc.asOf != 0 && (edge.ValidFrom > tc.asOf || edge.ValidUntil != nil && *edge.ValidUntil < tc.asOf) {
					t.Fatalf("invalid temporal envelope: %+v", edge)
				}
			}
		})
	}
}

func TestTemporalPathParallelHistoryPagination(t *testing.T) {
	w := newLiveWriter(t)
	seedTemporalPath(t, w, []map[string]any{
		temporalPathEdge("episode-a", "a", "b", int64(100), int64(120)),
		temporalPathEdge("episode-b", "a", "b", int64(120), nil),
		temporalPathEdge("tail", "b", "c", nil, nil),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	full, err := w.PathBetween(ctx, PathQuery{From: "a", To: "c"})
	if err != nil || len(full.Edges) != 3 {
		t.Fatalf("full parallel history=%+v err=%v", full, err)
	}
	if full.Edges[0].ValidFrom != 100 || full.Edges[0].ValidUntil == nil || *full.Edges[0].ValidUntil != 120 ||
		full.Edges[1].ValidFrom != 120 || full.Edges[1].ValidUntil != nil ||
		full.Edges[2].ValidFrom != 0 || full.Edges[2].ValidUntil != nil {
		t.Fatalf("parallel history lost temporal payload: %+v", full.Edges)
	}
	for run := 0; run < 3; run++ {
		var cursor string
		ids := []string{}
		union := []PathEdge{}
		for page := 0; page < 3; page++ {
			got, err := w.PathBetween(ctx, PathQuery{From: "a", To: "c", Limit: 1, Cursor: cursor})
			if err != nil || len(got.Edges) != 1 {
				t.Fatalf("parallel page=%d result=%+v err=%v", page, got, err)
			}
			ids = append(ids, temporalPathIDs(got.Edges)...)
			union = append(union, got.Edges...)
			cursor = got.NextCursor
			if (page < 2) != (cursor != "") {
				t.Fatalf("unexpected continuation at page=%d: %+v", page, got)
			}
		}
		if !reflect.DeepEqual(union, full.Edges) {
			t.Fatalf("page payload union differs from complete paths: pages=%+v full=%+v", union, full.Edges)
		}
		if !reflect.DeepEqual(ids, []string{"episode-a", "episode-b", "tail"}) {
			t.Fatalf("unstable or duplicate parallel history pages: %v", ids)
		}
	}
	if _, err := w.PathBetween(ctx, PathQuery{From: "a", To: "c", Cursor: "%%%"}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	missing, err := w.PathBetween(ctx, PathQuery{From: "a", To: "missing"})
	if err != nil || len(missing.Edges) != 0 {
		t.Fatalf("missing endpoint path=%+v err=%v", missing, err)
	}
}

func TestTemporalPathDefaultAndCappedHops(t *testing.T) {
	w := newLiveWriter(t)
	edges := make([]map[string]any, 0, 9)
	for i := 0; i < 9; i++ {
		edges = append(edges, temporalPathEdge(fmt.Sprintf("edge-%d", i), fmt.Sprintf("node-%d", i), fmt.Sprintf("node-%d", i+1), int64(100), nil))
	}
	seedTemporalPath(t, w, edges)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tc := range []struct {
		target     string
		hops, want int
	}{
		{"node-4", 0, 4},
		{"node-5", 0, 0},
		{"node-4", -1, 4},
		{"node-8", 100, 8},
		{"node-9", 100, 0},
	} {
		got, err := w.PathBetween(ctx, PathQuery{From: "node-0", To: tc.target, MaxHops: tc.hops, AsOf: 150})
		if err != nil || len(got.Edges) != tc.want {
			t.Fatalf("target=%s hops=%d edges=%+v err=%v want=%d", tc.target, tc.hops, got.Edges, err, tc.want)
		}
	}
}
