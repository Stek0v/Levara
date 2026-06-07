package graphdb

import (
	"context"
	"os"
	"testing"
	"time"
)

// requireLiveNeo4j skips the test unless NEO4J_TEST_URL is set. Optional
// NEO4J_TEST_USER / NEO4J_TEST_PASSWORD / NEO4J_TEST_DATABASE override the
// defaults (neo4j/test/neo4j).
func requireLiveNeo4j(t *testing.T) (url, user, pass, db string) {
	t.Helper()
	url = os.Getenv("NEO4J_TEST_URL")
	if url == "" {
		t.Skip("NEO4J_TEST_URL not set; skipping live Neo4j integration test")
	}
	user = os.Getenv("NEO4J_TEST_USER")
	if user == "" {
		user = "neo4j"
	}
	pass = os.Getenv("NEO4J_TEST_PASSWORD")
	if pass == "" {
		pass = "test"
	}
	db = os.Getenv("NEO4J_TEST_DATABASE")
	if db == "" {
		db = "neo4j"
	}
	return
}

func newLiveWriter(t *testing.T) *Writer {
	t.Helper()
	url, user, pass, db := requireLiveNeo4j(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := NewWriter(ctx, url, user, pass, db)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() {
		_ = w.Close(context.Background())
	})
	if _, err := w.Query(ctx, "MATCH (n) DETACH DELETE n", nil); err != nil {
		t.Fatalf("clean db: %v", err)
	}
	if err := w.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	return w
}

// TestBatchWrite_Atomicity verifies that an error inside the BatchWrite
// transaction rolls back ALL prior statements — nodes inserted before the
// failure must not survive. This is the central invariant the Stage 3
// refactor is meant to guarantee.
func TestBatchWrite_Atomicity(t *testing.T) {
	w := newLiveWriter(t)
	ctx := context.Background()

	nodes := []NodeRecord{
		{ID: "n-atomic-1", Label: "Entity", Properties: map[string]any{"name": "A"}},
		{ID: "n-atomic-2", Label: "Entity", Properties: map[string]any{"name": "B"}},
	}
	// Edge points at a node that does not exist; runEdgeMerge MATCHes both
	// endpoints so the inner statement returns 0 affected rows. To force a
	// hard failure we corrupt the relationship name so Cypher rejects it.
	edges := []EdgeRecord{
		{SourceID: "n-atomic-1", TargetID: "n-atomic-2", RelationshipName: "INVALID-NAME-WITH-DASH"},
	}

	res := w.BatchWrite(ctx, nodes, edges)
	if len(res.Errors) == 0 {
		t.Fatalf("expected error from invalid relationship name, got none; result=%+v", res)
	}
	if res.NodesWritten != 0 || res.EdgesWritten != 0 {
		t.Errorf("partial-success leak: NodesWritten=%d EdgesWritten=%d, want 0/0",
			res.NodesWritten, res.EdgesWritten)
	}

	got, err := w.ReadFullGraph(ctx)
	if err != nil {
		t.Fatalf("ReadFullGraph: %v", err)
	}
	if len(got.Nodes) != 0 {
		t.Errorf("rollback failed: %d nodes survived an aborted transaction", len(got.Nodes))
	}
}

// TestBatchWrite_HappyPath sanity-checks that a normal write inserts the
// requested nodes and edges and is visible to ReadFullGraph.
func TestBatchWrite_HappyPath(t *testing.T) {
	w := newLiveWriter(t)
	ctx := context.Background()

	nodes := []NodeRecord{
		{ID: "n-happy-1", Label: "Entity", Properties: map[string]any{"name": "A"}},
		{ID: "n-happy-2", Label: "Entity", Properties: map[string]any{"name": "B"}},
	}
	edges := []EdgeRecord{
		{SourceID: "n-happy-1", TargetID: "n-happy-2", RelationshipName: "RELATES_TO",
			Properties: map[string]any{"weight": 0.5}},
	}

	res := w.BatchWrite(ctx, nodes, edges)
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if res.NodesWritten != 2 || res.EdgesWritten != 1 {
		t.Errorf("counts: got %d nodes / %d edges, want 2 / 1", res.NodesWritten, res.EdgesWritten)
	}

	got, err := w.ReadFullGraph(ctx)
	if err != nil {
		t.Fatalf("ReadFullGraph: %v", err)
	}
	if len(got.Nodes) != 2 || len(got.Edges) != 1 {
		t.Errorf("read-back: got %d nodes / %d edges, want 2 / 1", len(got.Nodes), len(got.Edges))
	}
}
