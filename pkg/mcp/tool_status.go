package mcp

// levera_status MCP tool: operational snapshot for agents.
// Wraps the REST /status endpoint data through the handler.

import (
	"context"
)

// ToolLeveraStatus returns the unified operational status from the
// server's /status endpoint (A1). Agents use it before heavy operations
// to check memory pressure, corpus tier, and background job load.
func ToolLeveraStatus(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	body := deps.FetchStatus(ctx)
	if body == "" {
		return errorResult("status endpoint unavailable")
	}
	return jsonRawResult(body)
}

func jsonRawResult(raw string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: raw}}}
}
