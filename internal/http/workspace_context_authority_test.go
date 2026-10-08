package http

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceContextVerifiedDiscovery(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		cfg.WorkspaceWatcher.branches["alpha/main"] = WorkspaceBranchWatchStatus{ProjectID: "alpha", Branch: "main", ErrorCount: 1}
		cfg.WorkspaceWatcher.branches["beta/main"] = WorkspaceBranchWatchStatus{ProjectID: "beta", Branch: "main", LastError: "private-beta-error", ErrorCount: 9}
		cfg.WorkspaceWatcher.lastError, cfg.WorkspaceWatcher.errorCount = "private-beta-error", 10
		for _, kind := range []string{"viewer_discovery", "wrong_tenant", "expired", "no_read_permission"} {
			t.Run(kind, func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/workspace/context?branch=main")
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				defer func() {
					if stream, ok := c.Response().BodyStream().(io.Closer); ok {
						_ = stream.Close()
					}
				}()
				c.Locals("user_id", "viewer")
				c.Locals("tenant_id", "a")
				jwt := jwtPayload{Sub: "viewer", Exp: time.Now().Add(time.Hour).Unix()}
				switch kind {
				case "wrong_tenant":
					c.Locals("tenant_id", "b")
				case "expired":
					jwt.Exp = time.Now().Add(-time.Second).Unix()
				case "no_read_permission":
					c.Locals("api_key_permissions", "delete")
				}
				c.Locals("verified_jwt", jwt)
				err := workspaceContextHandler(cfg)(c)
				if kind != "viewer_discovery" {
					if err == nil && c.Response().StatusCode() < 400 {
						t.Fatal("invalid context authority accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				body := c.Response().Body()
				var result workspaceContextResponse
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Projects) != 1 || result.Projects[0].ProjectID != "alpha" || strings.Contains(string(body), "private-beta-error") || result.Watcher.ErrorCount != 1 {
					t.Fatalf("discovery leaked foreign metadata: %s", body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("fully drained discovery retained SQL")
				}
			})
		}
	})
}

func TestWorkspaceContextBareMCPDispatch(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth, cfg.JWTSecret = f.db, true, "context-bare-secret"
		f.db.SetMaxOpenConns(1)
		h := &mcpHandler{cfg: cfg, sessions: mcppkg.NewSessionStore()}
		for _, latest := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy", true: "latest"}[latest], func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("POST")
				raw.Request.Header.SetContentType("application/json")
				raw.Request.Header.Set("Authorization", "Bearer "+createJWT("viewer", "viewer@test.invalid", cfg.JWTSecret))
				raw.Request.Header.Set("X-Tenant-Id", "a")
				params := map[string]any{"name": "workspace_context", "arguments": map[string]any{}}
				if latest {
					raw.Request.Header.Set("Accept", "application/json, text/event-stream")
					raw.Request.Header.Set("MCP-Protocol-Version", mcppkg.LatestProtocolVersion)
					raw.Request.Header.Set("Mcp-Method", "tools/call")
					raw.Request.Header.Set("Mcp-Name", "workspace_context")
					params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": mcppkg.LatestProtocolVersion, "io.modelcontextprotocol/clientInfo": map[string]any{}, "io.modelcontextprotocol/clientCapabilities": map[string]any{}}
				}
				body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
				if err != nil {
					t.Fatal(err)
				}
				raw.Request.SetBody(body)
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				if latest {
					err = h.handleLatestRPC(c)
				} else {
					err = h.handleRPC(c)
				}
				if err != nil {
					t.Fatal(err)
				}
				stream, ok := c.Response().BodyStream().(io.ReadCloser)
				if !ok {
					t.Fatalf("bare MCP lacks protected body: %s", c.Response().Body())
				}
				defer stream.Close()
				body, err = io.ReadAll(stream)
				if err != nil {
					t.Fatal(err)
				}
				var response struct {
					Result mcpToolResult `json:"result"`
				}
				if err = json.Unmarshal(body, &response); err != nil {
					t.Fatal(err)
				}
				if response.Result.IsError || len(response.Result.Content) == 0 {
					t.Fatalf("bare MCP authority lost: %s", body)
				}
				var result workspaceContextResponse
				if err = json.Unmarshal([]byte(response.Result.Content[0].Text), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Projects) != 1 || result.Projects[0].ProjectID != "alpha" {
					t.Fatalf("bare discovery scope: %+v", result.Projects)
				}
			})
		}
	})
}
