package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPAddDeadlineIncludesAuthenticationAndFirstSQL(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT_MS", "50")
	t.Setenv("SEARCH_REQUEST_TIMEOUT_MS", "50")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, path := range []string{"/mcp", latestMCPPath} {
			for _, authenticated := range []bool{false, true} {
				cfg := f.cfg
				cfg.RequireAuth, cfg.JWTSecret = authenticated, "deadline-test"
				backend := newMemStorage()
				cfg.FileStorage = backend
				h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
				app := fiber.New()
				app.Post("/mcp", h.handleRPC)
				app.Post(latestMCPPath, h.handleLatestRPC)
				params := map[string]any{"name": "add", "arguments": map[string]any{"data": "must not publish", "dataset_name": "deadline"}}
				if path == latestMCPPath {
					var meta map[string]any
					if err := json.Unmarshal([]byte("{"+latestMCPMetaParams()+"}"), &meta); err != nil {
						t.Fatal(err)
					}
					params["_meta"] = meta["_meta"]
				}
				raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
				req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				if authenticated {
					req.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", cfg.JWTSecret))
				}
				if path == latestMCPPath {
					for key, value := range latestMCPHeaders("tools/call") {
						req.Header.Set(key, value)
					}
					req.Header.Set("Mcp-Name", "add")
				}
				conn, err := f.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				started := time.Now()
				response, err := app.Test(req, 1000)
				elapsed := time.Since(started)
				// Keep the only SQL slot held until the complete HTTP response.
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil || elapsed > 400*time.Millisecond {
					t.Fatalf("unbounded %s auth=%v elapsed=%v err=%v", path, authenticated, elapsed, err)
				}
				if authenticated && response.StatusCode != 404 {
					t.Fatalf("auth lookup failure %d %s", response.StatusCode, body)
				}
				if !authenticated && !strings.Contains(string(body), `"isError":true`) {
					t.Fatalf("blocked lookup reported success %d %s", response.StatusCode, body)
				}
				if len(backend.objects) != 0 {
					t.Fatal("failed lookup published bytes")
				}
			}
		}
	})
}

func TestMCPSessionLifecycleDeadlineIncludesAuthentication(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT_MS", "50")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		cfg := f.cfg
		cfg.RequireAuth, cfg.JWTSecret = true, "session-deadline-test"
		h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
		session := h.createSession("owner")
		app := fiber.New()
		app.Get("/mcp", h.handleSSEStream)
		app.Delete("/mcp", h.handleDeleteSession)
		for _, method := range []string{"GET", "DELETE"} {
			req := httptest.NewRequest(method, "/mcp", nil)
			req.Header.Set("Mcp-Session-Id", session)
			req.Header.Set("Accept", "text/event-stream")
			req.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", cfg.JWTSecret))
			conn, err := f.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			response, err := app.Test(req, 1000)
			elapsed := time.Since(started)
			conn.Close()
			if err != nil {
				t.Errorf("%s failed to respect authentication deadline: %v", method, err)
				continue
			}
			response.Body.Close()
			if elapsed > 400*time.Millisecond || response.StatusCode != 404 {
				t.Errorf("%s elapsed=%v status=%d", method, elapsed, response.StatusCode)
			}
			if h.getOrValidateSession(session) == nil {
				t.Fatal("failed authentication deleted session")
			}
		}
	})
}
