package graph

import (
	"math"
	"strings"
	"testing"
)

func almostEqual(a, b float32, eps float32) bool {
	return float32(math.Abs(float64(a-b))) < eps
}

func buildTestGraph() *Graph {
	g := NewGraph(3.5)

	// Nodes
	g.AddNode("n1", "Levara", "Vector database", "Software", "")
	g.AddNode("n2", "HNSW", "Graph index algorithm", "Algorithm", "")
	g.AddNode("n3", "WAL", "Write-Ahead Log", "Component", "")
	g.AddNode("n4", "SIMD", "Single Instruction Multiple Data", "Technology", "")
	g.AddNode("n5", "LanceDB", "Rust vector DB", "Software", "")

	// Edges
	g.AddEdge("n1", "n2", "uses", "Levara uses HNSW for indexing", "etype_1")
	g.AddEdge("n1", "n3", "has", "Levara has WAL for durability", "etype_2")
	g.AddEdge("n1", "n4", "acceleratedBy", "Levara accelerated by SIMD", "etype_3")
	g.AddEdge("n5", "n2", "uses", "LanceDB uses different index", "etype_1")
	g.AddEdge("n1", "n5", "competesWith", "Levara competes with LanceDB", "etype_4")

	return g
}

func TestNewGraph(t *testing.T) {
	g := NewGraph(0)
	if g.DistancePenalty != DefaultDistancePenalty {
		t.Errorf("expected default penalty %f, got %f", DefaultDistancePenalty, g.DistancePenalty)
	}

	g = NewGraph(2.0)
	if g.DistancePenalty != 2.0 {
		t.Errorf("expected penalty 2.0, got %f", g.DistancePenalty)
	}
}

func TestAddNodeAndEdge(t *testing.T) {
	g := buildTestGraph()

	if len(g.Nodes) != 5 {
		t.Errorf("expected 5 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 5 {
		t.Errorf("expected 5 edges, got %d", len(g.Edges))
	}

	// All nodes should have default distance
	for id, node := range g.Nodes {
		if node.Distance != 3.5 {
			t.Errorf("node %s: expected distance 3.5, got %f", id, node.Distance)
		}
	}
}

func TestMapNodeDistances(t *testing.T) {
	g := buildTestGraph()

	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.1},
		{ID: "n2", Distance: 0.5},
		{ID: "n99", Distance: 0.01}, // non-existent, should be ignored
	})

	if !almostEqual(g.Nodes["n1"].Distance, 0.1, 0.001) {
		t.Errorf("n1 distance: expected 0.1, got %f", g.Nodes["n1"].Distance)
	}
	if !almostEqual(g.Nodes["n2"].Distance, 0.5, 0.001) {
		t.Errorf("n2 distance: expected 0.5, got %f", g.Nodes["n2"].Distance)
	}
	// n3 should keep penalty
	if !almostEqual(g.Nodes["n3"].Distance, 3.5, 0.001) {
		t.Errorf("n3 distance: expected 3.5 (penalty), got %f", g.Nodes["n3"].Distance)
	}
}

func TestMapNodeDistancesLowestWins(t *testing.T) {
	g := buildTestGraph()

	// First collection
	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.5},
	})
	// Second collection with lower distance
	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.2},
	})
	// Third collection with higher distance (should NOT overwrite)
	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.8},
	})

	if !almostEqual(g.Nodes["n1"].Distance, 0.2, 0.001) {
		t.Errorf("n1 distance: expected 0.2 (lowest wins), got %f", g.Nodes["n1"].Distance)
	}
}

func TestMapEdgeDistances(t *testing.T) {
	g := buildTestGraph()

	g.MapEdgeDistances([]DistanceEntry{
		{ID: "etype_1", Distance: 0.3}, // applies to both "uses" edges
		{ID: "etype_2", Distance: 0.7},
	})

	// Both edges with etype_1 should get 0.3
	usesCount := 0
	for _, e := range g.Edges {
		if e.EdgeTypeID == "etype_1" {
			if !almostEqual(e.Distance, 0.3, 0.001) {
				t.Errorf("edge etype_1: expected 0.3, got %f", e.Distance)
			}
			usesCount++
		}
	}
	if usesCount != 2 {
		t.Errorf("expected 2 edges with etype_1, got %d", usesCount)
	}
}

func TestSearchTopK(t *testing.T) {
	g := buildTestGraph()

	// Set specific distances
	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.1},
		{ID: "n2", Distance: 0.2},
		{ID: "n3", Distance: 0.3},
	})
	g.MapEdgeDistances([]DistanceEntry{
		{ID: "etype_1", Distance: 0.15},
		{ID: "etype_2", Distance: 0.25},
	})

	results := g.SearchTopK(3)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Best should be n1→n2 (uses): 0.1 + 0.15 + 0.2 = 0.45
	best := results[0]
	expectedScore := float32(0.1 + 0.15 + 0.2)
	if !almostEqual(best.Score, expectedScore, 0.001) {
		t.Errorf("best score: expected %f, got %f", expectedScore, best.Score)
	}
	if best.Node1.ID != "n1" || best.Node2.ID != "n2" {
		t.Errorf("best triplet: expected n1→n2, got %s→%s", best.Node1.ID, best.Node2.ID)
	}

	// Scores should be non-decreasing
	for i := 1; i < len(results); i++ {
		if results[i].Score < results[i-1].Score {
			t.Errorf("results not sorted: score[%d]=%f < score[%d]=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestSearchTopKMoreThanEdges(t *testing.T) {
	g := buildTestGraph()
	results := g.SearchTopK(100) // more than 5 edges
	if len(results) != 5 {
		t.Errorf("expected 5 results (all edges), got %d", len(results))
	}
}

func TestSearchTopKMissingNodes(t *testing.T) {
	g := NewGraph(3.5)
	g.AddNode("n1", "A", "", "", "")
	// Edge references non-existent n2
	g.AddEdge("n1", "n999", "rel", "", "e1")

	results := g.SearchTopK(5)
	if len(results) != 0 {
		t.Errorf("expected 0 results (missing node), got %d", len(results))
	}
}

func TestSearchTopKAllPenalty(t *testing.T) {
	g := buildTestGraph()
	// No distances mapped — all penalty
	results := g.SearchTopK(2)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// All scores should be 3*3.5 = 10.5
	for _, r := range results {
		if !almostEqual(r.Score, 10.5, 0.001) {
			t.Errorf("expected score 10.5, got %f", r.Score)
		}
	}
}

func TestFormatTriplets(t *testing.T) {
	g := buildTestGraph()
	g.MapNodeDistances([]DistanceEntry{
		{ID: "n1", Distance: 0.1},
		{ID: "n2", Distance: 0.2},
	})
	g.MapEdgeDistances([]DistanceEntry{
		{ID: "etype_1", Distance: 0.15},
	})

	results := g.SearchTopK(1)
	formatted := FormatTriplets(results)

	if !strings.Contains(formatted, "Node1:") {
		t.Error("formatted output missing Node1")
	}
	if !strings.Contains(formatted, "Edge:") {
		t.Error("formatted output missing Edge")
	}
	if !strings.Contains(formatted, "Node2:") {
		t.Error("formatted output missing Node2")
	}
	if !strings.Contains(formatted, "Levara") {
		t.Error("formatted output missing node name 'Levara'")
	}
	if !strings.Contains(formatted, "relationship_type") {
		t.Error("formatted output missing relationship_type")
	}
}

func TestFormatTripletsEmpty(t *testing.T) {
	formatted := FormatTriplets(nil)
	if formatted != "" {
		t.Errorf("expected empty string for nil triplets, got %q", formatted)
	}
}

func BenchmarkSearchTopK(b *testing.B) {
	// Build a larger graph for benchmarking
	g := NewGraph(3.5)
	for i := 0; i < 10000; i++ {
		g.AddNode(
			strings.Repeat("n", 1)+string(rune('0'+i%10))+strings.Repeat("x", i%5),
			"Name"+string(rune(i)),
			"Desc", "Type", "",
		)
	}
	// Use simple string IDs for nodes
	nodeIDs := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		nodeIDs = append(nodeIDs, id)
	}
	for i := 0; i < len(nodeIDs)-1; i++ {
		g.AddEdge(nodeIDs[i], nodeIDs[i+1], "rel", "edge text", "et"+nodeIDs[i])
	}

	// Map some distances
	entries := make([]DistanceEntry, len(nodeIDs)/2)
	for i := range entries {
		entries[i] = DistanceEntry{ID: nodeIDs[i*2], Distance: float32(i) * 0.01}
	}
	g.MapNodeDistances(entries)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.SearchTopK(10)
	}
}
