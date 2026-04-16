package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/ingest"
)

// Deps is the narrow application-state surface that MCP tool
// implementations depend on. The concrete implementation lives in
// internal/http (wrapping APIConfig); tests pass their own fake.
//
// Each F-4 wave adds only the methods the next tool needs, so the seam
// stays minimal and tools can be unit-tested without pulling in the full
// HTTP config (embed client, LLM provider, Cluster, ...). Growth so far:
//   - wave 3a: DB + Q (toolDelete)
//   - wave 3b: no growth (toolPrune reused DB)
//   - wave 3c: + HasCollections + ListCollections (toolListData)
//   - wave 3d: + StoragePath (toolAdd)
//   - wave 3e: + CollectionExists (toolSetContext)
type Deps interface {
	// DB returns the shared *sql.DB used for palace / datasets / graph
	// tables. May be nil when no PostgresDSN is configured — tool
	// implementations must guard against it rather than panic.
	DB() *sql.DB
	// Q rewrites a query's placeholder style and syntax to match the
	// active DB dialect (Postgres passthrough, SQLite translation).
	// Tools should always route queries through Q so the SQLite test
	// builds stay working.
	Q(query string) string
	// HasCollections reports whether a vector-collection manager is
	// configured. Some tools (e.g. list_data) return an empty result
	// when this is false, mirroring a deployment that runs without the
	// vector engine.
	HasCollections() bool
	// ListCollections returns the registered collection names. Returns
	// nil when HasCollections() is false. The slice is safe to iterate
	// but must not be mutated.
	ListCollections() []string
	// StoragePath returns the on-disk directory where ingested files
	// are written. An empty string is treated as the legacy default
	// "data/uploads" by tools that need a path.
	StoragePath() string
	// CollectionExists reports whether a collection with the given name
	// is registered. Always false when HasCollections() is false.
	// Callers use this for soft validation — unknown names are still
	// allowed through ToolSetContext on the "will be created when data
	// is added" promise.
	CollectionExists(name string) bool
}

// ToolDelete deletes a dataset row by id.
//
// The DELETE is best-effort: any SQL error is swallowed to match the
// pre-refactor behavior in internal/http (MCP clients treat the
// response text as the source of truth, not the DB state). Returns an
// error ToolResult only when 'dataset_id' is missing or empty.
func ToolDelete(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	dsID, _ := args["dataset_id"].(string)
	if dsID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'dataset_id' required"}},
			IsError: true,
		}
	}

	if db := deps.DB(); db != nil {
		db.ExecContext(ctx, deps.Q("DELETE FROM datasets WHERE id = $1"), dsID)
	}

	return ToolResult{Content: []Content{{Type: "text", Text: fmt.Sprintf("Dataset %s deleted.", dsID)}}}
}

// pruneTables lists every table cleared by ToolPrune, in the order the
// DELETEs are issued. Child tables come first so a future FK constraint
// wouldn't block the parent delete — today the schema has no cross-table
// FKs enforced, but the order is cheap to keep correct.
var pruneTables = []string{
	"dataset_data",
	"data",
	"datasets",
	"graph_nodes",
	"graph_edges",
}

// ToolPrune clears all dataset, document, and graph rows.
//
// Like ToolDelete, each DELETE is best-effort: SQL errors are swallowed
// to match pre-refactor behavior. The queries are static (no placeholders),
// so Deps.Q is not needed — a plain ExecContext is fine on both Postgres
// and SQLite. nil DB is a silent no-op.
func ToolPrune(ctx context.Context, deps Deps) ToolResult {
	if db := deps.DB(); db != nil {
		for _, table := range pruneTables {
			db.ExecContext(ctx, "DELETE FROM "+table)
		}
	}
	return ToolResult{Content: []Content{{Type: "text", Text: "All data pruned."}}}
}

// listDataItemCap is the LIMIT applied to the data / datasets SELECTs.
// Matches the pre-refactor numbers — 200 for filtered data rows, 100
// for the unfiltered datasets listing.
const (
	listDataItemCap     = 200
	listDataDatasetsCap = 100
)

// ToolListData lists the available data for MCP clients.
//
// Three modes, chosen by argument shape:
//   - Collections not configured → "[]" (deployments without the vector
//     engine surface nothing here, matching the pre-refactor contract).
//   - With "room" or "tags" filter → SELECT from the data table.
//   - Otherwise → collection names from the manager + recent datasets.
//
// All DB errors are swallowed; a failing query simply contributes no
// items to the output.
func ToolListData(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	if !deps.HasCollections() {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}

	var wantTags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				wantTags = append(wantTags, s)
			}
		}
	}
	roomFilter, _ := args["room"].(string)
	hasFilter := len(wantTags) > 0 || roomFilter != ""

	var items []map[string]any
	if !hasFilter {
		for _, c := range deps.ListCollections() {
			items = append(items, map[string]any{"collection": c, "type": "vector_collection"})
		}
	}

	if db := deps.DB(); db != nil {
		if hasFilter {
			items = append(items, listDataFiltered(ctx, db, deps.Q, roomFilter, wantTags)...)
		} else {
			items = append(items, listDataUnfiltered(ctx, db, deps.Q)...)
		}
	}

	out, _ := json.MarshalIndent(items, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// listDataFiltered runs the tag/room-scoped SELECT against the data
// table. Returns an empty slice if the query fails — MCP callers
// distinguish "no match" from "error" by checking the enclosing
// ToolResult.IsError flag, which the filtered path never sets.
func listDataFiltered(ctx context.Context, db *sql.DB, rewrite func(string) string, roomFilter string, wantTags []string) []map[string]any {
	var conds []string
	var qargs []any
	pos := 1
	if roomFilter != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, roomFilter)
		pos++
	}
	for _, t := range wantTags {
		// JSON tag list is stored as a string like ["a","b"]; LIKE works
		// on both PG and SQLite because we match the quoted tag token.
		conds = append(conds, fmt.Sprintf("tags LIKE $%d", pos))
		qargs = append(qargs, "%\""+t+"\"%")
		pos++
	}
	sqlStr := `SELECT id, name, extension, room, tags FROM data`
	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlStr += fmt.Sprintf(" ORDER BY created_at DESC LIMIT %d", listDataItemCap)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, name, ext, rm, tg string
		rows.Scan(&id, &name, &ext, &rm, &tg)
		out = append(out, map[string]any{
			"id":        id,
			"name":      name,
			"extension": ext,
			"room":      rm,
			"tags":      json.RawMessage(tg),
			"type":      "data",
		})
	}
	return out
}

// listDataUnfiltered lists the most recent datasets (used when no
// room/tags filter is supplied).
func listDataUnfiltered(ctx context.Context, db *sql.DB, rewrite func(string) string) []map[string]any {
	rows, err := db.QueryContext(ctx, rewrite(fmt.Sprintf("SELECT id, name FROM datasets ORDER BY created_at DESC LIMIT %d", listDataDatasetsCap)))
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var id, name string
		rows.Scan(&id, &name)
		out = append(out, map[string]any{"id": id, "name": name, "type": "dataset"})
	}
	return out
}

// defaultStoragePath is used when Deps.StoragePath() returns empty.
// Matches the pre-refactor fallback in internal/http.
const defaultStoragePath = "data/uploads"

// mcpToolAddOwnerID is the owner ID stamped on records produced by the
// MCP add tool. Empty today because MCP tool calls run without user
// context; callers that need attribution should use the HTTP /add
// endpoint which reads the authenticated user from the JWT.
const mcpToolAddOwnerID = ""

// ToolAdd ingests raw text into a named dataset.
//
// Two side effects in order:
//  1. ingest.Ingest writes the raw bytes to disk under Deps.StoragePath
//     and returns Result rows with hashes + file paths.
//  2. When Deps.DB() is configured, ingest.MetadataWriter commits a
//     transaction inserting the dataset row (if new) and one data +
//     dataset_data link per result.
//
// DB metadata failures are silently swallowed to match pre-refactor
// behavior — the filesystem ingest is the authoritative side effect.
// Ingest failures, however, surface as IsError results because they
// indicate the data was not persisted at all.
func ToolAdd(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'data' required"}},
			IsError: true,
		}
	}

	datasetName, _ := args["dataset_name"].(string)
	if datasetName == "" {
		datasetName = "default"
	}

	storagePath := deps.StoragePath()
	if storagePath == "" {
		storagePath = defaultStoragePath
	}

	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	room, _ := args["room"].(string)

	items := []ingest.Item{{
		Text:        data,
		DatasetName: datasetName,
		OwnerID:     mcpToolAddOwnerID,
		Tags:        tags,
		Room:        room,
	}}
	results, err := ingest.Ingest(items, storagePath)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Ingest error: %s", err.Error())}},
			IsError: true,
		}
	}

	dsID := uuid.New().String()
	if db := deps.DB(); db != nil {
		mw := ingest.NewMetadataWriterFromDB(db)
		// Pre-refactor used context.Background() here rather than the
		// tool's ctx. Preserved for byte-for-byte parity — the metadata
		// write must not be cancelled if the MCP client disconnects
		// mid-call, since the filesystem side has already committed.
		mw.WriteMetadata(context.Background(), results, mcpToolAddOwnerID, dsID, datasetName)
	}

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Data ingested into dataset '%s' (dataset_id: %s, items: %d). Use 'cognify' tool to build knowledge graph.", datasetName, dsID, len(results)),
	}}}
}

// feedbackQueryLogMaxLen caps how much of the query text we echo back in
// ToolAddFeedback's success message. The DB row still carries the full
// query; this limit only affects the human-readable response.
const feedbackQueryLogMaxLen = 50

// ToolAddFeedback records user feedback on a search result.
//
// Validation: query required, rating must be 1..5, DB must be configured
// (unlike other tools, feedback has no useful degraded mode — if the
// feedback table can't be written, the caller should know).
//
// The SQL insert is fire-and-forget: error return from ExecContext is
// ignored to match pre-refactor behavior, since the duplicate-id
// collision can be a benign retry.
func ToolAddFeedback(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}
	rating := 0
	if r, ok := args["rating"].(float64); ok {
		rating = int(r)
	}
	if rating < 1 || rating > 5 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'rating' must be 1-5"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	resultID, _ := args["result_id"].(string)
	collection, _ := args["collection"].(string)
	searchType, _ := args["search_type"].(string)
	comment, _ := args["comment"].(string)
	userID := ""
	if uid, ok := ctx.Value(UserIDKey).(string); ok {
		userID = uid
	}

	id := uuid.New().String()
	db.ExecContext(ctx, deps.Q(`
		INSERT INTO search_feedback (id, query, result_id, collection, search_type, rating, comment, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`), id, query, resultID, collection, searchType, rating, comment, userID)

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Feedback saved: rating=%d for query '%s'", rating, Truncate(query, feedbackQueryLogMaxLen)),
	}}}
}

// ToolGetFeedbackStats aggregates the search_feedback table.
//
// Optional "collection" filter scopes to one collection. Nil DB is not
// an error here — returns {"total":0} to match pre-refactor behavior
// (some deployments have MCP without feedback logging).
func ToolGetFeedbackStats(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: `{"total":0}`}}}
	}
	collection, _ := args["collection"].(string)

	var total int
	var avgRating float64
	var worstQuery string

	if collection != "" {
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback WHERE collection = $1`),
			collection).Scan(&total, &avgRating)
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COALESCE(query,'') FROM search_feedback WHERE collection = $1 ORDER BY rating ASC LIMIT 1`),
			collection).Scan(&worstQuery)
	} else {
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback`)).Scan(&total, &avgRating)
		db.QueryRowContext(ctx,
			deps.Q(`SELECT COALESCE(query,'') FROM search_feedback ORDER BY rating ASC LIMIT 1`)).Scan(&worstQuery)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"total":       total,
		"avg_rating":  avgRating,
		"worst_query": worstQuery,
		"collection":  collection,
	}, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// ToolSetContext binds a default collection to the caller's session.
// Subsequent tool calls that omit the "collection" argument will use
// this default (see ResolveCollection).
//
// Unknown collection names are accepted with a "will be created when
// data is added" note — this lets clients pre-set a context before the
// first write. A nil session is treated as a client error (initialize
// was skipped).
func ToolSetContext(sess *Session, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'collection' required"}},
			IsError: true,
		}
	}
	if sess == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: no active session (send initialize first)"}},
			IsError: true,
		}
	}

	exists := deps.CollectionExists(collection)
	sess.DefaultCollection = collection

	status := "set"
	if !exists {
		status = "set (collection not yet created — will be used when data is added)"
	}
	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Context %s: default collection = '%s'", status, collection),
	}}}
}

// ── memory (palace) tools ──

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
		pos++
	}

	sqlStr := `SELECT id, key, value, type, owner_id, room, hall, is_pinned, pin_priority, created_at, updated_at FROM memories`
	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}
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
		WHERE is_pinned = 1 AND (owner_id = $1 OR owner_id = '')`
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

// ── chat history tools ──

const (
	// recallChatLimit caps messages returned by ToolRecallChat.
	recallChatLimit = 100
	// searchChatsLimit caps results returned by ToolSearchChats.
	searchChatsLimit = 20
)

// ToolSaveChat persists a batch of chat messages to the interactions
// table, one row per message. The role → column mapping preserves
// pre-refactor semantics: user messages go into `query`, everything
// else (assistant, system, tool) goes into `response`.
//
// Nil DB is a hard error — chat storage is the tool's only purpose.
// Individual message inserts are fire-and-forget; the saved count
// reflects non-empty role+content pairs only.
func ToolSaveChat(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'session_id' required"}},
			IsError: true,
		}
	}
	messagesRaw, ok := args["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'messages' array required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}

	saved := 0
	insertSQL := deps.Q(`
		INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`)
	for _, msgRaw := range messagesRaw {
		msg, ok := msgRaw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "" || content == "" {
			continue
		}

		id := uuid.New().String()
		now := time.Now().UTC()

		query, response := "", ""
		if role == "user" {
			query = content
		} else {
			response = content
		}

		db.ExecContext(ctx, insertSQL, id, sessionID, "", query, response, "chat", now)
		saved++
	}

	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Saved %d messages to session %s", saved, sessionID),
	}}}
}

// ToolRecallChat returns the first recallChatLimit messages in the
// session's interaction history, ordered by created_at ASC (oldest
// first — matches pre-refactor, preserves chronological reading).
//
// Each DB row emits up to two ToolResult items: one for the query
// (role=user) and one for the response (role=assistant). Empty
// columns are skipped, so a user-only message produces one item.
//
// Nil DB returns "[]" rather than an error — matches the read-only
// contract other list tools use.
func ToolRecallChat(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'session_id' required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}

	rows, err := db.QueryContext(ctx, deps.Q(fmt.Sprintf(`
		SELECT id, query, response, created_at FROM interactions
		WHERE session_id = $1 ORDER BY created_at LIMIT %d
	`, recallChatLimit)), sessionID)
	if err != nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var messages []map[string]any
	for rows.Next() {
		var id, query, response, ca string
		if err := rows.Scan(&id, &query, &response, &ca); err != nil {
			continue
		}
		if query != "" {
			messages = append(messages, map[string]any{
				"role": "user", "content": query, "created_at": ca,
			})
		}
		if response != "" {
			messages = append(messages, map[string]any{
				"role": "assistant", "content": response, "created_at": ca,
			})
		}
	}

	if len(messages) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No messages found for session."}}}
	}
	out, _ := json.MarshalIndent(messages, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// ToolSearchChats does a LIKE-based substring search across the
// query + response columns of interactions. Case-sensitivity follows
// the active DB dialect — Postgres ILIKE is not used here; callers
// who need case-insensitive search should pass the query in the
// expected casing or pre-filter.
//
// Results are ordered by created_at DESC (newest first) and capped
// at searchChatsLimit rows.
func ToolSearchChats(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'query' required"}},
			IsError: true,
		}
	}
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}

	pattern := "%" + query + "%"
	rows, err := db.QueryContext(ctx, deps.Q(fmt.Sprintf(`
		SELECT id, session_id, query, response, created_at FROM interactions
		WHERE query LIKE $1 OR response LIKE $2
		ORDER BY created_at DESC LIMIT %d
	`, searchChatsLimit)), pattern, pattern)
	if err != nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, sid, q, r, ca string
		if err := rows.Scan(&id, &sid, &q, &r, &ca); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "session_id": sid, "query": q, "response": r, "created_at": ca,
		})
	}

	if len(results) == 0 {
		return ToolResult{Content: []Content{{Type: "text", Text: "No matching chats found."}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// ── agent diary tools ──

// diaryReadLimit caps the number of entries ToolDiaryRead returns.
const diaryReadLimit = 100

// ToolDiaryWrite inserts or updates a diary memory scoped to a
// subagent (owner_id = "agent:<agent>"). Upsert semantics: same key
// under the same owner replaces value + collection + updated_at.
//
// The original pre-refactor SQL reused placeholders ($3, $5, $7 in
// both VALUES and DO UPDATE SET). The rewrite here uses unique
// placeholders ($8, $9, $10) instead, which means we don't need
// QArgs on the Deps interface — consistent with wave 3f's pin/unpin
// simplification. Behavior is byte-for-byte identical.
func ToolDiaryWrite(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	agent, _ := args["agent"].(string)
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if agent == "" || key == "" || value == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'agent', 'key', 'value' required"}},
			IsError: true,
		}
	}
	collectionName, _ := args["collection"].(string)
	owner := DiaryOwner(agent)
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	// value / collectionName / now repeat verbatim in the args slice
	// ($8/$9/$10) to keep the DO UPDATE SET clause readable without
	// QArgs placeholder-reuse machinery.
	_, err := db.ExecContext(ctx, deps.Q(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, 'diary', $4, $5, '', '', 0, 0, $6, $7)
		ON CONFLICT(key, owner_id) DO UPDATE SET value = $8, collection_name = $9, updated_at = $10
	`),
		id, key, value, owner, collectionName, now, now,
		value, collectionName, now)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}
	return ToolResult{Content: []Content{{
		Type: "text",
		Text: fmt.Sprintf("Diary[%s] wrote %s", agent, key),
	}}}
}

// ToolDiaryRead returns diary entries for a subagent, optionally
// filtered by query substring (matches key OR value) and/or
// collection. Nil DB returns "[]" rather than an error.
func ToolDiaryRead(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{Content: []Content{{Type: "text", Text: "[]"}}}
	}
	agent, _ := args["agent"].(string)
	if agent == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'agent' required"}},
			IsError: true,
		}
	}
	owner := DiaryOwner(agent)
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
	sqlStr := fmt.Sprintf(`
		SELECT key, value, created_at, updated_at FROM memories
		WHERE %s ORDER BY updated_at DESC LIMIT %d
	`, strings.Join(conds, " AND "), diaryReadLimit)

	rows, err := db.QueryContext(ctx, deps.Q(sqlStr), qargs...)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
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
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Diary[%s] is empty", agent),
		}}}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// ── knowledge graph query ──

const (
	// queryEntityNodeResolveLimit caps how many nodes we resolve for a
	// given entity name. Rarely more than one, but duplicates can exist
	// if the same name was imported from distinct sources.
	queryEntityNodeResolveLimit = 10
	// queryEntityEdgeLimit caps the number of edges returned per call.
	// Default value used when no "limit" arg is supplied.
	queryEntityEdgeLimit = 50
)

// ToolQueryEntity returns all graph edges touching the named entity,
// filtered by validity.
//
// Validity modes:
//  - No "as_of": only currently-active edges (valid_until is NULL or
//    in the future relative to CURRENT_TIMESTAMP).
//  - "as_of" supplied: temporal snapshot — edges whose validity window
//    (valid_from, valid_until) includes the given timestamp.
//
// Returns a not-found message (IsError=false) when the entity name
// resolves to zero nodes — an unknown name is a reasonable query, not
// a client error.
func ToolQueryEntity(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: database not configured"}},
			IsError: true,
		}
	}
	name, _ := args["name"].(string)
	if name == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'name' required"}},
			IsError: true,
		}
	}
	asOf, _ := args["as_of"].(string)
	limit := queryEntityEdgeLimit
	if l, ok := args["limit"].(float64); ok && l > 0 {
		limit = int(l)
	}

	nodeIDs := resolveEntityNodes(ctx, db, deps.Q, name)
	if len(nodeIDs) == 0 {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("No entity found with name '%s'", name),
		}}}
	}

	edges, err := queryEntityEdges(ctx, db, deps.Q, nodeIDs, asOf, limit)
	if err != nil {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: " + err.Error()}},
			IsError: true,
		}
	}

	resp := map[string]any{
		"entity":   name,
		"as_of":    asOf,
		"node_ids": nodeIDs,
		"edges":    edges,
	}
	out, _ := json.MarshalIndent(resp, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// resolveEntityNodes returns the graph node IDs that share the given
// entity name. Capped at queryEntityNodeResolveLimit; silent failure
// returns an empty slice (callers surface "No entity found").
func resolveEntityNodes(ctx context.Context, db *sql.DB, rewrite func(string) string, name string) []string {
	rows, err := db.QueryContext(ctx, rewrite(fmt.Sprintf(
		"SELECT id FROM graph_nodes WHERE name = $1 LIMIT %d", queryEntityNodeResolveLimit,
	)), name)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// queryEntityEdges fetches edges touching any of nodeIDs (as source
// or target), applying the active-now or as_of-based validity filter.
// Returns SQL errors to the caller since a malformed query here is a
// real fault, not a not-found.
func queryEntityEdges(ctx context.Context, db *sql.DB, rewrite func(string) string, nodeIDs []string, asOf string, limit int) ([]map[string]any, error) {
	srcPlaceholders := make([]string, 0, len(nodeIDs))
	tgtPlaceholders := make([]string, 0, len(nodeIDs))
	qargs := make([]any, 0, len(nodeIDs)*2+2)
	pos := 1
	for _, id := range nodeIDs {
		srcPlaceholders = append(srcPlaceholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}
	for _, id := range nodeIDs {
		tgtPlaceholders = append(tgtPlaceholders, fmt.Sprintf("$%d", pos))
		qargs = append(qargs, id)
		pos++
	}

	var validityClause string
	if asOf == "" {
		validityClause = " AND (valid_until IS NULL OR valid_until > CURRENT_TIMESTAMP)"
	} else {
		validityClause = fmt.Sprintf(
			" AND (valid_from IS NULL OR valid_from <= $%d) AND (valid_until IS NULL OR valid_until > $%d)",
			pos, pos+1,
		)
		qargs = append(qargs, asOf, asOf)
		pos += 2
	}

	sqlStr := fmt.Sprintf(`
		SELECT id, source_id, target_id, relationship_name, properties,
			COALESCE(valid_from, ''), COALESCE(valid_until, ''), superseded_by, confidence
		FROM graph_edges
		WHERE (source_id IN (%s) OR target_id IN (%s))%s
		ORDER BY updated_at DESC LIMIT $%d
	`, strings.Join(srcPlaceholders, ","), strings.Join(tgtPlaceholders, ","), validityClause, pos)
	qargs = append(qargs, limit)

	rows, err := db.QueryContext(ctx, rewrite(sqlStr), qargs...)
	if err != nil {
		return nil, err
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
			"id":            id,
			"source_id":     src,
			"target_id":     tgt,
			"relationship":  rel,
			"properties":    json.RawMessage(props),
			"valid_from":    vf,
			"valid_until":   vu,
			"superseded_by": sb,
			"confidence":    conf,
		})
	}
	return edges, nil
}
