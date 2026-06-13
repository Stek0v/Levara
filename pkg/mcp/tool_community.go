package mcp

// Graph community tools: list_communities + prune_graph.
// Migrated in F-4 wave 3n. Grouped together because both operate on
// graph metadata and neither needs new Deps methods — DB() and
// LogHeartbeat() were added in earlier waves.

import (
	"context"
	"fmt"

	"github.com/stek0v/levara/pkg/community"
)

// ToolListCommunities queries the graph_communities table and returns a
// JSON array of community objects. Returns "[]" (not IsError) on nil DB
// or SQL error — empty is a valid state for graphs with no communities
// yet.
//
// Args: limit (float64, default 20), min_members (float64, default 2),
// level (float64, optional).
func ToolListCommunities(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"communities": []any{}})
	}

	limit := 20
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		limit = int(lim)
	}
	minMembers := 2
	if mm, ok := args["min_members"].(float64); ok {
		minMembers = int(mm)
	}

	var (
		query     string
		queryArgs []any
	)
	if levelVal, ok := args["level"].(float64); ok {
		query = deps.Q(`SELECT id, level, parent_id, member_count, summary
			FROM graph_communities
			WHERE member_count >= $1 AND level = $2
			ORDER BY member_count DESC LIMIT $3`)
		queryArgs = []any{minMembers, int(levelVal), limit}
	} else {
		query = deps.Q(`SELECT id, level, parent_id, member_count, summary
			FROM graph_communities
			WHERE member_count >= $1
			ORDER BY level ASC, member_count DESC LIMIT $2`)
		queryArgs = []any{minMembers, limit}
	}

	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return jsonResult(map[string]any{"communities": []any{}})
	}
	defer rows.Close()

	var communities []map[string]any
	for rows.Next() {
		var id, parentID, summary string
		var level, memberCount int
		if rows.Scan(&id, &level, &parentID, &memberCount, &summary) != nil {
			continue
		}
		communities = append(communities, map[string]any{
			"id": id, "level": level, "parent_id": parentID,
			"member_count": memberCount, "summary": summary,
		})
	}

	if communities == nil {
		communities = []map[string]any{}
	}

	return jsonResult(map[string]any{"communities": communities})
}

// ToolPruneGraph removes superseded graph edges (and optionally orphan
// nodes) according to community.PruneConfig. Defaults to dry-run=true
// so the first call is always a preview.
//
// Args: max_age_days (float64), dry_run (bool), include_orphan_nodes (bool).
//
// Error branch: DB nil → `{"edges_deleted":0}` (not IsError);
// community.PruneGraph error → IsError.
func ToolPruneGraph(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"edges_deleted": 0, "nodes_deleted": 0, "dry_run": true})
	}

	cfg := community.PruneConfig{
		MaxAgeDays:      90,
		KeepSuperseding: true,
		DryRun:          true,
	}
	if days, ok := args["max_age_days"].(float64); ok && days > 0 {
		cfg.MaxAgeDays = int(days)
	}
	if dr, ok := args["dry_run"].(bool); ok {
		cfg.DryRun = dr
	}
	if io, ok := args["include_orphan_nodes"].(bool); ok {
		cfg.IncludeOrphans = io
	}

	result, err := community.PruneGraph(ctx, db, cfg)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
			IsError: true,
		}
	}

	deps.LogHeartbeat("prune", result)
	return jsonResult(map[string]any{
		"edges_deleted":      result.EdgesDeleted,
		"edges_would_delete": result.EdgesWouldDelete,
		"orphan_nodes":       result.OrphanNodes,
		"members_cleaned_up": result.MembersCleanedUp,
		"dry_run":            cfg.DryRun,
	})
}
