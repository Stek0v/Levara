package mcp

import (
	"sort"
	"strings"

	"github.com/stek0v/levara/internal/contract"
)

// MCPInventory returns every MCP tool exposed via ToolDescriptors() as
// a contract.MCPTool, sorted by name. Tools with no explicit Status
// default to StatusCanonical. Tools with no explicit Group are
// classified by deriveGroup.
func MCPInventory() []contract.MCPTool {
	descs := ToolDescriptors()
	out := make([]contract.MCPTool, 0, len(descs))
	for _, d := range descs {
		status := contract.Status(d.Status)
		if status == "" {
			status = contract.StatusCanonical
		}
		group := d.Group
		if group == "" {
			group = deriveGroup(d.Name)
		}
		out = append(out, contract.MCPTool{
			Name:   d.Name,
			Group:  group,
			Status: status,
		})
	}
	sort.Sort(contract.ByMCPTool(out))
	return out
}

// groupByName maps tool names to their functional group. workspace_* tools
// are derived by prefix in deriveGroup; all other tools are listed here
// explicitly so adding a new tool fails the architecture-contract check
// until it is classified.
var groupByName = map[string]string{
	"save_memory":         "memory",
	"recall_memory":       "memory",
	"list_memories":       "memory",
	"wake_up":             "memory",
	"pin_memory":          "memory",
	"unpin_memory":        "memory",
	"search":              "search",
	"cross_search":        "search",
	"query_entity":        "search",
	"list_communities":    "search",
	"cognify":             "cognify",
	"cognify_status":      "cognify",
	"codify":              "cognify",
	"save_chat":           "chat",
	"recall_chat":         "chat",
	"search_chats":        "chat",
	"add":                 "data",
	"list_data":           "data",
	"delete":              "data",
	"prune":               "data",
	"check_drift":         "data",
	"get_project_context": "context",
	"set_context":         "context",
	"diary_write":         "diary",
	"diary_read":          "diary",
	"sync":                "sync",
	"sync_status":         "sync",
	"add_feedback":        "feedback",
	"get_feedback_stats":  "feedback",
	"doctor":              "ops",
	"levara_instructions": "ops",
	"runtime_stats":       "ops",
	"ingestion_status":    "ops",
	"recent_errors":       "ops",
	"heartbeat":           "ops",
	"analyze_commits":     "git",
	"git_search":          "git",
	"prune_graph":         "git",
}

func deriveGroup(name string) string {
	if strings.HasPrefix(name, "workspace_") {
		return "workspace"
	}
	if g, ok := groupByName[name]; ok {
		return g
	}
	return ""
}
