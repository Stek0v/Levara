package consolidate

import (
	"sort"
	"testing"
)

func sortedIDs(c Cluster) []string {
	out := append([]string(nil), c.IDs...)
	sort.Strings(out)
	return out
}

func TestClusterComponents_GroupsByThreshold(t *testing.T) {
	edges := []SimEdge{
		{A: "a", B: "b", Score: 0.99}, // a-b tight
		{A: "b", B: "c", Score: 0.90}, // b-c above tau_low
		{A: "d", B: "e", Score: 0.80}, // below tau_low -> dropped
	}
	clusters := ClusterComponents(edges, 0.85)

	if len(clusters) != 1 {
		t.Fatalf("got %d clusters, want 1 (only a,b,c connected)", len(clusters))
	}
	got := sortedIDs(clusters[0])
	want := []string{"a", "b", "c"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("cluster members = %v, want %v", got, want)
	}
	if len(clusters[0].Edges) != 2 {
		t.Fatalf("cluster edges = %d, want 2", len(clusters[0].Edges))
	}
}

func TestClusterComponents_DropsSingletons(t *testing.T) {
	edges := []SimEdge{{A: "x", B: "y", Score: 0.50}} // below threshold
	clusters := ClusterComponents(edges, 0.85)
	if len(clusters) != 0 {
		t.Fatalf("got %d clusters, want 0 (no edge survives)", len(clusters))
	}
}
