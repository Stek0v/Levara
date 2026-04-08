// pgupsert.go — Batch upsert graph nodes and edges to PostgreSQL.
// Replaces Python's asyncio.gather(upsert_nodes, upsert_edges) with a single
// Go transaction using ON CONFLICT DO UPDATE.
package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/stek0v/cognevra/pkg/graph"
)

// exclusiveRelationships are relationship types where a single source entity
// can have at most one valid target at any moment in time. When a new edge
// is inserted for such a relationship, all PRIOR edges with the same source +
// relationship_name (and a different target) are marked as superseded:
//   valid_until = now()
//   superseded_by = <new edge id>
// This implements temporal validity for the knowledge graph.
//
// Non-exclusive relations (knows, mentions, related_to) are NEVER auto-
// superseded — adding a second target is meaningful coexistence, not a
// replacement.
//
// Extending this list is a deliberate code change so domain-specific edges
// (e.g. "owns_repo") can be added with intent.
var exclusiveRelationships = map[string]bool{
	"assigned_to":   true,
	"role_is":       true,
	"status_is":     true,
	"located_in":    true,
	"lives_in":      true,
	"works_at":      true,
	"owns":          true,
	"reports_to":    true,
	"current_state": true,
	"is_a":          true, // type changes are exclusive
}

// IsExclusiveRelationship returns true when prior edges with the same
// source+relation should be auto-superseded on a new insert. The check is
// case-insensitive — LLMs frequently emit "ASSIGNED_TO" or "Assigned_to".
func IsExclusiveRelationship(rel string) bool {
	return exclusiveRelationships[strings.ToLower(rel)]
}

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
			`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $6, $6)
			 ON CONFLICT (id) DO UPDATE SET
				source_id = EXCLUDED.source_id,
				target_id = EXCLUDED.target_id,
				relationship_name = EXCLUDED.relationship_name,
				properties = EXCLUDED.properties,
				updated_at = EXCLUDED.updated_at`,
			edgeID, e.SourceID, e.TargetID, e.RelationshipName, string(props), now)
		if err != nil {
			return nodesWritten, edgesWritten, fmt.Errorf("upsert edge %s: %w", edgeID, err)
		}
		edgesWritten++

		// Auto-supersession for exclusive relationships:
		// when a single source can have only one current target, mark
		// prior valid edges (different target, same source+rel) as
		// superseded by this new edge.
		if IsExclusiveRelationship(e.RelationshipName) {
			_, err := tx.ExecContext(ctx,
				`UPDATE graph_edges
				 SET valid_until = $1, superseded_by = $2, updated_at = $3
				 WHERE source_id = $4
				   AND relationship_name = $5
				   AND id <> $6
				   AND (valid_until IS NULL)`,
				now, edgeID, now, e.SourceID, e.RelationshipName, edgeID)
			if err != nil {
				return nodesWritten, edgesWritten, fmt.Errorf("supersede edges for %s: %w", edgeID, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}

	return nodesWritten, edgesWritten, nil
}
