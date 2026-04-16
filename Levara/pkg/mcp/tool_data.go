package mcp

// Dataset-facing tools: delete, prune, list_data, add.
// Extracted from deps.go during F-4 wave 3j-split.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/ingest"
)

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
