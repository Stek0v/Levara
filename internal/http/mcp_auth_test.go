package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

// TestMCPInitializeNoAuthRequired verifies that initialize succeeds with
// RequireAuth=false and no auth headers.
func TestMCPInitializeNoAuthRequired(t *testing.T) {
	app := fiber.New()
	h := &mcpHandler{cfg: APIConfig{RequireAuth: false}, sessions: mcp.NewSessionStore()}
	app.Post("/mcp", h.handleRPC)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	code, resp := postRPC(t, app, body, nil)

	if code != 200 {
		t.Errorf("expected 200, got %d. Response: %s", code, resp)
	}
}

// TestMCPInitializeAuthRequired verifies that initialize fails with
// RequireAuth=true and no auth headers.
func TestMCPInitializeAuthRequired(t *testing.T) {
	app := fiber.New()
	h := &mcpHandler{cfg: APIConfig{RequireAuth: true, JWTSecret: "test-secret"}, sessions: mcp.NewSessionStore()}
	app.Post("/mcp", h.handleRPC)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	code, resp := postRPC(t, app, body, nil)

	if code != 401 {
		t.Errorf("expected 401, got %d. Response: %s", code, resp)
	}
	if !strings.Contains(resp, "authorization required") {
		t.Errorf("expected 'authorization required' in response, got: %s", resp)
	}
}

func TestMCPReadOnlyAPIKeyCannotCallMutatingTool(t *testing.T) {
	h := &mcpHandler{cfg: APIConfig{}, sessions: mcp.NewSessionStore()}
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "reader")
	ctx = context.WithValue(ctx, mcpAPIKeyPermissionsKey, "read")

	denied := h.executeTool(ctx, nil, "save_memory", map[string]any{"key": "x", "value": "y"})
	if !denied.IsError || len(denied.Content) == 0 || !strings.Contains(denied.Content[0].Text, "permissions denied") {
		t.Fatalf("mutating tool result=%+v, want API-key denial", denied)
	}

	allowed := h.executeTool(ctx, nil, "levara_instructions", map[string]any{})
	if allowed.IsError {
		t.Fatalf("read-only tool denied: %+v", allowed)
	}
}

func TestMCPDeleteSessionRequiresBoundOwner(t *testing.T) {
	const secret = "session-owner-secret"
	h := &mcpHandler{cfg: APIConfig{RequireAuth: true, JWTSecret: secret}, sessions: mcp.NewSessionStore()}
	sessionID := h.createSession("owner-a")
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Delete("/mcp", h.handleDeleteSession)

	deleteAs := func(userID string) int {
		req, err := http.NewRequest(http.MethodDelete, "/mcp", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Mcp-Session-Id", sessionID)
		if userID != "" {
			req.Header.Set("Authorization", "Bearer "+createJWT(userID, userID+"@example.com", secret))
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if got := deleteAs("intruder"); got != fiber.StatusNotFound {
		t.Fatalf("intruder delete status=%d, want 404", got)
	}
	if h.getOrValidateSession(sessionID) == nil {
		t.Fatal("intruder deleted owner session")
	}
	if got := deleteAs("owner-a"); got != fiber.StatusNoContent {
		t.Fatalf("owner delete status=%d, want 204", got)
	}
}
