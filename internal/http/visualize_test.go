package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/valyala/fasthttp"
)

func TestDatasetGraph_SQLFallbackFiltersDataset(t *testing.T) {
	db := newDatasetGraphSQLiteDB(t)
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/datasets/:id/graph", DatasetGraph(GraphVisualizationConfig{DB: db, Authority: &APIConfig{DB: db, RequireAuth: false}}))

	resp, err := app.Test(httptest.NewRequest("GET", "/datasets/ds-a/graph", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var got GraphDTO
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Nodes) != 2 {
		t.Fatalf("nodes=%+v, want only ds-a nodes", got.Nodes)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("edges=%+v, want only ds-a edge whose endpoints are included", got.Edges)
	}
	if got.Edges[0].ID != "a-edge" || got.Edges[0].ValidFrom == 0 || got.Edges[0].ValidUntil == nil {
		t.Fatalf("edge metadata=%+v, want id and temporal validity", got.Edges[0])
	}
	for _, n := range got.Nodes {
		if n.Properties["dataset_id"] != "ds-a" {
			t.Fatalf("node leaked from another dataset: %+v", n)
		}
	}
}

func newDatasetGraphSQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/viz.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE graph_nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			type TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		CREATE TABLE graph_edges (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL,
			target_id TEXT NOT NULL,
			relationship_name TEXT NOT NULL DEFAULT '',
			properties TEXT NOT NULL DEFAULT '{}',
			valid_from TEXT,
			valid_until TEXT,
			dataset_id TEXT NOT NULL DEFAULT ''
		);
		INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES
			('a1','Alice','Person','ds-a'),
			('a2','Acme','Org','ds-a'),
			('b1','Bob','Person','ds-b');
		INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,valid_from,valid_until) VALUES
			('a-edge','a1','a2','works_at','ds-a','2026-01-01T00:00:00Z','2026-02-01T00:00:00Z'),
			('cross-edge','a1','b1','knows','ds-a','2026-01-01T00:00:00Z',NULL),
			('b-edge','b1','a2','mentions','ds-b','2026-01-01T00:00:00Z',NULL);
	`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}
	return db
}

func TestDatasetGraphVerifiedReadAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES('alpha-private','Private graph','Entity','alpha'),('beta-private','Other graph','Entity','beta')")
		metadata := datasetGraphDocumentAssertion(t, f, f.r)
		f.exec("UPDATE graph_nodes SET properties=$1 WHERE id='alpha-private'", metadata)
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		app := audienceApp(f, nil)
		cfg := f.cfg
		cfg.RequireAuth = true
		app.Get("/datasets/:id/graph", DatasetGraph(GraphVisualizationConfig{DB: f.db, Authority: &cfg}))
		get := func(dataset, user, tenant, key string) (int, []byte) {
			t.Helper()
			req := httptest.NewRequest("GET", "/datasets/"+dataset+"/graph", nil)
			req.Header.Set("X-Tenant-Id", tenant)
			if user != "" {
				req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", "chat-scope-secret"))
			}
			if key != "" {
				req.Header.Set("X-API-Key", key)
			}
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			return resp.StatusCode, body
		}
		assertDenied := func(dataset, user, tenant, key string) {
			t.Helper()
			code, body := get(dataset, user, tenant, key)
			var denied map[string]any
			if err := json.Unmarshal(body, &denied); err != nil {
				t.Fatal(err)
			}
			if code != 403 || denied["nodes"] != nil || denied["edges"] != nil {
				t.Fatalf("unauthorized graph exposed: %d %s", code, body)
			}
		}
		assertDenied("alpha", "viewer", "a", "")
		if code, body := get("alpha", "owner", "a", ""); code != 200 {
			t.Fatalf("owner graph: %d %s", code, body)
		}
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('graph-viewer','alpha','viewer','viewer')")
		f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES('graph-key',$1,'viewer','read')", apikeyHash("graph-read-key"))
		for _, key := range []string{"", "graph-read-key"} {
			user := "viewer"
			if key != "" {
				user = ""
			}
			code, body := get("alpha", user, "a", key)
			var graph GraphDTO
			if err := json.Unmarshal(body, &graph); err != nil {
				t.Fatal(err)
			}
			if code != 200 || len(graph.Nodes) != 1 || graph.Nodes[0].ID != "alpha-private" || graph.Nodes[0].Label != "Private graph" {
				t.Fatalf("granted graph DTO: %d %s", code, body)
			}
		}
		assertDenied("alpha", "foreign", "b", "")
		f.exec("DELETE FROM dataset_shares WHERE id='graph-viewer'")
		assertDenied("alpha", "viewer", "a", "")
		assertDenied("alpha", "", "a", "graph-read-key")
		if f.db.Stats().InUse != 0 {
			t.Fatal("graph authority response leaked SQL")
		}
	})
}

func TestDatasetGraphCredentialExpiryHoldsResponseFence(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		metadata := datasetGraphDocumentAssertion(t, f, f.r)
		f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES('held-graph','Protected graph','Entity','alpha',$1)", metadata)
		app := audienceApp(f, nil)
		cfg := f.cfg
		cfg.RequireAuth = true
		app.Get("/datasets/:id/graph", DatasetGraph(GraphVisualizationConfig{DB: f.db, Authority: &cfg}))
		expiresAt := time.Now().Unix() + 2
		token := signSessionPayload(jwtPayload{Sub: "viewer", Email: "viewer@test.invalid", Iat: time.Now().Unix(), Exp: expiresAt}, "chat-scope-secret")
		raw := &fasthttp.RequestCtx{}
		raw.Request.Header.SetMethod("GET")
		raw.Request.SetRequestURI("/datasets/alpha/graph")
		raw.Request.Header.Set("X-Tenant-Id", "a")
		raw.Request.Header.Set("Authorization", "Bearer "+token)
		app.Handler()(raw)
		stream, ok := raw.Response.BodyStream().(*fencedResponse)
		if !ok || raw.Response.StatusCode() != 200 {
			t.Fatalf("graph missing protected stream: %d", raw.Response.StatusCode())
		}
		defer stream.Close()
		if deadline, ok := stream.ctx.Deadline(); !ok || !deadline.Equal(time.Unix(expiresAt, 0)) {
			t.Fatalf("graph deadline exceeded credential: %v", deadline)
		}
		if n, err := stream.Read(make([]byte, 8)); n != 8 || err != nil {
			t.Fatalf("partial graph read: %d %v", n, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := f.db.ExecContext(ctx, "DELETE FROM dataset_shares WHERE id='viewer-share'"); err == nil {
			t.Fatal("revocation overtook held graph response")
		}
		select {
		case <-stream.ctx.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("graph response failed to expire")
		}
		if n, err := stream.Read(make([]byte, 8)); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired graph still readable: %d %v", n, err)
		}
		if f.db.Stats().InUse != 1 {
			t.Fatal("graph expiry released SQL before actual Close")
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if err := stream.Close(); err != nil {
			t.Fatal(err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("graph response close leaked SQL")
		}
	})
}

func datasetGraphDocumentAssertion(t *testing.T, f *documentHTTPFixture, resource accesspkg.DocumentResource) string {
	t.Helper()
	ctx := context.Background()
	if _, err := f.p.GrantDocument(ctx, f.owner, resource.DocumentRef, resource.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer); err != nil {
		t.Fatal(err)
	}
	version, hash, err := f.p.SourceVersion(ctx, resource.DocumentRef)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,sources_json,requires_admin,lineage_verified,source_revision,raw_content_hash)
	 VALUES('alpha',$1,'docs',$2,'active','[]',0,1,$3,$4)`, resource.DataID, resource.ContentRevision, version, hash)
	body, err := json.Marshal(searchDocumentSource{DocumentID: resource.DataID, ContentRevision: resource.ContentRevision, Collection: "docs", Generation: "active"})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestDatasetGraphRestrictedDocumentAssertions(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		allowed := datasetGraphDocumentAssertion(t, f, f.r)
		visible, err := f.p.RegisterDocument(context.Background(), f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		version, hash, err := f.p.SourceVersion(context.Background(), visible.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,sources_json,requires_admin,lineage_verified,source_revision,raw_content_hash)
		 VALUES('alpha','visible','docs',$1,'active','[]',0,1,$2,$3)`, visible.ContentRevision, version, hash)
		blocked, err := json.Marshal(searchDocumentSource{DocumentID: "visible", ContentRevision: visible.ContentRevision, Collection: "docs", Generation: "active"})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"allowed-a", "allowed-b"} {
			f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES($1,$1,'Entity','alpha',$2)", id, allowed)
		}
		f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES('secret-node','Secret property','Entity','alpha',$1),('legacy-node','Missing lineage','Entity','alpha','{}')", string(blocked))
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES('allowed-edge','allowed-a','allowed-b','VISIBLE','alpha',$1),('secret-edge','allowed-a','allowed-b','SECRET','alpha',$2),('secret-endpoint','allowed-a','secret-node','HIDDEN','alpha',$1)", allowed, string(blocked))
		app := audienceApp(f, nil)
		cfg := f.cfg
		cfg.RequireAuth = true
		app.Get("/datasets/:id/graph", DatasetGraph(GraphVisualizationConfig{DB: f.db, Authority: &cfg}))
		get := func() GraphDTO {
			t.Helper()
			req := httptest.NewRequest("GET", "/datasets/alpha/graph", nil)
			req.Header.Set("Authorization", "Bearer "+createJWT("viewer", "viewer@test.invalid", "chat-scope-secret"))
			req.Header.Set("X-Tenant-Id", "a")
			resp, err := app.Test(req, -1)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var graph GraphDTO
			if err := json.NewDecoder(resp.Body).Decode(&graph); err != nil || resp.StatusCode != 200 {
				t.Fatalf("document graph: %d %v", resp.StatusCode, err)
			}
			return graph
		}
		graph := get()
		if len(graph.Nodes) != 2 || len(graph.Edges) != 1 || graph.Edges[0].ID != "allowed-edge" {
			t.Fatalf("restricted assertion, properties or endpoint escaped: %+v", graph)
		}
		f.exec("UPDATE document_index_publications SET generation='retired' WHERE dataset_id='alpha' AND data_id='blob'")
		graph = get()
		if len(graph.Nodes) != 0 || len(graph.Edges) != 0 {
			t.Fatalf("stale publication escaped: %+v", graph)
		}
	})
}
