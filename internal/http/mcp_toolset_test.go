package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMCPToolsetAdmissionMatchesDiscovery(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		mode    string
		allowed bool
	}{
		{"personal_default", "personal", "", false},
		{"personal_whitespace", " Personal ", "   ", false},
		{"personal_explicit_full", "personal", "full", true},
		{"team_default", "team", "", true},
		{"default_full", "", "", true},
		{"unknown_profile_full", "pers0nal", "", true},
		{"unknown_explicit_full", "personal", "unknown-mode", true},
		{"light", "", "light", false},
		{"memory", "", "memory", false},
		{"ops", "", "ops", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEVARA_PROFILE", tc.profile)
			t.Setenv("LEVARA_MCP_TOOLSET", tc.mode)
			for _, transport := range []string{"legacy", "latest"} {
				t.Run(transport, func(t *testing.T) {
					app, h := mcpAdoptApp(t, APIConfig{})
					app.Post(latestMCPPath, h.handleLatestRPC)
					sessionID := h.createSession("")
					call := func(method, tool string) struct {
						Tools   []struct{ Name string } `json:"tools"`
						IsError bool                    `json:"isError"`
						Content []mcpContent            `json:"content"`
					} {
						t.Helper()
						params := map[string]any{}
						if tool != "" {
							params["name"], params["arguments"] = tool, map[string]any{}
						}
						endpoint := "/mcp"
						if transport == "latest" {
							endpoint = latestMCPPath
							var meta map[string]any
							if err := json.Unmarshal([]byte(latestMCPMeta()), &meta); err != nil {
								t.Fatal(err)
							}
							params["_meta"] = meta["_meta"]
						}
						body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
						if err != nil {
							t.Fatal(err)
						}
						req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
						req.Header.Set("Content-Type", "application/json")
						if transport == "latest" {
							req.Header.Set("Accept", "application/json, text/event-stream")
							for key, value := range latestMCPHeaders(method) {
								req.Header.Set(key, value)
							}
							if tool != "" {
								req.Header.Set("Mcp-Name", tool)
							}
						} else {
							req.Header.Set("Mcp-Session-Id", sessionID)
						}
						resp, err := app.Test(req, -1)
						if err != nil {
							t.Fatal(err)
						}
						defer resp.Body.Close()
						if resp.StatusCode != http.StatusOK {
							t.Fatalf("%s %s HTTP status=%d", method, tool, resp.StatusCode)
						}
						var envelope struct {
							Error  json.RawMessage `json:"error"`
							Result struct {
								Tools   []struct{ Name string } `json:"tools"`
								IsError bool                    `json:"isError"`
								Content []mcpContent            `json:"content"`
							} `json:"result"`
						}
						if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
							t.Fatal(err)
						}
						if len(envelope.Error) != 0 {
							t.Fatalf("unexpected JSON-RPC error: %s", envelope.Error)
						}
						return envelope.Result
					}
					list := call("tools/list", "")
					hasRuntime, hasInstructions := false, false
					for _, tool := range list.Tools {
						hasRuntime = hasRuntime || tool.Name == "runtime_stats"
						hasInstructions = hasInstructions || tool.Name == "levara_instructions"
					}
					if hasRuntime != tc.allowed || !hasInstructions {
						t.Fatalf("discovery runtime_stats=%v instructions=%v, want runtime_stats=%v", hasRuntime, hasInstructions, tc.allowed)
					}
					result := call("tools/call", "runtime_stats")
					if result.IsError == tc.allowed || len(result.Content) != 1 {
						t.Fatalf("runtime_stats result=%+v, allowed=%v", result, tc.allowed)
					}
					if !tc.allowed {
						if result.Content[0].Text != "method not found in active MCP toolset" {
							t.Fatalf("toolset denial changed: %+v", result)
						}
					} else {
						var stats map[string]any
						if err := json.Unmarshal([]byte(result.Content[0].Text), &stats); err != nil || stats["rss_bytes"] == nil {
							t.Fatalf("runtime_stats did not execute: %+v", result)
						}
					}
					instructions := call("tools/call", "levara_instructions")
					if instructions.IsError || len(instructions.Content) == 0 || instructions.Content[0].Text == "" {
						t.Fatalf("advertised instructions failed: %+v", instructions)
					}
				})
			}
		})
	}
}
