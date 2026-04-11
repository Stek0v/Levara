package community

import (
	"context"
	"database/sql"
	"log"
)

// IncrementalUpdate updates communities after new nodes/edges are added.
// If new nodes represent <20% of existing → Phase 1 only (fast).
// If >20% or no existing partition → full Louvain recompute.
//
// Returns the new dendrogram or nil if detection was skipped.
func IncrementalUpdate(ctx context.Context, db *sql.DB, g *Graph, cfg Config) (*Dendrogram, error) {
	if g.NodeCount() < 3 {
		return nil, nil
	}

	// Load existing partition from community_members
	existingPartition, existingNodeCount := loadPartition(ctx, db, g)

	newNodes := g.NodeCount() - existingNodeCount

	if existingNodeCount == 0 || newNodes < 0 {
		// No existing partition → full compute
		log.Printf("[community] no existing partition — full compute (%d nodes)", g.NodeCount())
		d := Louvain(g, cfg)
		return &d, nil
	}

	ratio := float64(newNodes) / float64(existingNodeCount)
	if ratio > 0.2 {
		// >20% new nodes → full recompute
		log.Printf("[community] %d new nodes (%.0f%% of %d) — full recompute", newNodes, ratio*100, existingNodeCount)
		d := Louvain(g, cfg)
		return &d, nil
	}

	// <20% new nodes → incremental: Phase 1 only on existing partition
	log.Printf("[community] %d new nodes (%.0f%% of %d) — incremental Phase 1", newNodes, ratio*100, existingNodeCount)

	// Run Phase 1 with existing partition as starting point
	partition, iters := phase1(g, existingPartition, cfg)
	Q := modularity(g, partition, cfg.Resolution)

	log.Printf("[community] incremental: Q=%.4f, %d iterations", Q, iters)

	// Build dendrogram from single-level partition
	levels := buildDendrogram(g, [][]int{partition}, cfg.Resolution)

	d := Dendrogram{
		Levels:     levels,
		Modularity: []float64{Q},
		MaxLevel:   0,
		TotalNodes: g.NodeCount(),
		Iterations: iters,
		Resolution: cfg.Resolution,
	}
	return &d, nil
}

// loadPartition loads the existing Level 0 partition from community_members table.
// Returns partition array (indexed by graph node index) and count of nodes that had communities.
func loadPartition(ctx context.Context, db *sql.DB, g *Graph) ([]int, int) {
	partition := initPartition(g.n) // default: each node in its own community

	if db == nil {
		return partition, 0
	}

	rows, err := db.QueryContext(ctx,
		"SELECT node_id, community_id FROM community_members WHERE level = 0")
	if err != nil {
		return partition, 0
	}
	defer rows.Close()

	// Map community_id → community index
	commIdx := make(map[string]int)
	existingCount := 0

	for rows.Next() {
		var nodeID, commID string
		if rows.Scan(&nodeID, &commID) != nil {
			continue
		}

		graphIdx, ok := g.idxOf[nodeID]
		if !ok {
			continue // node not in current graph
		}

		ci, ok := commIdx[commID]
		if !ok {
			ci = len(commIdx)
			commIdx[commID] = ci
		}
		partition[graphIdx] = ci
		existingCount++
	}

	return partition, existingCount
}
