package mcp

import (
	"strings"
	"testing"
)

// ToolDescriptorsLight is the /mcp-light contract: exactly the memory
// profile, never task_* tools under the default flags, and strictly a
// subset of the full descriptor set.
func TestToolDescriptorsLightMatchesMemoryProfile(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "")
	t.Setenv("LEVARA_MEMORY_COMMIT", "")

	light := ToolDescriptorsLight()
	if len(light) == 0 {
		t.Fatal("ToolDescriptorsLight returned no tools")
	}
	profile := map[string]bool{}
	for _, name := range toolProfiles["memory"] {
		profile[name] = true
	}
	seen := map[string]bool{}
	for _, tool := range light {
		if !profile[tool.Name] {
			t.Errorf("tool %q not in the memory profile", tool.Name)
		}
		if strings.HasPrefix(tool.Name, "task_") {
			t.Errorf("task tool %q in ToolDescriptorsLight", tool.Name)
		}
		seen[tool.Name] = true
	}
	for _, name := range []string{"save_memory", "recall_memory", "search", "doctor", "levara_instructions", "pin_memory"} {
		if !seen[name] {
			t.Errorf("expected memory tool %q in ToolDescriptorsLight", name)
		}
	}
	if seen["runtime_stats"] {
		t.Error("ops tool runtime_stats must not appear in ToolDescriptorsLight")
	}
	if seen["cognify"] {
		t.Error("cognify must not appear in ToolDescriptorsLight")
	}
	if len(light) >= len(ToolDescriptors()) {
		t.Errorf("ToolDescriptorsLight has %d tools, want fewer than full (%d)", len(light), len(ToolDescriptors()))
	}
}
