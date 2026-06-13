package mcp

import (
	"context"
	"strconv"
)

// ToolDeleteMemory permanently removes a memory by key — both the SQL row
// (source of truth) and its vector sidecar entry, so the record stops
// surfacing in recall.
//
// Ownership scope matches pin/unpin: the delete only matches rows owned by
// the caller or owned by the empty string (shared memories). An optional
// `collection` narrows the delete to a single pinned-context shard.
//
// Zero rows matched is surfaced as IsError (like pin, unlike the idempotent
// unpin): delete is a state-change request and the caller needs to know
// whether it took effect. The vector cleanup is best-effort — an orphaned
// vector is reaped by the reconcile_memory sweep, whereas a stale SQL row
// left by a failed delete is worse, so the SQL delete is authoritative.
func ToolDeleteMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	key, _ := args["key"].(string)
	if key == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'key' required"}},
			IsError: true,
		}
	}
	collectionName, _ := args["collection"].(string)
	ownerID := extractOwnerID(ctx)

	// Resolve the target rows first (id + collection_name) so we know which
	// vector sidecar/id to drop. (key, owner_id) is unique, so at most two
	// rows match — the caller's own and the shared empty-owner one. The
	// optional collection filter narrows it further. Each placeholder is
	// used once so Q (not QArgs) is sufficient (see Q placeholder gotcha).
	selSQL := `SELECT id, collection_name FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '')`
	qargs := []any{key, ownerID}
	if collectionName != "" {
		selSQL += ` AND collection_name = $3`
		qargs = append(qargs, collectionName)
	}
	rows, err := db.QueryContext(ctx, deps.Q(selSQL), qargs...)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	type target struct{ id, collection string }
	var targets []target
	for rows.Next() {
		var id, coll string
		if scanErr := rows.Scan(&id, &coll); scanErr != nil {
			continue
		}
		targets = append(targets, target{id: id, collection: coll})
	}
	rows.Close()
	if rowsErr := rows.Err(); rowsErr != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + rowsErr.Error()}},
			IsError: true,
		}
	}

	if len(targets) == 0 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "No memory matched key " + key}},
			IsError: true,
		}
	}

	// Delete the SQL rows (source of truth) with the same WHERE used to
	// resolve targets, so we remove exactly what we matched.
	delSQL := `DELETE FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '')`
	if collectionName != "" {
		delSQL += ` AND collection_name = $3`
	}
	if _, err := db.ExecContext(ctx, deps.Q(delSQL), qargs...); err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}

	// Best-effort vector cleanup so the record stops surfacing in recall
	// (unfiltered recall returns vector metadata directly). We don't fail the
	// delete if a sidecar drop errors — reconcile_memory reaps the orphan.
	if deps.HasCollections() {
		for _, t := range targets {
			_ = deps.CollectionDelete(memoryCollectionName(t.collection), t.id)
		}
	}

	return statusResult(true, "Deleted "+key+" ("+strconv.Itoa(len(targets))+" record(s))")
}
