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

// toolWakeUp returns a small bundle of critical context for session start:
//   - all pinned memories in the requested collection (priority-ordered)
//   - top-N currently-active graph entities (highest edge degree)
// The result is capped at max_tokens (approximated as chars/4).
func (h *mcpHandler) toolWakeUp(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}
	collectionName, _ := args["collection"].(string)
	maxTokens := 200
	if mt, ok := args["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = int(mt)
	}
	topEntities := 5
	if te, ok := args["top_entities"].(float64); ok && te > 0 {
		topEntities = int(te)
	}
	maxChars := maxTokens * 4

	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}

	// 1. Pinned memories — pin_priority DESC, updated_at DESC
	var pinned []map[string]any
	pinnedSQL := `SELECT key, value, hall, room, pin_priority FROM memories
		WHERE is_pinned = 1 AND (owner_id = $1 OR owner_id = '')`
	pinnedArgs := []any{ownerID}
	if collectionName != "" {
		pinnedSQL += " AND collection_name = $2"
		pinnedArgs = append(pinnedArgs, collectionName)
	}
	pinnedSQL += " ORDER BY pin_priority DESC, updated_at DESC LIMIT 50"
	if rows, err := h.cfg.DB.QueryContext(ctx, Q(pinnedSQL), pinnedArgs...); err == nil {
		defer rows.Close()
		for rows.Next() {
			var k, v, hl, rm string
			var prio int
			if err := rows.Scan(&k, &v, &hl, &rm, &prio); err != nil {
				continue
			}
			pinned = append(pinned, map[string]any{
				"key": k, "value": v, "hall": hl, "room": rm, "priority": prio,
			})
		}
	}

	// 2. Top entities by active-edge degree
	var entities []map[string]any
	entSQL := `SELECT n.name, n.type, COUNT(e.id) AS deg
		FROM graph_nodes n
		LEFT JOIN graph_edges e ON (e.source_id = n.id OR e.target_id = n.id)
			AND (e.valid_until IS NULL OR e.valid_until > $1)
		GROUP BY n.id, n.name, n.type
		ORDER BY deg DESC, n.updated_at DESC
		LIMIT $2`
	now := time.Now().UTC().Format(time.RFC3339)
	if rows, err := h.cfg.DB.QueryContext(ctx, Q(entSQL), now, topEntities); err == nil {
		defer rows.Close()
		for rows.Next() {
			var name, typ string
			var deg int
			if err := rows.Scan(&name, &typ, &deg); err != nil {
				continue
			}
			if name == "" {
				continue
			}
			entities = append(entities, map[string]any{
				"name": name, "type": typ, "degree": deg,
			})
		}
	}

	// 3. Trim to budget — pinned first, then entities.
	bundle := map[string]any{
		"collection":   collectionName,
		"max_tokens":   maxTokens,
		"pinned":       pinned,
		"top_entities": entities,
	}
	out, _ := json.MarshalIndent(bundle, "", "  ")
	if len(out) > maxChars {
		// Drop entities first, then trim pinned to fit
		bundle["top_entities"] = []any{}
		out, _ = json.MarshalIndent(bundle, "", "  ")
		for len(out) > maxChars && len(pinned) > 0 {
			pinned = pinned[:len(pinned)-1]
			bundle["pinned"] = pinned
			out, _ = json.MarshalIndent(bundle, "", "  ")
		}
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// ── pin / unpin ──

func (h *mcpHandler) toolPinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}
	key, _ := args["key"].(string)
	if key == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'key' required"}}, IsError: true}
	}
	priority := 1
	if p, ok := args["priority"].(float64); ok {
		priority = int(p)
	}
	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}
	q, qargs := QArgs(`UPDATE memories SET is_pinned = 1, pin_priority = $1, updated_at = $2
		WHERE key = $3 AND (owner_id = $4 OR owner_id = '')`,
		priority, time.Now().UTC().Format(time.RFC3339), key, ownerID)
	res, err := h.cfg.DB.ExecContext(ctx, q, qargs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No memory matched key " + key}}, IsError: true}
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Pinned %s (priority=%d)", key, priority)}}}
}

func (h *mcpHandler) toolUnpinMemory(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}
	key, _ := args["key"].(string)
	if key == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'key' required"}}, IsError: true}
	}
	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}
	q, qargs := QArgs(`UPDATE memories SET is_pinned = 0, pin_priority = 0, updated_at = $1
		WHERE key = $2 AND (owner_id = $3 OR owner_id = '')`,
		time.Now().UTC().Format(time.RFC3339), key, ownerID)
	if _, err := h.cfg.DB.ExecContext(ctx, q, qargs...); err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Unpinned " + key}}}
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

