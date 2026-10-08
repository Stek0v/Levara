package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/community"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

// This consumer fixture publishes a real native document generation. The
// community input hash is a fixture snapshot hash, not a builder attestation.
func communityPublicationFixture(t *testing.T, f *documentHTTPFixture, id string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	ref := f.r.DocumentRef
	revision, hash, err := f.p.SourceVersion(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	rawRevision, rawHash, err := f.p.SourceVersion(ctx, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	children, _ := json.Marshal([]community.Source{{DatasetID: "alpha", DocumentID: "visible", SourceRevision: rawRevision, RawContentHash: rawHash}})
	locked, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
	if err != nil {
		t.Fatal(err)
	}
	err = locked.CommitDocumentIndexVersioned(ctx, f.owner, ref, f.r.ContentRevision, "docs", "consumer-document-generation", accesspkg.DocumentPublicationLineage{SourceRevision: revision, RawContentHash: hash, SourcesJSON: string(children)})
	release()
	if err != nil {
		t.Fatal(err)
	}
	sources, _ := json.Marshal([]community.Source{{DatasetID: ref.DatasetID, DocumentID: ref.DataID, ContentRevision: f.r.ContentRevision, Derived: true, Collection: "docs", Generation: "consumer-document-generation", SourceRevision: revision, RawContentHash: hash, InputSHA256: strings.Repeat("a", 64)}})
	f.exec("UPDATE graph_communities SET generation='consumer-community-generation',sources_json=$1,lineage_verified=1 WHERE id=$2", string(sources), id)
	return string(sources)
}

func TestCommunityPublicationAccessConsumers(t *testing.T) {
	for _, scenario := range []string{"current", "unverified", "missing-generation", "empty", "malformed", "over-budget", "workspace", "missing-input-hash", "retired", "revised", "raw-child-revised", "wrong-document-generation", "stale-vector-generation", "malicious-vector-text", "peer", "selected-tenant", "SQL-failure"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.cfg.RequireAuth = true
				f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('publication',3,'SQL authoritative native summary')")
				proof := communityPublicationFixture(t, f, "publication")
				switch scenario {
				case "unverified":
					f.exec("UPDATE graph_communities SET lineage_verified=0 WHERE id='publication'")
				case "missing-generation":
					f.exec("UPDATE graph_communities SET generation='' WHERE id='publication'")
				case "empty":
					f.exec("UPDATE graph_communities SET sources_json='[]' WHERE id='publication'")
				case "malformed":
					f.exec("UPDATE graph_communities SET sources_json='[' WHERE id='publication'")
				case "over-budget":
					f.exec("UPDATE graph_communities SET sources_json=$1 WHERE id='publication'", strings.Repeat(" ", 128<<10)+"[]")
				case "workspace":
					f.exec("UPDATE graph_communities SET sources_json=$1 WHERE id='publication'", `[{"project_id":"alpha","path":"private.md"}]`)
				case "missing-input-hash":
					var sources []community.Source
					json.Unmarshal([]byte(proof), &sources)
					sources[0].InputSHA256 = ""
					raw, _ := json.Marshal(sources)
					f.exec("UPDATE graph_communities SET sources_json=$1 WHERE id='publication'", string(raw))
				case "retired":
					f.exec("UPDATE document_resources SET tombstoned=true WHERE dataset_id='alpha' AND data_id='blob'")
				case "revised":
					f.exec("UPDATE data SET source_revision=source_revision+1 WHERE id='blob'")
				case "raw-child-revised":
					f.exec("UPDATE data SET source_revision=source_revision+1 WHERE id='visible'")
				case "wrong-document-generation":
					f.exec("UPDATE document_index_publications SET generation='replacement' WHERE dataset_id='alpha' AND data_id='blob'")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				ctx = protectedResponseTestContext(ctx, f.cfg, time.Now().Add(time.Hour).Unix())
				actor := accesspkg.Actor{UserID: "root"}
				if scenario == "peer" {
					actor.UserID = "peer"
				}
				if scenario == "selected-tenant" {
					actor.TenantID = "a"
				}
				ctx = context.WithValue(ctx, mcp.UserIDKey, actor.UserID)
				ctx = context.WithValue(ctx, mcp.TenantIDKey, actor.TenantID)
				ctx = context.WithValue(ctx, searchActorKey{}, actor)
				if scenario == "SQL-failure" {
					f.exec("ALTER TABLE graph_communities RENAME TO unavailable_communities")
				}
				// Direct package admission proves HTTP-wrapper bypass does not grant access.
				result := mcp.ToolListCommunities(ctx, &mcpHandler{cfg: f.cfg}, nil)
				positive := scenario == "current" || scenario == "stale-vector-generation" || scenario == "malicious-vector-text"
				disclosed := len(result.Content) > 0 && strings.Contains(result.Content[0].Text, "SQL authoritative native summary")
				if disclosed != positive {
					t.Fatalf("package disclosure positive=%v result=%+v", positive, result)
				}
				if scenario == "SQL-failure" && !result.IsError {
					t.Fatal("authenticated SQL failure became successful empty list")
				}
				wire, _ := json.Marshal(result)
				if strings.Contains(string(wire), "sources_json") || strings.Contains(string(wire), "input_sha256") || strings.Contains(string(wire), "CommunityEvidence") {
					t.Fatalf("internal evidence leaked: %s", wire)
				}
				raw := map[string]any{"community_id": "publication", "generation": "consumer-community-generation", "text": "MALICIOUS VECTOR TEXT"}
				if scenario == "stale-vector-generation" {
					raw["generation"] = "obsolete"
				}
				metadata, _ := json.Marshal(raw)
				scored, err := filterMCPDocumentResults(ctx, f.cfg, []pipeline.ScoredResult{{ID: "publication", Metadata: metadata}})
				wantHit := scenario == "current" || scenario == "malicious-vector-text"
				if scenario == "SQL-failure" {
					if err == nil {
						t.Fatal("SQL loader failure hidden")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(scored) > 0 != wantHit {
					t.Fatalf("vector admission=%v want=%v", len(scored) > 0, wantHit)
				}
				if wantHit && (!strings.Contains(string(scored[0].Metadata), "SQL authoritative native summary") || strings.Contains(string(scored[0].Metadata), "MALICIOUS")) {
					t.Fatalf("vector text adopted: %s", scored[0].Metadata)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("consumer leaked pool1")
				}
			})
		})
	}
}

func TestCommunityPublicationFenceExactMaterializedProof(t *testing.T) {
	for _, mutation := range []string{"generation", "proof", "raw-child"} {
		t.Run(mutation, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.cfg.RequireAuth = true
				f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('publication',3,'materialized native summary')")
				proof := communityPublicationFixture(t, f, "publication")
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				ctx = protectedResponseTestContext(ctx, f.cfg, time.Now().Add(time.Hour).Unix())
				result := (&mcpHandler{cfg: f.cfg}).toolListCommunities(ctx, nil)
				if result.IsError || len(result.CommunityEvidence) != 1 || result.CommunityEvidence[0].SourcesJSON != proof {
					t.Fatalf("missing captured evidence: %+v", result)
				}
				switch mutation {
				case "generation":
					f.exec("UPDATE graph_communities SET generation='replacement' WHERE id='publication'")
				case "proof":
					var sources []community.Source
					json.Unmarshal([]byte(proof), &sources)
					sources[0].InputSHA256 = strings.Repeat("b", 64)
					raw, _ := json.Marshal(sources)
					f.exec("UPDATE graph_communities SET sources_json=$1 WHERE id='publication'", string(raw))
				case "raw-child":
					f.exec("UPDATE data SET source_revision=source_revision+1 WHERE id='visible'")
				}
				called := false
				err := withSearchReadFence(ctx, func(context.Context) error { called = true; return nil })
				if err == nil || called {
					t.Fatalf("retired evidence reached model fence: called=%v err=%v", called, err)
				}
				app := fiber.New()
				c := app.AcquireCtx(&fasthttp.RequestCtx{})
				defer app.ReleaseCtx(c)
				c.JSON(result)
				var denied *fiber.Error
				err = sendProtectedResponseWithFence(c, ctx)
				if !errors.As(err, &denied) || denied.Code != 403 {
					t.Fatalf("retired evidence reached completed body: %v", err)
				}
				if c.Response().IsBodyStream() || f.db.Stats().InUse != 0 {
					t.Fatal("retired publication leaked body/fence")
				}
			})
		})
	}
}

func TestCommunityPublicationRESTSearchNative(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	for _, kind := range []string{"COMMUNITY_LOCAL", "COMMUNITY_GLOBAL"} {
		for _, scenario := range []string{"current", "retired", "stale-vector", "unverified", "malicious-vector"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
					f.db.SetMaxOpenConns(1)
					cfg, closeFn := newWorkspaceTestConfig(t)
					defer closeFn()
					cfg.DB = f.db
					cfg.RequireAuth = true
					cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 4, 1)
					model := &recordingLLM{responses: []string{"native community answer"}}
					cfg.LLMProvider = model
					f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('publication',3,'SQL authoritative native summary')")
					communityPublicationFixture(t, f, "publication")
					f.exec("INSERT INTO graph_nodes(id,name,dataset_id,properties) VALUES('native-node','NATIVE_ENTITY','alpha',$1)", `{"document_id":"blob","content_revision":1,"collection":"docs","generation":"consumer-document-generation"}`)
					f.exec("INSERT INTO community_members(community_id,node_id,level) VALUES('publication','native-node',0)")
					collection := "_community_summaries"
					metadata := map[string]any{"community_id": "publication", "generation": "consumer-community-generation", "text": "MALICIOUS VECTOR TEXT"}
					if kind == "COMMUNITY_LOCAL" {
						collection = "community-entities"
						metadata = map[string]any{"name": "NATIVE_ENTITY", "text": "entity", "dataset_id": "alpha", "document_id": "blob", "content_revision": f.r.ContentRevision, "collection": "docs", "generation": "consumer-document-generation"}
					}
					if scenario == "stale-vector" {
						if kind == "COMMUNITY_LOCAL" {
							metadata["generation"] = "obsolete"
						} else {
							metadata["generation"] = "obsolete"
						}
					}
					if err := cfg.Collections.Create(collection); err != nil {
						t.Fatal(err)
					}
					if err := cfg.Collections.Insert(collection, "native-vector", []float32{5, 1}, metadata); err != nil {
						t.Fatal(err)
					}
					if scenario == "retired" {
						f.exec("UPDATE document_resources SET tombstoned=true WHERE dataset_id='alpha' AND data_id='blob'")
					}
					if scenario == "unverified" {
						f.exec("UPDATE graph_communities SET lineage_verified=0 WHERE id='publication'")
					}
					app := fiber.New()
					app.Use(func(c *fiber.Ctx) error {
						c.Locals("user_id", "root")
						c.Locals("verified_jwt", jwtPayload{Sub: "root", Exp: time.Now().Add(time.Hour).Unix()})
						return c.Next()
					})
					app.Post("/search", searchHandler(cfg))
					body, _ := json.Marshal(map[string]any{"query_text": "query", "query_type": kind, "collection": collection, "top_k": 5})
					request := httptest.NewRequest("POST", "/search", strings.NewReader(string(body)))
					request.Header.Set("Content-Type", "application/json")
					response, err := app.Test(request, -1)
					if err != nil {
						t.Fatal(err)
					}
					raw, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode != 200 {
						t.Fatalf("HTTP%d %s", response.StatusCode, raw)
					}
					positive := scenario == "current" || scenario == "malicious-vector"
					prompts := model.promptsSnapshot()
					if len(prompts) > 0 != positive {
						t.Fatalf("model reached=%v want=%v body=%s", len(prompts) > 0, positive, raw)
					}
					if positive && (!strings.Contains(strings.Join(prompts, "\n"), "SQL authoritative native summary") || strings.Contains(strings.Join(prompts, "\n"), "MALICIOUS")) {
						t.Fatalf("wrong model context %v", prompts)
					}
					if !positive && strings.Contains(string(raw), "publication") {
						t.Fatalf("retired community ID disclosed: %s", raw)
					}
					if strings.Contains(string(raw), "sources_json") || strings.Contains(string(raw), "input_sha256") {
						t.Fatalf("proof leaked: %s", raw)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("REST consumer leaked SQL")
					}
				})
			})
		}
	}
}

func TestCommunityPublicationNativeCollectionOrigin(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, scenario := range []string{"current", "missing-id", "malformed-metadata"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				cfg, closeFn := newWorkspaceTestConfig(t)
				defer closeFn()
				cfg.DB = f.db
				cfg.RequireAuth = true
				cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 4, 1)
				f.exec("INSERT INTO graph_communities(id,member_count,summary) VALUES('publication',3,'SQL origin-bound native summary')")
				communityPublicationFixture(t, f, "publication")
				if err := cfg.Collections.Create("_community_summaries"); err != nil {
					t.Fatal(err)
				}
				var metadata any = map[string]any{"community_id": "publication", "generation": "consumer-community-generation", "text": "MALICIOUS ORIGIN TEXT"}
				if scenario == "missing-id" {
					metadata = map[string]any{"generation": "consumer-community-generation", "text": "MALICIOUS ORIGIN TEXT", "collection": "ordinary-documents"}
				}
				if scenario == "malformed-metadata" {
					metadata = "malformed metadata"
				}
				if err := cfg.Collections.Insert("_community_summaries", "native-vector", []float32{5, 1}, metadata); err != nil {
					t.Fatal(err)
				}
				// Prove actual native retrieval stamps the origin independently of JSON.
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
				defer cancel()
				native := pipeline.NewSearchPipeline(cfg.EmbedClient, cfg.Collections, nil)
				hits, err := native.SearchByText(ctx, "_community_summaries", "query", 5)
				if err != nil || len(hits) != 1 || hits[0].Collection != "_community_summaries" {
					t.Fatalf("native origin hits=%+v err=%v", hits, err)
				}
				if scenario == "missing-id" {
					localCfg := cfg
					localCfg.RequireAuth = false
					localCtx := context.WithValue(ctx, searchActorKey{}, accesspkg.Actor{})
					local, err := filterMCPDocumentResults(localCtx, localCfg, hits)
					if err != nil || len(local) != 1 || !strings.Contains(string(local[0].Metadata), "MALICIOUS ORIGIN TEXT") {
						t.Fatalf("trusted anonymous legacy compatibility lost: %v %v", local, err)
					}
				}
				wire, _ := json.Marshal(hits[0])
				if strings.Contains(string(wire), "\"Collection\"") {
					t.Fatalf("internal origin serialized: %s", wire)
				}

				rest := fiber.New()
				rest.Use(func(c *fiber.Ctx) error {
					c.Locals("user_id", "root")
					c.Locals("verified_jwt", jwtPayload{Sub: "root", Exp: time.Now().Add(time.Hour).Unix()})
					return c.Next()
				})
				rest.Post("/search", searchHandler(cfg))
				request := httptest.NewRequest("POST", "/search", strings.NewReader(`{"query_text":"query","query_type":"CHUNKS","collection":"_community_summaries","top_k":5,"rerank":false}`))
				request.Header.Set("Content-Type", "application/json")
				response, err := rest.Test(request, -1)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(response.Body)
				response.Body.Close()
				positive := scenario == "current"
				if response.StatusCode != 200 || strings.Contains(string(raw), "native-vector") != positive || strings.Contains(string(raw), "MALICIOUS") {
					t.Fatalf("REST origin admission positive=%v status=%d body=%s", positive, response.StatusCode, raw)
				}
				if positive && !strings.Contains(string(raw), "SQL origin-bound native summary") {
					t.Fatalf("REST omitted SQL summary: %s", raw)
				}

				h := asyncAuthorityHandler(f)
				h.cfg.EmbedEndpoint = cfg.EmbedEndpoint
				h.cfg.EmbedModel = cfg.EmbedModel
				h.cfg.EmbedClient = cfg.EmbedClient
				h.cfg.Collections = cfg.Collections
				app := asyncAuthorityApp(h, nil)
				for _, path := range []string{"/mcp", latestMCPPath} {
					body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"search_query":"query","search_type":"BASIC","collection":"_community_summaries","top_k":5,"rerank":false}}}`
					if path == latestMCPPath {
						body = strings.TrimSuffix(body, "}}") + "," + latestMCPMetaParams() + "}}"
					}
					request := httptest.NewRequest("POST", path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Authorization", "Bearer "+createJWT("root", "root@test.invalid", h.cfg.JWTSecret))
					if path == latestMCPPath {
						request.Header.Set("Accept", "application/json, text/event-stream")
						for key, value := range latestMCPHeaders("tools/call") {
							request.Header.Set(key, value)
						}
						request.Header.Set("Mcp-Name", "search")
					} else {
						request.Header.Set("Mcp-Session-Id", h.createSession("root"))
					}
					response, err := app.Test(request, -1)
					if err != nil {
						t.Fatal(err)
					}
					raw, _ := io.ReadAll(response.Body)
					response.Body.Close()
					var envelope struct {
						Result mcp.ToolResult `json:"result"`
					}
					if response.StatusCode != 200 || json.Unmarshal(raw, &envelope) != nil || envelope.Result.IsError {
						t.Fatalf("MCP origin request %s status=%d body=%s", path, response.StatusCode, raw)
					}
					if strings.Contains(string(raw), "native-vector") != positive || strings.Contains(string(raw), "MALICIOUS") {
						t.Fatalf("MCP origin admission positive=%v body=%s", positive, raw)
					}
					if positive && !strings.Contains(string(raw), "SQL origin-bound native summary") {
						t.Fatalf("MCP omitted SQL summary: %s", raw)
					}
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("native origin check leaked SQL")
				}
			})
		})
	}
}
