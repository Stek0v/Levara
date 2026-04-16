package mcp

import (
	"context"
	"database/sql"
	"fmt"
)

// Deps is the narrow application-state surface that MCP tool
// implementations depend on. The concrete implementation lives in
// internal/http (wrapping APIConfig); tests pass their own fake.
//
// Each F-4 wave adds only the methods the next tool needs, so the seam
// stays minimal and tools can be unit-tested without pulling in the full
// HTTP config (embed client, LLM provider, Cluster, ...). The first wave
// (3a) ships just DB + Q — enough for toolDelete.
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
