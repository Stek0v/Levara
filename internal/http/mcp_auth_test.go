package http

import (
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
