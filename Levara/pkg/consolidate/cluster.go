package consolidate

// ClusterComponents keeps edges with Score >= tauLow and returns the connected
// components (size >= 2) as clusters, each carrying its surviving internal edges.
func ClusterComponents(edges []SimEdge, tauLow float64) []Cluster {
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[ra] = rb
		}
	}

	var kept []SimEdge
	for _, e := range edges {
		if e.Score < tauLow {
			continue
		}
		union(e.A, e.B)
		kept = append(kept, e)
	}

	idsByRoot := map[string][]string{}
	seen := map[string]bool{}
	for _, e := range kept {
		for _, id := range []string{e.A, e.B} {
			if !seen[id] {
				seen[id] = true
				r := find(id)
				idsByRoot[r] = append(idsByRoot[r], id)
			}
		}
	}
	edgesByRoot := map[string][]SimEdge{}
	for _, e := range kept {
		r := find(e.A)
		edgesByRoot[r] = append(edgesByRoot[r], e)
	}

	var clusters []Cluster
	for root, ids := range idsByRoot {
		if len(ids) < 2 {
			continue
		}
		clusters = append(clusters, Cluster{IDs: ids, Edges: edgesByRoot[root]})
	}
	return clusters
}
