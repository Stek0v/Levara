// Package community implements the Louvain algorithm for community detection
// in undirected weighted graphs, producing a multi-level dendrogram.
package community

import (
	"fmt"
	"sort"

	"github.com/google/uuid"
)

// --- Graph ---

// Graph is an undirected weighted graph for community detection.
// Internally index-based for cache locality; external IDs stored in nodeIDs.
type Graph struct {
	n       int               // node count
	nodeIDs []string          // external node ID per index
	idxOf   map[string]int    // external ID → internal index
	adj     [][]adjEntry      // adjacency lists (undirected: stored both ways)
	degree  []float64         // weighted degree k_i = sum of edge weights incident to i
	totalW  float64           // sum of ALL edge weights (each undirected edge counted once)
}

type adjEntry struct {
	target int
	weight float64
}

// NewGraph creates a graph from external node IDs.
func NewGraph(nodeIDs []string) *Graph {
	n := len(nodeIDs)
	g := &Graph{
		n:       n,
		nodeIDs: make([]string, n),
		idxOf:   make(map[string]int, n),
		adj:     make([][]adjEntry, n),
		degree:  make([]float64, n),
	}
	copy(g.nodeIDs, nodeIDs)
	for i, id := range nodeIDs {
		g.idxOf[id] = i
	}
	return g
}

// AddEdge adds an undirected weighted edge. If duplicate, weights are summed.
// Self-loops (src == dst) are ignored.
func (g *Graph) AddEdge(srcID, dstID string, weight float64) {
	si, ok1 := g.idxOf[srcID]
	di, ok2 := g.idxOf[dstID]
	if !ok1 || !ok2 || si == di { // skip unknown or self-loop
		return
	}
	// Check for existing edge and merge weight
	found := false
	for j := range g.adj[si] {
		if g.adj[si][j].target == di {
			g.adj[si][j].weight += weight
			found = true
			break
		}
	}
	if !found {
		g.adj[si] = append(g.adj[si], adjEntry{di, weight})
	}
	// Reverse direction
	found = false
	for j := range g.adj[di] {
		if g.adj[di][j].target == si {
			g.adj[di][j].weight += weight
			found = true
			break
		}
	}
	if !found {
		g.adj[di] = append(g.adj[di], adjEntry{si, weight})
	}
	g.degree[si] += weight
	g.degree[di] += weight
	g.totalW += weight
}

// NodeCount returns the number of nodes.
func (g *Graph) NodeCount() int { return g.n }

// --- Louvain algorithm ---

// Config holds parameters for the Louvain algorithm.
type Config struct {
	Resolution    float64 // γ: >1 = finer, <1 = coarser, default 1.0
	MinGain       float64 // minimum ΔQ per pass, default 1e-7
	MaxIterations int     // max Phase 1 passes per level, default 100
	MaxLevels     int     // max hierarchy depth, default 10
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		Resolution:    1.0,
		MinGain:       1e-7,
		MaxIterations: 100,
		MaxLevels:     10,
	}
}

// Community is a detected cluster of nodes.
type Community struct {
	ID             string   `json:"id"`
	Members        []string `json:"members"`         // external node IDs
	Level          int      `json:"level"`            // 0=finest, higher=coarser
	ParentID       string   `json:"parent_id"`        // community at level+1 (empty for top-level)
	InternalWeight float64  `json:"internal_weight"`  // sum of intra-community edges
	MemberCount    int      `json:"member_count"`
}

// Dendrogram is the hierarchical community structure.
type Dendrogram struct {
	Levels     [][]Community // Levels[0]=finest, Levels[N]=coarsest
	Modularity []float64     // modularity per level
	MaxLevel   int
	TotalNodes int
	Iterations int     // total Phase 1 passes across all levels
	Resolution float64 // γ used
}

// Louvain runs the full multi-level algorithm: Phase 1 (local moves) + Phase 2 (graph compression),
// repeated until no improvement. Returns a hierarchical dendrogram.
func Louvain(g *Graph, cfg Config) Dendrogram {
	if cfg.Resolution <= 0 {
		cfg.Resolution = 0.01 // clamp
	}
	if cfg.MinGain <= 0 {
		cfg.MinGain = 1e-7
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = 100
	}
	if cfg.MaxLevels <= 0 {
		cfg.MaxLevels = 10
	}

	result := Dendrogram{
		TotalNodes: g.n,
		Resolution: cfg.Resolution,
	}

	if g.n == 0 {
		return result
	}

	// Track which original nodes map to which current super-nodes at each level
	// membershipHistory[level] = partition mapping at that level
	var partitionHistory [][]int // partition per level (current graph node → community index)
	currentGraph := g

	for level := 0; level < cfg.MaxLevels; level++ {
		if currentGraph.n == 0 {
			break
		}

		// Phase 1: local moves
		partition := initPartition(currentGraph.n)
		partition, iters := phase1(currentGraph, partition, cfg)
		result.Iterations += iters

		Q := modularity(currentGraph, partition, cfg.Resolution)
		result.Modularity = append(result.Modularity, Q)

		// Count unique communities
		numComms := countUnique(partition)
		if numComms <= 1 || numComms == currentGraph.n {
			// All nodes in one community or all singleton → no further compression possible
			partitionHistory = append(partitionHistory, partition)
			break
		}

		partitionHistory = append(partitionHistory, partition)

		// Phase 2: graph compression
		superGraph := phase2(currentGraph, partition)
		if superGraph.n >= currentGraph.n {
			break // no compression achieved
		}
		currentGraph = superGraph
	}

	// Build dendrogram from partition history
	result.Levels = buildDendrogram(g, partitionHistory, cfg.Resolution)
	result.MaxLevel = len(result.Levels) - 1

	return result
}

// --- Phase 1: Local moves ---

// initPartition: each node in its own community.
func initPartition(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return p
}

// phase1 iteratively moves nodes to neighboring communities for modularity gain.
// Returns final partition and iteration count.
func phase1(g *Graph, partition []int, cfg Config) ([]int, int) {
	n := g.n
	m2 := g.totalW // total weight (each edge counted once in totalW)
	if m2 == 0 {
		return partition, 0
	}

	// Community statistics
	commSumTot := make([]float64, n) // sum of degree of nodes in community c
	commSumIn := make([]float64, n)  // sum of internal edge weights in community c
	for i := 0; i < n; i++ {
		commSumTot[partition[i]] += g.degree[i]
	}
	for i := 0; i < n; i++ {
		for _, e := range g.adj[i] {
			if partition[i] == partition[e.target] {
				commSumIn[partition[i]] += e.weight // counted twice (both directions)
			}
		}
	}
	// Internal weights are double-counted (a→b and b→a), normalize
	for i := range commSumIn {
		commSumIn[i] /= 2
	}

	iters := 0
	for iters < cfg.MaxIterations {
		improved := false
		iters++

		// Process nodes in sorted order for determinism
		for i := 0; i < n; i++ {
			oldComm := partition[i]

			// Collect neighboring communities and weights
			neighComms := make(map[int]float64) // community → sum of edge weights from i to that community
			for _, e := range g.adj[i] {
				neighComms[partition[e.target]] += e.weight
			}

			// Remove node i from its community
			commSumTot[oldComm] -= g.degree[i]
			commSumIn[oldComm] -= neighComms[oldComm]
			partition[i] = -1 // temporarily unassigned

			// Find best community
			bestComm := oldComm
			bestGain := 0.0

			for c, kvC := range neighComms {
				gain := deltaQ(kvC, commSumIn[c], commSumTot[c], g.degree[i], m2, cfg.Resolution)
				if gain > bestGain || (gain == bestGain && c < bestComm) {
					bestGain = gain
					bestComm = c
				}
			}
			// Also consider staying alone (gain = 0 vs moving to old community)
			if bestGain <= 0 {
				bestComm = oldComm
			}

			// Assign to best community
			partition[i] = bestComm
			commSumTot[bestComm] += g.degree[i]
			commSumIn[bestComm] += neighComms[bestComm]

			if bestComm != oldComm {
				improved = true
			}
		}

		if !improved {
			break
		}
	}

	return partition, iters
}

// deltaQ computes the modularity gain of moving a node into a target community.
// Formula: ΔQ = [k_v_c / m - γ * (Σ_tot * k_v) / (2 * m²)]
//
//	where k_v_c = sum of edge weights from node v to community c
//	      Σ_tot = sum of degrees in community c
//	      k_v = degree of node v
//	      m = total edge weight
func deltaQ(kvC, sumIn, sumTot, kv, m, resolution float64) float64 {
	if m == 0 {
		return 0
	}
	return kvC/m - resolution*(sumTot*kv)/(2*m*m)
}

// modularity computes Q for a given partition.
func modularity(g *Graph, partition []int, resolution float64) float64 {
	if g.totalW == 0 {
		return 0
	}
	m := g.totalW
	q := 0.0
	for i := 0; i < g.n; i++ {
		for _, e := range g.adj[i] {
			if partition[i] == partition[e.target] {
				q += e.weight - resolution*(g.degree[i]*g.degree[e.target])/(2*m)
			}
		}
	}
	return q / (2 * m)
}

// --- Phase 2: Graph compression ---

// phase2 creates a super-graph where each community becomes a super-node.
// Edge weights between super-nodes = sum of cross-community edges.
// Self-loops = 2 * internal weight (since adjEntry stores both directions).
func phase2(g *Graph, partition []int) *Graph {
	// Remap community indices to 0..K-1
	commMap := make(map[int]int)
	for _, c := range partition {
		if _, ok := commMap[c]; !ok {
			commMap[c] = len(commMap)
		}
	}
	k := len(commMap)

	superIDs := make([]string, k)
	for c, idx := range commMap {
		superIDs[idx] = fmt.Sprintf("super-%d", c)
	}

	sg := NewGraph(superIDs)

	// Aggregate edges
	edgeMap := make(map[[2]int]float64) // [super_src, super_dst] → weight
	for i := 0; i < g.n; i++ {
		si := commMap[partition[i]]
		for _, e := range g.adj[i] {
			di := commMap[partition[e.target]]
			if si <= di { // avoid double-counting
				key := [2]int{si, di}
				edgeMap[key] += e.weight
			}
		}
	}

	for key, w := range edgeMap {
		if key[0] == key[1] {
			continue // skip self-loops for now (they don't affect Louvain)
		}
		// AddEdge handles both directions
		sg.AddEdge(superIDs[key[0]], superIDs[key[1]], w/2) // /2 because we counted from both adj lists
	}

	return sg
}

// --- Build dendrogram ---

// buildDendrogram converts partition history into hierarchical Community structs.
func buildDendrogram(originalGraph *Graph, partitionHistory [][]int, resolution float64) [][]Community {
	if len(partitionHistory) == 0 {
		return nil
	}

	levels := make([][]Community, len(partitionHistory))

	for level, partition := range partitionHistory {
		// Group original nodes by community chain through all levels up to this one
		commMembers := make(map[int][]string)

		if level == 0 {
			// Level 0: partition directly maps original nodes to communities
			for nodeIdx, commIdx := range partition {
				if nodeIdx < originalGraph.n {
					commMembers[commIdx] = append(commMembers[commIdx], originalGraph.nodeIDs[nodeIdx])
				}
			}
		} else {
			// Level N: compose partitions — trace each original node through all levels
			for origIdx := 0; origIdx < originalGraph.n; origIdx++ {
				// Walk through partition history
				currentIdx := origIdx
				for l := 0; l <= level; l++ {
					if l < len(partitionHistory) && currentIdx < len(partitionHistory[l]) {
						currentIdx = partitionHistory[l][currentIdx]
					}
				}
				commMembers[currentIdx] = append(commMembers[currentIdx], originalGraph.nodeIDs[origIdx])
			}
		}

		// Build Community structs
		var communities []Community
		for _, members := range commMembers {
			if len(members) == 0 {
				continue
			}
			sort.Strings(members) // deterministic order

			communities = append(communities, Community{
				ID:          communityID(members, level),
				Members:     members,
				Level:       level,
				MemberCount: len(members),
			})
		}

		// Sort communities by member count descending for stable output
		sort.Slice(communities, func(i, j int) bool {
			if communities[i].MemberCount != communities[j].MemberCount {
				return communities[i].MemberCount > communities[j].MemberCount
			}
			return communities[i].ID < communities[j].ID
		})

		levels[level] = communities
	}

	// Set parent IDs: community at level L's parent = community at level L+1 that contains same members
	for l := 0; l < len(levels)-1; l++ {
		for i := range levels[l] {
			if len(levels[l][i].Members) == 0 {
				continue
			}
			firstMember := levels[l][i].Members[0]
			for _, parent := range levels[l+1] {
				for _, m := range parent.Members {
					if m == firstMember {
						levels[l][i].ParentID = parent.ID
						goto nextComm
					}
				}
			}
		nextComm:
		}
	}

	return levels
}

// communityID generates a deterministic UUID5 from sorted member IDs + level.
func communityID(members []string, level int) string {
	sorted := make([]string, len(members))
	copy(sorted, members)
	sort.Strings(sorted)
	name := fmt.Sprintf("community-L%d-%s", level, sorted)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

// countUnique returns the number of unique values in a slice.
func countUnique(s []int) int {
	m := make(map[int]bool)
	for _, v := range s {
		m[v] = true
	}
	return len(m)
}
