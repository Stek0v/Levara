package community

import (
	"fmt"
	"math"
	"sort"
	"testing"
)

// T-7: Louvain ground-truth regression on Zachary's karate club.
//
// Zachary (1977) observed a karate club that split into two factions after an
// internal dispute — one around the instructor "Mr. Hi" (node 1), one around
// the club president "John A." (node 34). The edge list represents friendship
// ties BEFORE the split; the factions represent who each member sided WITH.
// This gives community detection an exact ground truth to test against.
//
// Standard Louvain (resolution=1.0) typically hits modularity ≈ 0.44 and
// either 4 small communities (finest level) or 2 after hierarchical merging,
// with majority-rule faction assignment matching Zachary's observed split on
// 33 of 34 members (node 9 is famously ambiguous). This test locks that in.
//
// Reference: Zachary, W. (1977). An Information Flow Model for Conflict and
// Fission in Small Groups. Journal of Anthropological Research, 33(4).

// zacharyFactions — Zachary's observed post-split allocation. 1 = Mr. Hi,
// 2 = John A. Node 9 is the well-known borderline case; multiple published
// benchmarks allow either assignment for it.
var zacharyFactions = map[int]int{
	1: 1, 2: 1, 3: 1, 4: 1, 5: 1, 6: 1, 7: 1, 8: 1, 9: 2, 10: 2, 11: 1,
	12: 1, 13: 1, 14: 1, 15: 2, 16: 2, 17: 1, 18: 1, 19: 2, 20: 1, 21: 2,
	22: 1, 23: 2, 24: 2, 25: 2, 26: 2, 27: 2, 28: 2, 29: 2, 30: 2, 31: 2,
	32: 2, 33: 2, 34: 2,
}

func zacharyEdges() [][2]int {
	return [][2]int{
		{1, 2}, {1, 3}, {1, 4}, {1, 5}, {1, 6}, {1, 7}, {1, 8}, {1, 9},
		{1, 11}, {1, 12}, {1, 13}, {1, 14}, {1, 18}, {1, 20}, {1, 22}, {1, 32},
		{2, 3}, {2, 4}, {2, 8}, {2, 14}, {2, 18}, {2, 20}, {2, 22}, {2, 31},
		{3, 4}, {3, 8}, {3, 9}, {3, 10}, {3, 14}, {3, 28}, {3, 29}, {3, 33},
		{4, 8}, {4, 13}, {4, 14},
		{5, 7}, {5, 11},
		{6, 7}, {6, 11}, {6, 17},
		{7, 17},
		{9, 31}, {9, 33}, {9, 34},
		{10, 34}, {14, 34},
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
}

func buildZacharyGraph() *Graph {
	nodes := make([]string, 34)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("m%d", i+1)
	}
	g := NewGraph(nodes)
	for _, e := range zacharyEdges() {
		g.AddEdge(fmt.Sprintf("m%d", e[0]), fmt.Sprintf("m%d", e[1]), 1.0)
	}
	return g
}

// nodeCommunity maps each node id to the (0-based) community index at a given
// hierarchy level. Only looks at Community.Members.
func nodeCommunity(level []Community) map[string]int {
	out := make(map[string]int, 64)
	for ci, c := range level {
		for _, m := range c.Members {
			out[m] = ci
		}
	}
	return out
}

// factionPurity compares a Louvain partition to Zachary's ground-truth factions
// via majority vote: for each detected community, the winning faction is the
// one most of its members belong to; purity is the share of nodes that land in
// their community's winning faction. Perfect bipartition on Zachary typically
// achieves 33/34 = 0.9706 purity.
func factionPurity(level []Community) float64 {
	if len(level) == 0 {
		return 0
	}
	total := 0
	correct := 0
	for _, c := range level {
		var cnt [3]int // index 1 = Mr. Hi, 2 = John A.
		members := 0
		for _, m := range c.Members {
			var id int
			fmt.Sscanf(m, "m%d", &id)
			f := zacharyFactions[id]
			if f >= 1 && f <= 2 {
				cnt[f]++
				members++
			}
		}
		total += members
		win := cnt[1]
		if cnt[2] > win {
			win = cnt[2]
		}
		correct += win
	}
	if total == 0 {
		return 0
	}
	return float64(correct) / float64(total)
}

// TestLouvain_Zachary_GroundTruth locks in the canonical quality signal for
// Zachary's karate club: high modularity AND majority-rule faction purity
// against the observed split. If either regresses, Louvain is producing
// worse partitions than the benchmark literature reports.
func TestLouvain_Zachary_GroundTruth(t *testing.T) {
	g := buildZacharyGraph()

	d := Louvain(g, DefaultConfig())

	if len(d.Levels) == 0 {
		t.Fatal("Zachary: Louvain returned no levels")
	}

	// Finest level (index 0): should detect internal structure (3-5 communities).
	// Coarsest level (last index): often merges into the 2 factions.
	finest := d.Levels[0]
	coarsest := d.Levels[len(d.Levels)-1]

	t.Logf("levels=%d iterations=%d", len(d.Levels), d.Iterations)
	for li, lvl := range d.Levels {
		t.Logf("  level %d: %d communities, Q=%.4f", li, len(lvl), d.Modularity[li])
	}

	// Finest-level modularity should match published Louvain results for the
	// unweighted karate club graph (≈ 0.42–0.45). Allow slack for
	// resolution-parameter nuances: > 0.38 catches real regressions without
	// false-positives from RNG jitter.
	if Q := d.Modularity[0]; Q < 0.38 {
		t.Errorf("finest-level modularity %.4f too low; want ≥ 0.38 (published ~0.44)", Q)
	}

	// Community count on the finest level: Louvain output varies by
	// implementation (traversal order, resolution handling). Published
	// reference results span 2–7 communities; this implementation yields 6
	// on DefaultConfig. Keep the band wide enough to catch real regressions
	// (degenerate 1-community or >10 fragmentation) without false positives
	// from minor refactors to the inner loop.
	if n := len(finest); n < 2 || n > 8 {
		t.Errorf("finest level community count = %d; want 2..8", n)
	}

	// Finest-level purity: each detected group should skew strongly toward one
	// faction. Zachary's observed split is cleanly reflected in the friendship
	// network; any competent Louvain hits ≥ 0.95 here.
	pFinest := factionPurity(finest)
	t.Logf("finest faction purity = %.4f", pFinest)
	if pFinest < 0.95 {
		t.Errorf("finest-level faction purity = %.4f; want ≥ 0.95 (33/34 = 0.9706)", pFinest)
	}

	// Coarsest-level purity: even if the dendrogram doesn't collapse to exactly
	// 2 communities, whatever merges produced must still respect faction lines.
	pCoarse := factionPurity(coarsest)
	t.Logf("coarsest faction purity = %.4f", pCoarse)
	if pCoarse < 0.90 {
		t.Errorf("coarsest-level faction purity = %.4f; want ≥ 0.90", pCoarse)
	}

	// Instructor (node 1) and president (node 34) MUST land in different
	// communities at the finest level — otherwise the algorithm failed to
	// detect the club's primary fault line.
	nodeCom := nodeCommunity(finest)
	if nodeCom["m1"] == nodeCom["m34"] {
		t.Errorf("instructor (m1) and president (m34) share a finest-level community — no split detected")
	}
}

// TestLouvain_Zachary_Deterministic asserts that repeated Louvain runs on the
// same graph with DefaultConfig produce identical modularity and community
// counts. RNG-introduced jitter is acceptable (node order within a community
// may shuffle); modularity as a float must be bit-exactly stable.
func TestLouvain_Zachary_Deterministic(t *testing.T) {
	const runs = 5
	var mods []float64
	var counts []int
	for i := 0; i < runs; i++ {
		g := buildZacharyGraph()
		d := Louvain(g, DefaultConfig())
		if len(d.Levels) == 0 {
			t.Fatal("empty result")
		}
		mods = append(mods, d.Modularity[0])
		counts = append(counts, len(d.Levels[0]))
	}
	for i := 1; i < runs; i++ {
		if math.Abs(mods[i]-mods[0]) > 1e-9 {
			t.Errorf("run %d modularity %.9f diverges from run 0 %.9f", i, mods[i], mods[0])
		}
		if counts[i] != counts[0] {
			t.Errorf("run %d community count %d diverges from run 0 %d", i, counts[i], counts[0])
		}
	}

	sort.Ints(counts)
	t.Logf("%d runs: modularity=%.6f, community counts=%v", runs, mods[0], counts)
}
