// Package graphrank provides graph-aware reranking for search results.
// Combines vector similarity scores with graph proximity (hop distance)
// to produce a hybrid ranking that considers structural relationships.
package graphrank

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ProximityConfig controls graph proximity scoring.
// Formula: combined = Alpha*vectorScore + Beta*graphProximity + Gamma*rerankScore
type ProximityConfig struct {
	Alpha       float64 // weight for vector score, default 0.6
	Beta        float64 // weight for graph proximity, default 0.2
	Gamma       float64 // weight for rerank score, default 0.2 (0 = no rerank influence)
	MaxHops     int     // max hops to search, default 2
	DecayFactor float64 // score decay per hop, default 0.5
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() ProximityConfig {
	return ProximityConfig{
		Alpha: 0.6, Beta: 0.2, Gamma: 0.2,
		MaxHops: 2, DecayFactor: 0.5,
	}
}

// ScoredResult mirrors pipeline.ScoredResult to avoid circular imports.
type ScoredResult struct {
	ID          string
	Score       float32
	RerankScore float64 // 0 = not reranked
	Metadata json.RawMessage
}

// GraphProximity computes the proximity score between query entities and result entities.
// Uses batch SQL queries for efficiency: 1 query for 1-hop, 1 query for 2-hop.
//
// Score = max over all (query_entity, result_entity) pairs of:
//
//	1.0 if same entity (0 hops)
//	decay^1 if 1-hop neighbor
//	decay^2 if 2-hop neighbor
//	0.0 if not reachable within maxHops
func GraphProximity(ctx context.Context, db *sql.DB, queryEntityIDs, resultEntityIDs []string, cfg ProximityConfig) float64 {
	if db == nil || len(queryEntityIDs) == 0 || len(resultEntityIDs) == 0 {
		return 0
	}

	resultSet := make(map[string]bool, len(resultEntityIDs))
	for _, id := range resultEntityIDs {
		resultSet[id] = true
	}

	// 0-hop: check if any query entity IS a result entity
	for _, qe := range queryEntityIDs {
		if resultSet[qe] {
			return 1.0
		}
	}

	// 1-hop: direct neighbors of query entities
	oneHopNeighbors := batchNeighbors(ctx, db, queryEntityIDs)
	for _, re := range resultEntityIDs {
		if oneHopNeighbors[re] {
			return cfg.DecayFactor // decay^1
		}
	}

	if cfg.MaxHops < 2 {
		return 0
	}

	// 2-hop: neighbors of 1-hop neighbors
	oneHopIDs := make([]string, 0, len(oneHopNeighbors))
	for id := range oneHopNeighbors {
		oneHopIDs = append(oneHopIDs, id)
	}
	if len(oneHopIDs) == 0 {
		return 0
	}
	// Cap 2-hop expansion to avoid explosion
	if len(oneHopIDs) > 50 {
		oneHopIDs = oneHopIDs[:50]
	}
	twoHopNeighbors := batchNeighbors(ctx, db, oneHopIDs)
	for _, re := range resultEntityIDs {
		if twoHopNeighbors[re] {
			return cfg.DecayFactor * cfg.DecayFactor // decay^2
		}
	}

	return 0
}

// batchNeighbors returns the set of all 1-hop neighbors for given node IDs.
// Single SQL query for efficiency.
func batchNeighbors(ctx context.Context, db *sql.DB, nodeIDs []string) map[string]bool {
	result := make(map[string]bool)
	if len(nodeIDs) == 0 {
		return result
	}

	placeholders := make([]string, len(nodeIDs))
	args := make([]any, len(nodeIDs))
	for i, id := range nodeIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	inClause := strings.Join(placeholders, ",")

	query := fmt.Sprintf(
		`SELECT DISTINCT target_id FROM graph_edges WHERE source_id IN (%s) AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)
		 UNION
		 SELECT DISTINCT source_id FROM graph_edges WHERE target_id IN (%s) AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)`,
		inClause, inClause)

	rows, err := db.QueryContext(ctx, query, append(args, args...)...)
	if err != nil {
		return result
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			result[id] = true
		}
	}
	return result
}

// RerankWithGraph reranks search results using combined vector + graph proximity score.
// For each result: extract entity IDs from metadata → compute graph proximity → blend scores.
//
// Returns results sorted by combined score descending.
// If db is nil or queryEntityIDs empty, returns results in original order.
func RerankWithGraph(ctx context.Context, db *sql.DB, queryEntityIDs []string, results []ScoredResult, cfg ProximityConfig) []ScoredResult {
	if db == nil || len(queryEntityIDs) == 0 || len(results) == 0 {
		return results
	}

	// Pre-compute 1-hop and 2-hop neighbor sets (batch, 2 SQL queries total)
	oneHop := batchNeighbors(ctx, db, queryEntityIDs)
	oneHopIDs := make([]string, 0, len(oneHop))
	for id := range oneHop {
		oneHopIDs = append(oneHopIDs, id)
	}
	if len(oneHopIDs) > 50 {
		oneHopIDs = oneHopIDs[:50]
	}
	twoHop := batchNeighbors(ctx, db, oneHopIDs)

	querySet := make(map[string]bool, len(queryEntityIDs))
	for _, id := range queryEntityIDs {
		querySet[id] = true
	}

	type scored struct {
		idx      int
		combined float64
	}
	entries := make([]scored, len(results))

	for i, r := range results {
		// Extract entity IDs from result metadata
		entityIDs := extractEntityIDs(r.Metadata)

		// Compute graph proximity (fast: set lookups, no SQL)
		proximity := 0.0
		for _, eid := range entityIDs {
			if querySet[eid] {
				proximity = 1.0
				break
			}
			if oneHop[eid] && cfg.DecayFactor > proximity {
				proximity = cfg.DecayFactor
			}
			if twoHop[eid] && cfg.DecayFactor*cfg.DecayFactor > proximity {
				proximity = cfg.DecayFactor * cfg.DecayFactor
			}
		}

		vectorScore := float64(r.Score)
		rerankScore := r.RerankScore
		combined := cfg.Alpha*vectorScore + cfg.Beta*proximity + cfg.Gamma*rerankScore
		entries[i] = scored{idx: i, combined: combined}
	}

	sort.Slice(entries, func(a, b int) bool {
		return entries[a].combined > entries[b].combined
	})

	out := make([]ScoredResult, len(results))
	for i, e := range entries {
		out[i] = results[e.idx]
	}
	return out
}

// extractEntityIDs pulls entity identifiers from result metadata.
// Tries "name" field (entity metadata) or constructs ID from document context.
func extractEntityIDs(metadata json.RawMessage) []string {
	if len(metadata) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(metadata, &m) != nil {
		return nil
	}
	var ids []string
	// Entity metadata has "name" field
	if name, ok := m["name"].(string); ok && name != "" {
		ids = append(ids, name)
	}
	// Chunk metadata may reference entities via document_id
	if docID, ok := m["document_id"].(string); ok && docID != "" {
		ids = append(ids, docID)
	}
	return ids
}
