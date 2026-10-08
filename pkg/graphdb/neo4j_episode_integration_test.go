package graphdb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func episodeTestWrite(t *testing.T, w *Writer, nodes []NodeRecord, edges ...EdgeRecord) {
	t.Helper()
	result := w.BatchWrite(context.Background(), nodes, edges)
	if len(result.Errors) != 0 || result.NodesWritten != len(nodes) || result.EdgesWritten != len(edges) {
		t.Fatalf("batch: %+v", result)
	}
}

func episodeTestNodes(ids ...string) []NodeRecord {
	nodes := make([]NodeRecord, 0, len(ids))
	for _, id := range ids {
		nodes = append(nodes, NodeRecord{ID: id, Label: "Entity", Properties: map[string]any{"name": id}})
	}
	return nodes
}

func episodeTestEdge(source, target, relation, dataset string, properties map[string]any) EdgeRecord {
	props := map[string]any{"dataset_id": dataset, "edge_text": target}
	for key, value := range properties {
		props[key] = value
	}
	return EdgeRecord{SourceID: source, TargetID: target, RelationshipName: relation, Properties: props}
}

func episodeTestRead(t *testing.T, w *Writer, source, dataset, relation string) []map[string]any {
	t.Helper()
	rows, err := w.Query(context.Background(), `MATCH (a:__Node__ {id:$source})-[r]->(b:__Node__)
	 WHERE coalesce(r.dataset_id,'')=$dataset AND toLower(type(r))=toLower($type)
	 RETURN b.id AS target,type(r) AS type,properties(r) AS props ORDER BY r.valid_from,r.id`,
		map[string]any{"source": source, "dataset": dataset, "type": relation})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func episodeTestFail(t *testing.T, w *Writer, nodes []NodeRecord, edges ...EdgeRecord) {
	t.Helper()
	got := w.BatchWrite(context.Background(), nodes, edges)
	if len(got.Errors) == 0 || got.NodesWritten != 0 || got.EdgesWritten != 0 {
		t.Fatalf("batch must fail atomically: %+v", got)
	}
}

func TestNeo4jEpisodeHistoryAndScope(t *testing.T) {
	w := newLiveWriter(t)
	episodeTestWrite(t, w, episodeTestNodes("source", "A", "B"))
	write := func(target, relation, dataset string, props map[string]any) {
		episodeTestWrite(t, w, nil, episodeTestEdge("source", target, relation, dataset, props))
	}
	write("A", "assigned_to", "alpha", nil)
	first := episodeTestRead(t, w, "source", "alpha", "assigned_to")[0]["props"].(map[string]any)
	write("B", "Assigned_To", "alpha", nil)
	closed := episodeTestRead(t, w, "source", "alpha", "assigned_to")
	write("A", "ASSIGNED_TO", "alpha", map[string]any{"edge_text": "new evidence"})
	before := episodeTestRead(t, w, "source", "alpha", "assigned_to")
	if len(before) != 3 {
		t.Fatalf("A-B-A lost history: %v", before)
	}
	var active map[string]any
	for _, row := range before {
		props := row["props"].(map[string]any)
		if props["valid_until"] == nil {
			active = props
			if row["target"] != "A" || props["id"] == first["id"] {
				t.Fatalf("return reused closed A: %v", row)
			}
		}
		for _, old := range closed {
			oldProps := old["props"].(map[string]any)
			if oldProps["id"] == first["id"] && props["id"] == first["id"] && !reflect.DeepEqual(old, row) {
				t.Fatalf("closed evidence changed: old=%v new=%v", old, row)
			}
		}
	}
	if active == nil {
		t.Fatal("missing current episode")
	}
	byID := map[string]map[string]any{}
	for _, row := range before {
		p := row["props"].(map[string]any)
		byID[p["id"].(string)] = p
	}
	for _, p := range byID {
		if p["valid_until"] != nil {
			successor := byID[p["superseded_by"].(string)]
			if successor == nil || p["valid_until"] != successor["valid_from"] {
				t.Fatalf("broken adjacent episode: %v", p)
			}
		}
	}
	write("A", "ASSIGNED_TO", "alpha", map[string]any{"edge_text": "new evidence"})
	if got := episodeTestRead(t, w, "source", "alpha", "assigned_to"); !reflect.DeepEqual(before, got) {
		t.Fatalf("retry changed episode: before=%v after=%v", before, got)
	}
	write("A", "assigned_to", "beta", nil)
	write("B", "assigned_to", "beta", nil)
	if !reflect.DeepEqual(before, episodeTestRead(t, w, "source", "alpha", "assigned_to")) {
		t.Fatal("foreign dataset superseded alpha")
	}
	write("A", "knows", "alpha", nil)
	write("B", "knows", "alpha", nil)
	coexist := episodeTestRead(t, w, "source", "alpha", "knows")
	write("A", "knows", "alpha", nil)
	if len(coexist) != 2 || !reflect.DeepEqual(coexist, episodeTestRead(t, w, "source", "alpha", "knows")) {
		t.Fatal("nonexclusive retry/coexistence changed")
	}
	graph, err := w.ReadFullGraph(context.Background())
	if err != nil || len(graph.Nodes) != 3 {
		t.Fatalf("internal identities leaked graph: %+v %v", graph, err)
	}
}

func TestNeo4jEpisodeExplicitEnvelopeAndRollback(t *testing.T) {
	w := newLiveWriter(t)
	episodeTestWrite(t, w, episodeTestNodes("s", "a", "b"))
	historical := episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"id": "history", "valid_from": "1970-01-01T00:01:40.9Z", "valid_until": json.Number("200")})
	episodeTestWrite(t, w, nil, historical)
	before := episodeTestRead(t, w, "s", "alpha", "owns")
	p := before[0]["props"].(map[string]any)
	if p["valid_from"] != int64(100) || p["valid_until"] != int64(200) || p["id"] != "history" {
		t.Fatalf("explicit envelope changed: %v", p)
	}
	episodeTestWrite(t, w, nil, historical)
	if !reflect.DeepEqual(before, episodeTestRead(t, w, "s", "alpha", "owns")) {
		t.Fatal("closed retry mutated history")
	}
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "b", "owns", "alpha", map[string]any{"id": "current", "valid_from": int64(300)}))
	current := episodeTestRead(t, w, "s", "alpha", "owns")
	episodeTestFail(t, w, episodeTestNodes("transient"),
		episodeTestEdge("s", "a", "knows", "alpha", nil),
		episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"id": "history", "valid_from": 100, "valid_until": 201}))
	if !reflect.DeepEqual(current, episodeTestRead(t, w, "s", "alpha", "owns")) {
		t.Fatal("failed batch altered history")
	}
	rows, err := w.Query(context.Background(), "MATCH (n:__Node__ {id:'transient'}) RETURN count(n) AS count", nil)
	if err != nil || rows[0]["count"] != int64(0) {
		t.Fatalf("rollback leaked node: %v %v", rows, err)
	}
	rows, err = w.Query(context.Background(), "MATCH (n:__EpisodeIdentity__ {id:'s_knows_a'}) RETURN count(n) AS count", nil)
	if err != nil || rows[0]["count"] != int64(0) {
		t.Fatalf("rollback leaked reservation: %v %v", rows, err)
	}
	for _, raw := range []json.Number{"9007199254740992.1", "1e-400"} {
		episodeTestFail(t, w, episodeTestNodes("invalid-number"), episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"valid_from": raw}))
		rows, err := w.Query(context.Background(), "MATCH (n:__Node__ {id:'invalid-number'}) RETURN count(n) AS count", nil)
		if err != nil || rows[0]["count"] != int64(0) || !reflect.DeepEqual(current, episodeTestRead(t, w, "s", "alpha", "owns")) {
			t.Fatal("invalid exact number changed batch state")
		}
	}
	episodeTestFail(t, w, nil, episodeTestEdge("s", "b", "owns", "alpha", map[string]any{"valid_from": 301}))
	episodeTestFail(t, w, nil, episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"valid_from": 299}))
	episodeTestFail(t, w, episodeTestNodes("missing-rollback"), episodeTestEdge("s", "absent", "owns", "alpha", nil))
	// A historical import must not close the current relationship.
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"id": "epoch-history", "valid_until": "90"}))
	for _, row := range episodeTestRead(t, w, "s", "alpha", "owns") {
		p := row["props"].(map[string]any)
		if p["id"] == "current" && p["valid_until"] != nil {
			t.Fatal("closed import superseded current")
		}
		if p["id"] == "epoch-history" && p["valid_from"] != int64(0) {
			t.Fatal("missing historical start is not epoch")
		}
	}
	// Reusing an occupied current hint allocates a fresh ID instead of reopening history.
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "a", "owns", "alpha", map[string]any{"id": "history", "valid_from": 400}))
	for _, row := range episodeTestRead(t, w, "s", "alpha", "owns") {
		p := row["props"].(map[string]any)
		if p["valid_until"] == nil && p["id"] == "history" {
			t.Fatal("occupied ID reopened")
		}
	}
}

func TestNeo4jEpisodeLegacyAndDynamicNames(t *testing.T) {
	w := newLiveWriter(t)
	episodeTestWrite(t, w, episodeTestNodes("s", "a", "b"))
	_, err := RunWriteTx(context.Background(), w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		_, err := episodeRows(ctx, tx, "MATCH (a:__Node__ {id:'s'}),(b:__Node__ {id:'a'}) CREATE (a)-[r:owns {dataset_id:'alpha',edge_text:'a',superseded_by:'stale'}]->(b) RETURN r", nil)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunWriteTx(context.Background(), w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		_, err := episodeRows(ctx, tx, "MATCH (a:__Node__ {id:'s'}),(b:__Node__ {id:'b'}) CREATE (a)-[r:knows {id:'legacy-closed',valid_until:90,edge_text:'b'}]->(b) RETURN r", nil)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	closedBefore := episodeTestRead(t, w, "s", "", "knows")
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "b", "knows", "", map[string]any{"id": "legacy-closed", "valid_until": 90}))
	if !reflect.DeepEqual(closedBefore, episodeTestRead(t, w, "s", "", "knows")) {
		t.Fatal("legacy closed retry normalized immutable properties")
	}
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "a", "owns", "alpha", nil))
	before := episodeTestRead(t, w, "s", "alpha", "owns")
	p := before[0]["props"].(map[string]any)
	if len(before) != 1 || p["id"] == nil || p["valid_from"] != int64(0) || p["superseded_by"] != nil {
		t.Fatalf("legacy active repair: %v", before)
	}
	episodeTestWrite(t, w, nil, episodeTestEdge("s", "a", "owns", "alpha", nil))
	if !reflect.DeepEqual(before, episodeTestRead(t, w, "s", "alpha", "owns")) {
		t.Fatal("legacy retry changed identity")
	}
	episodeTestWrite(t, w, []NodeRecord{{ID: "dynamic", Label: "label-with space"}}, episodeTestEdge("dynamic", "a", "relation-with space", "alpha", nil))
	for _, node := range []NodeRecord{{ID: "reserved", Label: "__EpisodeIdentity__"}} {
		episodeTestFail(t, w, []NodeRecord{node}, episodeTestEdge("s", "b", "owns", "alpha", nil))
	}
	rows, err := w.Query(context.Background(), "MATCH (n:__Node__ {id:'reserved'}) RETURN count(n) AS count", nil)
	if err != nil || rows[0]["count"] != int64(0) {
		t.Fatal("reserved label failure leaked node")
	}
}

func TestNeo4jEpisodeTemporalInputs(t *testing.T) {
	for _, value := range []any{int(12), int64(12), uint64(12), float64(12), json.Number("12"), json.Number("12.0"), json.Number("1.2e1"), "12", "1970-01-01T00:00:12.9Z"} {
		got, present, err := episodeSecond(value)
		if err != nil || !present || got != 12 {
			t.Fatalf("supported timestamp %v: %d %v %v", value, got, present, err)
		}
	}
	for _, value := range []any{"bad", "", 1.5, math.NaN(), math.Inf(1), uint64(math.MaxUint64), float64(9223372036854775808.0), map[string]any{"x": 1}, []any{1}, true, json.Number("1.2"), json.Number("9007199254740992.1"), json.Number("1e-400"), json.Number("9223372036854775808.0"), json.Number("-9223372036854775809.0"), json.Number("1e-999999999"), json.Number("1e999999999")} {
		if _, _, err := episodeSecond(value); err == nil {
			t.Fatalf("invalid timestamp accepted: %v", value)
		}
	}
	for raw, want := range map[string]int64{"9007199254740993.0": 9007199254740993, "9223372036854775807.0": math.MaxInt64, "-9223372036854775808.0": math.MinInt64, "0e999999999": 0} {
		got, present, err := episodeSecond(json.Number(raw))
		if err != nil || !present || got != want {
			t.Fatalf("exact number %s: %d %v %v", raw, got, present, err)
		}
	}
	for _, props := range []map[string]any{{"valid_from": 2, "valid_until": 1}, {"id": map[string]any{"x": 1}}, {"id": []any{"x"}}, {"valid_from": map[string]any{"x": 1}}, {"valid_until": []any{1}}} {
		if _, err := prepareEpisodeBatch([]EdgeRecord{episodeTestEdge("s", "a", "owns", "alpha", props)}); err == nil {
			t.Fatalf("raw metadata bypass: %v", props)
		}
	}
}

func TestNeo4jEpisodeConcurrentBatches(t *testing.T) {
	for _, scenario := range []string{"edge-only", "crossed", "current-same-id", "closed-same-id"} {
		t.Run(scenario, func(t *testing.T) {
			w := newLiveWriter(t)
			episodeTestWrite(t, w, episodeTestNodes("s", "a", "b", "other", "c", "d"))
			start := make(chan struct{})
			results := make(chan BatchWriteResult, 2)
			for i := 0; i < 2; i++ {
				go func(i int) {
					<-start
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					var nodes []NodeRecord
					source, target := "s", []string{"a", "b"}[i]
					props := map[string]any{}
					if scenario == "crossed" {
						source, target = []string{"s", "a"}[i], []string{"a", "s"}[i]
						nodes = episodeTestNodes(target)
					}
					if strings.Contains(scenario, "same-id") {
						source, target = []string{"s", "other"}[i], []string{"a", "c"}[i]
						props["id"] = "shared-hint"
					}
					if scenario == "closed-same-id" {
						props["valid_from"] = 100
						props["valid_until"] = 200
					}
					results <- w.BatchWrite(ctx, nodes, []EdgeRecord{episodeTestEdge(source, target, "owns", "alpha", props)})
				}(i)
			}
			close(start)
			failures := 0
			for i := 0; i < 2; i++ {
				result := <-results
				if len(result.Errors) > 0 {
					failures++
					if result.NodesWritten != 0 || result.EdgesWritten != 0 {
						t.Fatalf("failed competitor partial: %+v", result)
					}
				}
			}
			if scenario == "closed-same-id" {
				if failures != 1 {
					t.Fatalf("closed conflicting IDs failures=%d want1", failures)
				}
			} else if failures != 0 {
				t.Fatalf("concurrent %s failures=%d", scenario, failures)
			}
			rows, err := w.Query(context.Background(), "MATCH ()-[r]->() RETURN r.id AS id, count(r) AS count", nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row["count"] != int64(1) {
					t.Fatalf("duplicate global episode identity: %v", rows)
				}
			}
			if scenario == "edge-only" {
				rows := episodeTestRead(t, w, "s", "alpha", "owns")
				active := 0
				for _, row := range rows {
					if row["props"].(map[string]any)["valid_until"] == nil {
						active++
					}
				}
				if len(rows) != 2 || active != 1 {
					t.Fatalf("concurrent current episodes: %v", rows)
				}
			}
		})
	}
}

func TestNeo4jEpisodeCanceledReadinessCanRetry(t *testing.T) {
	w := newLiveWriter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.ensureEpisodeIdentitySchema(ctx); err == nil || w.episodeSchemaReady {
		t.Fatal("canceled readiness accepted or poisoned state")
	}
	episodeTestWrite(t, w, episodeTestNodes("s", "a"), episodeTestEdge("s", "a", "owns", "alpha", nil))
	if !w.episodeSchemaReady {
		t.Fatal("readiness did not recover")
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	result := w.BatchWrite(ctx, episodeTestNodes("canceled"), []EdgeRecord{episodeTestEdge("s", "a", "owns", "alpha", nil)})
	if len(result.Errors) == 0 || result.NodesWritten != 0 || result.EdgesWritten != 0 {
		t.Fatalf("canceled batch accepted: %+v", result)
	}
}

func TestNeo4jEpisodeBootstrapWithoutEnsureSchema(t *testing.T) {
	w := newLiveWriter(t)
	rows, err := w.Query(context.Background(), "SHOW CONSTRAINTS YIELD name,labelsOrTypes WHERE '__Node__' IN labelsOrTypes OR '__EpisodeIdentity__' IN labelsOrTypes RETURN name", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		name := strings.ReplaceAll(row["name"].(string), "`", "``")
		_, err := RunWriteTx(context.Background(), w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
			res, err := tx.Run(ctx, fmt.Sprintf("DROP CONSTRAINT `%s`", name), nil)
			if err != nil {
				return struct{}{}, err
			}
			_, err = res.Consume(ctx)
			return struct{}{}, err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	url, user, password, database := requireLiveNeo4j(t)
	fresh, err := NewWriter(context.Background(), url, user, password, database)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close(context.Background())
	var wg sync.WaitGroup
	results := make(chan BatchWriteResult, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- fresh.BatchWrite(context.Background(), episodeTestNodes("shared", "target"), []EdgeRecord{episodeTestEdge("shared", "target", "owns", "alpha", nil)})
		}(i)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if len(result.Errors) > 0 {
			t.Fatalf("lazy bootstrap competitor: %+v", result)
		}
	}
	rows, err = fresh.Query(context.Background(), "MATCH (n:__Node__ {id:'shared'}) RETURN count(n) AS count", nil)
	if err != nil || rows[0]["count"] != int64(1) {
		t.Fatalf("missing base uniqueness after lazy bootstrap: %v %v", rows, err)
	}
}
