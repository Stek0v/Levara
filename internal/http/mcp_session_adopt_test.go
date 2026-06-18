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

// mcpAdoptApp builds a Fiber app with just the /mcp routes wired to a fresh
// session store, mirroring RegisterMCPAPI but returning the handler so a test
// can inspect the store. The empty store stands in for the post-restart state
// where a client's previously-issued Mcp-Session-Id is no longer known.
func mcpAdoptApp(t *testing.T, cfg APIConfig) (*fiber.App, *mcpHandler) {
	t.Helper()
	app := fiber.New()
	h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
	app.Post("/mcp", h.handleRPC)
	return app, h
}

// postRPC issues a single JSON-RPC POST to /mcp and returns the status code
// plus the (possibly empty) body. headers are applied verbatim.
func postRPC(t *testing.T, app *fiber.App, body string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
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
	return resp.StatusCode, string(out)
}

const staleSessionID = "mcp-from-a-process-that-restarted"

// A replayed (unknown) Mcp-Session-Id under require-auth=false is adopted
// rather than 404'd: the request succeeds and the store now holds the id.
func TestMCP_AdoptsStaleSession_NoAuth(t *testing.T) {
	app, h := mcpAdoptApp(t, APIConfig{})

	status, _ := postRPC(t, app,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{"Mcp-Session-Id": staleSessionID})

	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (stale session adopted, not 404)", status)
	}
	if h.sessions.Get(staleSessionID) == nil {
		t.Error("session not adopted into the store after a non-initialize call")
	}
}

// Under require-auth=true a replayed id with a VALID JWT is adopted and its
// owner is bound from the token's sub — restarts stay transparent and the
// owner survives the store reset.
func TestMCP_AdoptsStaleSession_BindsOwnerFromJWT(t *testing.T) {
	const secret = "test-secret-adopt"
	app, h := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: secret})
	token := createJWT("owner-xyz", "o@example.com", secret)

	status, _ := postRPC(t, app,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{
			"Mcp-Session-Id": staleSessionID,
			"Authorization":  "Bearer " + token,
		})

	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (adopted with JWT)", status)
	}
	sess := h.sessions.Get(staleSessionID)
	if sess == nil {
		t.Fatal("session not adopted")
		return
	}
	if sess.UserID != "owner-xyz" {
		t.Errorf("adopted session owner = %q, want owner-xyz (bound from JWT sub)", sess.UserID)
	}
}

// Under require-auth=true a replayed id with NO credentials can't establish an
// owner, so we keep the spec-correct 404 (client should re-initialize).
func TestMCP_StaleSession_404WhenUnauthenticated(t *testing.T) {
	app, h := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: "test-secret-adopt"})

	status, _ := postRPC(t, app,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{"Mcp-Session-Id": staleSessionID})

	if status != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no auth → can't adopt)", status)
	}
	if h.sessions.Get(staleSessionID) != nil {
		t.Error("session should NOT be adopted when auth fails")
	}
}

// tools/call resolves the owner from the request JWT even when the session was
// adopted empty — closing the owner_id='' footgun. We assert the response is a
// well-formed JSON-RPC result (the tool runs) rather than a transport 404.
func TestMCP_ToolCall_OwnerFromJWT(t *testing.T) {
	const secret = "test-secret-adopt"
	app, _ := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: secret})
	token := createJWT("caller-1", "c@example.com", secret)

	status, body := postRPC(t, app,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"levara_instructions","arguments":{}}}`,
		map[string]string{
			"Mcp-Session-Id": staleSessionID,
			"Authorization":  "Bearer " + token,
		})

	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &rpc); err != nil {
		t.Fatalf("decode rpc: %v\n%s", err, body)
	}
	if rpc.Error != nil {
		t.Fatalf("tool call returned rpc error: %s", rpc.Error.Message)
	}
	if len(rpc.Result) == 0 {
		t.Error("expected a tool result for an owner-resolved call")
	}
}

// ── Empty Mcp-Session-Id read-isolation gap ──
//
// A request that omits the session header entirely (not just a stale one)
// must be subject to the same auth gate as one that replays a stale id.
// Otherwise an anonymous caller under require-auth could reach tools/list
// and tools/call simply by NOT sending a session id — the auth/adopt block
// only ran when sessionID != "".

// No session header + no credentials under require-auth must 404 (not leak
// the tool catalogue to an anonymous caller).
func TestMCP_NoSessionHeader_RejectsAnonUnderRequireAuth(t *testing.T) {
	app, _ := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: "test-secret-adopt"})

	status, _ := postRPC(t, app,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{}) // no Mcp-Session-Id, no Authorization

	if status != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 (anon tools/list with no session must be rejected)", status)
	}
}

// No session header but a VALID Bearer under require-auth must still succeed —
// dropping the session id is legitimate as long as the request authenticates.
// Regression guard: this stays green before and after the fix.
func TestMCP_NoSessionHeader_AllowsValidBearer(t *testing.T) {
	const secret = "test-secret-adopt"
	app, _ := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: secret})
	token := createJWT("caller-2", "c2@example.com", secret)

	status, body := postRPC(t, app,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		map[string]string{"Authorization": "Bearer " + token}) // no Mcp-Session-Id

	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (valid Bearer, no session id); body=%s", status, body)
	}
}

// No session header + no credentials under require-auth must also block
// tools/call — closing the path where an anon caller ran a tool with
// owner_id='' just by omitting the session id.
func TestMCP_NoSessionHeader_RejectsAnonToolCall(t *testing.T) {
	app, _ := mcpAdoptApp(t, APIConfig{RequireAuth: true, JWTSecret: "test-secret-adopt"})

	status, _ := postRPC(t, app,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"levara_instructions","arguments":{}}}`,
		map[string]string{}) // no Mcp-Session-Id, no Authorization

	if status != fiber.StatusNotFound {
		t.Fatalf("status = %d, want 404 (anon tools/call with no session must be rejected)", status)
	}
}
