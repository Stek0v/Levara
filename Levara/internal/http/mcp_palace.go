// mcp_palace.go — Memory-palace MCP handlers: wake_up, pin/unpin, query_entity,
// agent diaries, and the controlled "hall" vocabulary.
//
// Inspired by milla-jovovich/mempalace's Wings/Rooms/Halls metaphor: rooms are
// sub-topics within a collection, halls classify the genre of a memory (fact,
// event, decision, ...). Combined with structural filters this raises recall
// precision substantially over flat metadata search.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stek0v/cognevra/pkg/mcp"
)

// Hall vocabulary, ChunkMetaMatches, and IsValidHall live in pkg/mcp now
// (F-4 wave 1a) — see pkg/mcp/hall.go.

// ── wake_up ──

// toolWakeUp is a thin shim over mcp.ToolWakeUp (F-4 wave 3f).
func (h *mcpHandler) toolWakeUp(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolWakeUp(ctx, h, args)
}

// ── pin / unpin ──

// toolPinMemory / toolUnpinMemory are thin shims over pkg/mcp (F-4 wave 3f).
func (h *mcpHandler) toolPinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolPinMemory(ctx, h, args)
}

func (h *mcpHandler) toolUnpinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolUnpinMemory(ctx, h, args)
}

// ── query_entity ──

// toolQueryEntity returns all graph edges touching the named entity. Filters
// by validity: by default only currently-active edges; if as_of is supplied,
// returns the snapshot at that point in time (edges whose validity window
// included as_of).
func (h *mcpHandler) toolQueryEntity(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}
	name, _ := args["name"].(string)
	if name == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'name' required"}}, IsError: true}
	}
	asOf, _ := args["as_of"].(string)
	limit := 50
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	// Resolve entity → node IDs
	var nodeIDs []string
	if rows, err := h.cfg.DB.QueryContext(ctx,
		Q(`SELECT id FROM graph_nodes WHERE name = $1 LIMIT 10`), name); err == nil {
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				nodeIDs = append(nodeIDs, id)
			}
		}
	}
	if len(nodeIDs) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("No entity found with name '%s'", name)}}}
	}

	// Build IN clause and validity predicate
	placeholders := make([]string, 0, len(nodeIDs)*2)
	qargs := make([]any, 0, len(nodeIDs)*2+2)
	pos := 1
	for _, id := range nodeIDs {
		placeholders = append(placeholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}
	srcIn := strings.Join(placeholders, ",")
	tgtPlaceholders := make([]string, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		tgtPlaceholders = append(tgtPlaceholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}
	tgtIn := strings.Join(tgtPlaceholders, ",")

	validityClause := ""
	if asOf == "" {
		validityClause = " AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)"
	} else {
		// Edge was valid at as_of: valid_from is null OR <= as_of, AND valid_until is null OR > as_of
		validityClause = fmt.Sprintf(" AND (valid_from IS NULL OR valid_from <= $%d) AND (valid_until IS NULL OR valid_until > $%d)", pos, pos+1)
		qargs = append(qargs, asOf, asOf)
		pos += 2
	}

	sqlStr := fmt.Sprintf(`SELECT id, source_id, target_id, relationship_name, properties,
			COALESCE(valid_from, ''), COALESCE(valid_until, ''), superseded_by, confidence
		FROM graph_edges
		WHERE (source_id IN (%s) OR target_id IN (%s))%s
		ORDER BY updated_at DESC LIMIT $%d`, srcIn, tgtIn, validityClause, pos)
	qargs = append(qargs, limit)

	rows, err := h.cfg.DB.QueryContext(ctx, Q(sqlStr), qargs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}
	defer rows.Close()

	var edges []map[string]any
	for rows.Next() {
		var id, src, tgt, rel, props, vf, vu, sb string
		var conf float64
		if err := rows.Scan(&id, &src, &tgt, &rel, &props, &vf, &vu, &sb, &conf); err != nil {
			continue
		}
		edges = append(edges, map[string]any{
			"id":               id,
			"source_id":        src,
			"target_id":        tgt,
			"relationship":     rel,
			"properties":       json.RawMessage(props),
			"valid_from":       vf,
			"valid_until":      vu,
			"superseded_by":    sb,
			"confidence":       conf,
		})
	}

	resp := map[string]any{
		"entity":   name,
		"as_of":    asOf,
		"node_ids": nodeIDs,
		"edges":    edges,
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// ── Agent diaries ──

// DiaryOwnerPrefix and DiaryOwner moved to pkg/mcp/util.go.

// toolDiaryWrite / toolDiaryRead are thin shims over pkg/mcp (F-4 wave 3g).
func (h *mcpHandler) toolDiaryWrite(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDiaryWrite(ctx, h, args)
}

func (h *mcpHandler) toolDiaryRead(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDiaryRead(ctx, h, args)
}

