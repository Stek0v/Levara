package mcp

import (
	"encoding/json"
	"testing"
)

// Smoke tests for the MCP tool registry. These lock in the contract that
// Claude Code, Cursor, Cline, and other MCP clients see when they call
// tools/list — field names + required fields + inputSchema validity.

func TestToolDescriptors_NotEmpty(t *testing.T) {
	tools := ToolDescriptors()
	if len(tools) < 15 {
		t.Errorf("got %d tools, want ≥ 15 (Levara advertises ~25)", len(tools))
	}
}

func TestToolDescriptors_EveryToolHasRequiredFields(t *testing.T) {
	for _, tool := range ToolDescriptors() {
		if tool.Name == "" {
			t.Errorf("tool with empty Name: %+v", tool)
		}
		if tool.Description == "" {
			t.Errorf("tool %q missing Description", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q missing InputSchema", tool.Name)
			continue
		}
		// MCP clients reject schemas without type=object.
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q: InputSchema.type = %v, want 'object'",
				tool.Name, tool.InputSchema["type"])
		}
	}
}

func TestToolDescriptors_NamesUnique(t *testing.T) {
	seen := make(map[string]int)
	for _, tool := range ToolDescriptors() {
		seen[tool.Name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("tool %q appears %d times", name, n)
		}
	}
}

func TestToolDescriptors_FreshSlicePerCall(t *testing.T) {
	// Callers must not be able to corrupt the canonical list by mutating the
	// returned slice. The function returns a new slice literal each call.
	a := ToolDescriptors()
	a[0].Name = "CORRUPTED"
	b := ToolDescriptors()
	if b[0].Name == "CORRUPTED" {
		t.Error("ToolDescriptors returned a shared slice — callers can corrupt")
	}
}

func TestToolDescriptors_JSONMarshalsCleanly(t *testing.T) {
	// MCP wire format: `{"tools": [...]}`. Our tools must round-trip through
	// json.Marshal/Unmarshal so the response handler can emit them verbatim.
	tools := ToolDescriptors()
	raw, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Tools) != len(tools) {
		t.Errorf("roundtrip length = %d, want %d", len(got.Tools), len(tools))
	}
	// Spot-check first tool's name survives the roundtrip.
	if got.Tools[0]["name"] != tools[0].Name {
		t.Errorf("first tool name lost: got %v, want %q",
			got.Tools[0]["name"], tools[0].Name)
	}
}

func TestToolDescriptors_RequiredCoreTools(t *testing.T) {
	// These tool names are referenced by the dispatch switch in the handler
	// and documented in CLAUDE.md. Losing one = silent feature breakage for
	// every MCP client out there.
	required := []string{
		"cognify", "search", "list_data", "delete",
		"save_memory", "recall_memory", "set_context",
	}
	have := make(map[string]struct{})
	for _, t := range ToolDescriptors() {
		have[t.Name] = struct{}{}
	}
	for _, name := range required {
		if _, ok := have[name]; !ok {
			t.Errorf("core tool %q missing from registry", name)
		}
	}
}
