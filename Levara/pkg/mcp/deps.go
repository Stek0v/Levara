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
