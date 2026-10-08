package http

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"

	mcppkg "github.com/stek0v/levara/pkg/mcp"
)

func TestWorkspaceReadResponseFence(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth, cfg.JWTSecret = f.db, true, "workspace-response-test-secret"
		f.db.SetMaxOpenConns(1)
		path, _, err := workspaceFilePath(cfg, "alpha", "main", "ledger.md")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		const text = "Copper count 17. Zinc count 23. Total units 40."
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		f.app.Get("/api/v1/workspace-read-response", workspaceReadHandler(cfg))
		h := &mcpHandler{cfg: cfg, sessions: mcppkg.NewSessionStore()}
		f.app.Post("/api/v1/workspace-mcp-response", h.handleRPC)

		for _, transport := range []string{"rest", "mcp"} {
			t.Run(transport+"_partial_drain", func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/workspace-read?project_id=alpha&path=ledger.md")
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				defer func() {
					if stream, ok := c.Response().BodyStream().(io.Closer); ok {
						_ = stream.Close()
					}
				}()
				c.Locals("user_id", "owner")
				c.Locals("tenant_id", "a")
				c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
				var err error
				if transport == "mcp" {
					raw.Request.Header.SetMethod("POST")
					raw.Request.SetRequestURI("/mcp")
					raw.Request.Header.SetContentType("application/json")
					raw.Request.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", cfg.JWTSecret))
					raw.Request.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workspace_read","arguments":{"project_id":"alpha","path":"ledger.md"}}}`)
					err = h.handleRPC(c)
				} else {
					err = workspaceReadHandler(cfg)(c)
				}
				if err != nil {
					t.Fatal(err)
				}
				stream, ok := c.Response().BodyStream().(io.ReadCloser)
				if !ok || f.db.Stats().InUse != 1 {
					t.Fatal("pending response must retain the authority transaction")
				}
				buf := make([]byte, 16)
				if n, err := stream.Read(buf); n == 0 || err != nil {
					t.Fatalf("partial body read: bytes=%d error=%v", n, err)
				}
				if f.db.Stats().InUse != 1 {
					t.Fatal("partial body read released authority early")
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("body close retained authority transaction")
				}
			})
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		for _, tc := range []struct {
			name    string
			ctx     context.Context
			expires int64
			status  int
		}{
			{"expired", context.Background(), time.Now().Add(-time.Second).Unix(), 403},
			{"cancelled", cancelled, time.Now().Add(time.Hour).Unix(), 504},
		} {
			t.Run(tc.name, func(t *testing.T) {
				actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: tc.expires}}
				route := "/workspace-response-" + tc.name
				f.app.Get("/api/v1"+route, func(c *fiber.Ctx) error {
					if err := c.JSON(fiber.Map{"text": text}); err != nil {
						return err
					}
					return sendWorkspaceProtectedResponse(c, cfg, tc.ctx, actor, "alpha")
				})
				status, body, _ := f.request("owner", "GET", route, "")
				if status != tc.status || strings.Contains(string(body), text) {
					t.Fatalf("unavailable authority response status=%d body=%s", status, body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("failed response retained SQL connection")
				}
			})
		}
		for _, user := range []string{"owner", "viewer"} {
			t.Run(user+"_rest", func(t *testing.T) {
				status, body, _ := f.request(user, "GET", "/workspace-read-response?project_id=alpha&path=ledger.md", "")
				if status != 200 || !strings.Contains(string(body), text) {
					t.Fatalf("read status=%d body=%s", status, body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("response drain retained SQL connection")
				}
			})
			t.Run(user+"_mcp", func(t *testing.T) {
				token := createJWT(user, user+"@test.invalid", cfg.JWTSecret)
				status, body, _ := f.request(user, "POST", "/workspace-mcp-response", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"workspace_read","arguments":{"project_id":"alpha","path":"ledger.md"}}}`, "Authorization", "Bearer "+token)
				if status != 200 || !strings.Contains(string(body), text) || strings.Contains(string(body), `"isError":true`) {
					t.Fatalf("MCP read status=%d body=%s", status, body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("MCP response drain retained SQL connection")
				}
			})
		}
	})
}
