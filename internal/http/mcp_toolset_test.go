package http

import (
	"testing"
)

func TestEffectiveMCPToolset(t *testing.T) {
	cases := []struct {
		name       string
		toolset    string
		profile    string
		wantMode   string
		wantSource string
	}{
		{"no env keeps full default", "", "", "", "default"},
		{"personal binds core", "", "personal", "core", "profile:personal"},
		{"personal case-insensitive with spaces", "", " Personal ", "core", "profile:personal"},
		{"explicit toolset wins over personal", "memory", "personal", "memory", "env"},
		{"explicit full wins over personal", "full", "personal", "full", "env"},
		{"solo_pro does not bind", "", "solo_pro", "", "default"},
		{"team does not bind", "", "team", "", "default"},
		{"enterprise does not bind", "", "enterprise", "", "default"},
		{"unknown profile does not bind", "", "pers0nal", "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEVARA_MCP_TOOLSET", tc.toolset)
			t.Setenv("LEVARA_PROFILE", tc.profile)
			gotMode, gotSource := EffectiveMCPToolset()
			if gotMode != tc.wantMode || gotSource != tc.wantSource {
				t.Fatalf("EffectiveMCPToolset() = (%q, %q), want (%q, %q)", gotMode, gotSource, tc.wantMode, tc.wantSource)
			}
		})
	}
}

func TestEffectiveMCPToolsetNameResolvesStableName(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	t.Setenv("LEVARA_PROFILE", "")
	name, source := EffectiveMCPToolsetName()
	if name != "full" || source != "default" {
		t.Fatalf("default resolution = (%q, %q), want (full, default)", name, source)
	}

	t.Setenv("LEVARA_PROFILE", "personal")
	name, source = EffectiveMCPToolsetName()
	if name != "core" || source != "profile:personal" {
		t.Fatalf("personal resolution = (%q, %q), want (core, profile:personal)", name, source)
	}
}

// TestConfiguredMCPToolDescriptorsPersonalBinding pins the Р1=A+ surface:
// a personal deployment that sets nothing else advertises the 13-tool core
// set, including the two hygiene tools, and nothing beyond it.
func TestConfiguredMCPToolDescriptorsPersonalBinding(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	t.Setenv("LEVARA_PROFILE", "")
	full := configuredMCPToolDescriptors()

	t.Setenv("LEVARA_PROFILE", "personal")
	personal := configuredMCPToolDescriptors()

	if len(personal) != 13 {
		t.Fatalf("personal toolset size = %d, want 13", len(personal))
	}
	has := make(map[string]bool, len(personal))
	for _, descriptor := range personal {
		has[descriptor.Name] = true
	}
	for _, required := range []string{"supersede_memory", "delete_memory", "wake_up", "save_memory", "recall_memory", "search", "doctor"} {
		if !has[required] {
			t.Errorf("personal toolset missing %s", required)
		}
	}
	for _, leaked := range []string{"consolidate", "workspace_search", "task_plan", "chat_distill"} {
		if has[leaked] {
			t.Errorf("personal toolset leaked %s", leaked)
		}
	}
	if len(full) <= len(personal) {
		t.Fatalf("full toolset = %d, must exceed personal = %d", len(full), len(personal))
	}
}
