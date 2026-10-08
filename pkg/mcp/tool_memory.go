package mcp

// Memory palace tools: list, pin, unpin, wake_up.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/sqlcompat"
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
		return toolError("database not configured")
	}

	filterType, _ := args["type"].(string)
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	ownerID := extractOwnerID(ctx)

	var conds []string
	var qargs []any
	pos := 1
	// Ownership scope: the caller's own rows plus shared (empty-owner) rows.
	// Without this list_memories returned every owner's memories, leaking
	// other users' rows on a shared deployment.
	conds = append(conds, fmt.Sprintf("(owner_id = $%d OR owner_id = '')", pos))
	qargs = append(qargs, ownerID)
	pos++
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

	conds = append(conds, "superseded_by = ''", "valid_until IS NULL")

	sqlStr := `SELECT id, key, value, type, owner_id, room, hall, is_pinned, pin_priority, created_at, updated_at,
		COALESCE(NULLIF(verification_status,''),'unverified'), COALESCE(source_task_id,''), COALESCE(source_receipt_ids,'[]') FROM memories`
	sqlStr += " WHERE " + strings.Join(conds, " AND ")
	sqlStr += fmt.Sprintf(" ORDER BY updated_at DESC LIMIT %d", listMemoriesCap)

	rows, err := db.QueryContext(ctx, deps.Q(sqlStr), qargs...)
	if err != nil {
		return toolError(err.Error())
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, ownerID, rm, hl, ca, ua string
		var verification, taskID, receiptJSON string
		var pinned bool
		var prio int
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &rm, &hl, &pinned, &prio, &ca, &ua, &verification, &taskID, &receiptJSON); err != nil {
			return toolError(err.Error())
		}
		var receipts []string
		_ = json.Unmarshal([]byte(receiptJSON), &receipts)
		if receipts == nil {
			receipts = []string{}
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "room": rm, "hall": hl,
			"is_pinned": pinned, "pin_priority": prio,
			"created_at": ca, "updated_at": ua,
			"verification_status": verification, "source_task_id": taskID, "source_receipt_ids": receipts,
		})
	}

	if err := rows.Err(); err != nil {
		return toolError(err.Error())
	}
	if results == nil {
		return jsonResult(map[string]any{"memories": []any{}, "total": 0})
	}
	return jsonResult(map[string]any{"memories": results, "total": len(results)})
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
// A nonempty collection narrows the update; omitted/empty keeps legacy scope.
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
	collectionName, ok := args["collection"].(string)
	if _, present := args["collection"]; present && !ok {
		return toolError("'collection' must be a string")
	}
	priority := 1
	if p, ok := args["priority"].(float64); ok {
		priority = int(p)
	}
	ownerID := extractOwnerID(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	// Each placeholder is used once — Q (not QArgs) is sufficient.
	sqlStr := `
		UPDATE memories SET is_pinned = TRUE, pin_priority = $1, updated_at = $2
		WHERE key = $3 AND (owner_id = $4 OR owner_id = '') AND superseded_by='' AND valid_until IS NULL
	`
	qargs := []any{priority, now, key, ownerID}
	if collectionName != "" {
		sqlStr += " AND collection_name = $5"
		qargs = append(qargs, collectionName)
	}
	res, err := db.ExecContext(ctx, deps.Q(sqlStr), qargs...)
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
	return statusResult(true, fmt.Sprintf("Pinned %s (priority=%d)", key, priority))
}

// ToolUnpinMemory clears the pin flag and priority on a memory row.
//
// Unlike Pin, a missing row is NOT reported as an error — the target
// state (unpinned) is already satisfied, so the call is idempotent by
// design. Matches pre-refactor behavior.
// Collection selection follows the same rule as Pin.
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
	collectionName, ok := args["collection"].(string)
	if _, present := args["collection"]; present && !ok {
		return toolError("'collection' must be a string")
	}
	ownerID := extractOwnerID(ctx)
	now := time.Now().UTC().Format(time.RFC3339)

	sqlStr := `
		UPDATE memories SET is_pinned = FALSE, pin_priority = 0, updated_at = $1
		WHERE key = $2 AND (owner_id = $3 OR owner_id = '') AND superseded_by='' AND valid_until IS NULL
	`
	qargs := []any{now, key, ownerID}
	if collectionName != "" {
		sqlStr += " AND collection_name = $4"
		qargs = append(qargs, collectionName)
	}
	if _, err := db.ExecContext(ctx, deps.Q(sqlStr), qargs...); err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	return statusResult(true, "Unpinned "+key)
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
	entities := wakeUpEntities(ctx, deps, collectionName, topEntities)
	scopeStatus := "exact"
	if collectionName == "" {
		scopeStatus = "empty"
	}

	bundle := map[string]any{
		"collection":   collectionName,
		"max_tokens":   maxTokens,
		"pinned":       pinned,
		"top_entities": entities,
		"scope_status": scopeStatus,
		"tokens_used":  0,
	}
	out, _ := json.MarshalIndent(bundle, "", "  ")
	bundle["tokens_used"] = len(out) / wakeUpCharsPerToken
	out, _ = json.MarshalIndent(bundle, "", "  ")
	if len(out) > maxChars {
		// Drop entities first, then trim pinned to fit.
		bundle["top_entities"] = []any{}
		out, _ = json.MarshalIndent(bundle, "", "  ")
		for len(out) > maxChars && len(pinned) > 0 {
			pinned = pinned[:len(pinned)-1]
			bundle["pinned"] = pinned
			out, _ = json.MarshalIndent(bundle, "", "  ")
		}
		bundle["tokens_used"] = len(out) / wakeUpCharsPerToken
		out, _ = json.MarshalIndent(bundle, "", "  ")
	}

	return ToolResult{
		Content:           []Content{{Type: "text", Text: string(out)}},
		StructuredContent: bundle,
	}
}

// wakeUpPinned loads pinned memories owned by ownerID (or the empty
// shared owner) in priority-desc order.
func wakeUpPinned(ctx context.Context, db *sql.DB, rewrite func(string) string, ownerID, collectionName string) []map[string]any {
	sqlStr := fmt.Sprintf(`SELECT key, value, hall, room, pin_priority,
		COALESCE(NULLIF(verification_status,''),'unverified'), COALESCE(source_task_id,''), COALESCE(source_receipt_ids,'[]') FROM memories
		WHERE %s AND (owner_id = $1 OR owner_id = '') AND superseded_by = '' AND valid_until IS NULL`, sqlcompat.BoolTrue("is_pinned"))
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
		var verification, taskID, receiptJSON string
		var prio int
		if err := rows.Scan(&k, &v, &hl, &rm, &prio, &verification, &taskID, &receiptJSON); err != nil {
			continue
		}
		var receipts []string
		_ = json.Unmarshal([]byte(receiptJSON), &receipts)
		if receipts == nil {
			receipts = []string{}
		}
		out = append(out, map[string]any{
			"key": k, "value": v, "hall": hl, "room": rm, "priority": prio,
			"verification_status": verification, "source_task_id": taskID, "source_receipt_ids": receipts,
		})
	}
	return out
}

// wakeUpEntities loads the top-N graph nodes by active-edge degree.
// Edges are "active" if their valid_until is NULL or in the future.

func wakeUpEntities(ctx context.Context, deps Deps, collectionName string, topN int) []map[string]any {
	// Graph context without an explicit collection is unsafe: global nodes can
	// silently contaminate a project bootstrap. Pinned global memories remain
	// backward compatible, but graph entities require an exact scope.
	if collectionName == "" {
		return []map[string]any{}
	}
	db := deps.DB()
	allowed := []string(nil)
	if authorizer, ok := deps.(GraphAssertionAuthorizer); ok {
		var err error
		allowed, err = authorizer.GraphDatasetIDs(ctx)
		if err != nil || len(allowed) == 0 {
			return []map[string]any{}
		}
	}
	type entity struct {
		id, name, typ, dataset, properties, updated string
		degree                                      int
	}
	queryArgs := []any{collectionName, collectionName}
	nodeQuery := `SELECT n.id,n.name,n.type,COALESCE(n.dataset_id,''),COALESCE(n.properties,'{}'),COALESCE(CAST(n.updated_at AS TEXT),'')
		FROM graph_nodes n WHERE (n.collection_id = $1 OR n.dataset_id = $2)`
	nodeQuery += graphDatasetPredicate("n.dataset_id", allowed, &queryArgs)
	rows, err := db.QueryContext(ctx, deps.Q(nodeQuery), queryArgs...)
	if err != nil {
		return nil
	}
	var candidates []*entity
	for rows.Next() {
		candidate := new(entity)
		if err := rows.Scan(&candidate.id, &candidate.name, &candidate.typ, &candidate.dataset, &candidate.properties, &candidate.updated); err != nil {
			rows.Close()
			return nil
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil
	}
	rows.Close()
	entities := make(map[string]*entity)
	for _, candidate := range candidates {
		if candidate.name != "" && graphAssertionsAllowed(ctx, deps, []GraphAssertion{{DatasetID: candidate.dataset, Properties: []byte(candidate.properties)}}) {
			entities[candidate.id] = candidate
		}
	}
	if len(entities) == 0 {
		return []map[string]any{}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	edgeQuery := `SELECT e.source_id,e.target_id,
		COALESCE(src.dataset_id,''),COALESCE(src.properties,'{}'),
		COALESCE(e.dataset_id,''),COALESCE(e.properties,'{}'),
		COALESCE(dst.dataset_id,''),COALESCE(dst.properties,'{}')
		FROM graph_edges e
		JOIN graph_nodes src ON src.id=e.source_id
		JOIN graph_nodes dst ON dst.id=e.target_id
		WHERE (e.valid_until IS NULL OR e.valid_until > $1)
		AND (src.collection_id=$2 OR src.dataset_id=$3 OR dst.collection_id=$4 OR dst.dataset_id=$5)`
	rows, err = db.QueryContext(ctx, deps.Q(edgeQuery), now, collectionName, collectionName, collectionName, collectionName)
	if err != nil {
		return nil
	}
	type edge struct {
		sourceID, targetID, sourceDataset, sourceProperties, dataset, properties, targetDataset, targetProperties string
	}
	var edges []edge
	for rows.Next() {
		var candidate edge
		if err := rows.Scan(&candidate.sourceID, &candidate.targetID, &candidate.sourceDataset, &candidate.sourceProperties, &candidate.dataset, &candidate.properties, &candidate.targetDataset, &candidate.targetProperties); err != nil {
			rows.Close()
			return nil
		}
		edges = append(edges, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil
	}
	rows.Close()
	for _, candidate := range edges {
		if candidate.dataset == "" && candidate.sourceDataset == candidate.targetDataset {
			candidate.dataset = candidate.sourceDataset
		}
		if !graphAssertionsAllowed(ctx, deps, []GraphAssertion{
			{DatasetID: candidate.sourceDataset, Properties: []byte(candidate.sourceProperties)},
			{DatasetID: candidate.dataset, Properties: []byte(candidate.properties)},
			{DatasetID: candidate.targetDataset, Properties: []byte(candidate.targetProperties)},
		}) {
			continue
		}
		if source := entities[candidate.sourceID]; source != nil {
			source.degree++
		}
		if target := entities[candidate.targetID]; target != nil && candidate.targetID != candidate.sourceID {
			target.degree++
		}
	}
	ranked := make([]*entity, 0, len(entities))
	for _, candidate := range entities {
		ranked = append(ranked, candidate)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].degree != ranked[j].degree {
			return ranked[i].degree > ranked[j].degree
		}
		if ranked[i].updated != ranked[j].updated {
			return ranked[i].updated > ranked[j].updated
		}
		return ranked[i].id < ranked[j].id
	})
	if len(ranked) > topN {
		ranked = ranked[:topN]
	}
	out := make([]map[string]any, 0, len(ranked))
	for _, candidate := range ranked {
		out = append(out, map[string]any{"name": candidate.name, "type": candidate.typ, "edge_count": candidate.degree})
	}
	return out
}
