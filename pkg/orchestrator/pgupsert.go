// pgupsert.go — Batch upsert graph nodes and edges to PostgreSQL.
// Replaces Python's asyncio.gather(upsert_nodes, upsert_edges) with a single
// Go transaction using ON CONFLICT DO UPDATE.
package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/graph"
)

// exclusiveRelationships are relationship types where a single source entity
// can have at most one valid target at any moment in time. When a new edge
// is inserted for such a relationship, all PRIOR edges with the same source +
// relationship_name (and a different target) are marked as superseded:
//
//	valid_until = now()
//	superseded_by = <new edge id>
//
// This implements temporal validity for the knowledge graph.
//
// Non-exclusive relations (knows, mentions, related_to) are NEVER auto-
// superseded — adding a second target is meaningful coexistence, not a
// replacement.
//
// Extending this list is a deliberate code change so domain-specific edges
// (e.g. "owns_repo") can be added with intent.
// IsExclusiveRelationship preserves the exported native writer lookup.
func IsExclusiveRelationship(rel string) bool {
	return graph.IsExclusiveRelationship(rel)
}

// UpsertGraphToPostgres writes deduped nodes and edges to PostgreSQL in a single transaction.
// datasetID scopes the rows to a tenant/project; pass "" for legacy/global.
// Auto-supersession is also dataset-scoped — exclusive edges in dataset A
// never collide with the same source+relation in dataset B.
// Closed episodes retain their original IDs and intervals. An exclusive edge-only
// batch requires its source node to exist; otherwise the whole batch rolls back.
// Returns (nodesWritten, edgesWritten, error).
func UpsertGraphToPostgres(ctx context.Context, db *sql.DB, datasetID string, nodes []graph.DedupNode, edges []graph.DedupEdge) (int, int, error) {
	if db == nil || (len(nodes) == 0 && len(edges) == 0) {
		return 0, 0, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	nodesWritten := 0
	edgesWritten := 0

	// Node writes and exclusive source locks share one order, including edge-only
	// batches. Separate node-then-source ordering can deadlock crossed batches.
	nodesByID := map[string][]graph.DedupNode{}
	lockIDs := map[string]bool{}
	for _, node := range nodes {
		nodesByID[node.ID] = append(nodesByID[node.ID], node)
		lockIDs[node.ID] = true
	}
	for _, edge := range edges {
		if IsExclusiveRelationship(edge.RelationshipName) {
			lockIDs[edge.SourceID] = true
		}
	}
	ordered := make([]string, 0, len(lockIDs))
	for id := range lockIDs {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		if len(nodesByID[id]) == 0 {
			result, err := tx.ExecContext(ctx, "UPDATE graph_nodes SET id=id WHERE id=$1", id)
			if err != nil {
				return nodesWritten, edgesWritten, fmt.Errorf("lock exclusive source %s: %w", id, err)
			}
			n, err := result.RowsAffected()
			if err != nil {
				return nodesWritten, edgesWritten, err
			}
			if n != 1 {
				return nodesWritten, edgesWritten, fmt.Errorf("exclusive source %s missing", id)
			}
		}
		for _, node := range nodesByID[id] {
			props, _ := json.Marshal(map[string]any{
				"name": node.Name, "type": node.Type, "description": node.Description,
				"document_id": node.SourceDocID, "content_revision": node.ContentRevision, "generation": node.Generation, "collection": node.Collection,
			})
			now := time.Now().UTC()
			_, err := tx.ExecContext(ctx,
				`INSERT INTO graph_nodes (id, name, type, description, properties, dataset_id, created_at, updated_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
				 ON CONFLICT (id) DO UPDATE SET
					name = EXCLUDED.name,
					type = EXCLUDED.type,
					description = EXCLUDED.description,
					properties = EXCLUDED.properties,
					dataset_id = EXCLUDED.dataset_id,
					updated_at = EXCLUDED.updated_at`,
				node.ID, node.Name, node.Type, node.Description, string(props), datasetID, now, now)
			if err != nil {
				return nodesWritten, edgesWritten, fmt.Errorf("upsert node %s: %w", node.ID, err)
			}
			nodesWritten++
		}
	}
	// A transaction that waited for another transition must not backdate its
	// episode or close the predecessor before that predecessor began.
	// Normalize once before binding: PostgreSQL truncates raw nanoseconds while
	// SQLite may round them. Every new interval shares this exact microsecond.
	now := time.Now().UTC().Round(time.Microsecond)

	// Batch upsert edges
	for _, e := range edges {
		edgeID := ""
		relationMatch := "relationship_name = $3"
		if IsExclusiveRelationship(e.RelationshipName) {
			relationMatch = "LOWER(relationship_name) = LOWER($3)"
		}
		err := tx.QueryRowContext(ctx,
			"SELECT id FROM graph_edges WHERE source_id=$1 AND target_id=$2 AND "+relationMatch+" AND dataset_id=$4 AND valid_until IS NULL ORDER BY id LIMIT 1",
			e.SourceID, e.TargetID, e.RelationshipName, datasetID).Scan(&edgeID)
		if err != nil && err != sql.ErrNoRows {
			return nodesWritten, edgesWritten, fmt.Errorf("find active episode: %w", err)
		}
		if err == sql.ErrNoRows {
			edgeID = fmt.Sprintf("%s_%s_%s", e.SourceID, e.RelationshipName, e.TargetID)
			var occupied string
			err := tx.QueryRowContext(ctx, "SELECT id FROM graph_edges WHERE id=$1", edgeID).Scan(&occupied)
			if err != nil && err != sql.ErrNoRows {
				return nodesWritten, edgesWritten, fmt.Errorf("find legacy edge: %w", err)
			}
			if err == nil {
				edgeID += "_" + uuid.NewString()
			}
		}
		props, _ := json.Marshal(map[string]any{"edge_text": e.EdgeText, "document_id": e.SourceDocID, "content_revision": e.ContentRevision, "generation": e.Generation, "collection": e.Collection})
		query := `INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties, valid_from, dataset_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $6, $6)
			 ON CONFLICT (id) DO UPDATE SET
				relationship_name = EXCLUDED.relationship_name,
				properties = EXCLUDED.properties,
				updated_at = EXCLUDED.updated_at
			 WHERE graph_edges.valid_until IS NULL
			   AND graph_edges.source_id = EXCLUDED.source_id
			   AND graph_edges.target_id = EXCLUDED.target_id
			   AND graph_edges.dataset_id = EXCLUDED.dataset_id
			   AND LOWER(graph_edges.relationship_name) = LOWER(EXCLUDED.relationship_name)`
		result, err := tx.ExecContext(ctx, query, edgeID, e.SourceID, e.TargetID, e.RelationshipName, string(props), now, datasetID)
		if err != nil {
			return nodesWritten, edgesWritten, fmt.Errorf("upsert edge %s: %w", edgeID, err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nodesWritten, edgesWritten, err
		}
		if n == 0 {
			// A concurrent legacy/nonexclusive writer may have occupied this ID.
			// Never overwrite a foreign tuple or reopen a closed episode.
			edgeID += "_" + uuid.NewString()
			if _, err := tx.ExecContext(ctx, query, edgeID, e.SourceID, e.TargetID, e.RelationshipName, string(props), now, datasetID); err != nil {
				return nodesWritten, edgesWritten, fmt.Errorf("insert episode %s: %w", edgeID, err)
			}
		}

		edgesWritten++

		// Auto-supersession for exclusive relationships, scoped to dataset:
		// when a single source can have only one current target, mark
		// prior valid edges (different target, same source+rel, same
		// dataset) as superseded by this new edge.
		if IsExclusiveRelationship(e.RelationshipName) {
			// Case-insensitive relation match (finding M6, 2026-09-03
			// review): LLM extraction emits mixed-case variants of the same
			// exclusive relation; the supersede predicate must match them
			// exactly like IsExclusiveRelationship does.
			_, err := tx.ExecContext(ctx,
				`UPDATE graph_edges
				 SET valid_until = $1, superseded_by = $2, updated_at = $3
				 WHERE source_id = $4
				   AND LOWER(relationship_name) = LOWER($5)
				   AND id <> $6
				   AND dataset_id = $7
				   AND (valid_until IS NULL)`,
				now, edgeID, now, e.SourceID, e.RelationshipName, edgeID, datasetID)
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
