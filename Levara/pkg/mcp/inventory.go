package mcp

import (
	"sort"

	"github.com/stek0v/levara/internal/contract"
)

// MCPInventory returns every MCP tool exposed via ToolDescriptors() as
// a contract.MCPTool, sorted by name. Tools with no explicit Status
// default to StatusCanonical.
func MCPInventory() []contract.MCPTool {
	descs := ToolDescriptors()
	out := make([]contract.MCPTool, 0, len(descs))
	for _, d := range descs {
		status := contract.Status(d.Status)
		if status == "" {
			status = contract.StatusCanonical
		}
		out = append(out, contract.MCPTool{
			Name:   d.Name,
			Group:  d.Group,
			Status: status,
		})
	}
	sort.Sort(contract.ByMCPTool(out))
	return out
}
