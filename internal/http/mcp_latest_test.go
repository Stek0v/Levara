package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

func latestMCPTestApp() *fiber.App {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	h := &mcpHandler{cfg: APIConfig{}, sessions: mcp.NewSessionStore()}
	app.Post(latestMCPPath, h.handleLatestRPC)
	app.Get(latestMCPPath, methodNotAllowed)
	app.Delete(latestMCPPath, methodNotAllowed)
	app.Post("/mcp", h.handleRPC)
	return app
}

func latestMCPPost(t *testing.T, app *fiber.App, body string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, latestMCPPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func latestMCPHeaders(method string) map[string]string {
	return map[string]string{
		"MCP-Protocol-Version": mcp.LatestProtocolVersion,
		"Mcp-Method":           method,
	}
}

func latestMCPMeta() string {
	return `{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"contract-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}`
}

func TestMCPLatestContractIsStateless(t *testing.T) {
	app := latestMCPTestApp()
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":`+latestMCPMeta()+`}`, latestMCPHeaders("tools/list"))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("latest response minted legacy session %q", got)
	}
	var payload struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, tool := range payload.Result.Tools {
		if tool.Name == "set_context" {
			t.Fatal("stateless endpoint advertised session-only set_context")
		}
	}

	headers := latestMCPHeaders("tools/call")
	headers["Mcp-Name"] = "levara_instructions"
	headers["Mcp-Session-Id"] = "legacy-session-must-be-ignored"
	resp = latestMCPPost(t, app, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"levara_instructions","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"contract-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") != "" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("stateless tool call status=%d session=%q body=%s", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), body)
	}
}

func TestMCPLatestDiscoveryContract(t *testing.T) {
	app := latestMCPTestApp()
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":"discover","method":"server/discover","params":`+latestMCPMeta()+`}`, latestMCPHeaders("server/discover"))
	defer resp.Body.Close()
	var payload struct {
		Result struct {
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("discovery status=%d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Result.SupportedVersions) != 1 || payload.Result.SupportedVersions[0] != mcp.LatestProtocolVersion {
		t.Fatalf("invalid discovery response: status=%d versions=%v", resp.StatusCode, payload.Result.SupportedVersions)
	}
}

func TestMCPLatestContractValidatesRequestMetadata(t *testing.T) {
	app := latestMCPTestApp()
	headers := latestMCPHeaders("tools/call")
	headers["Mcp-Name"] = "other_tool"
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"levara_instructions","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"contract-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	var payload struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != -32020 {
		t.Fatalf("error code=%d, want -32020", payload.Error.Code)
	}
}

func TestMCPLatestContractRejectsBrowserOrigin(t *testing.T) {
	app := latestMCPTestApp()
	headers := latestMCPHeaders("tools/list")
	headers["Origin"] = "https://example.test"
	resp := latestMCPPost(t, app, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":`+latestMCPMeta()+`}`, headers)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%d, want 403", resp.StatusCode)
	}
}

func TestMCPLegacyContractKeepsSessionHandshake(t *testing.T) {
	app := latestMCPTestApp()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"legacy","version":"1"}}}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") == "" {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("legacy handshake status=%d session=%q body=%s", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), body)
	}
	var payload struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.ProtocolVersion != mcp.LegacyProtocolVersion {
		t.Fatalf("legacy protocol=%q", payload.Result.ProtocolVersion)
	}
}

func TestMCPLatestOnlyAllowsPost(t *testing.T) {
	app := latestMCPTestApp()
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, latestMCPPath, nil)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s status=%d, want 405", method, resp.StatusCode)
		}
	}
}
