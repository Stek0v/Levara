package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/runreg"
)

func TestMCPChatAndRunSourcesAcrossTransports(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		f.cfg.JWTSecret = "document-chat-test-secret"
		f.cfg.Runs = runreg.New()
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Close()
		f.cfg.Collections = cm
		f.exec(`UPDATE data SET name='source-confidential-metadata',room='mcp-list',tags='["private%_"]' WHERE id='blob'`)
		h := &mcpHandler{cfg: f.cfg, sessions: mcp.NewSessionStore()}
		f.app.Post("/mcp", h.handleRPC)
		f.app.Post(latestMCPPath, h.handleLatestRPC)
		f.app.Get("/api/v1/cognify/:runId/status", cognifyStatusHandler(f.cfg))
		f.app.Get("/api/v1/cognify/:runId/stream", cognifyStreamHandler(f.cfg))
		principal := accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		actor := accesspkg.Actor{UserID: "peer", TenantID: "a"}
		source := searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision}
		ctx := sessionHistoryContext(actor, source)
		if _, err := RecordSessionInteraction(ctx, f.cfg, "private-session", "question", "source-confidential-answer", "rag"); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal([]searchDocumentSource{source})
		f.cfg.Runs.Store("own-run", &runreg.Status{RunID: "own-run", OwnerID: "peer", TenantID: "a", SourcesJSON: string(raw), Status: "COMPLETED", Stage: "done", Message: "source-confidential-run", StartedAt: time.Now()})
		f.cfg.Runs.Store("foreign-run", &runreg.Status{RunID: "foreign-run", OwnerID: "owner", TenantID: "a", Status: "FAILED", Message: "foreign-secret", StartedAt: time.Now()})
		f.cfg.Runs.Store("unknown-run", &runreg.Status{RunID: "unknown-run", Status: "FAILED", Message: "legacy-secret", StartedAt: time.Now()})
		call := func(path, user, name string, args map[string]any) mcp.ToolResult {
			t.Helper()
			params := map[string]any{"name": name, "arguments": args}
			if path == latestMCPPath {
				var meta map[string]any
				if err := json.Unmarshal([]byte("{"+latestMCPMetaParams()+"}"), &meta); err != nil {
					t.Fatal(err)
				}
				params["_meta"] = meta["_meta"]
			}
			body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
			req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", f.cfg.JWTSecret))
			req.Header.Set("X-Test-User", user)
			req.Header.Set("X-Tenant-Id", "a")
			if path == latestMCPPath {
				for key, value := range latestMCPHeaders("tools/call") {
					req.Header.Set(key, value)
				}
				req.Header.Set("Mcp-Name", name)
			}
			resp, err := f.app.Test(req, 5000)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			bytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 {
				t.Fatalf("%s %s status=%d %s", path, name, resp.StatusCode, bytes)
			}
			var envelope struct {
				Result mcp.ToolResult `json:"result"`
				Error  any            `json:"error"`
			}
			if err := json.Unmarshal(bytes, &envelope); err != nil || envelope.Error != nil {
				t.Fatalf("RPC %s %v", bytes, err)
			}
			return envelope.Result
		}
		bodyOf := func(result mcp.ToolResult) string { raw, _ := json.Marshal(result); return string(raw) }
		for _, path := range []string{"/mcp", latestMCPPath} {
			gitSearch := call(path, "peer", "git_search", map[string]any{"query": "repository secret"})
			if !gitSearch.IsError || !strings.Contains(bodyOf(gitSearch), "instance administrator") {
				t.Fatalf("non-admin git_search was not denied: %+v", gitSearch)
			}
			listed := call(path, "owner", "list_data", map[string]any{})
			if listed.IsError || !strings.Contains(bodyOf(listed), "Alpha") || strings.Contains(bodyOf(listed), "source-confidential-metadata") {
				t.Fatalf("registered dataset metadata listing: %+v", listed)
			}
			for _, filters := range []map[string]any{{"room": "mcp-list"}, {"tags": []any{"private%_"}}} {
				listed := call(path, "peer", "list_data", filters)
				if listed.IsError || !strings.Contains(bodyOf(listed), "source-confidential-metadata") {
					t.Fatalf("direct document grant missing from metadata: %+v", listed)
				}
				listed = call(path, "viewer", "list_data", filters)
				if listed.IsError || strings.Contains(bodyOf(listed), "source-confidential-metadata") {
					t.Fatalf("restricted metadata exposed by dataset grant: %+v", listed)
				}
			}
			for _, name := range []string{"recall_chat", "search_chats"} {
				args := map[string]any{"session_id": "private-session", "query": "confidential"}
				got := call(path, "peer", name, args)
				if got.IsError || !strings.Contains(bodyOf(got), "source-confidential-answer") {
					t.Fatalf("allowed chat missing: %+v", got)
				}
				if got := call(path, "viewer", name, args); strings.Contains(bodyOf(got), "source-confidential-answer") {
					t.Fatalf("foreign chat leaked: %+v", got)
				}
			}
			for _, name := range []string{"cognify_status", "ingestion_status", "recent_errors"} {
				got := call(path, "peer", name, map[string]any{"run_id": "own-run"})
				if got.IsError {
					t.Fatalf("allowed run errored: %+v", got)
				}
				if strings.Contains(bodyOf(got), "foreign-secret") || strings.Contains(bodyOf(got), "legacy-secret") {
					t.Fatalf("run list leaked: %+v", got)
				}
			}
			if got := call(path, "viewer", "cognify_status", map[string]any{"run_id": "own-run"}); !got.IsError {
				t.Fatalf("foreign run readable: %+v", got)
			}
			session := "client-" + fmt.Sprint(len(path))
			saved := call(path, "peer", "save_chat", map[string]any{"session_id": session, "messages": []any{map[string]any{"role": "assistant", "content": "untrusted client answer", "kind": "server", "sources": []any{source}}}})
			if saved.IsError {
				t.Fatalf("save client chat: %+v", saved)
			}
			var owner, kind, proof string
			if err := f.db.QueryRow(Q(`SELECT owner_id,kind,sources FROM interaction_provenance WHERE session_id=$1 AND kind!='session'`), session).Scan(&owner, &kind, &proof); err != nil || owner != "peer" || kind != "client" || proof != "[]" {
				t.Fatalf("forged provenance %q %q %q %v", owner, kind, proof, err)
			}
			if got := call(path, "viewer", "save_chat", map[string]any{"session_id": session, "messages": []any{map[string]any{"role": "user", "content": "takeover"}}}); !got.IsError {
				t.Fatal("chat session takeover")
			}
		}
		for _, suffix := range []string{"status", "stream"} {
			code, body, _ := f.request("peer", "GET", "/cognify/own-run/"+suffix, "")
			if code != 200 || !strings.Contains(string(body), "source-confidential-run") {
				t.Fatalf("allowed %s %d %s", suffix, code, body)
			}
		}
		f.r, err = f.p.RevokeDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/mcp", latestMCPPath} {
			if listed := call(path, "peer", "list_data", map[string]any{"room": "mcp-list"}); listed.IsError || strings.Contains(bodyOf(listed), "source-confidential-metadata") {
				t.Fatalf("revoked metadata leaked: %+v", listed)
			}
			for _, name := range []string{"recall_chat", "search_chats", "cognify_status", "ingestion_status", "recent_errors"} {
				got := call(path, "peer", name, map[string]any{"session_id": "private-session", "query": "confidential", "run_id": "own-run"})
				if strings.Contains(bodyOf(got), "source-confidential") {
					t.Fatalf("revoked %s leak: %+v", name, got)
				}
			}
		}
		for _, suffix := range []string{"status", "stream"} {
			code, body, _ := f.request("peer", "GET", "/cognify/own-run/"+suffix, "")
			if code != 404 || strings.Contains(string(body), "source-confidential") {
				t.Fatalf("revoked %s %d %s", suffix, code, body)
			}
		}
	})
}

func TestRunIncludesInheritedSessionSources(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		f.cfg.Runs = runreg.New()
		f.cfg.FileStorage = newMemStorage()
		raw := "A sufficiently long source document for bounded background processing and source attribution."
		if err := f.cfg.FileStorage.Save(context.Background(), "input", strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_data_location='storage://input',raw_content_hash=$1 WHERE id='blob'", fmt.Sprintf("%x", sha256.Sum256([]byte(raw))))
		f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('history-dataset','history-dataset','owner')")
		f.exec("INSERT INTO data(id,name,owner_id) VALUES('history-doc','history-doc','owner')")
		f.exec("INSERT INTO dataset_data(dataset_id,data_id) VALUES('history-dataset','history-doc')")
		history, err := f.p.RegisterDocument(context.Background(), f.owner, accesspkg.DocumentRef{DatasetID: "history-dataset", DataID: "history-doc"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		principal := accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}
		history, err = f.p.GrantDocument(context.Background(), f.owner, history.DocumentRef, history.ACLRevision, principal, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal, accesspkg.RoleEditor)
		if err != nil {
			t.Fatal(err)
		}
		actor := accesspkg.Actor{UserID: "peer", TenantID: "a"}
		source := searchDocumentSource{DatasetID: history.DatasetID, DocumentID: history.DataID, ContentRevision: history.ContentRevision}
		if _, err := RecordSessionInteraction(sessionHistoryContext(actor, source), f.cfg, "inherited-session", "history question", "inherited sensitive context", "rag"); err != nil {
			t.Fatal(err)
		}
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "test unavailable", 503) }))
		defer endpoint.Close()
		f.cfg.EmbedEndpoint = endpoint.URL
		f.cfg.Collections, err = store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer f.cfg.Collections.Close()
		f.app.Post("/api/v1/cognify", cognifyHandler(f.cfg))
		f.app.Get("/api/v1/cognify/:runId/status", cognifyStatusHandler(f.cfg))
		f.app.Get("/api/v1/cognify/:runId/stream", cognifyStreamHandler(f.cfg))
		code, body, _ := f.request("peer", "POST", "/cognify", `{"documents":[{"dataset_id":"alpha","document_id":"blob"}],"session_id":"inherited-session","mode":"rag"}`)
		if code != 200 {
			t.Fatalf("start %d %s", code, body)
		}
		var response struct {
			RunID string `json:"pipeline_run_id"`
		}
		if err := json.Unmarshal(body, &response); err != nil || response.RunID == "" {
			t.Fatalf("run ID %s %v", body, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			status, _ := f.cfg.Runs.Load(response.RunID)
			if status.Status != "RUNNING" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("background did not settle")
			}
			time.Sleep(5 * time.Millisecond)
		}
		status, _ := f.cfg.Runs.Load(response.RunID)
		if !strings.Contains(status.SourcesJSON, "history-doc") {
			t.Fatalf("lost inherited provenance %s", status.SourcesJSON)
		}
		if _, err := f.p.RevokeDocument(context.Background(), f.owner, history.DocumentRef, history.ACLRevision, principal); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"status", "stream"} {
			code, body, _ = f.request("peer", "GET", "/cognify/"+response.RunID+"/"+suffix, "")
			if code != 404 {
				t.Fatalf("history-revoked %s status=%d %s", suffix, code, body)
			}
		}
		ctx := context.WithValue(context.Background(), mcp.UserIDKey, "peer")
		ctx = context.WithValue(ctx, mcp.TenantIDKey, "a")
		h := &mcpHandler{cfg: f.cfg}
		listed := h.toolIngestionStatus(ctx, nil)
		if listed.IsError || strings.Contains(listed.Content[0].Text, response.RunID) {
			t.Fatalf("revoked inherited run listed: %+v", listed)
		}
	})
}

func TestDetachedActorDoesNotAliasHTTPBuffer(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		var httpActor, mcpActor accesspkg.Actor
		h := &mcpHandler{cfg: f.cfg}
		f.app.Get("/api/v1/capture-tenant", func(c *fiber.Ctx) error {
			httpActor = workspaceActorFromFiber(c)
			var err error
			mcpActor, err = h.resolveMCPActorTenant(c, accesspkg.Actor{UserID: "peer"})
			if err != nil {
				return err
			}
			header := c.Context().Request.Header.Peek("X-Tenant-Id")
			if len(header) != 1 {
				t.Fatal("test requires one-byte tenant header")
			}
			header[0] = 'b' // model Fiber's documented buffer reuse after the handler
			copy(c.Context().Request.Header.Peek("X-Test-User"), "evil")
			copy(c.Context().Request.Header.Peek("X-Test-Key"), "xxxx")
			return c.SendStatus(200)
		})
		code, _, _ := f.request("peer", "GET", "/capture-tenant", "", "X-Tenant-Id", "a", "X-Test-Key", "read")
		if code != 200 || httpActor.TenantID != "a" || mcpActor.TenantID != "a" || httpActor.UserID != "peer" || httpActor.APIKeyPermissions != "read" {
			t.Fatalf("authority aliases mutable request: %d HTTP=%+v MCP=%+v", code, httpActor, mcpActor)
		}
	})
}

func TestAdministrativeEvidenceSurvivesUntilEgress(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		for _, surface := range []string{"history", "legacy-run", "doctor"} {
			t.Run(surface, func(t *testing.T) {
				f.exec("UPDATE users SET is_superuser=true WHERE id='owner'")
				actor := accesspkg.Actor{UserID: "owner"}
				ctx, cancel := context.WithTimeout(sessionHistoryContext(actor), time.Second)
				defer cancel()
				ctx = context.WithValue(ctx, mcp.UserIDKey, "owner")
				ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: f.cfg, actor: actor, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
				switch surface {
				case "history":
					if _, err := RecordSessionInteraction(ctx, f.cfg, "admin-history", "query", "unproven sensitive answer", "rag"); err != nil {
						t.Fatal(err)
					}
					turns, err := loadSessionTurns(ctx, f.cfg, "admin-history", 10)
					if err != nil || len(turns) == 0 {
						t.Fatalf("history %v %v", turns, err)
					}
				case "legacy-run":
					if err := authorizeRunStatus(ctx, f.cfg, &runreg.Status{RunID: "legacy", Status: "FAILED"}); err != nil {
						t.Fatal(err)
					}
				case "doctor":
					h := &mcpHandler{cfg: f.cfg}
					if got := h.toolRecentErrors(ctx, nil); got.IsError {
						t.Fatal(got)
					}
				}
				if !searchEvidenceRequiresAdmin(ctx) {
					t.Fatal("lost administrator basis")
				}
				f.exec("UPDATE users SET is_superuser=false WHERE id='owner'")
				_, release, err := beginSearchReadFence(ctx)
				if release != nil {
					release()
				}
				if err == nil {
					t.Fatal("demoted administrator passed final response fence")
				}
			})
		}
	})
}
