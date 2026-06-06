package mcp

// AI-backed memory palace tools: save_memory, recall_memory.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
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
	// saveMemoryEmbedTimeout is the hard ceiling on the synchronous
	// embed+index step. A healthy local embed returns in milliseconds; this
	// only bites when the embed service is stuck, bounding how long a save
	// can block before it degrades to a divergence heartbeat.
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
// embedding is available, vector-indexes the key+value pair synchronously
// so the record is immediately recallable (including under a room/hall
// filter — see indexMemorySync for why this is no longer a goroutine).
//
// The SQL path is the source of truth: the row is committed before the
// vector index step, and the save still succeeds even if indexing fails
// (the failure surfaces as a divergence heartbeat, not an error). Indexing
// runs under a detached, bounded context so a fast MCP client disconnect
// can't cancel it mid-flight.
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
		indexMemorySync(deps, collectionName, canonicalID, key, value, memType)
	}

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Memory saved: %s = %s (type: %s)", key, Truncate(value, memoryValueLogMaxLen), memType),
	}}}
}

// indexMemorySync vector-indexes the memory inline — before ToolSaveMemory
// returns — so the fresh record is immediately discoverable by semantic
// recall, including filtered recall_memory(query, room=…).
//
// This used to run in a goroutine. The embed window between the synchronous
// SQL commit and the asynchronous vector insert was a race: a recall fired
// right after a save found nothing via the vector path (the vector wasn't in
// the collection yet) and fell through to the literal SQL-LIKE fallback,
// which misses any paraphrased/semantic query. Indexing synchronously closes
// that window so recall stays on the fast vector path instead of leaning on
// the slower SQL substring scan.
//
// A detached, bounded context decouples the embed/insert from the caller's
// ctx so an MCP client that closes its connection on receipt of the save
// confirmation cannot cancel indexing mid-flight (the property the old
// goroutine bought, kept here without the race). The SQL row is already
// committed (source of truth) and the save still succeeds regardless; every
// failure mode is verified rather than swallowed: an embed error, an insert
// error, or a vector absent on read-back each emit a
// "memory_index_divergence" heartbeat (mirrored to
// levara_memory_index_divergence_total). The insert+verify is retried once;
// the reconcile_memory sweep is the durable backstop beyond that.
func indexMemorySync(deps Deps, collectionName, id, key, value, memType string) {
	embedCtx, cancel := context.WithTimeout(context.Background(), saveMemoryEmbedTimeout)
	defer cancel()

	sidecar := memoryCollectionName(collectionName)

	vec, err := deps.Embed(embedCtx, key+" "+value)
	if err != nil {
		reportMemoryDivergence(deps, sidecar, id, "embed_failed", err.Error())
		return
	}

	meta, _ := json.Marshal(map[string]string{
		"key":        key,
		"value":      value,
		"type":       memType,
		"collection": collectionName,
		"memory_id":  id,
	})

	// Insert, then verify the vector actually landed under the canonical
	// id (synchronous index lookup, not a vector search — see
	// CollectionHasRecord). Retry once on failure.
	for attempt := 1; attempt <= memoryIndexMaxAttempts; attempt++ {
		insErr := deps.CollectionInsert(sidecar, id, vec, meta)
		if insErr != nil {
			if attempt == memoryIndexMaxAttempts {
				reportMemoryDivergence(deps, sidecar, id, "insert_failed", insErr.Error())
			}
			continue
		}
		if deps.CollectionHasRecord(sidecar, id) {
			return // verified present — SQL and vector agree
		}
		if attempt == memoryIndexMaxAttempts {
			reportMemoryDivergence(deps, sidecar, id, "missing_after_insert", "vector absent on read-back")
		}
	}
}

// memoryIndexMaxAttempts bounds the insert+verify retry loop in
// indexMemorySync. One retry covers a transient collection/disk hiccup;
// reconcile_memory is the durable backstop beyond that.
const memoryIndexMaxAttempts = 2

// reportMemoryDivergence records a single SQL↔vector mismatch for the
// memory write path. Routed through LogHeartbeat (the pkg/mcp
// observability seam) so the row is queryable via the heartbeat tools and
// mirrored to Prometheus by the concrete handler — pkg/mcp keeps no
// internal/metrics dependency.
func reportMemoryDivergence(deps Deps, collection, id, reason, detail string) {
	deps.LogHeartbeat("memory_index_divergence", map[string]any{
		"collection": collection,
		"memory_id":  id,
		"reason":     reason,
		"detail":     Truncate(detail, memoryValueLogMaxLen),
		"at":         time.Now().UTC().Format(time.RFC3339),
	})
}

// ToolRecallMemory returns memory rows matching a query.
//
// Two strategies, in order:
//  1. Vector semantic search — runs whenever embedding is available,
//     INCLUDING under a room/hall filter. With no filter it returns the
//     vector metadata directly. With a room/hall filter the hits are
//     hydrated through SQL so the filter applies authoritatively (SQL is
//     the source of truth for room/hall/owner/superseded) while semantic
//     ranking is preserved. Previously any filter forced strategy 2 and
//     silently degraded recall to literal-substring matching, so a
//     semantic query that wasn't a verbatim substring of key/value
//     returned nothing even when a matching row existed.
//  2. SQL LIKE on key + value, scoped to the caller's owner_id (or
//     shared empty-owner rows), with optional collection/room/hall
//     filters. Runs when embedding is unavailable or the vector path
//     yields no usable hit.
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

	// Strategy 1: vector semantic search, always hydrated through SQL so the
	// owner scope (and any optional room/hall filter) is enforced
	// authoritatively without losing semantic recall. The unfiltered path used
	// to return vector hits' Data verbatim with no owner check, leaking other
	// owners' rows; routing every recall through the SQL-hydrated path closes
	// that. Empty/error is a soft miss → fall through to the SQL LIKE path.
	if deps.EmbedAvailable() {
		if res, ok := recallViaVectorFiltered(ctx, deps, db, deps.Q, query, collectionName, room, hall, ownerID, includeSuperseded); ok {
			return res
		}
	}

	// Strategy 2: SQL LIKE.
	return recallViaSQLLike(ctx, db, deps.Q, query, collectionName, room, hall, ownerID, includeSuperseded)
}

// memoryRowColumns is the SELECT list shared by the SQL recall paths so
// recallViaSQLLike and recallViaVectorFiltered hydrate an identical shape.
const memoryRowColumns = "id, key, value, type, owner_id, room, hall, created_at, updated_at"

// appendMemoryFilters appends the structural filters shared by both SQL
// recall paths — owner scope, then optional collection/room/hall, then
// the superseded guard — starting at placeholder position pos. Keeping it
// in one place ensures the LIKE-fallback and vector-hydration paths filter
// identically. Returns the grown conds/qargs and the next free position.
func appendMemoryFilters(conds []string, qargs []any, pos int, collectionName, room, hall, ownerID string, includeSuperseded bool) ([]string, []any, int) {
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
	return conds, qargs, pos
}

// scanMemoryRows decodes rows from a memoryRowColumns SELECT into the JSON
// shape both recall SQL paths return.
func scanMemoryRows(rows *sql.Rows) []map[string]any {
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
	return results
}

// recallViaVectorFiltered runs semantic recall under a room/hall filter.
// Vector search ranks candidates, then SQL hydrates them with the
// structural filters applied authoritatively (SQL is the source of truth
// for room/hall/owner/superseded), preserving vector rank order. Old
// vectors whose metadata predates room/hall propagation still filter
// correctly because the filter runs on the SQL row keyed by the vector id.
// Returns (result, true) on hit; (_, false) to fall through to SQL LIKE —
// embed/search errors and an empty filtered set are all soft misses.
func recallViaVectorFiltered(ctx context.Context, deps Deps, db *sql.DB, rewrite func(string) string, query, collectionName, room, hall, ownerID string, includeSuperseded bool) (ToolResult, bool) {
	vec, err := deps.Embed(ctx, query)
	if err != nil {
		return ToolResult{}, false
	}
	results, err := deps.CollectionSearch(memoryCollectionName(collectionName), vec, recallMemoryVectorTopK)
	if err != nil || len(results) == 0 {
		return ToolResult{}, false
	}

	// Candidate ids in vector-rank order (de-duped). The vector record id
	// is the canonical SQL row id (see indexMemoryAsync), so it keys the
	// hydration directly.
	rank := make(map[string]int, len(results))
	ids := make([]string, 0, len(results))
	for i, r := range results {
		if r.ID == "" {
			continue
		}
		if _, seen := rank[r.ID]; seen {
			continue
		}
		rank[r.ID] = i
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		return ToolResult{}, false
	}

	placeholders := make([]string, len(ids))
	qargs := make([]any, 0, len(ids)+4)
	pos := 1
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", pos)
		qargs = append(qargs, id)
		pos++
	}
	conds := []string{fmt.Sprintf("id IN (%s)", strings.Join(placeholders, ", "))}
	conds, qargs, _ = appendMemoryFilters(conds, qargs, pos, collectionName, room, hall, ownerID, includeSuperseded)
	sqlStr := fmt.Sprintf(`SELECT %s FROM memories WHERE %s`, memoryRowColumns, strings.Join(conds, " AND "))

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return ToolResult{}, false
	}
	defer rows.Close()
	out := scanMemoryRows(rows)
	if len(out) == 0 {
		return ToolResult{}, false
	}

	// SQL IN doesn't preserve order — restore vector-similarity ranking.
	sort.SliceStable(out, func(i, j int) bool {
		return rank[out[i]["id"].(string)] < rank[out[j]["id"].(string)]
	})
	payload, _ := json.MarshalIndent(out, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(payload)}}}, true
}

// recallViaSQLLike runs the structural-filter path: key/value LIKE
// match scoped to the caller's owner, with optional collection /
// room / hall filters. Always terminating — either returns rows,
// a "no results" message, or a SQL error surfaced as IsError.
func recallViaSQLLike(ctx context.Context, db *sql.DB, rewrite func(string) string, query, collectionName, room, hall, ownerID string, includeSuperseded bool) ToolResult {
	pattern := "%" + query + "%"
	conds := []string{"(key LIKE $1 OR value LIKE $2)"}
	qargs := []any{pattern, pattern}
	conds, qargs, _ = appendMemoryFilters(conds, qargs, 3, collectionName, room, hall, ownerID, includeSuperseded)
	sqlStr := fmt.Sprintf(`
		SELECT %s
		FROM memories WHERE %s ORDER BY updated_at DESC LIMIT %d
	`, memoryRowColumns, strings.Join(conds, " AND "), recallMemorySQLLimit)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}},
			IsError: true,
		}
	}
	defer rows.Close()

	results := scanMemoryRows(rows)
	if len(results) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No memories found matching query."}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
