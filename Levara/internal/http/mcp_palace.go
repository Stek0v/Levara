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
	"time"

	"github.com/google/uuid"
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

func (h *mcpHandler) toolDiaryWrite(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}
	agent, _ := args["agent"].(string)
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if agent == "" || key == "" || value == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'agent', 'key', 'value' required"}}, IsError: true}
	}
	collectionName, _ := args["collection"].(string)
	owner := mcp.DiaryOwner(agent)
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	q, qargs := QArgs(`INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, 'diary', $4, $5, '', '', 0, 0, $6, $7)
		ON CONFLICT(key, owner_id) DO UPDATE SET value = $3, collection_name = $5, updated_at = $7`,
		id, key, value, owner, collectionName, now, now)
	if _, err := h.cfg.DB.ExecContext(ctx, q, qargs...); err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Diary[%s] wrote %s", agent, key)}}}
}

func (h *mcpHandler) toolDiaryRead(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	agent, _ := args["agent"].(string)
	if agent == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'agent' required"}}, IsError: true}
	}
	owner := mcp.DiaryOwner(agent)
	queryStr, _ := args["query"].(string)
	collectionName, _ := args["collection"].(string)

	var conds []string
	var qargs []any
	pos := 1
	conds = append(conds, fmt.Sprintf("owner_id = $%d", pos))
	qargs = append(qargs, owner)
	pos++
	if queryStr != "" {
		pat := "%" + queryStr + "%"
		conds = append(conds, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", pos, pos+1))
		qargs = append(qargs, pat, pat)
		pos += 2
	}
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
		pos++
	}
	sqlStr := `SELECT key, value, created_at, updated_at FROM memories WHERE ` +
		strings.Join(conds, " AND ") + ` ORDER BY updated_at DESC LIMIT 100`

	rows, err := h.cfg.DB.QueryContext(ctx, Q(sqlStr), qargs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}
	defer rows.Close()
	var entries []map[string]any
	for rows.Next() {
		var k, v, ca, ua string
		if err := rows.Scan(&k, &v, &ca, &ua); err != nil {
			continue
		}
		entries = append(entries, map[string]any{
			"key": k, "value": v, "created_at": ca, "updated_at": ua,
		})
	}
	if entries == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Diary[%s] is empty", agent)}}}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

