package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

func TestCommunityListAuthorityTransports(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, path := range []string{"/mcp", latestMCPPath} {
		for _, scenario := range []string{"admin", "peer", "selected-tenant", "forged-actor", "read-key", "delete-key"} {
			t.Run(path+"/"+scenario, func(t *testing.T) {
				documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
					f.db.SetMaxOpenConns(1)
					f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('global-proof',3,'global confidential community')")
					communityPublicationFixture(t, f, "global-proof")
					h := asyncAuthorityHandler(f)
					app := asyncAuthorityApp(h, nil)
					user := "root"
					if scenario == "peer" || scenario == "forged-actor" {
						user = "peer"
					}
					if scenario == "selected-tenant" {
						f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('root','a')")
					}
					args := "{}"
					if scenario == "forged-actor" {
						args = `{"actor_id":"root","owner_id":"root","tenant_id":""}`
					}
					body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_communities","arguments":%s}}`, args)
					if path == latestMCPPath {
						body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_communities","arguments":%s,%s}}`, args, latestMCPMetaParams())
					}
					req := httptest.NewRequest("POST", path, strings.NewReader(body))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", h.cfg.JWTSecret))
					if scenario == "selected-tenant" {
						req.Header.Set("X-Tenant-Id", "a")
					}
					if scenario == "read-key" || scenario == "delete-key" {
						permission := "read"
						if scenario == "delete-key" {
							permission = "delete"
						}
						f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('community-key',$1,'root',$2)", apikeyHash("community-key"), permission)
						req.Header.Del("Authorization")
						req.Header.Set("X-Api-Key", "community-key")
					}
					if path == latestMCPPath {
						req.Header.Set("Accept", "application/json, text/event-stream")
						for key, value := range latestMCPHeaders("tools/call") {
							req.Header.Set(key, value)
						}
						req.Header.Set("Mcp-Name", "list_communities")
					} else {
						req.Header.Set("Mcp-Session-Id", h.createSession(user))
					}
					resp, err := app.Test(req, -1)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					if err != nil {
						t.Fatal(err)
					}
					var envelope struct {
						Result mcp.ToolResult `json:"result"`
						Error  any            `json:"error"`
					}
					if resp.StatusCode != 200 || json.Unmarshal(raw, &envelope) != nil || envelope.Error != nil {
						t.Fatalf("RPC status=%d body=%s", resp.StatusCode, raw)
					}
					allowed := scenario == "admin" || scenario == "read-key"
					if envelope.Result.IsError == allowed || strings.Contains(string(raw), "global confidential community") != allowed {
						t.Fatalf("community admission allowed=%v body=%s", allowed, raw)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("community transport leaked SQL")
					}
				})
			})
		}
	}
}

func TestCommunityListAuthorityRetainedTransportBody(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('global-proof',3,'global confidential community')")
		communityPublicationFixture(t, f, "global-proof")
		h := asyncAuthorityHandler(f)
		app := fiber.New()
		for _, latest := range []bool{false, true} {
			t.Run(fmt.Sprint(latest), func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("POST")
				raw.Request.Header.SetContentType("application/json")
				raw.Request.Header.Set("Authorization", "Bearer "+createJWT("root", "root@test.invalid", h.cfg.JWTSecret))
				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_communities","arguments":{}}}`
				if latest {
					raw.Request.SetRequestURI(latestMCPPath)
					body = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_communities","arguments":{},` + latestMCPMetaParams() + `}}`
					raw.Request.Header.Set("Accept", "application/json, text/event-stream")
					for key, value := range latestMCPHeaders("tools/call") {
						raw.Request.Header.Set(key, value)
					}
					raw.Request.Header.Set("Mcp-Name", "list_communities")
				} else {
					raw.Request.SetRequestURI("/mcp")
					raw.Request.Header.Set("Mcp-Session-Id", h.createSession("root"))
				}
				raw.Request.SetBodyString(body)
				c := app.AcquireCtx(raw)
				defer app.ReleaseCtx(c)
				var err error
				if latest {
					err = h.handleLatestRPC(c)
				} else {
					err = h.handleRPC(c)
				}
				if err != nil {
					t.Fatal(err)
				}
				stream, ok := c.Response().BodyStream().(io.ReadCloser)
				if !ok || f.db.Stats().InUse != 1 {
					t.Fatal("community response lacks retained authority")
				}
				defer stream.Close()
				if n, err := stream.Read(make([]byte, 8)); n == 0 || err != nil || f.db.Stats().InUse != 1 {
					t.Fatalf("partial body %d %v", n, err)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("community Close leaked SQL")
				}
			})
		}
	})
}

func TestCommunityListAuthorityLocalCompatibility(t *testing.T) {
	empty := (&mcpHandler{}).toolListCommunities(context.Background(), nil)
	if empty.IsError || len(empty.Content) != 1 || !strings.Contains(empty.Content[0].Text, `"communities": []`) {
		t.Fatalf("nil DB shape=%+v", empty)
	}
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		h := &mcpHandler{cfg: f.cfg}
		f.exec("INSERT INTO graph_communities(id,level,member_count,summary) VALUES('low',0,2,'low summary'),('high',1,4,'high summary')")
		result := h.toolListCommunities(context.Background(), map[string]any{"level": float64(1), "min_members": float64(3), "limit": float64(1)})
		if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "high summary") || strings.Contains(result.Content[0].Text, "low summary") {
			t.Fatalf("local filters=%+v", result)
		}
		f.exec("ALTER TABLE graph_communities RENAME TO unavailable_communities")
		result = h.toolListCommunities(context.Background(), nil)
		if result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, `"communities": []`) {
			t.Fatalf("local SQL error shape=%+v", result)
		}
	})
}

// Direct wrapper calls need the same admin-evidence marker as transport calls;
// authority after materialization is rechecked by the shared completed sender.
func TestCommunityListAuthorityMarksAdminEvidence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, mcp.UserIDKey, "root")
		ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		if result := (&mcpHandler{cfg: f.cfg}).toolListCommunities(ctx, nil); result.IsError || !searchEvidenceRequiresAdmin(ctx) {
			t.Fatalf("missing admin evidence: %+v", result)
		}
	})
}
