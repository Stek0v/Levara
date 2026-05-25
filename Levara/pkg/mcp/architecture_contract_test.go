package mcp

import "testing"

func TestToolDescriptorsArchitectureContract(t *testing.T) {
	seen := map[string]bool{}
	for _, tool := range ToolDescriptors() {
		if tool.Name == "" {
			t.Fatal("tool descriptor with empty name")
		}
		if seen[tool.Name] {
			t.Fatalf("duplicate MCP tool descriptor: %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.InputSchema == nil {
			t.Fatalf("tool %s has nil input schema", tool.Name)
		}
		if tool.OutputSchema == nil {
			t.Fatalf("tool %s has nil output schema", tool.Name)
		}
	}

	for _, critical := range []string{
		"set_context",
		"wake_up",
		"save_memory",
		"recall_memory",
		"search",
		"cognify",
		"sync",
		"query_entity",
		"workspace_context",
		"workspace_search",
		"doctor",
	} {
		if !seen[critical] {
			t.Fatalf("critical MCP tool missing from descriptors: %s", critical)
		}
	}
}

func TestMCPInventoryCoversCritical(t *testing.T) {
	got := map[string]bool{}
	for _, m := range MCPInventory() {
		got[m.Name] = true
	}
	for _, name := range []string{"search", "save_memory", "wake_up", "set_context"} {
		if !got[name] {
			t.Fatalf("MCPInventory missing %s", name)
		}
	}
}
