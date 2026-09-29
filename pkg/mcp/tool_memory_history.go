package mcp

import (
	"context"
	"database/sql"
	"strings"
)

// Audit recall combines literal historical matches with semantic candidates and
// their linked predecessors. Retired vectors are deliberately not re-published.
func recallWithHistory(ctx context.Context, deps Deps, db *sql.DB, query, collection, room, hall, owner string) ToolResult {
	literal := recallViaSQLLike(ctx, db, deps.Q, query, collection, room, hall, owner, true)
	if literal.IsError {
		return literal
	}
	rows := make([]map[string]any, 0, recallMemorySQLLimit)
	seen := map[string]bool{}
	appendRows := func(items []map[string]any) {
		for _, row := range items {
			id, _ := row["id"].(string)
			if id != "" && !seen[id] && len(rows) < recallMemorySQLLimit {
				seen[id] = true
				rows = append(rows, row)
			}
		}
	}
	items, _ := literal.StructuredContent.(map[string]any)["results"].([]map[string]any)
	appendRows(items)
	if deps.EmbedAvailable() {
		if semantic, ok := recallViaVectorFiltered(ctx, deps, db, deps.Q, query, collection, room, hall, owner, true); ok {
			if semantic.IsError {
				return semantic
			}
			items, _ = semantic.StructuredContent.(map[string]any)["results"].([]map[string]any)
			appendRows(items)
		}
	}
	// ponytail: audit-only traversal is capped at 20 rows/queries; batch it if
	// measured history latency warrants that complexity. Seen IDs bound cycles.
	for i := 0; i < len(rows) && len(rows) < recallMemorySQLLimit; i++ {
		conds := []string{`id IN (SELECT parent.id FROM memories parent JOIN memories child
			ON child.supersedes_memory_id=parent.id AND parent.superseded_by=child.id
			AND parent.owner_id=child.owner_id AND parent.collection_name=child.collection_name
			WHERE child.id=$1)`}
		args := []any{rows[i]["id"]}
		conds, args, _ = appendMemoryFilters(conds, args, 2, collection, room, hall, owner, true)
		result, err := db.QueryContext(ctx, deps.Q("SELECT "+memoryRowColumns+" FROM memories WHERE "+strings.Join(conds, " AND ")), args...)
		if err != nil {
			return toolError(err.Error())
		}
		parents, err := scanMemoryRows(result)
		result.Close()
		if err != nil {
			return toolError(err.Error())
		}
		appendRows(parents)
	}
	return jsonResult(map[string]any{"results": rows})
}
