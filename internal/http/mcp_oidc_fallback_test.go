package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

// OIDC fallback for MCP requests (dogfood finding, 2026-09-04): a bearer
// token that is not a Levara JWT must authenticate through the external
// OIDC provider, mirroring JWTMiddlewareWithOIDC on the HTTP side.

type stubOIDCBearer struct {
	accept func(token string) bool
	userID string
	email  string
}

func (s *stubOIDCBearer) Authenticate(_ context.Context, token string) (ExternalPrincipal, error) {
	if s.accept == nil || !s.accept(token) {
		return ExternalPrincipal{}, context.Canceled // any non-nil error
	}
	return ExternalPrincipal{UserID: s.userID, Email: s.email}, nil
}

// mintOIDCToken produces a token that is NOT a valid Levara JWT (signed with
// a different secret) but IS accepted by the stub OIDC bearer.
func mintOIDCToken(secret string) string {
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	hdr, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	pl, _ := json.Marshal(map[string]any{"iss": "https://github.com", "aud": "levara", "sub": "gh-user-1"})
	signing := b64(hdr) + "." + b64(pl)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signing))
	return signing + "." + b64(mac.Sum(nil))
}

func TestMCPOIDCBearerFallback(t *testing.T) {
	bearer := &stubOIDCBearer{userID: "oidc-user-1", email: "alice@clienta.example"}
	// The stub accepts only its own tokens; a real deployment plugs the
	// verifier + identity bridge here (cmd/server oidcBearerAuth).
	bearer.accept = func(token string) bool { return strings.Count(token, ".") == 2 }

	h := &mcpHandler{cfg: APIConfig{
		JWTSecret:   "test-secret",
		RequireAuth: true,
		OIDCBearer:  bearer,
	}, sessions: mcp.NewSessionStore()}
	app := fiber.New()
	app.Post("/mcp", h.handleRPC)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`

	// External OIDC token → accepted.
	code, resp := postRPC(t, app, body, map[string]string{"Authorization": "Bearer " + mintOIDCToken("not-the-levara-secret")})
	if code != 200 {
		t.Fatalf("oidc token: expected 200, got %d: %s", code, resp)
	}

	// Unknown token → closed (401).
	code, _ = postRPC(t, app, body, map[string]string{"Authorization": "Bearer totally-invalid"})
	if code != 401 {
		t.Fatalf("invalid token: expected 401, got %d", code)
	}

	// A valid Levara JWT still takes the local path.
	local := CreateSessionJWT("local-user", "local@example.com", "test-secret")
	code, resp = postRPC(t, app, body, map[string]string{"Authorization": "Bearer " + local})
	if code != 200 {
		t.Fatalf("local jwt: expected 200, got %d: %s", code, resp)
	}
}

func TestMCPOIDCBearerOffByDefault(t *testing.T) {
	// Without OIDCBearer wired, an external token must stay rejected
	// (fail-closed, previous behavior preserved).
	h := &mcpHandler{cfg: APIConfig{JWTSecret: "test-secret", RequireAuth: true}, sessions: mcp.NewSessionStore()}
	app := fiber.New()
	app.Post("/mcp", h.handleRPC)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	code, _ := postRPC(t, app, body, map[string]string{"Authorization": "Bearer " + mintOIDCToken("not-the-levara-secret")})
	if code != 401 {
		t.Fatalf("expected 401 without OIDC wired, got %d", code)
	}
}
