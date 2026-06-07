package community

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// PruneConfig controls graph edge cleanup.
type PruneConfig struct {
	MaxAgeDays      int  // delete superseded edges older than N days (default 90)
	KeepSuperseding bool // keep the edge that superseded the deleted one (default true)
	DryRun          bool // report what would be deleted without deleting
	IncludeOrphans  bool // also delete orphaned nodes (no remaining edges)
}

// PruneResult reports cleanup statistics.
type PruneResult struct {
	EdgesDeleted     int `json:"edges_deleted"`
	EdgesWouldDelete int `json:"edges_would_delete"` // for dry-run
	OrphanNodes      int `json:"orphan_nodes"`
	MembersCleanedUp int `json:"members_cleaned_up"`
}

// PruneGraph deletes old superseded edges and optionally orphaned nodes.
// Also cleans up community_members referencing deleted nodes.
func PruneGraph(ctx context.Context, db *sql.DB, cfg PruneConfig) (PruneResult, error) {
	if db == nil {
		return PruneResult{}, nil
	}
	if cfg.MaxAgeDays <= 0 {
		cfg.MaxAgeDays = 90
	}

	var result PruneResult

	// Count candidates: superseded edges older than MaxAgeDays
	interval := fmt.Sprintf("-%d days", cfg.MaxAgeDays)
	countQuery := `SELECT COUNT(*) FROM graph_edges
		WHERE superseded_by != ''
		AND valid_until IS NOT NULL
		AND valid_until < datetime('now', ?)`

	var count int
	if err := db.QueryRowContext(ctx, countQuery, interval).Scan(&count); err != nil {
		// Might fail if datetime function not available — try simpler approach
		log.Printf("[prune] count query: %v", err)
		count = 0
	}

	if cfg.DryRun {
		result.EdgesWouldDelete = count
		// Count orphan nodes
		if cfg.IncludeOrphans {
			var orphans int
			db.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_nodes gn
				WHERE NOT EXISTS (SELECT 1 FROM graph_edges ge WHERE ge.source_id = gn.id OR ge.target_id = gn.id)`).Scan(&orphans)
			result.OrphanNodes = orphans
		}
		return result, nil
	}

	// Delete superseded edges
	deleteQuery := `DELETE FROM graph_edges
		WHERE superseded_by != ''
		AND valid_until IS NOT NULL
		AND valid_until < datetime('now', ?)`

	res, err := db.ExecContext(ctx, deleteQuery, interval)
	if err != nil {
		return result, fmt.Errorf("delete superseded edges: %w", err)
	}
	affected, _ := res.RowsAffected()
	result.EdgesDeleted = int(affected)

	// Delete orphaned nodes if requested
	if cfg.IncludeOrphans {
		orphanRes, err := db.ExecContext(ctx, `DELETE FROM graph_nodes
			WHERE NOT EXISTS (SELECT 1 FROM graph_edges ge WHERE ge.source_id = graph_nodes.id OR ge.target_id = graph_nodes.id)`)
		if err == nil {
			aff, _ := orphanRes.RowsAffected()
			result.OrphanNodes = int(aff)
		}
	}

	// Clean up community_members for nodes no longer in graph
	cleanRes, err := db.ExecContext(ctx, `DELETE FROM community_members
		WHERE node_id NOT IN (SELECT id FROM graph_nodes)`)
	if err == nil {
		aff, _ := cleanRes.RowsAffected()
		result.MembersCleanedUp = int(aff)
	}

	log.Printf("[prune] deleted %d edges, %d orphan nodes, %d stale community members",
		result.EdgesDeleted, result.OrphanNodes, result.MembersCleanedUp)

	return result, nil
}
