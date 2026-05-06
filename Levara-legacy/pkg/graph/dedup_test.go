package graph

import (
	"strings"
	"testing"
)

func TestDeduplicateNodes(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "A"},
		{ID: "n2", Name: "B"},
		{ID: "n1", Name: "A-duplicate"}, // dup
		{ID: "n3", Name: "C"},
		{ID: "n2", Name: "B-duplicate"}, // dup
	}

	result := Deduplicate(nodes, nil)

	if len(result.Nodes) != 3 {
		t.Fatalf("expected 3 unique nodes, got %d", len(result.Nodes))
	}
	// First occurrence wins
	if result.Nodes[0].Name != "A" {
		t.Errorf("expected first n1 to have Name 'A', got %q", result.Nodes[0].Name)
	}
	if result.Nodes[1].Name != "B" {
		t.Errorf("expected first n2 to have Name 'B', got %q", result.Nodes[1].Name)
	}
}

func TestDeduplicateEdges(t *testing.T) {
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n2", RelationshipName: "knows"},
		{SourceID: "n1", TargetID: "n2", RelationshipName: "knows"}, // dup
		{SourceID: "n1", TargetID: "n2", RelationshipName: "hired"}, // different rel → not dup
		{SourceID: "n2", TargetID: "n1", RelationshipName: "knows"}, // reversed → not dup
	}

	result := Deduplicate(nil, edges)

	if len(result.Edges) != 3 {
		t.Fatalf("expected 3 unique edges, got %d", len(result.Edges))
	}
}

func TestDeduplicateGeneratesTriplets(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "Cognevra", Text: "Vector database"},
		{ID: "n2", Name: "HNSW", Text: "Graph index"},
	}
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n2", RelationshipName: "uses", EdgeText: "uses for indexing"},
	}

	result := Deduplicate(nodes, edges)

	if len(result.Triplets) != 1 {
		t.Fatalf("expected 1 triplet, got %d", len(result.Triplets))
	}

	trip := result.Triplets[0]
	if trip.FromNodeID != "n1" || trip.ToNodeID != "n2" {
		t.Errorf("triplet nodes: expected n1→n2, got %s→%s", trip.FromNodeID, trip.ToNodeID)
	}
	if !strings.Contains(trip.Text, "Vector database") {
		t.Error("triplet text should contain source text")
	}
	if !strings.Contains(trip.Text, "uses for indexing") {
		t.Error("triplet text should contain edge text")
	}
	if !strings.Contains(trip.Text, "Graph index") {
		t.Error("triplet text should contain target text")
	}
	if !strings.Contains(trip.Text, "-›") {
		t.Error("triplet text should contain arrow separator")
	}
	if trip.ID == "" {
		t.Error("triplet ID should not be empty")
	}
}

func TestDeduplicateTripletUsesEdgeTextOverRelName(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "A"},
		{ID: "n2", Name: "B"},
	}
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n2", RelationshipName: "rel", EdgeText: "custom edge description"},
	}

	result := Deduplicate(nodes, edges)
	if !strings.Contains(result.Triplets[0].Text, "custom edge description") {
		t.Error("should use EdgeText when available")
	}
}

func TestDeduplicateTripletFallsBackToRelName(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "A"},
		{ID: "n2", Name: "B"},
	}
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n2", RelationshipName: "knows"},
	}

	result := Deduplicate(nodes, edges)
	if !strings.Contains(result.Triplets[0].Text, "knows") {
		t.Error("should fall back to RelationshipName when EdgeText is empty")
	}
}

func TestDeduplicateSkipsMissingNodes(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "A"},
	}
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n_missing", RelationshipName: "ref"},
	}

	result := Deduplicate(nodes, edges)
	if len(result.Triplets) != 0 {
		t.Errorf("expected 0 triplets for missing target node, got %d", len(result.Triplets))
	}
}

func TestDeduplicateSkipsEmptyRelationship(t *testing.T) {
	nodes := []DedupNode{
		{ID: "n1", Name: "A"},
		{ID: "n2", Name: "B"},
	}
	edges := []DedupEdge{
		{SourceID: "n1", TargetID: "n2", RelationshipName: ""},
	}

	result := Deduplicate(nodes, edges)
	if len(result.Triplets) != 0 {
		t.Errorf("expected 0 triplets for empty relationship, got %d", len(result.Triplets))
	}
}

func TestGenerateNodeID(t *testing.T) {
	// Must match Python: uuid5(NAMESPACE_OID, "test-doc-0")
	id := GenerateNodeID("test-doc-0")
	if id != "ff501e71-5b83-59e4-b2b7-77c20fcc0ab3" {
		t.Errorf("UUID5 mismatch: got %s, expected ff501e71-5b83-59e4-b2b7-77c20fcc0ab3", id)
	}

	// Normalization: spaces→underscores, lowercase, remove quotes
	id1 := GenerateNodeID("Hello World")
	id2 := GenerateNodeID("hello_world")
	if id1 != id2 {
		t.Errorf("normalization failed: %q != %q", id1, id2)
	}

	id3 := GenerateNodeID("it's")
	id4 := GenerateNodeID("its")
	if id3 != id4 {
		t.Errorf("quote removal failed: %q != %q", id3, id4)
	}
}

func TestDeduplicateTripletIDDeterministic(t *testing.T) {
	nodes := []DedupNode{
		{ID: "src-1", Name: "A"},
		{ID: "tgt-1", Name: "B"},
	}
	edges := []DedupEdge{
		{SourceID: "src-1", TargetID: "tgt-1", RelationshipName: "knows"},
	}

	r1 := Deduplicate(nodes, edges)
	r2 := Deduplicate(nodes, edges)

	if r1.Triplets[0].ID != r2.Triplets[0].ID {
		t.Error("triplet IDs should be deterministic")
	}
}

func BenchmarkDeduplicate(b *testing.B) {
	nodes := make([]DedupNode, 5000)
	for i := range nodes {
		nodes[i] = DedupNode{ID: string(rune('A' + i%26)) + string(rune(i)), Name: "N"}
	}
	// 50% duplicates
	for i := 2500; i < 5000; i++ {
		nodes[i].ID = nodes[i-2500].ID
	}

	edges := make([]DedupEdge, 10000)
	for i := range edges {
		edges[i] = DedupEdge{
			SourceID:         nodes[i%2500].ID,
			TargetID:         nodes[(i+1)%2500].ID,
			RelationshipName: "rel",
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Deduplicate(nodes, edges)
	}
}
