package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

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
