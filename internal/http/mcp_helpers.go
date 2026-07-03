package http

import "encoding/json"

// mcpJSONResult wraps any value into an MCP tool result.
func mcpJSONResult(v any) mcpToolResult {
	data, _ := json.MarshalIndent(v, "", "  ")
	return mcpToolResult{
		Content:           []mcpContent{{Type: "text", Text: string(data)}},
		StructuredContent: v,
	}
}
