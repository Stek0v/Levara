package community

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/stek0v/levara/pkg/sqlcompat"
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

	// Bind one UTC cutoff using placeholders understood by both SQL drivers.
	// SQLite stores timestamps as text in multiple formats; normalize them so
	// offsets and the space/T separator cannot change chronological ordering.
	cutoff := time.Now().UTC().AddDate(0, 0, -cfg.MaxAgeDays).Format(time.RFC3339Nano)
	agePredicate := "valid_until < $1"
	if sqlcompat.CurrentProvider() == sqlcompat.SQLite {
		agePredicate = "julianday(valid_until) < julianday($1)"
	}
	candidates := "superseded_by != '' AND valid_until IS NOT NULL AND " + agePredicate

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, fmt.Errorf("begin prune: %w", err)
	}
	defer tx.Rollback()
	var result PruneResult

	if cfg.DryRun {
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM graph_edges WHERE "+candidates, cutoff).Scan(&result.EdgesWouldDelete); err != nil {
			return PruneResult{}, fmt.Errorf("count superseded edges: %w", err)
		}
		// Preview the same orphan set as deletion, ignoring candidate edges.
		// IS NOT TRUE retains edges with NULL fields that are not candidates.
		if cfg.IncludeOrphans {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM graph_nodes gn
				WHERE NOT EXISTS (SELECT 1 FROM graph_edges ge
					WHERE (ge.source_id = gn.id OR ge.target_id = gn.id)
					AND (`+candidates+`) IS NOT TRUE)`, cutoff).Scan(&result.OrphanNodes); err != nil {
				return PruneResult{}, fmt.Errorf("count orphan nodes: %w", err)
			}
		}
	} else {
		res, err := tx.ExecContext(ctx, "DELETE FROM graph_edges WHERE "+candidates, cutoff)
		if err != nil {
			return PruneResult{}, fmt.Errorf("delete superseded edges: %w", err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return PruneResult{}, fmt.Errorf("count deleted edges: %w", err)
		}
		result.EdgesDeleted = int(affected)

		if cfg.IncludeOrphans {
			res, err := tx.ExecContext(ctx, `DELETE FROM graph_nodes
				WHERE NOT EXISTS (SELECT 1 FROM graph_edges ge WHERE ge.source_id = graph_nodes.id OR ge.target_id = graph_nodes.id)`)
			if err != nil {
				return PruneResult{}, fmt.Errorf("delete orphan nodes: %w", err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return PruneResult{}, fmt.Errorf("count deleted orphan nodes: %w", err)
			}
			result.OrphanNodes = int(affected)
		}

		res, err = tx.ExecContext(ctx, `DELETE FROM community_members
			WHERE node_id NOT IN (SELECT id FROM graph_nodes)`)
		if err != nil {
			return PruneResult{}, fmt.Errorf("clean community members: %w", err)
		}
		affected, err = res.RowsAffected()
		if err != nil {
			return PruneResult{}, fmt.Errorf("count cleaned community members: %w", err)
		}
		result.MembersCleanedUp = int(affected)
	}
	if err := tx.Commit(); err != nil {
		return PruneResult{}, fmt.Errorf("commit prune: %w", err)
	}
	if !cfg.DryRun {
		log.Printf("[prune] deleted %d edges, %d orphan nodes, %d stale community members",
			result.EdgesDeleted, result.OrphanNodes, result.MembersCleanedUp)
	}
	return result, nil
}
