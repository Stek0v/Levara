package graphstore

import (
	"context"
	"os"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stek0v/levara/pkg/graphdb"
)

// Deletes all nodes only in the explicit disposable NEO4J_TEST_* database.
func TestNeo4jEpisodeAdapterIdentity(t *testing.T) {
	url := os.Getenv("NEO4J_TEST_URL")
	if url == "" {
		t.Skip("NEO4J_TEST_URL not set")
	}
	user, password, database := os.Getenv("NEO4J_TEST_USER"), os.Getenv("NEO4J_TEST_PASSWORD"), os.Getenv("NEO4J_TEST_DATABASE")
	if user == "" {
		user = "neo4j"
	}
	if password == "" {
		password = "test"
	}
	if database == "" {
		database = "neo4j"
	}
	ctx := context.Background()
	w, err := graphdb.NewWriter(ctx, url, user, password, database)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(ctx)
	_, err = graphdb.RunWriteTx(ctx, w, func(ctx context.Context, tx neo4j.ManagedTransaction) (struct{}, error) {
		res, err := tx.Run(ctx, "MATCH (n) DETACH DELETE n", nil)
		if err != nil {
			return struct{}{}, err
		}
		_, err = res.Consume(ctx)
		return struct{}{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewNeo4jGraphStore(w)
	nodes := []NodeRecord{{ID: "source", Name: "Source", Type: "Entity"}, {ID: "target", Name: "Target", Type: "Entity"}}
	result := adapter.WriteGraph(ctx, "alpha", nodes, []EdgeRecord{{ID: "adapter-id", SourceID: "source", TargetID: "target", RelationshipName: "owns"}})
	if len(result.Errors) > 0 || result.NodesWritten != 2 || result.EdgesWritten != 1 {
		t.Fatalf("adapter write: %+v", result)
	}
	graph, err := w.ReadFullGraph(ctx)
	if err != nil || len(graph.Nodes) != 2 || len(graph.Edges) != 1 || graph.Edges[0].Properties["id"] != "adapter-id" || graph.Edges[0].Properties["dataset_id"] != "alpha" {
		t.Fatalf("adapter identity lost: %+v %v", graph, err)
	}
	result = adapter.WriteGraph(ctx, "alpha", []NodeRecord{{ID: "conflict-node", Name: "No effects", Type: "Entity"}}, []EdgeRecord{{ID: "separate", SourceID: "source", TargetID: "target", RelationshipName: "owns", Properties: map[string]any{"id": "different"}}})
	if len(result.Errors) == 0 || result.NodesWritten != 0 || result.EdgesWritten != 0 {
		t.Fatalf("ID conflict accepted: %+v", result)
	}
	graph, err = w.ReadFullGraph(ctx)
	if err != nil || len(graph.Nodes) != 2 || len(graph.Edges) != 1 || graph.Edges[0].Properties["id"] != "adapter-id" {
		t.Fatalf("ID conflict changed graph: %+v %v", graph, err)
	}
}
