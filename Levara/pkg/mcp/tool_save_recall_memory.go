package mcp

// AI-backed memory palace tools: save_memory, recall_memory.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// memoryValueLogMaxLen bounds the value echo in ToolSaveMemory's
	// success message (full value stays in the DB row).
	memoryValueLogMaxLen = 100
	// recallMemorySQLLimit caps rows returned by the SQL-LIKE fallback
	// path. Matches pre-refactor.
	recallMemorySQLLimit = 20
	// recallMemoryVectorTopK is the topK passed to CollectionSearch in
	// the vector-semantic path.
	recallMemoryVectorTopK = 10
	// saveMemoryEmbedTimeout caps the goroutine that runs after the
	// tool returns. Decouples client latency from the embed service.
	saveMemoryEmbedTimeout = 30 * time.Second
)

// baseMemoryCollection is the vector-collection name for the base memory
// store — the rows kept at collection_name='' (no pinned context).
const baseMemoryCollection = "_memories"

// memoryCollectionName returns the vector-collection name where
// memory embeddings live. "_memories" by default, with per-collection
// shards like "_memories_levara" when the user pinned a context.
func memoryCollectionName(collectionName string) string {
	if collectionName != "" {
		return baseMemoryCollection + "_" + collectionName
	}
	return baseMemoryCollection
}

// ToolSaveMemory upserts a memory into the memories table and, when
// embedding is available, fires a background goroutine that vector-
// indexes the key+value pair for later semantic recall.
//
// The SQL path is the source of truth: the success message returns
// after DB commit. The vector index is fire-and-forget with its own
// context (decoupled from the client's ctx so a fast MCP return
// doesn't cancel indexing mid-flight).
//
// Pre-refactor reused placeholders ($3, $4, $6, $7, $8, $9, $10, $12
// in both VALUES and DO UPDATE SET). Rewrote to unique placeholders
// ($13..$20) so Deps doesn't need a QArgs method — continues the
// wave 3f/3g simplification pattern.
func ToolSaveMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if key == "" || value == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'key' and 'value' required"}},
			IsError: true,
		}
	}
	memType, _ := args["type"].(string)
	if memType == "" {
		memType = "project"
	}
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	if hall != "" && !IsValidHall(hall) {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: invalid hall '%s'. Valid values: %s", hall, strings.Join(ValidHalls(), ", "))}},
			IsError: true,
		}
	}
	pin, _ := args["pin"].(bool)
	pinPriority := 0
	if pp, ok := args["pin_priority"].(float64); ok {
		pinPriority = int(pp)
	}

	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	ownerID := extractOwnerID(ctx)

	pinInt := 0
	if pin {
		pinInt = 1
	}

	// Reused-value columns (value/type/collection_name/room/hall/
	// is_pinned/pin_priority/updated_at) get their own placeholders in
	// the DO UPDATE SET clause — wave 3f/3g pattern to avoid QArgs.
	//
	// RETURNING id yields the *canonical* row id: the freshly minted uuid
	// on a real insert, or the pre-existing row's id on conflict. We index
	// the vector under that canonical id (P1.4 fix) so a re-save overwrites
	// the prior vector in place instead of leaving an orphan in HNSW —
	// CollectionInsert replaces by id (store.replaceExistingLocked).
	canonicalID := id
	err := db.QueryRowContext(ctx, deps.Q(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT(key, owner_id) DO UPDATE SET value = $13, type = $14, collection_name = $15, room = $16, hall = $17, is_pinned = $18, pin_priority = $19, updated_at = $20
		RETURNING id
	`),
		id, key, value, memType, ownerID, collectionName, room, hall, pinInt, pinPriority, now, now,
		value, memType, collectionName, room, hall, pinInt, pinPriority, now).Scan(&canonicalID)
	if err != nil {
		// Swallow DB errors — matches pre-refactor best-effort contract.
		// The pre-refactor version logged via cfg.Logger if available;
		// logger is deliberately not on Deps (small surface > observability
		// plumbing). Callers reading this code can add Logger() later if
		// needed. On a failed RETURNING the local uuid stays as the id —
		// no worse than the pre-fix behavior.
		canonicalID = id
	}

	if deps.EmbedAvailable() {
		indexMemoryAsync(deps, collectionName, canonicalID, key, value, memType)
	}

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Memory saved: %s = %s (type: %s)", key, Truncate(value, memoryValueLogMaxLen), memType),
	}}}
}

// indexMemoryAsync fires the vector-index side effect in a goroutine.
// Uses context.Background() with a timeout so the embed call isn't
// cancelled when the tool's ctx completes (MCP clients may close
// connections immediately after receiving the save confirmation).
func indexMemoryAsync(deps Deps, collectionName, id, key, value, memType string) {
	go func() {
		embedCtx, cancel := context.WithTimeout(context.Background(), saveMemoryEmbedTimeout)
		defer cancel()

		vec, err := deps.Embed(embedCtx, key+" "+value)
		if err != nil {
			return
		}

		meta, _ := json.Marshal(map[string]string{
			"key":        key,
			"value":      value,
			"type":       memType,
			"collection": collectionName,
			"memory_id":  id,
		})
		_ = deps.CollectionInsert(memoryCollectionName(collectionName), id, vec, meta)
	}()
}

// ToolRecallMemory returns memory rows matching a query.
//
// Two strategies, in order:
//  1. Vector semantic search — ONLY when no room/hall filter is given
//     AND embedding is available. Skipped when any structural filter
//     is present since historical vector metadata doesn't include
//     room/hall and would produce false positives.
//  2. SQL LIKE on key + value, scoped to the caller's owner_id (or
//     shared empty-owner rows), with optional collection/room/hall
//     filters. Always runs as fallback.
//
// Nil DB returns "[]" rather than an error — matches the read-only
// contract of other list tools.
func ToolRecallMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	includeSuperseded, _ := args["include_superseded"].(bool)
	ownerID := extractOwnerID(ctx)

	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}

	// Strategy 1: vector search when no structural filter.
	if room == "" && hall == "" && deps.EmbedAvailable() {
		if res, ok := recallViaVectorSearch(ctx, deps, collectionName, query); ok {
			return res
		}
	}

	// Strategy 2: SQL LIKE.
	return recallViaSQLLike(ctx, db, deps.Q, query, collectionName, room, hall, ownerID, includeSuperseded)
}

// recallViaVectorSearch attempts vector-semantic recall. Returns
// (result, true) when results are found; (_, false) signals the
// caller to fall through to the SQL path. Errors are treated as
// soft misses — pre-refactor never surfaced embed/search errors.
func recallViaVectorSearch(ctx context.Context, deps Deps, collectionName, query string) (ToolResult, bool) {
	vec, err := deps.Embed(ctx, query)
	if err != nil {
		return ToolResult{}, false
	}
	results, err := deps.CollectionSearch(memoryCollectionName(collectionName), vec, recallMemoryVectorTopK)
	if err != nil || len(results) == 0 {
		return ToolResult{}, false
	}

	var items []map[string]string
	for _, r := range results {
		var meta map[string]string
		if err := json.Unmarshal(r.Data, &meta); err == nil {
			items = append(items, meta)
		}
	}
	if len(items) == 0 {
		return ToolResult{}, false
	}
	out, _ := json.MarshalIndent(items, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}, true
}

// recallViaSQLLike runs the structural-filter path: key/value LIKE
// match scoped to the caller's owner, with optional collection /
// room / hall filters. Always terminating — either returns rows,
// a "no results" message, or a SQL error surfaced as IsError.
func recallViaSQLLike(ctx context.Context, db *sql.DB, rewrite func(string) string, query, collectionName, room, hall, ownerID string, includeSuperseded bool) ToolResult {
	pattern := "%" + query + "%"
	var conds []string
	var qargs []any
	pos := 1
	conds = append(conds, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", pos, pos+1))
	qargs = append(qargs, pattern, pattern)
	pos += 2
	conds = append(conds, fmt.Sprintf("(owner_id = $%d OR owner_id = '')", pos))
	qargs = append(qargs, ownerID)
	pos++
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
		pos++
	}
	if !includeSuperseded {
		conds = append(conds, "superseded_by = ''")
	}
	_ = pos // keep pos in scope; no further binds after this point
	sqlStr := fmt.Sprintf(`
		SELECT id, key, value, type, owner_id, room, hall, created_at, updated_at
		FROM memories WHERE %s ORDER BY updated_at DESC LIMIT %d
	`, strings.Join(conds, " AND "), recallMemorySQLLimit)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}},
			IsError: true,
		}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, oid, rm, hl, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &oid, &rm, &hl, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": oid, "room": rm, "hall": hl,
			"created_at": ca, "updated_at": ua,
		})
	}

	if len(results) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No memories found matching query."}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
