package mcp

import "strings"

var toolProfiles = map[string][]string{
	"core": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "list_memories", "pin_memory", "unpin_memory",
		"search", "doctor",
	},
	"memory": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "list_memories", "pin_memory", "unpin_memory", "delete_memory",
		"search", "doctor", "consolidate", "consolidation_status", "consolidation_revert", "diary_write", "diary_read",
		"add_feedback", "get_feedback_stats",
	},
	"workspace": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "list_memories", "pin_memory", "unpin_memory", "search", "doctor",
		"workspace_context", "workspace_search", "workspace_read", "workspace_write", "workspace_commit", "workspace_conflicts",
	},
	"ops": {
		"levara_instructions", "doctor", "runtime_stats", "ingestion_status", "recent_errors", "heartbeat",
		"reconcile_memory", "sync", "sync_status", "workspace_ops_status", "workspace_index_jobs",
		"memory_index_status", "memory_index_retry",
		"workspace_watch_status", "workspace_audit_log", "workspace_conflicts",
	},
}

// ToolsetName returns the stable effective profile. Empty and unknown values
// intentionally remain full for backward compatibility. light is the legacy
// conversational alias and resolves to memory.
func ToolsetName(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "light" {
		return "memory"
	}
	if mode == "full" || mode == "" {
		return "full"
	}
	if _, ok := toolProfiles[mode]; ok {
		return mode
	}
	return "full"
}

func ToolDescriptorsForMode(mode string) []Tool {
	effective := ToolsetName(mode)
	if effective == "full" {
		return ToolDescriptors()
	}
	keep := make(map[string]bool, len(toolProfiles[effective]))
	for _, name := range toolProfiles[effective] {
		keep[name] = true
	}
	full := ToolDescriptors()
	filtered := make([]Tool, 0, len(keep))
	for _, tool := range full {
		if keep[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

func ToolDescriptorsLight() []Tool { return ToolDescriptorsForMode("memory") }

func ToolAllowedForMode(mode, name string) bool {
	for _, tool := range ToolDescriptorsForMode(mode) {
		if tool.Name == name {
			return true
		}
	}
	return false
}
