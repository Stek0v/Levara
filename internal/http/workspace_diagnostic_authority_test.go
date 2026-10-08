package http

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceAccessDiagnosticAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		for _, kind := range []string{"viewer_read", "viewer_write", "foreign_read", "expired", "wrong_tenant"} {
			t.Run(kind, func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("POST")
				raw.Request.Header.SetContentType("application/json")
				project, action := "alpha", "read"
				if kind == "viewer_write" {
					action = "write"
				}
				if kind == "foreign_read" {
					project = "beta"
				}
				body, err := json.Marshal(map[string]string{"project_id": project, "access": action})
				if err != nil {
					t.Fatal(err)
				}
				raw.Request.SetBody(body)
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				defer func() {
					if s, ok := c.Response().BodyStream().(io.Closer); ok {
						_ = s.Close()
					}
				}()
				c.Locals("user_id", "viewer")
				c.Locals("tenant_id", "a")
				jwt := jwtPayload{Sub: "viewer", Exp: time.Now().Add(time.Hour).Unix()}
				if kind == "expired" {
					jwt.Exp = time.Now().Add(-time.Second).Unix()
				}
				if kind == "wrong_tenant" {
					c.Locals("tenant_id", "b")
				}
				c.Locals("verified_jwt", jwt)
				err = workspaceAccessCheckHandler(cfg)(c)
				if kind == "expired" || kind == "wrong_tenant" {
					if err == nil && c.Response().StatusCode() < 400 {
						t.Fatal("invalid diagnostic authority accepted")
					}
					return
				}
				if err != nil || c.Response().StatusCode() != 200 {
					t.Fatalf("diagnostic failed: %v status=%d", err, c.Response().StatusCode())
				}
				var result workspaceAccessCheckResponse
				if err := json.Unmarshal(c.Response().Body(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Allowed != (kind == "viewer_read") {
					t.Fatalf("diagnostic allowed=%v kind=%s", result.Allowed, kind)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("drained diagnostic retained SQL")
				}
			})
		}
		t.Run("mcp_readonly_permission", func(t *testing.T) {
			ctx := context.WithValue(context.Background(), mcpUserIDKey, "owner")
			ctx = context.WithValue(ctx, mcppkg.TenantIDKey, "a")
			ctx = context.WithValue(ctx, mcpAPIKeyPermissionsKey, "read")
			f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'owner','read')", "diagnostic-read-key", "verified-key-fixture-hash")
			actor := f.owner
			actor.APIKeyPermissions = "read"
			ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor, kind: "api_key", keyID: "diagnostic-read-key"})
			result := (&mcpHandler{cfg: cfg}).toolWorkspaceAccessCheck(ctx, map[string]any{"project_id": "alpha", "access": "write"})
			if result.IsError || len(result.Content) == 0 {
				t.Fatalf("diagnostic error: %+v", result)
			}
			var check workspaceAccessCheckResponse
			if err := json.Unmarshal([]byte(result.Content[0].Text), &check); err != nil {
				t.Fatal(err)
			}
			if check.Allowed || check.APIKeyAllowed {
				t.Fatal("readonly permission was discarded by MCP diagnostic")
			}
		})
	})
}
