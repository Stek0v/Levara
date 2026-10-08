package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

func TestWorkspaceMetadataScope(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth, cfg.JWTSecret = f.db, true, "metadata-scope-secret"
		f.db.SetMaxOpenConns(1)
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		cfg.WorkspaceWatcher.branches["alpha/main"] = WorkspaceBranchWatchStatus{ProjectID: "alpha", Branch: "main", LastError: "alpha error", LastErrorAt: "2026-10-05T10:00:00Z", ErrorCount: 1}
		cfg.WorkspaceWatcher.branches["beta/main"] = WorkspaceBranchWatchStatus{ProjectID: "beta", Branch: "main", LastError: "secret-beta-error", LastErrorAt: "2026-10-06T10:00:00Z", ErrorCount: 9}
		cfg.WorkspaceWatcher.lastError, cfg.WorkspaceWatcher.lastProjectID, cfg.WorkspaceWatcher.errorCount = "secret-beta-error", "beta", 10
		registry := workspaceContextArtifactRegistry{Version: workspaceArtifactRegistryVersion, Artifacts: []workspaceContextArtifactRequest{{ProjectID: "alpha", Path: "note.md"}, {ProjectID: "beta", Path: "secret-beta-file.md"}}}
		registryPath := workspaceContextArtifactRegistryPath(cfg)
		if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(registryPath, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := startWorkspaceRun(cfg, workspaceRunStartRequest{ProjectID: "alpha", RunID: "metadata-run", Result: "alpha-run-secret"}); err != nil {
			t.Fatal(err)
		}
		h := &mcpHandler{cfg: cfg, sessions: mcppkg.NewSessionStore()}
		tools := []struct {
			name    string
			handler fiber.Handler
		}{{"workspace_context", workspaceContextHandler(cfg)}, {"workspace_context_artifacts", workspaceContextArtifactsHandler(cfg)}, {"workspace_ops_status", workspaceOpsStatusHandler(cfg)}, {"workspace_conflicts", workspaceConflictsHandler(cfg)}, {"workspace_watch_status", workspaceWatchStatusHandler(cfg)}, {"workspace_index_jobs", workspaceIndexJobsHandler(cfg)}, {"workspace_audit_log", workspaceAuditLogHandler(cfg)}, {"workspace_run_get", workspaceRunGetHandler(cfg)}, {"workspace_access_check", workspaceAccessCheckHandler(cfg)}}
		for _, tool := range tools {
			for _, transport := range []string{"rest", "mcp", "mcp-latest"} {
				t.Run(tool.name+"/"+transport+"/partial_drain", func(t *testing.T) {
					raw := &fasthttp.RequestCtx{}
					raw.Request.Header.SetMethod("GET")
					raw.Request.SetRequestURI("/metadata?project_id=alpha&branch=main&run_id=metadata-run")
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
					if transport != "rest" {
						raw.Request.Header.SetMethod("POST")
						raw.Request.Header.SetContentType("application/json")
						raw.Request.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", cfg.JWTSecret))
						raw.Request.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool.name + `","arguments":{"project_id":"alpha","branch":"main","run_id":"metadata-run","access":"read"}}}`)
						if transport == "mcp-latest" {
							raw.Request.Header.Set("Accept", "application/json, text/event-stream")
							raw.Request.Header.Set("MCP-Protocol-Version", mcppkg.LatestProtocolVersion)
							raw.Request.Header.Set("Mcp-Method", "tools/call")
							raw.Request.Header.Set("Mcp-Name", tool.name)
							raw.Request.SetBodyString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool.name + `","arguments":{"project_id":"alpha","branch":"main","run_id":"metadata-run","access":"read"},"_meta":{"io.modelcontextprotocol/protocolVersion":"` + mcppkg.LatestProtocolVersion + `","io.modelcontextprotocol/clientInfo":{},"io.modelcontextprotocol/clientCapabilities":{}}}}`)
							err = h.handleLatestRPC(c)
						} else {
							err = h.handleRPC(c)
						}
					} else {
						if tool.name == "workspace_access_check" {
							raw.Request.Header.SetMethod("POST")
							raw.Request.Header.SetContentType("application/json")
							raw.Request.SetBodyString(`{"project_id":"alpha","access":"read"}`)
						}
						err = tool.handler(c)
					}
					if err != nil {
						t.Fatal(err)
					}
					stream, ok := c.Response().BodyStream().(io.ReadCloser)
					if !ok || f.db.Stats().InUse != 1 {
						t.Fatal("metadata response lacks retained SQL fence")
					}
					buf := make([]byte, 16)
					if n, err := stream.Read(buf); n == 0 || err != nil || f.db.Stats().InUse != 1 {
						t.Fatalf("partial drain released fence: bytes=%d error=%v", n, err)
					}
					if err := stream.Close(); err != nil {
						t.Fatal(err)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("metadata close retained SQL")
					}
				})
			}
			if tool.name == "workspace_conflicts" || tool.name == "workspace_context" {
				continue
			}
			t.Run(tool.name+"/projectless_rest", func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/metadata")
				c := f.app.AcquireCtx(raw)
				defer f.app.ReleaseCtx(c)
				c.Locals("user_id", "owner")
				c.Locals("tenant_id", "a")
				if tool.name == "workspace_access_check" {
					raw.Request.Header.SetMethod("POST")
					raw.Request.Header.SetContentType("application/json")
					raw.Request.SetBodyString(`{"access":"read"}`)
				}
				if err := tool.handler(c); err == nil && c.Response().StatusCode() < 400 {
					t.Fatal("authenticated projectless metadata accepted")
				}
			})
			t.Run(tool.name+"/projectless_mcp", func(t *testing.T) {
				ctx := context.WithValue(context.Background(), mcpUserIDKey, "owner")
				ctx = context.WithValue(ctx, mcppkg.TenantIDKey, "a")
				if result := h.executeTool(ctx, nil, tool.name, map[string]any{}); !result.IsError {
					t.Fatal("authenticated projectless MCP metadata accepted")
				}
			})
		}
		t.Run("scoped_watcher", func(t *testing.T) {
			status, err := collectWorkspaceOpsStatus(cfg, workspaceOpsStatusRequest{ProjectID: "alpha", Branch: "main"})
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "secret-beta-error") || strings.Contains(string(body), `"beta"`) || len(status.Watcher.Branches) != 1 || status.Watcher.ErrorCount != 1 {
				t.Fatalf("scoped watcher leaks other project: %s", body)
			}
		})
	})
}

func TestWorkspaceArtifactReindexAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		registry := workspaceContextArtifactRegistry{Version: workspaceArtifactRegistryVersion}
		for _, branch := range []string{"main", "feature/topic"} {
			file, _, err := workspaceFilePath(cfg, "alpha", branch, "note.md")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(strings.Repeat("Copper seventeen. Zinc twenty-three. Total forty units. ", 20)), 0600); err != nil {
				t.Fatal(err)
			}
			registry.Artifacts = append(registry.Artifacts, workspaceContextArtifactRequest{ProjectID: "alpha", Branch: branch, Path: "note.md"})
		}
		p := workspaceContextArtifactRegistryPath(cfg)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0600); err != nil {
			t.Fatal(err)
		}
		endpoint, err := url.Parse(cfg.EmbedEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		proxy := httputil.NewSingleHostReverseProxy(endpoint)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if f.db.Stats().InUse != 1 {
				t.Error("artifact embedding lacks authority SQL fence")
			}
			proxy.ServeHTTP(w, r)
		}))
		defer server.Close()
		cfg.EmbedEndpoint = server.URL
		raw := &fasthttp.RequestCtx{}
		raw.Request.Header.SetMethod("POST")
		raw.Request.Header.SetContentType("application/json")
		raw.Request.SetBodyString(`{"project_id":"alpha","generation":"artifact-native","activate_generation":true}`)
		c := f.app.AcquireCtx(raw)
		defer f.app.ReleaseCtx(c)
		c.Locals("user_id", "owner")
		c.Locals("tenant_id", "a")
		c.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.SetUserContext(ctx)
		if err := workspaceReindexArtifactsHandler(cfg)(c); err != nil {
			t.Fatal(err)
		}
		var response workspaceReindexArtifactsResponse
		if err := json.Unmarshal(c.Response().Body(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 2 || calls.Load() != 2 || f.db.Stats().InUse != 0 {
			t.Fatalf("multi-branch artifact index did not drain: results=%d calls=%d", len(response.Results), calls.Load())
		}
		before := map[string][]byte{}
		for _, branch := range []string{"main", "feature/topic"} {
			body, err := os.ReadFile(workspaceManifestPath(cfg, "alpha", branch))
			if err != nil {
				t.Fatal(err)
			}
			before[branch] = body
		}
		for _, kind := range []string{"viewer", "expired", "cancelled", "reserved"} {
			t.Run(kind+"_preserves_existing", func(t *testing.T) {
				actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
				request := workspaceReindexArtifactsRequest{workspaceReindexRequest: workspaceReindexRequest{ProjectID: "alpha", Generation: "must-not-publish", ActivateGeneration: true}}
				ctx := context.Background()
				switch kind {
				case "viewer":
					actor.UserID = "viewer"
				case "expired":
					actor.Credential.ExpiresAt = time.Now().Add(-time.Second).Unix()
				case "cancelled":
					cancelled, cancel := context.WithCancel(ctx)
					cancel()
					ctx = cancelled
				case "reserved":
					request.Collection = "_memories"
				}
				previousCalls := calls.Load()
				if _, err := reindexWorkspaceContextArtifactsAuthorized(ctx, cfg, request, actor); err == nil {
					t.Fatal("denied artifact effect accepted")
				}
				for branch, body := range before {
					after, err := os.ReadFile(workspaceManifestPath(cfg, "alpha", branch))
					if err != nil || string(body) != string(after) {
						t.Fatalf("denied artifact changed manifest: %v", err)
					}
				}
				for _, result := range response.Results {
					for _, id := range result.VectorIDs {
						if !cfg.Collections.HasRecord(result.Collection, id) {
							t.Fatal("denied artifact removed existing vector")
						}
					}
				}
				if calls.Load() != previousCalls || f.db.Stats().InUse != 0 {
					t.Fatal("denied artifact called provider or leaked SQL")
				}
			})
		}
	})
}

func TestWorkspaceWatchStatusScope(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = f.db, true
		f.db.SetMaxOpenConns(1)
		cfg.WorkspaceWatcher = NewWorkspaceWatchState()
		cfg.WorkspaceWatcher.branches["alpha/main"] = WorkspaceBranchWatchStatus{ProjectID: "alpha", Branch: "main", ErrorCount: 1}
		cfg.WorkspaceWatcher.branches["alpha/feature"] = WorkspaceBranchWatchStatus{ProjectID: "alpha", Branch: "feature", LastError: "hidden-feature-error", ErrorCount: 3}
		cfg.WorkspaceWatcher.branches["beta/main"] = WorkspaceBranchWatchStatus{ProjectID: "beta", Branch: "main", LastError: "hidden-beta-error", ErrorCount: 9}
		cfg.WorkspaceWatcher.lastError, cfg.WorkspaceWatcher.errorCount = "hidden-beta-error", 13
		for _, kind := range []string{"viewer", "foreign_project", "wrong_tenant", "expired", "no_read_permission"} {
			t.Run(kind, func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/workspace/watch/status?project_id=alpha&branch=main")
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
				case "foreign_project":
					raw.Request.SetRequestURI("/workspace/watch/status?project_id=beta")
				case "wrong_tenant":
					c.Locals("tenant_id", "b")
				case "expired":
					jwt.Exp = time.Now().Add(-time.Second).Unix()
				case "no_read_permission":
					c.Locals("api_key_permissions", "delete")
				}
				c.Locals("verified_jwt", jwt)
				err := workspaceWatchStatusHandler(cfg)(c)
				if kind != "viewer" {
					if err == nil && c.Response().StatusCode() < 400 {
						t.Fatal("invalid watch authority accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				body := c.Response().Body()
				var status WorkspaceWatchStatus
				if err := json.Unmarshal(body, &status); err != nil {
					t.Fatal(err)
				}
				if len(status.Branches) != 1 || status.ErrorCount != 1 || strings.Contains(string(body), "hidden-") {
					t.Fatalf("watch scope leaks other project or branch: %s", body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("drained watch response retained SQL")
				}
			})
		}
	})
}
