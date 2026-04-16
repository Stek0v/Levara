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
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/runreg"
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
//   - wave 3i: + EmbedAvailable + Embed + CollectionInsert + CollectionSearch
//              (toolSaveMemory + toolRecallMemory) — first AI seam. Types
//              stay small ([]float32 and SearchResult) so pkg/mcp doesn't
//              import pkg/embed or internal/store.
//   - wave 3j: + Runs + BaseCognifyConfig + OntologyPromptSuffix +
//              PersistPipelineStatus + LogHeartbeat + RunPipeline
//              (toolCognify + toolCognifyStatus). BaseCognifyConfig brings
//              in pkg/orchestrator as a new pkg/mcp import; Runs brings in
//              pkg/runreg. RunPipeline abstracts orchestrator.Run so the
//              cognify goroutine is testable without the real LLM/embed
//              stack.
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
	// EmbedAvailable reports whether the embedding service + collection
	// manager are both configured. Memory tools fall back to SQL-only
	// paths when false. Cheap boolean gate — separate from HasCollections
	// because some deployments have a vector engine without an embed
	// service.
	EmbedAvailable() bool
	// Embed generates a single-vector embedding for the given text.
	// Caller should guard with EmbedAvailable(); implementations may
	// panic or error if called without a configured service.
	Embed(ctx context.Context, text string) ([]float32, error)
	// CollectionInsert adds a vector + metadata to a named collection.
	// Best-effort — callers ignore the error (matches pre-refactor
	// fire-and-forget ingest behavior for vector-indexed memories).
	CollectionInsert(collection, id string, vec []float32, meta any) error
	// CollectionSearch finds the topK nearest vectors in a named
	// collection. Returns an empty slice + nil error when no matches
	// (caller distinguishes "no hits" from "error" via the err return).
	CollectionSearch(collection string, query []float32, topK int) ([]SearchResult, error)
	// Runs returns the shared pipeline-run registry. Cognify stores a
	// *runreg.Status here so cognify_status (and the REST SSE stream in
	// internal/http) can read progress updates. The concrete pointer is
	// shared across the whole server — MCP-initiated and REST-initiated
	// runs coexist in the same map.
	Runs() *runreg.Registry
	// BaseCognifyConfig returns an orchestrator.Config pre-populated with
	// deployment-level settings: embed endpoint/model, LLM endpoint/model,
	// LLM provider + cache, Neo4j credentials, collection manager, BM25
	// indexes, DB handle. Tool-level fields (Collection, DatasetID, Room,
	// Tags, SystemPrompt, SkipGraph, chunking overrides, ...) are the
	// caller's responsibility and override the returned base.
	BaseCognifyConfig() orchestrator.Config
	// OntologyPromptSuffix returns the ontology-guided extraction text to
	// append to the system prompt for a given collection. Returns empty
	// string when no ontology is configured for the collection. Harmless
	// to concatenate unconditionally.
	OntologyPromptSuffix(collection string) string
	// PersistPipelineStatus writes terminal pipeline state to the data
	// table so subsequent /cognify calls can skip already-processed
	// datasets. Best-effort — errors are swallowed to match the
	// pre-refactor signature which takes no error return.
	PersistPipelineStatus(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64)
	// LogHeartbeat records an event (arbitrary payload) into the
	// heartbeats table when DB is configured, no-op otherwise. Used by
	// long-running tools (cognify, sync, prune) for observability.
	LogHeartbeat(eventType string, payload any)
	// RunPipeline runs the cognify orchestrator end-to-end against texts
	// with the given config, emitting Progress on progressCh and closing
	// it when done. Production wiring calls orchestrator.Run; tests can
	// substitute a stub that closes the channel immediately to exercise
	// the post-run bookkeeping (status transition, persist, heartbeat)
	// without the real LLM + embed stack.
	RunPipeline(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error
}

// SearchResult is one entry returned by CollectionSearch. Kept small
// and type-clean so pkg/mcp doesn't need to import internal/store's
// VectroRecord. Data carries raw JSON metadata that callers unmarshal
// on demand.
type SearchResult struct {
	ID    string
	Score float32
	Data  []byte
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

// ── memory palace: save + recall (AI-dependent) ──

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

// memoryCollectionName returns the vector-collection name where
// memory embeddings live. "_memories" by default, with per-collection
// shards like "_memories_levara" when the user pinned a context.
func memoryCollectionName(collectionName string) string {
	if collectionName != "" {
		return "_memories_" + collectionName
	}
	return "_memories"
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
	_, err := db.ExecContext(ctx, deps.Q(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT(key, owner_id) DO UPDATE SET value = $13, type = $14, collection_name = $15, room = $16, hall = $17, is_pinned = $18, pin_priority = $19, updated_at = $20
	`),
		id, key, value, memType, ownerID, collectionName, room, hall, pinInt, pinPriority, now, now,
		value, memType, collectionName, room, hall, pinInt, pinPriority, now)
	if err != nil {
		// Swallow DB errors — matches pre-refactor best-effort contract.
		// The pre-refactor version logged via cfg.Logger if available;
		// logger is deliberately not on Deps (small surface > observability
		// plumbing). Callers reading this code can add Logger() later if
		// needed.
		_ = err
	}

	if deps.EmbedAvailable() {
		indexMemoryAsync(deps, collectionName, id, key, value, memType)
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
	return recallViaSQLLike(ctx, db, deps.Q, query, collectionName, room, hall, ownerID)
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
func recallViaSQLLike(ctx context.Context, db *sql.DB, rewrite func(string) string, query, collectionName, room, hall, ownerID string) ToolResult {
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

// cognifyProgressBufSize matches the pre-refactor channel capacity on
// internal/http/mcp.go. 100 is enough slack that orchestrator stages
// never block emitting progress while the tool goroutine's reader loop
// is running a map update.
const cognifyProgressBufSize = 100

// cognifyDefaultCollection is the collection name used when the caller
// does not supply one. Must match the REST default in api.go so both
// paths converge on the same vector store.
const cognifyDefaultCollection = "default"

// ToolCognify starts a background cognify pipeline run.
//
// Returns immediately with a RUNNING status entry in the registry; the
// caller polls via cognify_status (or subscribes to the REST SSE stream)
// to observe progress. The pipeline goroutine runs under
// context.Background so an MCP client disconnect during ingestion does
// not cancel the work mid-way.
//
// Error branches that produce IsError=true:
//   - Missing 'data' arg.
//   - EmbedEndpoint not configured (registry gets FAILED state first).
//
// Successful start returns a human-readable RunID pointer; the caller
// feeds that ID back into cognify_status.
func ToolCognify(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return ToolResult{Content: []Content{{Type: "text", Text: "Error: 'data' parameter required"}}, IsError: true}
	}

	runID := uuid.New().String()
	collection, _ := args["collection"].(string)
	if collection == "" {
		collection = cognifyDefaultCollection
	}

	status := &runreg.Status{
		RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
	}
	deps.Runs().Store(runID, status)

	pipeCfg := deps.BaseCognifyConfig()
	if pipeCfg.EmbedEndpoint == "" {
		status.Status = "FAILED"
		status.Message = "Embedding service not configured (EMBED_ENDPOINT)"
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: embedding service not configured"}},
			IsError: true,
		}
	}

	pipeCfg.Collection = collection
	pipeCfg.DatasetID = runID
	pipeCfg.GenerateTriplets = true
	trueVal := true
	pipeCfg.UseStructuredOutput = &trueVal

	// RAG mode: skip graph extraction (chunk+embed only, no LLM needed)
	if mode, _ := args["mode"].(string); mode == "rag" {
		pipeCfg.SkipGraph = true
		pipeCfg.GenerateTriplets = false
	}
	if cs, _ := args["chunk_strategy"].(string); cs != "" {
		pipeCfg.ChunkStrategy = cs
	}
	if oc, ok := args["overlap_chars"].(float64); ok && oc > 0 {
		pipeCfg.OverlapChars = int(oc)
	}
	if snap, ok := args["snap_to_sentence"].(bool); ok {
		pipeCfg.SnapToSentence = &snap
	}
	if pc, ok := args["parent_child"].(bool); ok && pc {
		pipeCfg.ParentChild = true
	}
	if dt, _ := args["document_title"].(string); dt != "" {
		pipeCfg.DocumentTitle = dt
	}
	if di, _ := args["document_id"].(string); di != "" {
		pipeCfg.DocumentID = di
	}
	if cr, ok := args["community_resolution"].(float64); ok && cr > 0 {
		pipeCfg.CommunityResolution = cr
	}
	if dt, ok := args["dedup_threshold"].(float64); ok && dt > 0 {
		pipeCfg.DedupThreshold = dt
	}
	if minC, ok := args["min_chunk_chars"].(float64); ok && minC > 0 {
		pipeCfg.MinChunkChars = int(minC)
	}
	if maxC, ok := args["max_chunk_chars"].(float64); ok && maxC > 0 {
		pipeCfg.MaxChunkChars = int(maxC)
	}
	if cp, _ := args["custom_prompt"].(string); cp != "" {
		pipeCfg.SystemPrompt = cp
	}
	if room, _ := args["room"].(string); room != "" {
		pipeCfg.Room = room
	}
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				pipeCfg.Tags = append(pipeCfg.Tags, s)
			}
		}
	}
	if suffix := deps.OntologyPromptSuffix(collection); suffix != "" {
		pipeCfg.SystemPrompt += suffix
	}

	texts := []string{data}

	go runCognifyPipeline(deps, runID, collection, texts, pipeCfg, status)

	return ToolResult{
		Content: []Content{{
			Type: "text",
			Text: fmt.Sprintf("Cognify pipeline started. Run ID: %s. Use cognify_status tool to check progress.", runID),
		}},
	}
}

// runCognifyPipeline drives the orchestrator to completion and performs
// post-run bookkeeping: final status update, PersistPipelineStatus,
// heartbeat log. Extracted into its own function so the tool body stays
// readable and so tests that install a pipelineFn can also verify the
// bookkeeping runs after the stub finishes.
func runCognifyPipeline(deps Deps, runID, collection string, texts []string, pipeCfg orchestrator.Config, status *runreg.Status) {
	progressCh := make(chan orchestrator.Progress, cognifyProgressBufSize)
	errCh := make(chan error, 1)

	go func() {
		errCh <- deps.RunPipeline(context.Background(), texts, pipeCfg, progressCh)
	}()

	for p := range progressCh {
		status.Stage = p.Stage
		status.Message = p.Message
		status.Chunks = p.ChunksCreated
		status.Entities = p.EntitiesExtracted
		status.Edges = p.EdgesExtracted
		status.ElapsedMs = p.ElapsedMs
	}

	if err := <-errCh; err != nil {
		status.Status = "FAILED"
		status.Message = err.Error()
	} else {
		status.Status = "COMPLETED"
	}
	status.ElapsedMs = time.Since(status.StartedAt).Milliseconds()

	deps.PersistPipelineStatus(runID, collection,
		status.Status, status.Chunks, status.Entities, status.Edges, status.ElapsedMs)

	deps.LogHeartbeat("cognify", map[string]any{
		"run_id":     runID,
		"collection": collection,
		"status":     status.Status,
		"chunks":     status.Chunks,
		"entities":   status.Entities,
		"elapsed_ms": status.ElapsedMs,
	})
}

// ToolCognifyStatus returns the current state of a pipeline run as
// pretty-printed JSON. IsError=true when run_id is missing or unknown.
// Successful lookup returns the Status struct JSON so the caller can see
// stage, progress counters, and message fields.
func ToolCognifyStatus(deps Deps, args map[string]any) ToolResult {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return ToolResult{Content: []Content{{Type: "text", Text: "Error: 'run_id' required"}}, IsError: true}
	}

	val, ok := deps.Runs().Load(runID)
	if !ok {
		return ToolResult{Content: []Content{{Type: "text", Text: fmt.Sprintf("Run %s not found.", runID)}}, IsError: true}
	}

	out, _ := json.MarshalIndent(val, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}
