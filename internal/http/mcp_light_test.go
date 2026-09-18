package http

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

// mcpLightApp wires both MCP endpoints onto one Fiber app the way
// RegisterMCPAPI/RegisterMCPAPILight do, but with manually constructed
// handlers so tests can inspect the shared store without spawning
// cleanup-loop goroutines.
func mcpLightApp(t *testing.T, cfg APIConfig) (*fiber.App, *mcpHandler, *mcpHandler) {
	t.Helper()
	store := sharedMCPSessionStore()
	full := &mcpHandler{cfg: cfg, sessions: store}
	light := &mcpHandler{cfg: cfg, sessions: store, toolsetMode: "memory"}
	app := fiber.New()
	app.Post("/mcp", full.handleRPC)
	app.Post("/mcp-light", light.handleRPC)
	return app, full, light
}

// postRPCPath issues a JSON-RPC POST to the given path and returns the
// status code plus the body.
func postRPCPath(t *testing.T, app *fiber.App, path, body string, headers map[string]string) (int, string, string) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out), resp.Header.Get("Mcp-Session-Id")
}

type rpcReply struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func decodeRPC(t *testing.T, body string) rpcReply {
	t.Helper()
	var reply rpcReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("decode rpc response: %v\n%s", err, body)
	}
	if reply.Error != nil {
		t.Fatalf("unexpected rpc error: %s\n%s", reply.Error.Message, body)
	}
	return reply
}

// The light endpoint's initialize must advertise the pinned memory
// toolset, not the environment-configured one.
func TestMCPLight_InitializeReportsMemoryToolset(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "")
	app, _, _ := mcpLightApp(t, APIConfig{})

	status, body, sid := postRPCPath(t, app, "/mcp-light",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"test","version":"1"}}}`, nil)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if sid == "" {
		t.Fatal("initialize did not return Mcp-Session-Id")
	}
	var reply struct {
		Result struct {
			Toolset struct {
				Name      string `json:"name"`
				ToolCount int    `json:"tool_count"`
			} `json:"toolset"`
		} `json:"result"`
	}
	decodeRPCInto(t, body, &reply)
	if reply.Result.Toolset.Name != "memory" {
		t.Errorf("toolset.name = %q, want memory (pinned profile)", reply.Result.Toolset.Name)
	}
	want := len(mcp.ToolDescriptorsForMode("memory"))
	if reply.Result.Toolset.ToolCount != want {
		t.Errorf("toolset.tool_count = %d, want %d", reply.Result.Toolset.ToolCount, want)
	}
}

func decodeRPCInto(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode rpc response: %v\n%s", err, body)
	}
}

// tools/list on /mcp-light must expose exactly the memory profile: no
// task_* tools, no ops-only tools, and strictly fewer than full.
func TestMCPLight_ToolsListMatchesMemoryProfile(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "")
	app, _, _ := mcpLightApp(t, APIConfig{})

	_, body, sid := postRPCPath(t, app, "/mcp-light", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if sid == "" {
		t.Fatalf("no session id; body=%s", body)
	}
	status, body, _ := postRPCPath(t, app, "/mcp-light", `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		map[string]string{"Mcp-Session-Id": sid})
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var reply struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	decodeRPCInto(t, body, &reply)

	allowed := map[string]bool{}
	for _, tool := range mcp.ToolDescriptorsForMode("memory") {
		allowed[tool.Name] = true
	}
	if len(reply.Result.Tools) == 0 {
		t.Fatal("tools/list returned no tools")
	}
	for _, tool := range reply.Result.Tools {
		if !allowed[tool.Name] {
			t.Errorf("tool %q advertised on /mcp-light but not in memory profile", tool.Name)
		}
		if strings.HasPrefix(tool.Name, "task_") {
			t.Errorf("task tool %q advertised on /mcp-light", tool.Name)
		}
	}
	for _, name := range []string{"save_memory", "recall_memory", "search", "doctor", "levara_instructions"} {
		if !allowed[name] {
			continue
		}
		found := false
		for _, tool := range reply.Result.Tools {
			if tool.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("memory-profile tool %q missing from /mcp-light tools/list", name)
		}
	}
	if len(reply.Result.Tools) >= len(mcp.ToolDescriptors()) {
		t.Errorf("/mcp-light advertised %d tools, want fewer than the full set (%d)",
			len(reply.Result.Tools), len(mcp.ToolDescriptors()))
	}
}

// tools/call on /mcp-light must enforce the pinned profile: an ops-only
// tool is rejected with the toolset error, while a memory tool runs.
func TestMCPLight_ToolsCallEnforcesMemoryProfile(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	app, _, _ := mcpLightApp(t, APIConfig{})

	_, body, sid := postRPCPath(t, app, "/mcp-light", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if sid == "" {
		t.Fatalf("no session id; body=%s", body)
	}
	headers := map[string]string{"Mcp-Session-Id": sid}

	status, body, _ := postRPCPath(t, app, "/mcp-light",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"runtime_stats","arguments":{}}}`, headers)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	result := toolCallResult(t, body)
	if !result.IsError {
		t.Fatal("runtime_stats call on /mcp-light must be an error result (not in memory profile)")
	}
	if !strings.Contains(result.Text, "not found in active MCP toolset") {
		t.Fatalf("unexpected rejection text: %q", result.Text)
	}

	status, body, _ = postRPCPath(t, app, "/mcp-light",
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"levara_instructions","arguments":{}}}`, headers)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if result = toolCallResult(t, body); result.IsError {
		t.Fatalf("levara_instructions must run on /mcp-light, got error: %q", result.Text)
	}
}

// The default /mcp endpoint keeps its env-configured full toolset and
// allows tools the light endpoint rejects — no global regression.
func TestMCPFull_EnvToolsetUnchangedByLightRegistration(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "")
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "")
	app, _, _ := mcpLightApp(t, APIConfig{})

	_, body, sid := postRPCPath(t, app, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if sid == "" {
		t.Fatalf("no session id; body=%s", body)
	}
	var init struct {
		Result struct {
			Toolset struct {
				Name string `json:"name"`
			} `json:"toolset"`
		} `json:"result"`
	}
	decodeRPCInto(t, body, &init)
	if init.Result.Toolset.Name != "full" {
		t.Errorf("/mcp toolset.name = %q, want full (default env)", init.Result.Toolset.Name)
	}

	status, body, _ := postRPCPath(t, app, "/mcp",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"runtime_stats","arguments":{}}}`,
		map[string]string{"Mcp-Session-Id": sid})
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if result := toolCallResult(t, body); result.IsError && strings.Contains(result.Text, "not found in active MCP toolset") {
		t.Fatalf("runtime_stats must remain callable on /mcp under the default env, got toolset rejection: %q", result.Text)
	}
}

// /mcp and /mcp-light share one session store: a session created on
// either endpoint is valid on the other, and MCPSessionsActive is fed by
// exactly one store.
func TestMCPLight_SharedSessionStoreAcrossEndpoints(t *testing.T) {
	app, full, light := mcpLightApp(t, APIConfig{})
	if full.sessions != light.sessions {
		t.Fatal("full and light handlers must share one session store")
	}
	store := sharedMCPSessionStore()
	if full.sessions != store {
		t.Fatal("handlers must use sharedMCPSessionStore()")
	}

	// Session born on /mcp is usable on /mcp-light.
	_, body, sidFull := postRPCPath(t, app, "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, nil)
	if sidFull == "" {
		t.Fatalf("no session id; body=%s", body)
	}
	status, _, _ := postRPCPath(t, app, "/mcp-light", `{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		map[string]string{"Mcp-Session-Id": sidFull})
	if status != fiber.StatusOK {
		t.Fatalf("session from /mcp rejected on /mcp-light: status=%d", status)
	}

	// Session born on /mcp-light is usable on /mcp.
	_, body, sidLight := postRPCPath(t, app, "/mcp-light", `{"jsonrpc":"2.0","id":3,"method":"initialize"}`, nil)
	if sidLight == "" {
		t.Fatalf("no session id; body=%s", body)
	}
	status, _, _ = postRPCPath(t, app, "/mcp", `{"jsonrpc":"2.0","id":4,"method":"ping"}`,
		map[string]string{"Mcp-Session-Id": sidLight})
	if status != fiber.StatusOK {
		t.Fatalf("session from /mcp-light rejected on /mcp: status=%d", status)
	}
}

// RegisterMCPAPILight wires the full route set: initialize creates a
// session, DELETE terminates it (204 then 404).
func TestMCPLight_RoutesRegisteredAndSessionDeleted(t *testing.T) {
	app := fiber.New()
	RegisterMCPAPILight(app, APIConfig{})

	status, body, sid := postRPCPath(t, app, "/mcp-light",
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"test","version":"1"}}}`, nil)
	if status != fiber.StatusOK {
		t.Fatalf("initialize status = %d, want 200; body=%s", status, body)
	}
	if sid == "" {
		t.Fatal("initialize did not return Mcp-Session-Id")
	}

	del := httptest.NewRequest("DELETE", "/mcp-light", nil)
	del.Header.Set("Mcp-Session-Id", sid)
	resp, err := app.Test(del, -1)
	if err != nil {
		t.Fatalf("app.Test DELETE: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", resp.StatusCode)
	}

	delAgain := httptest.NewRequest("DELETE", "/mcp-light", nil)
	delAgain.Header.Set("Mcp-Session-Id", sid)
	resp, err = app.Test(delAgain, -1)
	if err != nil {
		t.Fatalf("app.Test DELETE again: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != fiber.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404 (session gone)", resp.StatusCode)
	}
}

type toolCallOutcome struct {
	IsError bool
	Text    string
}

func toolCallResult(t *testing.T, body string) toolCallOutcome {
	t.Helper()
	reply := decodeRPC(t, body)
	var result struct {
		IsError  bool `json:"isError"`
		Content  []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v\n%s", err, body)
	}
	out := toolCallOutcome{IsError: result.IsError}
	if len(result.Content) > 0 {
		out.Text = result.Content[0].Text
	}
	return out
}
