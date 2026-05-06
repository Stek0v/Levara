package aggregator

import "testing"

func TestAggregate_RanksByScore(t *testing.T) {
	edges := []ScoredEdge{
		{SourceID: "a", SourceName: "Alice", TargetID: "b", TargetName: "Bob", RelationshipName: "knows",
			SourceDist: 0.5, TargetDist: 0.3, EdgeDist: 0.2}, // score=1.0
		{SourceID: "c", SourceName: "Charlie", TargetID: "d", TargetName: "Dave", RelationshipName: "works",
			SourceDist: 0.1, TargetDist: 0.1, EdgeDist: 0.1}, // score=0.3
	}
	result := Aggregate(edges, 10)
	if len(result.RankedEdges) != 2 {
		t.Fatalf("expected 2, got %d", len(result.RankedEdges))
	}
	if result.RankedEdges[0].SourceName != "Charlie" {
		t.Errorf("expected Charlie first (lower score), got %s", result.RankedEdges[0].SourceName)
	}
}

func TestAggregate_TopKTruncation(t *testing.T) {
	var edges []ScoredEdge
	for i := 0; i < 20; i++ {
		edges = append(edges, ScoredEdge{
			SourceID: "s", SourceName: "S", TargetID: "t", TargetName: "T",
			RelationshipName: "r", SourceDist: float32(i), TargetDist: 0, EdgeDist: 0,
		})
	}
	result := Aggregate(edges, 5)
	if len(result.RankedEdges) != 5 {
		t.Errorf("expected 5, got %d", len(result.RankedEdges))
	}
}

func TestAggregate_DeduplicatesNodes(t *testing.T) {
	edges := []ScoredEdge{
		{SourceID: "a", SourceName: "Alice", TargetID: "b", TargetName: "Bob", RelationshipName: "knows"},
		{SourceID: "a", SourceName: "Alice", TargetID: "c", TargetName: "Charlie", RelationshipName: "knows"},
	}
	result := Aggregate(edges, 10)
	if result.UniqueNodeCount != 3 { // Alice, Bob, Charlie
		t.Errorf("expected 3 unique nodes, got %d", result.UniqueNodeCount)
	}
}

func TestAggregate_FormattedContext(t *testing.T) {
	edges := []ScoredEdge{
		{SourceID: "a", SourceName: "Alice", SourceText: "Alice text", TargetID: "b", TargetName: "Bob", TargetText: "Bob text", RelationshipName: "knows"},
	}
	result := Aggregate(edges, 10)
	if !contains(result.FormattedContext, "Nodes:") {
		t.Error("missing Nodes: header")
	}
	if !contains(result.FormattedContext, "Connections:") {
		t.Error("missing Connections: header")
	}
	if !contains(result.FormattedContext, "Alice --[knows]--> Bob") {
		t.Error("missing connection line")
	}
	if !contains(result.FormattedContext, "__node_content_start__") {
		t.Error("missing node content markers")
	}
}

func TestCreateTitle_Short(t *testing.T) {
	got := createTitle("Hello world")
	if got != "Hello world" {
		t.Errorf("short text should be unchanged: %s", got)
	}
}

func TestCreateTitle_Long(t *testing.T) {
	text := "The quick brown fox jumps over the lazy dog and runs through the forest quickly"
	got := createTitle(text)
	if !contains(got, "The quick brown fox jumps over the") {
		t.Errorf("should start with first 7 words: %s", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}
func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
