package mcp

// Shared OutputSchema constructors for MCP tools (T14).
//
// Every tool returns a ToolResult envelope (Content + IsError). The MCP
// "outputSchema" field on Tool describes the structured payload that
// lives *inside* Content[0].Text — most of our tools marshal JSON into
// that text so MCP clients can parse it. Plain-text tools don't need an
// outputSchema; we document the absence with a small helper.
//
// Keeping the schemas here rather than inline in tools.go lets us share
// common shapes (search result, status, count) and change them in one
// place when the payload evolves. The constructors return fresh maps —
// tools consumers may introspect and won't accidentally mutate a global.

// objectSchema builds {type: "object", properties: {...}, required: [...]}.
func objectSchema(properties map[string]any, required ...string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// stringProp / integerProp / numberProp / booleanProp / arrayProp return
// single-property schema fragments with a description. Saves boilerplate
// at call sites.
func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func integerProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func numberProp(desc string) map[string]any {
	return map[string]any{"type": "number", "description": desc}
}
func booleanProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
func arrayOfStringsProp(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}
func arrayOfObjectsProp(itemSchema map[string]any, desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       itemSchema,
		"description": desc,
	}
}

// searchResultItemSchema describes one hit returned by search / cross_search
// / recall_memory / recall_chat.
func searchResultItemSchema() map[string]any {
	return objectSchema(map[string]any{
		"id":         stringProp("Stable result ID."),
		"score":      numberProp("Fused / rerank score — higher is better."),
		"text":       stringProp("Snippet of chunk text or entity name."),
		"collection": stringProp("Source collection."),
		"metadata":   stringProp("Serialized chunk or entity metadata (room, tags, dataset_id, ...)."),
	})
}

// runStatusSchema describes a pipeline run's observable state — matches
// pkg/runreg.Status exactly.
func runStatusSchema() map[string]any {
	return objectSchema(map[string]any{
		"pipeline_run_id":    stringProp("Run ID."),
		"status":             map[string]any{"type": "string", "enum": []string{"RUNNING", "COMPLETED", "FAILED"}},
		"stage":              stringProp("Current pipeline stage."),
		"message":            stringProp("Human-readable status or error."),
		"chunks_created":     integerProp("Chunks created so far."),
		"entities_extracted": integerProp("Entities extracted so far."),
		"edges_extracted":    integerProp("Graph edges extracted so far."),
		"elapsed_ms":         integerProp("Milliseconds since run start."),
		"started_at":         stringProp("RFC3339 start timestamp."),
	}, "pipeline_run_id", "status")
}

// statusMessageSchema is the generic {ok, message} envelope some tools
// return for confirmation-only operations (pin, unpin, delete, sync ack).
func statusMessageSchema() map[string]any {
	return objectSchema(map[string]any{
		"ok":      booleanProp("True when the operation succeeded."),
		"message": stringProp("Human-readable confirmation or failure reason."),
	})
}
