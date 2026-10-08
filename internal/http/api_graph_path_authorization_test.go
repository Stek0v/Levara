package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/valyala/fasthttp"
)

func TestGraphPathDocumentAuthorization(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		ctx := context.Background()
		if _, err := f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer); err != nil {
			t.Fatal(err)
		}
		visible, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		metadata := map[string]string{}
		for _, resource := range []accesspkg.DocumentResource{f.r, visible} {
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
			metadata[resource.DataID] = string(body)
		}
		for _, id := range []string{"a", "b", "c", "x", "y"} {
			f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES($1,$1,'Entity','alpha',$2)", id, metadata["blob"])
		}
		for _, edge := range [][3]string{{"ab", "a", "b"}, {"bc", "b", "c"}, {"xy", "x", "y"}} {
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties,valid_from,valid_until) VALUES($1,$2,$3,'LINK','alpha',$4,$5,$6)", edge[0], edge[1], edge[2], metadata["blob"], time.Unix(100, 0).UTC().Format(time.RFC3339), time.Unix(200, 0).UTC().Format(time.RFC3339))
		}
		// The shortest raw route is forbidden; the permitted two-hop route must survive.
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES('shortcut','a','c','SECRET','alpha',$1)", metadata["visible"])
		f.app.Get("/api/v1/graph/path", graphPathHandler(f.cfg))
		path := func(t *testing.T, from, to, extra string, want int) graphdb.PathResult {
			t.Helper()
			status, body, _ := f.request("viewer", "GET", "/graph/path?from="+from+"&to="+to+extra, "", "X-Tenant-ID", "a")
			var result graphdb.PathResult
			if status != 200 || json.Unmarshal(body, &result) != nil || len(result.Edges) != want {
				t.Fatalf("path %s->%s%s: status=%d body=%s want_edges=%d", from, to, extra, status, body, want)
			}
			return result
		}
		t.Run("allowed_long_route", func(t *testing.T) {
			result := path(t, "a", "c", "", 2)
			for _, edge := range result.Edges {
				if edge.Type != "LINK" {
					t.Fatalf("denied shortcut leaked: %+v", edge)
				}
			}
		})
		t.Run("temporal_and_pagination", func(t *testing.T) {
			path(t, "a", "c", "&as_of=200", 2)
			path(t, "a", "c", "&as_of=201", 0)
			first := path(t, "a", "c", "&limit=1", 1)
			if first.NextCursor == "" {
				t.Fatal("missing continuation")
			}
			second := path(t, "a", "c", "&limit=1&cursor="+first.NextCursor, 1)
			if second.NextCursor != "" || first.Edges[0].SourceID == second.Edges[0].SourceID && first.Edges[0].TargetID == second.Edges[0].TargetID {
				t.Fatalf("pagination changed: first=%+v second=%+v", first, second)
			}
		})
		for _, tc := range []struct{ name, properties string }{
			{"denied_edge", metadata["visible"]},
			{"missing_assertion", `{}`},
			{"stale_publication", strings.Replace(metadata["blob"], `"active"`, `"retired"`, 1)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f.exec("UPDATE graph_edges SET properties=$1 WHERE id='xy'", tc.properties)
				path(t, "x", "y", "", 0)
			})
		}
		f.exec("UPDATE graph_edges SET properties=$1 WHERE id='xy'", metadata["blob"])
		t.Run("positive_edge", func(t *testing.T) { path(t, "x", "y", "", 1) })
		t.Run("denied_endpoint", func(t *testing.T) {
			f.exec("UPDATE graph_nodes SET properties=$1 WHERE id='y'", metadata["visible"])
			path(t, "x", "y", "", 0)
			f.exec("UPDATE graph_nodes SET properties=$1 WHERE id='y'", metadata["blob"])
		})
		t.Run("selected_tenant", func(t *testing.T) {
			f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('foreign-graph','Foreign Graph','foreign')")
			f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('foreign-viewer','foreign-graph','viewer','viewer')")
			f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('viewer','b')")
			for _, id := range []string{"foreign-a", "foreign-b"} {
				f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES($1,$1,'Entity','foreign-graph')", id)
			}
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id) VALUES('foreign-edge','foreign-a','foreign-b','LINK','foreign-graph')")
			path(t, "foreign-a", "foreign-b", "", 0)
			status, body, _ := f.request("viewer", "GET", "/graph/path?from=foreign-a&to=foreign-b", "", "X-Tenant-ID", "b")
			var result graphdb.PathResult
			if status != 200 || json.Unmarshal(body, &result) != nil || len(result.Edges) != 1 {
				t.Fatalf("selected tenant positive: status=%d body=%s", status, body)
			}
		})
		t.Run("retained_body", func(t *testing.T) {
			raw := &fasthttp.RequestCtx{}
			raw.Request.SetRequestURI("/graph/path?from=a&to=c")
			c := f.app.AcquireCtx(raw)
			defer f.app.ReleaseCtx(c)
			c.Locals("user_id", "viewer")
			c.Locals("tenant_id", "a")
			c.Locals("verified_jwt", jwtPayload{Sub: "viewer", Exp: time.Now().Add(time.Hour).Unix()})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			c.SetUserContext(ctx)
			if err := graphPathHandler(f.cfg)(c); err != nil {
				t.Fatal(err)
			}
			stream, ok := c.Response().BodyStream().(io.ReadCloser)
			if !ok {
				t.Fatal("graph path response lacks authority stream")
			}
			defer stream.Close()
			<-ctx.Done()
			if f.db.Stats().InUse != 1 {
				t.Fatal("graph authority released before response Close")
			}
			if n, err := stream.Read(make([]byte, 1)); n != 0 || err == nil {
				t.Fatal("expired graph path still readable")
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			if f.db.Stats().InUse != 0 {
				t.Fatal("closed graph response retained SQL")
			}
		})
		t.Run("credential_boundary", func(t *testing.T) {
			f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions,revoked) VALUES('revoked-graph-key','fixture-hash','viewer','read-write',true)")
			for _, name := range []string{"unverified", "expired", "revoked_key"} {
				t.Run(name, func(t *testing.T) {
					raw := &fasthttp.RequestCtx{}
					raw.Request.SetRequestURI("/graph/path?from=a&to=c")
					c := f.app.AcquireCtx(raw)
					defer f.app.ReleaseCtx(c)
					c.Locals("user_id", "viewer")
					c.Locals("tenant_id", "a")
					if name == "expired" {
						c.Locals("verified_jwt", jwtPayload{Sub: "viewer", Exp: time.Now().Add(-time.Hour).Unix()})
					}
					if name == "revoked_key" {
						c.Locals("verified_api_key", accesspkg.APIKeyIdentity{KeyID: "revoked-graph-key", UserID: "viewer", Permissions: "read-write"})
						c.Locals("api_key_permissions", "read-write")
					}
					err := graphPathHandler(f.cfg)(c)
					var authority *fiber.Error
					if (!errors.As(err, &authority) || authority.Code != 403) && (err != nil || c.Response().StatusCode() != 403) {
						t.Fatalf("credential %s accepted: status=%d err=%v", name, c.Response().StatusCode(), err)
					}
					if f.db.Stats().InUse != 0 {
						t.Fatal("denied credential retained SQL")
					}
				})
			}
		})
		t.Run("invalid_cursor", func(t *testing.T) {
			status, _, _ := f.request("viewer", "GET", "/graph/path?from=a&to=c&cursor=invalid", "", "X-Tenant-ID", "a")
			if status != 400 {
				t.Fatalf("invalid cursor status=%d", status)
			}
		})
		t.Run("sql_failure", func(t *testing.T) {
			f.exec("DROP TABLE graph_edges")
			status, body, _ := f.request("viewer", "GET", "/graph/path?from=a&to=c", "", "X-Tenant-ID", "a")
			if status != 503 || strings.Contains(string(body), "graph_edges") {
				t.Fatalf("unsafe SQL failure: status=%d body=%s", status, body)
			}
		})
	})
}

func TestGraphPathAuthenticatedNeo4jWithoutSQLFailsClosed(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Get("/graph/path", graphPathHandler(APIConfig{RequireAuth: true, Neo4jCfg: GraphVisualizationConfig{Neo4jURL: "bolt://127.0.0.1:1"}}))
	raw := &fasthttp.RequestCtx{}
	raw.Request.SetRequestURI("/graph/path?from=a&to=b")
	c := app.AcquireCtx(raw)
	defer app.ReleaseCtx(c)
	c.Locals("user_id", "viewer")
	c.Locals("verified_jwt", jwtPayload{Sub: "viewer", Exp: time.Now().Add(time.Hour).Unix()})
	if err := graphPathHandler(APIConfig{RequireAuth: true, Neo4jCfg: GraphVisualizationConfig{Neo4jURL: "bolt://127.0.0.1:1"}})(c); err != nil || c.Response().StatusCode() != 503 {
		t.Fatalf("authenticated Neo4j without SQL: status=%d err=%v", c.Response().StatusCode(), err)
	}
}
