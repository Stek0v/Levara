package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/valyala/fasthttp"
)

const codifyNativeCode = "package example\nimport \"fmt\"\ntype A struct{}\ntype B struct{}\nfunc (a A) Work() { fmt.Println(\"one\") }\nfunc (b B) Work() {}\n"

func codifyRPCBody(t *testing.T, path string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	meta := ""
	if path == latestMCPPath {
		meta = "," + latestMCPMetaParams()
	}
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"codify","arguments":%s%s}}`, raw, meta)
}

func codifyRPCHeaders(h *mcpHandler, path, user string) map[string]string {
	headers := map[string]string{"Authorization": "Bearer " + createJWT(user, user+"@test.invalid", h.cfg.JWTSecret)}
	if path == latestMCPPath {
		for key, value := range latestMCPHeaders("tools/call") {
			headers[key] = value
		}
		headers["Accept"] = "application/json, text/event-stream"
		headers["Mcp-Name"] = "codify"
	} else {
		headers["Mcp-Session-Id"] = h.createSession(user)
	}
	return headers
}

func codifyNativeRPC(t *testing.T, app *fiber.App, h *mcpHandler, path, user string, args map[string]any, extra map[string]string) (int, mcp.ToolResult) {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(codifyRPCBody(t, path, args)))
	req.Header.Set("Content-Type", "application/json")
	for key, value := range codifyRPCHeaders(h, path, user) {
		req.Header.Set(key, value)
	}
	for key, value := range extra {
		if value == "" {
			req.Header.Del(key)
		} else {
			req.Header.Set(key, value)
		}
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result *mcp.ToolResult `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if resp.StatusCode != 200 {
		if resp.StatusCode != 401 && resp.StatusCode != 403 && resp.StatusCode != 404 {
			t.Fatalf("unexpected RPC status=%d body=%s", resp.StatusCode, raw)
		}
		return resp.StatusCode, mcp.ToolResult{IsError: true}
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Result == nil || len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		t.Fatalf("invalid codify envelope: %s decode=%v", raw, err)
	}
	return resp.StatusCode, *envelope.Result
}

func codifyNativeSnapshot(t *testing.T, f *documentHTTPFixture) string {
	t.Helper()
	var tables [][][]any
	for _, table := range []string{"datasets", "data", "dataset_data", "document_resources", "document_structured_artifacts", "document_pipeline_statuses", "document_index_publications", "graph_nodes", "graph_edges"} {
		rows, err := f.db.Query("SELECT * FROM " + table + " ORDER BY 1,2")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var records [][]any
		for rows.Next() {
			record := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range record {
				dest[i] = &record[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			records = append(records, record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		tables = append(tables, records)
	}
	raw, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCodifyAuthorityNativeTransports(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, path := range []string{"/mcp", latestMCPPath} {
		documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
			ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
			t.Cleanup(func() { ingest.SetSQLiteMode(false) })
			f.db.SetMaxOpenConns(1)
			h := asyncAuthorityHandler(f)
			app := asyncAuthorityApp(h, nil)
			for _, scenario := range []string{"peer", "selected-tenant", "read-key", "delete-key", "expired", "inactive", "revoked-key", "revoked-admin", "javascript", "malformed-go"} {
				t.Run(path+"/"+scenario, func(t *testing.T) {
					args := map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}
					user := "root"
					extra := map[string]string{}
					switch scenario {
					case "peer":
						user = "peer"
					case "inactive":
						user = "inactive"
					case "selected-tenant":
						f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('root','a') ON CONFLICT DO NOTHING")
						defer f.exec("DELETE FROM user_tenant WHERE user_id='root' AND tenant_id='a'")
						extra["X-Tenant-Id"] = "a"
					case "read-key", "delete-key", "revoked-key":
						permission := strings.TrimSuffix(scenario, "-key")
						if scenario == "revoked-key" {
							permission = "write"
						}
						f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'root',$3)", scenario, apikeyHash(scenario), permission)
						if scenario == "revoked-key" {
							f.exec("DELETE FROM api_keys WHERE id=$1", scenario)
						}
						extra["Authorization"] = ""
						extra["X-Api-Key"] = scenario
					case "expired":
						extra["Authorization"] = "Bearer " + signSessionPayload(jwtPayload{Sub: "root", Email: "root@test.invalid", Iat: time.Now().Add(-2 * time.Hour).Unix(), Exp: time.Now().Add(-time.Hour).Unix()}, h.cfg.JWTSecret)
					case "revoked-admin":
						f.exec("UPDATE users SET is_superuser=false WHERE id='root'")
						defer f.exec("UPDATE users SET is_superuser=true WHERE id='root'")
					case "javascript":
						args["filename"] = "fixture.js"
					case "malformed-go":
						args["code"] = "package example\nfunc broken("
					}
					before := codifyNativeSnapshot(t, f)
					_, result := codifyNativeRPC(t, app, h, path, user, args, extra)
					if !result.IsError {
						t.Fatalf("denied scenario %s returned success: %+v", scenario, result)
					}
					if after := codifyNativeSnapshot(t, f); after != before {
						t.Fatalf("%s modified native knowledge", scenario)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("denied codify leaked SQL connection")
					}
				})
			}
			for _, key := range []bool{false, true} {
				extra := map[string]string{}
				if key {
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('codify-write',$1,'root','write')", apikeyHash("codify-write"))
					extra["Authorization"] = ""
					extra["X-Api-Key"] = "codify-write"
				}
				status, result := codifyNativeRPC(t, app, h, path, "root", map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}, extra)
				if status != 200 || result.IsError || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, `"language": "go"`) {
					t.Fatalf("admin/key codify status=%d result=%+v", status, result)
				}
			}
		})
	}
}

func codifyNativePublication(t *testing.T, f *documentHTTPFixture, code string) searchDocumentSource {
	t.Helper()
	var source searchDocumentSource
	var requiresAdmin int
	err := f.db.QueryRow(Q("SELECT p.dataset_id,p.data_id,p.content_revision,p.generation,p.source_revision,p.raw_content_hash,p.requires_admin FROM document_index_publications p JOIN data d ON d.id=p.data_id WHERE d.owner_id='root' AND p.collection_name='code_knowledge' AND d.raw_content_hash=$1"), fmt.Sprintf("%x", sha256.Sum256([]byte(code)))).Scan(&source.DatasetID, &source.DocumentID, &source.ContentRevision, &source.Generation, &source.SourceRevision, &source.RawContentHash, &requiresAdmin)
	if err != nil {
		t.Fatal(err)
	}
	source.Collection, source.Derived = "code_knowledge", true
	if requiresAdmin != 1 || source.Generation == "" || source.SourceRevision < 1 || source.RawContentHash == "" || source.ContentRevision < 0 {
		t.Fatalf("invalid native publication %+v requires_admin=%d", source, requiresAdmin)
	}
	revision, hash, err := f.p.SourceVersion(context.Background(), accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID})
	if err != nil || revision != source.SourceRevision || hash != source.RawContentHash {
		t.Fatalf("source version mismatch revision=%d hash=%s err=%v publication=%+v", revision, hash, err, source)
	}
	resource, err := f.p.GetDocumentResource(context.Background(), accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID})
	if errors.Is(err, accesspkg.ErrDocumentNotFound) {
		if source.ContentRevision != 0 {
			t.Fatalf("legacy source has invented content revision: %+v", source)
		}
	} else if err != nil || resource.ContentRevision != source.ContentRevision {
		t.Fatalf("resource content revision mismatch resource=%+v source=%+v err=%v", resource, source, err)
	}
	return source
}

func codifyNativeGraphProof(t *testing.T, f *documentHTTPFixture, source searchDocumentSource) []string {
	t.Helper()
	var edgeIDs []string
	for _, table := range []string{"graph_nodes", "graph_edges"} {
		nameColumn := "''"
		if table == "graph_nodes" {
			nameColumn = "name"
		}
		rows, err := f.db.Query(Q("SELECT id,"+nameColumn+",properties FROM "+table+" WHERE dataset_id=$1"), source.DatasetID)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		methods := map[string]bool{}
		for rows.Next() {
			var id, name, properties string
			if err := rows.Scan(&id, &name, &properties); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			proof, err := decodeSearchDocumentSource(properties)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if proof.DocumentID != source.DocumentID || proof.Generation != source.Generation {
				continue
			}
			if proof.ContentRevision != source.ContentRevision || proof.Collection != source.Collection {
				rows.Close()
				t.Fatalf("unstamped %s assertion %s proof=%+v", table, id, proof)
			}
			count++
			if strings.HasSuffix(name, ".Work") {
				methods[name] = true
			}
			if table == "graph_edges" {
				edgeIDs = append(edgeIDs, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("no current %s assertions", table)
		}
		if table == "graph_nodes" && len(methods) != 2 {
			t.Fatalf("same-name methods merged: %v", methods)
		}
	}
	var dangling int
	if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM graph_edges e LEFT JOIN graph_nodes s ON s.id=e.source_id LEFT JOIN graph_nodes d ON d.id=e.target_id WHERE e.dataset_id=$1 AND (s.id IS NULL OR d.id IS NULL)"), source.DatasetID).Scan(&dangling); err != nil || dangling != 0 {
		t.Fatalf("dangling native endpoints=%d err=%v", dangling, err)
	}
	return edgeIDs
}

func TestCodifyNativePublicationLifecycle(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	var modelCalls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		http.Error(w, "static code must not call model", 500)
	}))
	defer model.Close()
	t.Setenv("LLM_ENDPOINT", model.URL)
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		publish := func(code string) {
			t.Helper()
			status, result := codifyNativeRPC(t, app, h, latestMCPPath, "root", map[string]any{"code": code, "filename": "fixture.go"}, nil)
			if status != 200 || result.IsError {
				t.Fatalf("native publication status=%d result=%+v", status, result)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, mcp.UserIDKey, "root")
		ctx = context.WithValue(ctx, searchActorKey{}, accesspkg.Actor{UserID: "root"})
		visible := func(source searchDocumentSource, want bool) {
			t.Helper()
			allowed, err := searchDocumentAllowed(ctx, h.cfg, accesspkg.Actor{UserID: "root"}, source)
			if err != nil || allowed != want {
				t.Fatalf("source eligible=%v want=%v err=%v source=%+v", allowed, want, err, source)
			}
		}
		publish(codifyNativeCode)
		first := codifyNativePublication(t, f, codifyNativeCode)
		firstEdges := codifyNativeGraphProof(t, f, first)
		visible(first, true)
		result := h.toolQueryEntity(ctx, map[string]any{"name": "fixture.go", "dataset_id": first.DatasetID})
		found := false
		if !result.IsError && len(result.Content) > 0 {
			for _, id := range firstEdges {
				found = found || strings.Contains(result.Content[0].Text, id)
			}
		}
		if !found {
			t.Fatalf("native graph retrieval lacks actual current module edge: %+v", result)
		}
		publish(codifyNativeCode)
		current := codifyNativePublication(t, f, codifyNativeCode)
		if current.DocumentID != first.DocumentID || current.DatasetID != first.DatasetID || current.Generation == first.Generation {
			t.Fatalf("repeat source/generation first=%+v current=%+v", first, current)
		}
		visible(first, false)
		visible(current, true)
		codifyNativeGraphProof(t, f, current)
		result = h.toolQueryEntity(ctx, map[string]any{"name": "fixture.go", "dataset_id": current.DatasetID})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("repeat graph retrieval failed: %+v", result)
		}
		for _, id := range firstEdges {
			if strings.Contains(result.Content[0].Text, id) {
				t.Fatalf("old generation edge remained visible: %s", id)
			}
		}
		changed := strings.Replace(codifyNativeCode, "\"one\"", "\"two\"", 1)
		publish(changed)
		second := codifyNativePublication(t, f, changed)
		if second.DocumentID == current.DocumentID {
			t.Fatal("different immutable bytes overwrote first source")
		}
		secondEdges := codifyNativeGraphProof(t, f, second)
		for _, a := range secondEdges {
			for _, b := range firstEdges {
				if a == b {
					t.Fatal("independent source graph IDs collided")
				}
			}
		}
		visible(current, true)
		visible(second, true)
		// Native codify starts with legacy global inclusions. Register both
		// sources, then publish their actual registered content revisions
		// before exercising document content/retirement mutations.
		for _, source := range []searchDocumentSource{current, second} {
			if _, err := f.p.RegisterDocument(ctx, accesspkg.Actor{UserID: "root"}, accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID}, "a", accesspkg.DocumentInherit); err != nil {
				t.Fatal(err)
			}
		}
		publish(codifyNativeCode)
		current = codifyNativePublication(t, f, codifyNativeCode)
		publish(changed)
		second = codifyNativePublication(t, f, changed)
		secondEdges = codifyNativeGraphProof(t, f, second)
		visible(current, true)
		visible(second, true)
		ref := accesspkg.DocumentRef{DatasetID: second.DatasetID, DataID: second.DocumentID}
		resource, err := f.p.GetDocumentResource(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.AdvanceDocumentContent(ctx, accesspkg.Actor{UserID: "root"}, ref, resource.ACLRevision, resource.ContentRevision); err != nil {
			t.Fatal(err)
		}
		visible(second, false)
		visible(current, true)
		result = h.toolQueryEntity(ctx, map[string]any{"name": "fixture.go", "dataset_id": second.DatasetID})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("advanced graph retrieval failed: %+v", result)
		}
		for _, id := range secondEdges {
			if strings.Contains(result.Content[0].Text, id) {
				t.Fatalf("advanced source graph remained visible: %s", id)
			}
		}
		ref = accesspkg.DocumentRef{DatasetID: current.DatasetID, DataID: current.DocumentID}
		resource, err = f.p.GetDocumentResource(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.p.TombstoneDocument(ctx, accesspkg.Actor{UserID: "root"}, ref, resource.ACLRevision, resource.ContentRevision); err != nil {
			t.Fatal(err)
		}
		visible(current, false)
		result = h.toolQueryEntity(ctx, map[string]any{"name": "fixture.go", "dataset_id": current.DatasetID})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("retired graph retrieval failed: %+v", result)
		}
		for _, id := range codifyNativeGraphProof(t, f, current) {
			if len(result.Content) > 0 && strings.Contains(result.Content[0].Text, id) {
				t.Fatalf("retired source graph remained visible: %s", id)
			}
		}
		if modelCalls.Load() != 0 {
			t.Fatalf("static pipeline called model %d times", modelCalls.Load())
		}
	})
}

func TestCodifyAuthorityRetainedBody(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		app := fiber.New()
		for _, path := range []string{"/mcp", latestMCPPath} {
			t.Run(path, func(t *testing.T) {
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("POST")
				raw.Request.Header.SetContentType("application/json")
				raw.Request.SetRequestURI(path)
				for key, value := range codifyRPCHeaders(h, path, "root") {
					raw.Request.Header.Set(key, value)
				}
				raw.Request.SetBodyString(codifyRPCBody(t, path, map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}))
				c := app.AcquireCtx(raw)
				defer app.ReleaseCtx(c)
				var err error
				if path == latestMCPPath {
					err = h.handleLatestRPC(c)
				} else {
					err = h.handleRPC(c)
				}
				if err != nil {
					t.Fatal(err)
				}
				stream, ok := c.Response().BodyStream().(io.ReadCloser)
				if !ok || f.db.Stats().InUse != 1 {
					t.Fatal("successful codify lacks retained response authority")
				}
				defer stream.Close()
				prefix := make([]byte, 8)
				n, err := stream.Read(prefix)
				if n == 0 || err != nil || f.db.Stats().InUse != 1 {
					t.Fatalf("partial response n=%d err=%v", n, err)
				}
				rest, err := io.ReadAll(stream)
				if err != nil || f.db.Stats().InUse != 1 {
					t.Fatalf("completed response released authority before Close: %v", err)
				}
				var envelope struct {
					Result *mcp.ToolResult `json:"result"`
					Error  any             `json:"error"`
				}
				body := append(prefix[:n], rest...)
				if json.Unmarshal(body, &envelope) != nil || envelope.Error != nil || envelope.Result == nil || envelope.Result.IsError {
					t.Fatalf("retained success body=%s", body)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("codify response Close leaked authority transaction")
				}
			})
		}
	})
}

func codifyAssertNoEligiblePublication(t *testing.T, f *documentHTTPFixture, h *mcpHandler) {
	t.Helper()
	rows, err := f.db.Query("SELECT dataset_id,data_id,content_revision,generation,source_revision,raw_content_hash,collection_name FROM document_index_publications")
	if err != nil {
		t.Fatal(err)
	}
	var sources []searchDocumentSource
	for rows.Next() {
		var source searchDocumentSource
		if err := rows.Scan(&source.DatasetID, &source.DocumentID, &source.ContentRevision, &source.Generation, &source.SourceRevision, &source.RawContentHash, &source.Collection); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		source.Derived = true
		sources = append(sources, source)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, source := range sources {
		allowed, err := searchDocumentAllowed(ctx, h.cfg, accesspkg.Actor{UserID: "root"}, source)
		if err != nil || allowed {
			t.Fatalf("failed code publication eligible=%v err=%v source=%+v", allowed, err, source)
		}
	}
	if f.db.Stats().InUse != 0 {
		t.Fatal("failed pipeline retained SQL connection")
	}
}

func TestCodifyNativeConfiguredEmbeddingFailure(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			http.Error(w, "configured embedding failure", http.StatusBadRequest)
		}))
		defer server.Close()
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Close()
		h.cfg.Collections = cm
		h.cfg.EmbedEndpoint = server.URL + "/v1/embeddings"
		h.cfg.EmbedModel = "codify-test"
		h.cfg.EmbedClient = embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 1, 1)
		status, result := codifyNativeRPC(t, asyncAuthorityApp(h, nil), h, latestMCPPath, "root", map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}, nil)
		if status != 200 || !result.IsError || calls.Load() == 0 {
			t.Fatalf("configured embed failure status=%d calls=%d result=%+v", status, calls.Load(), result)
		}
		var rawSources int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM data WHERE owner_id='root'").Scan(&rawSources); err != nil || rawSources != 1 {
			t.Fatalf("expected allowed immutable failed-index source count=%d err=%v", rawSources, err)
		}
		codifyAssertNoEligiblePublication(t, f, h)
	})
}

func TestCodifyNativeCredentialExpiresDuringEmbedding(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		f.db.SetMaxOpenConns(1)
		h := asyncAuthorityHandler(f)
		inputs, calls, unblock := asyncAuthorityBlockedEmbed(t, h)
		defer unblock()
		expiry := time.Now().Add(3 * time.Second).Unix()
		extra := map[string]string{"Authorization": "Bearer " + signSessionPayload(jwtPayload{Sub: "root", Email: "root@test.invalid", Iat: time.Now().Unix(), Exp: expiry}, h.cfg.JWTSecret)}
		done := make(chan mcp.ToolResult, 1)
		app := asyncAuthorityApp(h, nil)
		go func() {
			_, result := codifyNativeRPC(t, app, h, latestMCPPath, "root", map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}, extra)
			done <- result
		}()
		select {
		case <-inputs:
		case <-done:
			t.Fatal("short verified credential failed before actual embedding")
		case <-time.After(5 * time.Second):
			t.Fatal("actual embedding did not start")
		}
		select {
		case result := <-done:
			if !result.IsError {
				t.Fatalf("blocked code request completed successfully: %+v", result)
			}
		case <-time.After(6 * time.Second):
			t.Fatal("expired verified credential did not cancel blocked embedding")
		}
		if time.Now().Unix() < expiry || calls.Load() != 1 {
			t.Fatalf("expiry/call barrier now=%d expiry=%d calls=%d", time.Now().Unix(), expiry, calls.Load())
		}
		unblock()
		codifyAssertNoEligiblePublication(t, f, h)
	})
}

func TestCodifyNativeLateSourceCAS(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	for _, mutation := range []string{"source", "content"} {
		t.Run(mutation, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
				t.Cleanup(func() { ingest.SetSQLiteMode(false) })
				f.db.SetMaxOpenConns(1)
				h := asyncAuthorityHandler(f)
				app := asyncAuthorityApp(h, nil)
				publish := func() mcp.ToolResult {
					_, result := codifyNativeRPC(t, app, h, latestMCPPath, "root", map[string]any{"code": codifyNativeCode, "filename": "fixture.go"}, nil)
					return result
				}
				if result := publish(); result.IsError {
					t.Fatalf("CAS successful control=%+v", result)
				}
				source := codifyNativePublication(t, f, codifyNativeCode)
				if mutation == "content" {
					if _, err := f.p.RegisterDocument(context.Background(), accesspkg.Actor{UserID: "root"}, accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID}, "a", accesspkg.DocumentInherit); err != nil {
						t.Fatal(err)
					}
					if result := publish(); result.IsError {
						t.Fatalf("registered CAS successful control=%+v", result)
					}
					source = codifyNativePublication(t, f, codifyNativeCode)
				}
				f.exec("CREATE TABLE codify_mutation_probe (n INTEGER NOT NULL)")
				f.exec("INSERT INTO codify_mutation_probe(n) VALUES(0)")
				var statement string
				if mutation == "source" {
					statement = "UPDATE data SET source_revision=source_revision+1 WHERE id='" + source.DocumentID + "';"
				} else {
					statement = "UPDATE document_resources SET content_revision=content_revision+1 WHERE dataset_id='" + source.DatasetID + "' AND data_id='" + source.DocumentID + "';"
				}
				if GetDBProvider() == DBSQLite {
					f.exec("CREATE TRIGGER codify_late_source AFTER INSERT ON graph_nodes BEGIN " + statement + " UPDATE codify_mutation_probe SET n=n+1; END")
				} else {
					f.exec("CREATE FUNCTION codify_late_source_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN " + statement + " UPDATE codify_mutation_probe SET n=n+1; RETURN NEW; END $$")
					f.exec("CREATE TRIGGER codify_late_source AFTER INSERT ON graph_nodes FOR EACH ROW EXECUTE FUNCTION codify_late_source_fn()")
				}
				if result := publish(); !result.IsError {
					t.Fatalf("late %s mutation certified success: %+v", mutation, result)
				}
				var mutations int
				if err := f.db.QueryRow("SELECT n FROM codify_mutation_probe").Scan(&mutations); err != nil || mutations == 0 {
					t.Fatalf("late SQL mutation barrier not reached n=%d err=%v", mutations, err)
				}
				codifyAssertNoEligiblePublication(t, f, h)
			})
		})
	}
}
