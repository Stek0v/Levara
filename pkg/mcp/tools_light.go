package mcp

import (
	"os"
	"strings"
)

var toolProfiles = map[string][]string{
	"core": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "list_memories", "pin_memory", "unpin_memory",
		"search", "doctor",
	},
	"memory": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "memory_garden", "memory_markdown_digest", "memory_scaffold_block", "list_memories", "pin_memory", "unpin_memory", "delete_memory",
		"search", "doctor", "consolidate", "consolidation_status", "consolidation_revert", "diary_write", "diary_read",
		"supersede_memory", "memory_commit_preview", "memory_commit_apply", "add_feedback", "get_feedback_stats",
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
	"long-horizon": {
		"levara_instructions", "set_context", "get_project_context", "wake_up",
		"save_memory", "recall_memory", "list_memories", "pin_memory", "unpin_memory", "supersede_memory",
		"search", "doctor", "runtime_stats", "recent_errors",
		"task_open", "task_bootstrap", "task_plan", "task_step", "task_checkpoint", "task_receipt", "task_validate", "task_complete",
	},
}

func longHorizonEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_LONG_HORIZON_RUNTIME"))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func memoryCommitEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LEVARA_MEMORY_COMMIT"))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func isLongHorizonTaskTool(name string) bool { return strings.HasPrefix(name, "task_") }

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
		full := ToolDescriptors()
		if longHorizonEnabled() {
			return full
		}
		filtered := make([]Tool, 0, len(full))
		for _, tool := range full {
			if !isLongHorizonTaskTool(tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		return filtered
	}
	keep := make(map[string]bool, len(toolProfiles[effective]))
	for _, name := range toolProfiles[effective] {
		keep[name] = true
	}
	full := ToolDescriptors()
	filtered := make([]Tool, 0, len(keep))
	for _, tool := range full {
		if keep[tool.Name] && (longHorizonEnabled() || !isLongHorizonTaskTool(tool.Name)) {
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
