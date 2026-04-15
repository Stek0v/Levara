package community

import (
	"testing"
	"time"
	"fmt"
	"math/rand"
)

func TestLouvain_EmptyGraph(t *testing.T) {
	g := NewGraph(nil)
	d := Louvain(g, DefaultConfig())
	if len(d.Levels) != 0 {
		t.Errorf("Empty graph: expected 0 levels, got %d", len(d.Levels))
	}
	if d.TotalNodes != 0 {
		t.Errorf("Expected 0 total nodes, got %d", d.TotalNodes)
	}
}

func TestLouvain_SingleNode(t *testing.T) {
	g := NewGraph([]string{"A"})
	d := Louvain(g, DefaultConfig())
	if d.TotalNodes != 1 {
		t.Errorf("Expected 1 node, got %d", d.TotalNodes)
	}
	if len(d.Levels) == 0 {
		t.Fatal("Expected at least 1 level")
	}
	if len(d.Levels[0]) != 1 {
		t.Errorf("Expected 1 community, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_TwoNodesOneEdge(t *testing.T) {
	g := NewGraph([]string{"A", "B"})
	g.AddEdge("A", "B", 1.0)
	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Two connected nodes should be in one community
	if len(d.Levels[0]) != 1 {
		t.Errorf("Expected 1 community for 2 connected nodes, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_TwoDisconnected(t *testing.T) {
	g := NewGraph([]string{"A", "B"})
	// No edges
	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Two disconnected nodes → 2 singletons
	if len(d.Levels[0]) != 2 {
		t.Errorf("Expected 2 singleton communities, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_Triangle(t *testing.T) {
	g := NewGraph([]string{"A", "B", "C"})
	g.AddEdge("A", "B", 1.0)
	g.AddEdge("B", "C", 1.0)
	g.AddEdge("A", "C", 1.0)
	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	if len(d.Levels[0]) != 1 {
		t.Errorf("Triangle: expected 1 community, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_Barbell(t *testing.T) {
	// Two 5-cliques connected by a single bridge edge
	nodes := make([]string, 10)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)

	// Clique 1: n0-n4
	for i := 0; i < 5; i++ {
		for j := i + 1; j < 5; j++ {
			g.AddEdge(nodes[i], nodes[j], 1.0)
		}
	}
	// Clique 2: n5-n9
	for i := 5; i < 10; i++ {
		for j := i + 1; j < 10; j++ {
			g.AddEdge(nodes[i], nodes[j], 1.0)
		}
	}
	// Bridge
	g.AddEdge("n4", "n5", 1.0)

	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Should detect 2 communities
	if len(d.Levels[0]) != 2 {
		t.Errorf("Barbell: expected 2 communities, got %d", len(d.Levels[0]))
		for _, c := range d.Levels[0] {
			t.Logf("  Community %s: %v", c.ID[:8], c.Members)
		}
	}
	if d.Modularity[0] < 0.3 {
		t.Errorf("Barbell modularity too low: %.4f (expected > 0.3)", d.Modularity[0])
	}
	t.Logf("Barbell: %d communities, Q=%.4f", len(d.Levels[0]), d.Modularity[0])
}

// Zachary karate club: 34 nodes, 78 edges. Reference modularity ~0.38-0.42.
func TestLouvain_ZacharyKarateClub(t *testing.T) {
	nodes := make([]string, 34)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("m%d", i+1)
	}
	g := NewGraph(nodes)

	// Zachary karate club edge list (1-indexed)
	edges := [][2]int{
		{1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}, {1, 7}, {1, 8}, {1, 9}, {1, 11}, {1, 12}, {1, 13}, {1, 14}, {1, 18}, {1, 20}, {1, 22}, {1, 32},
		{2, 3}, {2, 4}, {2, 8}, {2, 14}, {2, 18}, {2, 20}, {2, 22}, {2, 31},
		{3, 4}, {3, 8}, {3, 9}, {3, 10}, {3, 14}, {3, 28}, {3, 29}, {3, 33},
		{4, 8}, {4, 13}, {4, 14},
		{5, 7}, {5, 11},
		{6, 7}, {6, 11}, {6, 17},
		{7, 17},
		{9, 31}, {9, 33}, {9, 34},
		{10, 34},
		{14, 34},
		{15, 33}, {15, 34},
		{16, 33}, {16, 34},
		{19, 33}, {19, 34},
		{20, 34},
		{21, 33}, {21, 34},
		{23, 33}, {23, 34},
		{24, 26}, {24, 28}, {24, 30}, {24, 33}, {24, 34},
		{25, 26}, {25, 28}, {25, 32},
		{26, 32},
		{27, 30}, {27, 34},
		{28, 34},
		{29, 32}, {29, 34},
		{30, 33}, {30, 34},
		{31, 33}, {31, 34},
		{32, 33}, {32, 34},
		{33, 34},
	}

	for _, e := range edges {
		g.AddEdge(fmt.Sprintf("m%d", e[0]), fmt.Sprintf("m%d", e[1]), 1.0)
	}

	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels for karate club")
	}

	numComms := len(d.Levels[0])
	Q := d.Modularity[0]

	if numComms < 2 || numComms > 6 {
		t.Errorf("Karate club: expected 2-6 communities, got %d", numComms)
	}
	if Q < 0.30 {
		t.Errorf("Karate club modularity too low: %.4f (expected > 0.30)", Q)
	}

	t.Logf("Karate club: %d communities, Q=%.4f, %d iterations, %d levels",
		numComms, Q, d.Iterations, len(d.Levels))
	for _, c := range d.Levels[0] {
		t.Logf("  Community (%d members): %v", c.MemberCount, c.Members)
	}
}

func TestLouvain_Star(t *testing.T) {
	nodes := []string{"hub"}
	for i := 0; i < 10; i++ {
		nodes = append(nodes, fmt.Sprintf("spoke%d", i))
	}
	g := NewGraph(nodes)
	for i := 0; i < 10; i++ {
		g.AddEdge("hub", fmt.Sprintf("spoke%d", i), 1.0)
	}

	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Star graph: hub connects everyone → 1 community
	if len(d.Levels[0]) != 1 {
		t.Errorf("Star: expected 1 community, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_SelfLoopIgnored(t *testing.T) {
	g := NewGraph([]string{"A", "B"})
	g.AddEdge("A", "A", 1.0) // self-loop
	g.AddEdge("A", "B", 1.0)
	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Self-loop ignored, A-B connected → 1 community
	if len(d.Levels[0]) != 1 {
		t.Errorf("Expected 1 community, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_DuplicateEdgesMerged(t *testing.T) {
	g := NewGraph([]string{"A", "B"})
	g.AddEdge("A", "B", 1.0)
	g.AddEdge("A", "B", 1.0) // duplicate → weight merged to 2.0

	// Check merged weight
	if g.degree[0] != 2.0 {
		t.Errorf("Expected degree 2.0 after merge, got %.1f", g.degree[0])
	}
}

func TestLouvain_WeightedEdges(t *testing.T) {
	g := NewGraph([]string{"A", "B", "C"})
	g.AddEdge("A", "B", 10.0) // strong connection
	g.AddEdge("B", "C", 0.1)  // weak connection

	d := Louvain(g, DefaultConfig())
	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}

	// With strong A-B and weak B-C, might be 1 or 2 communities
	t.Logf("Weighted: %d communities", len(d.Levels[0]))
}

func TestLouvain_1K_Performance(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("perf thresholds calibrated for non-race non-short runs")
	}
	n := 1000
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)

	// Random graph: ~5 edges per node
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < n*5/2; i++ {
		a := rng.Intn(n)
		b := rng.Intn(n)
		if a != b {
			g.AddEdge(nodes[a], nodes[b], 1.0)
		}
	}

	start := time.Now()
	d := Louvain(g, DefaultConfig())
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("1K nodes took %v, expected < 50ms", elapsed)
	}
	t.Logf("1K nodes: %d communities, Q=%.4f, %v, %d iterations",
		len(d.Levels[0]), d.Modularity[0], elapsed, d.Iterations)
}

func TestLouvain_10K_Performance(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("perf thresholds calibrated for non-race non-short runs")
	}
	n := 10000
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)

	rng := rand.New(rand.NewSource(42))
	for i := 0; i < n*5/2; i++ {
		a := rng.Intn(n)
		b := rng.Intn(n)
		if a != b {
			g.AddEdge(nodes[a], nodes[b], 1.0)
		}
	}

	start := time.Now()
	d := Louvain(g, DefaultConfig())
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("10K nodes took %v, expected < 2s", elapsed)
	}
	t.Logf("10K nodes: %d communities, Q=%.4f, %v, %d iterations, %d levels",
		len(d.Levels[0]), d.Modularity[0], elapsed, d.Iterations, len(d.Levels))
}

func TestLouvain_Deterministic(t *testing.T) {
	nodes := make([]string, 20)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}

	makeGraph := func() *Graph {
		g := NewGraph(nodes)
		rng := rand.New(rand.NewSource(123))
		for i := 0; i < 40; i++ {
			a := rng.Intn(20)
			b := rng.Intn(20)
			g.AddEdge(nodes[a], nodes[b], 1.0)
		}
		return g
	}

	d1 := Louvain(makeGraph(), DefaultConfig())
	d2 := Louvain(makeGraph(), DefaultConfig())

	if len(d1.Levels[0]) != len(d2.Levels[0]) {
		t.Errorf("Determinism: %d vs %d communities", len(d1.Levels[0]), len(d2.Levels[0]))
	}
	for i := range d1.Levels[0] {
		if d1.Levels[0][i].ID != d2.Levels[0][i].ID {
			t.Errorf("Determinism: community %d ID differs: %s vs %s",
				i, d1.Levels[0][i].ID, d2.Levels[0][i].ID)
		}
	}
}

func TestLouvain_ResolutionHigher(t *testing.T) {
	// Barbell graph: γ=2.0 should give more communities than γ=1.0
	nodes := make([]string, 10)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	makeBarbell := func() *Graph {
		g := NewGraph(nodes)
		for i := 0; i < 5; i++ {
			for j := i + 1; j < 5; j++ {
				g.AddEdge(nodes[i], nodes[j], 1.0)
			}
		}
		for i := 5; i < 10; i++ {
			for j := i + 1; j < 10; j++ {
				g.AddEdge(nodes[i], nodes[j], 1.0)
			}
		}
		g.AddEdge("n4", "n5", 1.0)
		return g
	}

	cfg1 := DefaultConfig()
	cfg1.Resolution = 1.0
	d1 := Louvain(makeBarbell(), cfg1)

	cfg2 := DefaultConfig()
	cfg2.Resolution = 2.0
	d2 := Louvain(makeBarbell(), cfg2)

	n1 := len(d1.Levels[0])
	n2 := len(d2.Levels[0])

	if n2 < n1 {
		t.Errorf("Higher resolution should give >= communities: γ=1.0→%d, γ=2.0→%d", n1, n2)
	}
	t.Logf("Resolution: γ=1.0→%d communities, γ=2.0→%d communities", n1, n2)
}

func TestLouvain_ResolutionLower(t *testing.T) {
	nodes := make([]string, 10)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	makeBarbell := func() *Graph {
		g := NewGraph(nodes)
		for i := 0; i < 5; i++ {
			for j := i + 1; j < 5; j++ {
				g.AddEdge(nodes[i], nodes[j], 1.0)
			}
		}
		for i := 5; i < 10; i++ {
			for j := i + 1; j < 10; j++ {
				g.AddEdge(nodes[i], nodes[j], 1.0)
			}
		}
		g.AddEdge("n4", "n5", 1.0)
		return g
	}

	cfg1 := DefaultConfig()
	cfg1.Resolution = 1.0
	d1 := Louvain(makeBarbell(), cfg1)

	cfg05 := DefaultConfig()
	cfg05.Resolution = 0.5
	d05 := Louvain(makeBarbell(), cfg05)

	n1 := len(d1.Levels[0])
	n05 := len(d05.Levels[0])

	if n05 > n1 {
		t.Errorf("Lower resolution should give <= communities: γ=1.0→%d, γ=0.5→%d", n1, n05)
	}
	t.Logf("Resolution: γ=1.0→%d communities, γ=0.5→%d communities", n1, n05)
}

func TestLouvain_CompleteGraph(t *testing.T) {
	n := 10
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			g.AddEdge(nodes[i], nodes[j], 1.0)
		}
	}

	d := Louvain(g, DefaultConfig())
	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Complete graph: 1 community (all equally connected)
	if len(d.Levels[0]) != 1 {
		t.Errorf("Complete graph K10: expected 1 community, got %d", len(d.Levels[0]))
	}
}

func TestLouvain_ChainGraph(t *testing.T) {
	n := 7
	nodes := make([]string, n)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)
	for i := 0; i < n-1; i++ {
		g.AddEdge(nodes[i], nodes[i+1], 1.0)
	}

	d := Louvain(g, DefaultConfig())
	if len(d.Levels) == 0 {
		t.Fatal("Expected levels")
	}
	// Chain: 1-3 communities depending on resolution
	t.Logf("Chain(%d): %d communities, Q=%.4f", n, len(d.Levels[0]), d.Modularity[0])
}

func TestLouvain_MultiLevel(t *testing.T) {
	// 3 groups of 3 cliques: should produce multi-level dendrogram
	nodes := make([]string, 27)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("n%d", i)
	}
	g := NewGraph(nodes)

	// 3 groups of 3 cliques (3 nodes each)
	for group := 0; group < 3; group++ {
		for clique := 0; clique < 3; clique++ {
			base := group*9 + clique*3
			for i := 0; i < 3; i++ {
				for j := i + 1; j < 3; j++ {
					g.AddEdge(nodes[base+i], nodes[base+j], 2.0) // strong intra-clique
				}
			}
		}
		// Connect cliques within group
		for clique := 0; clique < 2; clique++ {
			a := group*9 + clique*3 + 2
			b := group*9 + (clique+1)*3
			g.AddEdge(nodes[a], nodes[b], 0.5) // weak inter-clique
		}
	}
	// Connect groups
	g.AddEdge(nodes[8], nodes[9], 0.1)   // group 0 → group 1
	g.AddEdge(nodes[17], nodes[18], 0.1) // group 1 → group 2

	d := Louvain(g, DefaultConfig())

	if len(d.Levels) < 1 {
		t.Fatal("Expected at least 1 level")
	}
	t.Logf("Multi-level: %d levels, modularity=%v", len(d.Levels), d.Modularity)
	for l, comms := range d.Levels {
		t.Logf("  Level %d: %d communities", l, len(comms))
	}
}

func TestModularity_Known(t *testing.T) {
	// Simple graph: triangle. All in one community → Q should be positive.
	g := NewGraph([]string{"A", "B", "C"})
	g.AddEdge("A", "B", 1.0)
	g.AddEdge("B", "C", 1.0)
	g.AddEdge("A", "C", 1.0)

	partAll := []int{0, 0, 0}     // all in community 0
	partSplit := []int{0, 0, 1}   // A,B together, C alone

	qAll := modularity(g, partAll, 1.0)
	qSplit := modularity(g, partSplit, 1.0)

	// All together should have higher Q for a triangle
	if qAll < qSplit {
		t.Errorf("Triangle: all-together Q=%.4f < split Q=%.4f", qAll, qSplit)
	}
	t.Logf("Triangle Q: all=%.4f, split=%.4f", qAll, qSplit)
}
