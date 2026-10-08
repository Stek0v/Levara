package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPSearchStrategyTransportParity(t *testing.T) {
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, dialect := range []string{"sqlite", "postgres"} {
		for _, profile := range []string{"core", "full"} {
			for _, path := range []string{"/mcp", latestMCPPath} {
				t.Run(dialect+"/"+profile+"/"+strings.TrimPrefix(path, "/"), func(t *testing.T) {
					t.Setenv("LEVARA_MCP_TOOLSET", profile)
					app, db := documentACLHTTPFixture(t, dialect)
					cm, err := store.NewCollectionManager(2, t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { cm.Close() })
					index := bm25.NewIndexRegistry()
					policy := accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					for _, query := range []string{`INSERT INTO tenants(id,name,owner_id) VALUES('t','Team','alice')`, `INSERT INTO user_tenant(user_id,tenant_id) VALUES('alice','t'),('bob','t')`} {
						if _, err := db.Exec(query); err != nil {
							t.Fatal(err)
						}
					}
					for _, doc := range []struct{ id, dataset, owner string }{{"allowed", "a", "alice"}, {"denied", "b", "bob"}} {
						for _, query := range []string{
							fmt.Sprintf("INSERT INTO data(id,name,raw_content_hash) VALUES('%s','%s','%s')", doc.id, doc.id, strings.Repeat("a", 64)),
							fmt.Sprintf("INSERT INTO dataset_data(dataset_id,data_id) VALUES('%s','%s')", doc.dataset, doc.id),
						} {
							if _, err := db.Exec(query); err != nil {
								t.Fatal(err)
							}
						}
						owner := accesspkg.Actor{UserID: doc.owner, TenantID: "t"}
						resource, err := policy.RegisterDocument(ctx, owner, accesspkg.DocumentRef{DatasetID: doc.dataset, DataID: doc.id}, "t", accesspkg.DocumentRestricted)
						if err != nil {
							t.Fatal(err)
						}
						fenced, release, err := policy.BeginReadFence(ctx, dialect == "sqlite")
						if err != nil {
							t.Fatal(err)
						}
						err = fenced.CommitDocumentIndexWithLineage(ctx, owner, resource.DocumentRef, resource.ContentRevision, "docs", "published", accesspkg.DocumentPublicationLineage{SourcesJSON: "[]"})
						release()
						if err != nil {
							t.Fatal(err)
						}
						meta := searchDocumentSource{DatasetID: doc.dataset, DocumentID: doc.id, ContentRevision: resource.ContentRevision, Collection: "docs", Generation: "published"}
						encoded, err := json.Marshal(meta)
						if err != nil {
							t.Fatal(err)
						}
						index.GetOrCreate("docs", nil).Add(doc.id, "summary of project", string(encoded))
						if err := cm.Insert("docs", doc.id, []float32{1, 0}, meta); err != nil {
							t.Fatal(err)
						}
					}
					var embeddings, reranks atomic.Int32
					endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.Contains(r.URL.Path, "rerank") {
							reranks.Add(1)
							http.Error(w, "unexpected rerank", 500)
							return
						}
						embeddings.Add(1)
						json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 0}}}})
					}))
					t.Cleanup(endpoint.Close)
					provider := &recordingLLM{}
					h := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true, JWTSecret: "rbac-regression-secret", Collections: cm, BM25Indexes: index, EmbedEndpoint: endpoint.URL, EmbedClient: embed.NewClient(endpoint.URL, "fixture", 1, 1), LLMProvider: provider, RerankEndpoint: endpoint.URL + "/rerank"}, sessions: mcp.NewSessionStore()}
					app.Post("/mcp", h.handleRPC)
					app.Post(latestMCPPath, h.handleLatestRPC)
					session := h.createSession("alice")
					rpc := func(method string, args map[string]any) json.RawMessage {
						t.Helper()
						params := map[string]any{}
						if method == "tools/call" {
							params["name"], params["arguments"] = "search", args
						}
						headers := map[string]string{"Authorization": "Bearer " + createJWT("alice", "alice@example.test", h.cfg.JWTSecret), "Mcp-Session-Id": session, "X-Tenant-Id": "t"}
						if path == latestMCPPath {
							var meta map[string]any
							if err := json.Unmarshal([]byte(latestMCPMeta()), &meta); err != nil {
								t.Fatal(err)
							}
							params["_meta"] = meta["_meta"]
							for key, value := range latestMCPHeaders(method) {
								headers[key] = value
							}
							if method == "tools/call" {
								headers["Mcp-Name"] = "search"
							}
						}
						body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
						if err != nil {
							t.Fatal(err)
						}
						req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Accept", "application/json, text/event-stream")
						for key, value := range headers {
							req.Header.Set(key, value)
						}
						resp, err := app.Test(req, -1)
						if err != nil {
							t.Fatal(err)
						}
						defer resp.Body.Close()
						if resp.StatusCode != fiber.StatusOK || (path == latestMCPPath && resp.Header.Get("Mcp-Session-Id") != "") {
							t.Fatalf("status=%d session=%q", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"))
						}
						var envelope struct {
							Result json.RawMessage `json:"result"`
							Error  any             `json:"error"`
						}
						if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || envelope.Error != nil || len(envelope.Result) == 0 {
							t.Fatalf("RPC error=%v envelope=%+v", err, envelope)
						}
						return envelope.Result
					}
					var catalog struct {
						Tools []mcp.Tool `json:"tools"`
					}
					if err := json.Unmarshal(rpc("tools/list", nil), &catalog); err != nil {
						t.Fatal(err)
					}
					var descriptor *mcp.Tool
					for i := range catalog.Tools {
						if catalog.Tools[i].Name == "search" {
							descriptor = &catalog.Tools[i]
							break
						}
					}
					if descriptor == nil {
						t.Fatal("search not advertised")
					}
					properties := descriptor.InputSchema["properties"].(map[string]any)
					description := properties["search_type"].(map[string]any)["description"].(string)
					if !strings.Contains(description, "BM25") || !strings.Contains(description, "rejected") {
						t.Fatalf("strategy description=%q", description)
					}
					for _, mode := range properties["mode"].(map[string]any)["enum"].([]any) {
						if mode == "graph" {
							t.Fatal("unimplemented graph mode advertised")
						}
					}
					call := func(args map[string]any) mcp.ToolResult {
						t.Helper()
						var result mcp.ToolResult
						if err := json.Unmarshal(rpc("tools/call", args), &result); err != nil || len(result.Content) == 0 {
							t.Fatalf("result=%+v error=%v", result, err)
						}
						return result
					}
					for _, typ := range []string{"BM25", "AUTO"} {
						t.Run(typ, func(t *testing.T) {
							before := embeddings.Load()
							result := call(map[string]any{"search_query": "summary of project", "collection": "docs", "search_type": typ})
							if result.IsError {
								t.Fatalf("search failed: %+v", result)
							}
							if err := validateMCPOutputSchema(descriptor.OutputSchema, result.Content[0].Text, "$"); err != nil {
								t.Fatal(err)
							}
							if result.StructuredContent == nil {
								t.Fatal("missing structured output")
							}
							if err := validateMCPStructuredOutputSchema(descriptor.OutputSchema, result.StructuredContent, "$"); err != nil {
								t.Fatal(err)
							}
							body := result.StructuredContent.(map[string]any)
							hits := body["results"].([]any)
							if len(hits) != 1 || hits[0].(map[string]any)["id"] != "allowed" {
								t.Fatalf("ACL results=%+v", hits)
							}
							want := "CHUNKS_LEXICAL"
							if typ == "AUTO" {
								want = "HYBRID"
								if body["routing"].(map[string]any)["selected_type"] != want {
									t.Fatalf("routing=%+v", body)
								}
							}
							if body["search_type"] != want {
								t.Fatalf("search_type=%v want=%s", body["search_type"], want)
							}
							if (embeddings.Load() > before) != (typ == "AUTO") {
								t.Fatalf("embedding delta=%d type=%s", embeddings.Load()-before, typ)
							}
						})
					}
					t.Run("AUTO-without-embedding-client", func(t *testing.T) {
						client := h.cfg.EmbedClient
						h.cfg.EmbedClient = nil
						defer func() { h.cfg.EmbedClient = client }()
						before := embeddings.Load()
						result := call(map[string]any{"search_query": "summary of project", "collection": "docs", "search_type": "AUTO"})
						if result.IsError || result.StructuredContent == nil {
							t.Fatalf("lexical fallback=%+v", result)
						}
						body := result.StructuredContent.(map[string]any)
						hits := body["results"].([]any)
						if body["search_type"] != "CHUNKS_LEXICAL" || body["routing"].(map[string]any)["selected_type"] != "CHUNKS_LEXICAL" || len(hits) != 1 || hits[0].(map[string]any)["id"] != "allowed" || embeddings.Load() != before {
							t.Fatalf("missing-client AUTO=%+v embedding delta=%d", body, embeddings.Load()-before)
						}
					})
					for _, typ := range []string{"RAG_COMPLETION", "GRAPH_COMPLETION", "TEMPORAL", "SUMMARIES", "CODING_RULES", "CYPHER", "COMMUNITY_LOCAL", "unknown"} {
						t.Run("reject-"+typ, func(t *testing.T) {
							before := embeddings.Load()
							result := call(map[string]any{"search_query": "summary of project", "search_type": typ, "rerank": true, "multi_query": true})
							if !result.IsError || !strings.Contains(result.Content[0].Text, "unsupported search_type") {
								t.Fatalf("result=%+v", result)
							}
							if embeddings.Load() != before || reranks.Load() != 0 || len(provider.promptsSnapshot()) != 0 {
								t.Fatal("rejected strategy reached provider")
							}
						})
					}
					result := call(map[string]any{"search_query": "summary of project", "search_type": "BM25", "mode": "graph"})
					if !result.IsError || !strings.Contains(result.Content[0].Text, "unsupported search mode") {
						t.Fatalf("graph result=%+v", result)
					}
					if reranks.Load() != 0 || len(provider.promptsSnapshot()) != 0 {
						t.Fatal("retrieval unexpectedly generated or reranked")
					}
				})
			}
		}
	}
}
