package mcp

// Memory palace tools: list, pin, unpin, wake_up.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// listMemoriesCap bounds the rows returned by ToolListMemories.
// Matches pre-refactor LIMIT.
const listMemoriesCap = 100

// ToolListMemories returns rows from the memories table with optional
// type / collection / room / hall filters.
//
// Nil DB returns "[]" rather than an error, so clients built against a
// deployment without the palace table keep working.
func ToolListMemories(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}

	filterType, _ := args["type"].(string)
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)

	var conds []string
	var qargs []any
	pos := 1
	if filterType != "" {
		conds = append(conds, fmt.Sprintf("type = $%d", pos))
		qargs = append(qargs, filterType)
		pos++
	}
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
		pos++
	}
	if room != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, room)
		pos++
	}
	if hall != "" {
		conds = append(conds, fmt.Sprintf("hall = $%d", pos))
		qargs = append(qargs, hall)
	}

	conds = append(conds, "superseded_by = ''")

	sqlStr := `SELECT id, key, value, type, owner_id, room, hall, is_pinned, pin_priority, created_at, updated_at FROM memories`
	sqlStr += " WHERE " + strings.Join(conds, " AND ")
	sqlStr += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT %d", listMemoriesCap)

	rows, err := db.QueryContext(ctx, deps.Q(sqlStr), qargs...)
	if err != nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, ownerID, rm, hl, ca, ua string
		var pinned, prio int
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &rm, &hl, &pinned, &prio, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "room": rm, "hall": hl,
			"is_pinned": pinned == 1, "pin_priority": prio,
			"created_at": ca, "updated_at": ua,
		})
	}

	if results == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// extractOwnerID reads the MCP user ID from the request context (set by
// the HTTP handler on auth). Returns empty string for anonymous tool
// calls. Used by the ownership-scoped memory tools.
func extractOwnerID(ctx context.Context) string {
	if uid, ok := ctx.Value(UserIDKey).(string); ok {
		return uid
	}
	return ""
}

// ToolPinMemory flags a memory row as pinned with the given priority.
//
// Ownership scope: the update only matches rows owned by the caller or
// owned by the empty string (shared memories). Zero rows affected is
// surfaced as IsError so the client knows the pin had no effect.
func ToolPinMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
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
	priority := 1
	if p, ok := args["priority"].(float64); ok {
		priority = int(p)
	}
	ownerID := extractOwnerID(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	// Placeholders $1..$4 are each used once — Q (not QArgs) is sufficient.
	res, err := db.ExecContext(ctx, deps.Q(`
		UPDATE memories SET is_pinned = 1, pin_priority = $1, updated_at = $2
		WHERE key = $3 AND (owner_id = $4 OR owner_id = '')
	`), priority, now, key, ownerID)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "No memory matched key " + key}},
			IsError: true,
		}
	}
	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Pinned %s (priority=%d)", key, priority),
	}}}
}

// ToolUnpinMemory clears the pin flag and priority on a memory row.
//
// Unlike Pin, a missing row is NOT reported as an error — the target
// state (unpinned) is already satisfied, so the call is idempotent by
// design. Matches pre-refactor behavior.
func ToolUnpinMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
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
	ownerID := extractOwnerID(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := db.ExecContext(ctx, deps.Q(`
		UPDATE memories SET is_pinned = 0, pin_priority = 0, updated_at = $1
		WHERE key = $2 AND (owner_id = $3 OR owner_id = '')
	`), now, key, ownerID); err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	return ToolResult{Content: []Content{{Type: "text", Text: "Unpinned " + key}}}
}

// Defaults for ToolWakeUp. max_tokens caps total response size;
// top_entities limits the graph-entity section; chars/4 approximates
// token count (matches pre-refactor tokenizer-free heuristic).
const (
	wakeUpDefaultMaxTokens   = 200
	wakeUpDefaultTopEntities = 5
	wakeUpPinnedRowLimit     = 50
	wakeUpCharsPerToken      = 4
)

// ToolWakeUp returns a small bundle of critical context for session
// start: pinned memories + top-N active graph entities, trimmed to a
// token budget.
//
// Trim strategy matches pre-refactor: if the initial bundle exceeds
// max_tokens*4 chars, drop all entities first, then pop pinned entries
// from the tail until it fits.
func ToolWakeUp(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	collectionName, _ := args["collection"].(string)
	maxTokens := wakeUpDefaultMaxTokens
	if mt, ok := args["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = int(mt)
	}
	topEntities := wakeUpDefaultTopEntities
	if te, ok := args["top_entities"].(float64); ok && te > 0 {
		topEntities = int(te)
	}
	maxChars := maxTokens * wakeUpCharsPerToken
	ownerID := extractOwnerID(ctx)

	pinned := wakeUpPinned(ctx, db, deps.Q, ownerID, collectionName)
	entities := wakeUpEntities(ctx, db, deps.Q, topEntities)

	bundle := map[string]any{
		"collection":   collectionName,
		"max_tokens":   maxTokens,
		"pinned":       pinned,
		"top_entities": entities,
	}
	out, _ := json.MarshalIndent(bundle, "", "  ")
	if len(out) > maxChars {
		// Drop entities first, then trim pinned to fit.
		bundle["top_entities"] = []any{}
		out, _ = json.MarshalIndent(bundle, "", "  ")
		for len(out) > maxChars && len(pinned) > 0 {
			pinned = pinned[:len(pinned)-1]
			bundle["pinned"] = pinned
			out, _ = json.MarshalIndent(bundle, "", "  ")
		}
	}

	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// wakeUpPinned loads pinned memories owned by ownerID (or the empty
// shared owner) in priority-desc order.
func wakeUpPinned(ctx context.Context, db *sql.DB, rewrite func(string) string, ownerID, collectionName string) []map[string]any {
	sqlStr := `SELECT key, value, hall, room, pin_priority FROM memories
		WHERE is_pinned = 1 AND (owner_id = $1 OR owner_id = '') AND superseded_by = ''`
	qargs := []any{ownerID}
	if collectionName != "" {
		sqlStr += " AND collection_name = $2"
		qargs = append(qargs, collectionName)
	}
	sqlStr += fmt.Sprintf(" ORDER BY pin_priority DESC, updated_at DESC LIMIT %d", wakeUpPinnedRowLimit)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var k, v, hl, rm string
		var prio int
		if err := rows.Scan(&k, &v, &hl, &rm, &prio); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"key": k, "value": v, "hall": hl, "room": rm, "priority": prio,
		})
	}
	return out
}

// wakeUpEntities loads the top-N graph nodes by active-edge degree.
// Edges are "active" if their valid_until is NULL or in the future.
func wakeUpEntities(ctx context.Context, db *sql.DB, rewrite func(string) string, topN int) []map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	sqlStr := `SELECT n.name, n.type, COUNT(e.id) AS deg
		FROM graph_nodes n
		LEFT JOIN graph_edges e ON (e.source_id = n.id OR e.target_id = n.id)
			AND (e.valid_until IS NULL OR e.valid_until > $1)
		GROUP BY n.id, n.name, n.type
		ORDER BY deg DESC, n.updated_at DESC
		LIMIT $2`

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), now, topN)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var name, typ string
		var deg int
		if err := rows.Scan(&name, &typ, &deg); err != nil {
			continue
		}
		if name == "" {
			continue
		}
		out = append(out, map[string]any{
			"name": name, "type": typ, "degree": deg,
		})
	}
	return out
}
