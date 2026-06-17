package mcp

// ToolDescriptorsLight returns a subset of MCP tools suitable for
// integration with Hermes Agent. Omits admin/ops workspace tools,
// git analysis, feedback, and internal data-management tools that
// an agent conversation would never call.
//
// Kept tools: memory palace, search, workspace context/read/write,
// diaries, cognify, sync, and key observability tools.
// Total: ~19 tools vs 67 in the full set, saving ~14K tokens per LLM call.
func ToolDescriptorsLight() []Tool {
	full := ToolDescriptors()
	keep := map[string]bool{
		// Memory palace — daily use
		"save_memory":         true,
		"recall_memory":       true,
		"list_memories":       true,
		"delete_memory":       true,
		"pin_memory":          true,
		"unpin_memory":        true,
		"wake_up":             true,
		"set_context":         true,
		"get_project_context": true,
		"levara_instructions": true,
		"consolidate":         true,

		// Search — as needed
		"search":       true,
		"cross_search": true,
		"query_entity": true,

		// Workspace — project context
		"workspace_context": true,
		"workspace_read":    true,
		"workspace_search":  true,
		"workspace_write":   true,

		// Diaries — subagent coordination
		"diary_write": true,
		"diary_read":  true,

		// Data listing — browse collections
		"list_data": true,

		// Ingestion — occasional doc indexing
		"cognify":        true,
		"cognify_status": true,
		"codify":         true,

		// Sync — cross-instance
		"sync": true,

		// Ops — health checks
		"doctor":        true,
		"runtime_stats": true,
	}

	var filtered []Tool
	for _, t := range full {
		if keep[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
