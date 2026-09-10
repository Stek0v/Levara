package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestDocumentGraphRequiresEveryAssertionSource(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		reader := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), searchActorKey{}, reader), 30*time.Second)
		defer cancel()
		f.exec("UPDATE data SET source_revision=7 WHERE id='blob'")
		meta, err := json.Marshal(searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision, Generation: "published", Collection: "docs"})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"source", "target"} {
			f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES($1,$2,'Entity','alpha',$3)", id, id, string(meta))
		}
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES('edge','source','target','KNOWS','alpha',$1)", string(meta))
		check := func(want int) {
			t.Helper()
			items := graphContextItemsFromPostgres(ctx, f.cfg, []string{"source"}, []string{})
			if len(items) != want {
				t.Fatalf("graph assertions=%+v want count %d", items, want)
			}
		}
		checkMCP := func(want bool) {
			t.Helper()
			queryCtx := context.WithValue(context.WithValue(ctx, mcp.UserIDKey, reader.UserID), mcp.TenantIDKey, reader.TenantID)
			result := mcp.ToolQueryEntity(queryCtx, NewMCPDeps(f.cfg), map[string]any{"name": "source", "dataset_id": "alpha"})
			got := !result.IsError && len(result.Content) > 0 && strings.Contains(result.Content[0].Text, `"id": "edge"`)
			if got != want {
				t.Fatalf("query_entity published edge=%v want=%v result=%+v", got, want, result)
			}
		}
		check(0) // Dataset viewer cannot read a restricted document.
		checkMCP(false)
		r, err := f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		// Two pages of unproven assertions must not starve the later valid hit.
		for i := 0; i < 260; i++ {
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES($1,'source','target','KNOWS','alpha','{}')", fmt.Sprintf("a-%03d", i))
		}
		check(0) // Complete source proof still requires successful publication.
		checkMCP(false)
		fenced, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		if err := fenced.CommitDocumentIndexWithLineage(ctx, f.owner, f.r.DocumentRef, f.r.ContentRevision, "docs", "published", accesspkg.DocumentPublicationLineage{SourcesJSON: "[]"}); err != nil {
			release()
			t.Fatal(err)
		}
		release()
		check(1) // Exact document grant works with no whole-dataset grant.
		checkMCP(true)
		for _, table := range []string{"graph_nodes", "graph_edges"} {
			suffix := ""
			if table == "graph_edges" {
				suffix = " WHERE id='edge'"
			}
			f.exec("UPDATE " + table + " SET properties='{}'" + suffix)
			check(0) // Every endpoint AND relationship must name its generation.
			f.exec("UPDATE "+table+" SET properties=$1"+suffix, string(meta))
			check(1)
		}
		if _, err := f.p.AdvanceDocumentContent(ctx, f.owner, r.DocumentRef, r.ACLRevision, r.ContentRevision); err != nil {
			t.Fatal(err)
		}
		check(0)
		checkMCP(false)
	})
}

func TestSearchDocumentRequiresLiveGrantAndGeneration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			for _, q := range []string{`INSERT INTO tenants(id,name,owner_id) VALUES ('t','Team','alice')`, `INSERT INTO user_tenant(user_id,tenant_id) VALUES ('alice','t'),('bob','t')`, `INSERT INTO data(id,name) VALUES ('d','document')`, `INSERT INTO dataset_data(dataset_id,data_id) VALUES ('a','d')`, `INSERT INTO dataset_shares(id,dataset_id,user_id,role,granted_by) VALUES ('b-read','a','bob','viewer','alice')`} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			p := accesspkg.SQLPolicy{DB: db, Q: Q, QA: QArgs}
			owner := accesspkg.Actor{UserID: "alice", TenantID: "t"}
			reader := accesspkg.Actor{UserID: "bob", TenantID: "t"}
			r, err := p.RegisterDocument(ctx, owner, accesspkg.DocumentRef{DatasetID: "a", DataID: "d"}, "t", accesspkg.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			cfg := APIConfig{DB: db, RequireAuth: true}
			source := searchDocumentSource{DatasetID: "a", DocumentID: "d", ContentRevision: r.ContentRevision}
			check := func(source searchDocumentSource, want bool) {
				t.Helper()
				allowed, err := searchDocumentAllowed(ctx, cfg, reader, source)
				if err != nil || allowed != want {
					t.Fatalf("source=%+v allowed=%v want=%v err=%v", source, allowed, want, err)
				}
			}
			check(source, false)
			r, err = p.GrantDocument(ctx, owner, r.DocumentRef, r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "bob"}, accesspkg.RoleViewer)
			if err != nil {
				t.Fatal(err)
			}
			check(source, true)
			if _, err := db.Exec(`DELETE FROM dataset_shares WHERE id='b-read'`); err != nil {
				t.Fatal(err)
			}
			check(source, true) // exact grant does not require a whole-dataset grant
			check(searchDocumentSource{DatasetID: "a"}, false)
			check(searchDocumentSource{}, false)
			stale := source
			stale.ContentRevision = 0
			check(stale, false)
			r, err = p.AdvanceDocumentContent(ctx, owner, r.DocumentRef, r.ACLRevision, r.ContentRevision)
			if err != nil {
				t.Fatal(err)
			}
			check(source, false)
			source.ContentRevision = r.ContentRevision
			check(source, true)
			if _, err := db.Exec(`DELETE FROM user_tenant WHERE user_id='bob'`); err != nil {
				t.Fatal(err)
			}
			check(source, false)
			if _, err := db.Exec(`ALTER TABLE document_resources RENAME TO unavailable_document_resources`); err != nil {
				t.Fatal(err)
			}
			if allowed, err := searchDocumentAllowed(ctx, cfg, reader, source); err == nil || allowed {
				t.Fatalf("SQL failure allowed fallback: %v %v", allowed, err)
			}
		})
	}
}
