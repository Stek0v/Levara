// pgupsert.go — Batch upsert graph nodes and edges to PostgreSQL.
// Replaces Python's asyncio.gather(upsert_nodes, upsert_edges) with a single
// Go transaction using ON CONFLICT DO UPDATE.
package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/stek0v/cognevra/pkg/graph"
)

// UpsertGraphToPostgres writes deduped nodes and edges to PostgreSQL in a single transaction.
// Returns (nodesWritten, edgesWritten, error).
func UpsertGraphToPostgres(ctx context.Context, db *sql.DB, nodes []graph.DedupNode, edges []graph.DedupEdge) (int, int, error) {
	if db == nil || (len(nodes) == 0 && len(edges) == 0) {
		return 0, 0, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	nodesWritten := 0
	edgesWritten := 0

	// Batch upsert nodes
	for _, n := range nodes {
		props, _ := json.Marshal(map[string]string{
			"name": n.Name, "type": n.Type, "description": n.Description,
		})
		_, err := tx.ExecContext(ctx,
			`INSERT INTO graph_nodes (id, name, type, description, properties, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (id) DO UPDATE SET
				name = EXCLUDED.name,
				type = EXCLUDED.type,
				description = EXCLUDED.description,
				properties = EXCLUDED.properties,
				updated_at = EXCLUDED.updated_at`,
			n.ID, n.Name, n.Type, n.Description, string(props), now, now)
		if err != nil {
			return nodesWritten, edgesWritten, fmt.Errorf("upsert node %s: %w", n.ID, err)
		}
		nodesWritten++
	}

	// Batch upsert edges
	for _, e := range edges {
		edgeID := fmt.Sprintf("%s_%s_%s", e.SourceID, e.RelationshipName, e.TargetID)
		props, _ := json.Marshal(map[string]string{"edge_text": e.EdgeText})
		_, err := tx.ExecContext(ctx,
			`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT (id) DO UPDATE SET
				source_id = EXCLUDED.source_id,
				target_id = EXCLUDED.target_id,
				relationship_name = EXCLUDED.relationship_name,
				properties = EXCLUDED.properties,
				updated_at = EXCLUDED.updated_at`,
			edgeID, e.SourceID, e.TargetID, e.RelationshipName, string(props), now, now)
		if err != nil {
			return nodesWritten, edgesWritten, fmt.Errorf("upsert edge %s: %w", edgeID, err)
		}
		edgesWritten++
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}

	return nodesWritten, edgesWritten, nil
}
