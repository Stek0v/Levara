package http

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestDocumentPublicationRechecksInheritedSources(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		principal := accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "peer"}
		var err error
		f.r, err = f.p.GrantDocument(context.Background(), f.owner, f.r.DocumentRef, f.r.ACLRevision, principal, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.p.RegisterDocument(context.Background(), f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		b, err = f.p.GrantDocument(context.Background(), f.owner, b.DocumentRef, b.ACLRevision, principal, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		dependency := searchDocumentSource{DatasetID: b.DatasetID, DocumentID: b.DataID, ContentRevision: b.ContentRevision}
		raw, _ := json.Marshal([]searchDocumentSource{dependency})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		p, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			t.Fatal(err)
		}
		err = p.CommitDocumentIndexWithLineage(ctx, f.owner, f.r.DocumentRef, f.r.ContentRevision, "docs", "gen", accesspkg.DocumentPublicationLineage{SourcesJSON: string(raw)})
		release()
		if err != nil {
			t.Fatal(err)
		}
		a := searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision, Collection: "docs", Generation: "gen", Derived: true}
		actor := accesspkg.Actor{UserID: "peer", TenantID: "a"}
		if allowed, err := searchDocumentAllowed(sessionHistoryContext(actor), f.cfg, actor, a); err != nil || !allowed {
			t.Fatalf("initial generation hidden %v %v", allowed, err)
		}
		if _, err := f.p.RevokeDocument(context.Background(), f.owner, b.DocumentRef, b.ACLRevision, principal); err != nil {
			t.Fatal(err)
		}
		// A fresh API config/context has no request or run-registry memory.
		cfg := APIConfig{DB: f.db, RequireAuth: true}
		if allowed, err := searchDocumentAllowed(sessionHistoryContext(actor), cfg, actor, a); err != nil || allowed {
			t.Fatalf("published A leaked revoked B: allowed=%v err=%v", allowed, err)
		}
	})
}

func TestDocumentPublicationLineageFailsClosed(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		ctx := context.Background()
		b, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentInherit)
		if err != nil {
			t.Fatal(err)
		}
		aSource := searchDocumentSource{DatasetID: "alpha", DocumentID: "blob", ContentRevision: f.r.ContentRevision, Collection: "docs", Generation: "a", Derived: true}
		bSource := searchDocumentSource{DatasetID: "alpha", DocumentID: "visible", ContentRevision: b.ContentRevision, Collection: "docs", Generation: "b", Derived: true}
		put := func(source searchDocumentSource, dependencies string, admin, verified int) {
			t.Helper()
			version, hash, err := f.p.SourceVersion(ctx, accesspkg.DocumentRef{DatasetID: source.DatasetID, DataID: source.DocumentID})
			if err != nil {
				t.Fatal(err)
			}
			f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,sources_json,requires_admin,lineage_verified,source_revision,raw_content_hash)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(dataset_id,data_id,collection_name)
			DO UPDATE SET sources_json=EXCLUDED.sources_json,requires_admin=EXCLUDED.requires_admin,lineage_verified=EXCLUDED.lineage_verified`, source.DatasetID, source.DocumentID, source.Collection, source.ContentRevision, source.Generation, dependencies, admin, verified, version, hash)
		}
		check := func(actor accesspkg.Actor, want bool) {
			t.Helper()
			got, err := searchDocumentAllowed(sessionHistoryContext(actor), f.cfg, actor, aSource)
			if got != want || (want && err != nil) {
				t.Fatalf("allowed=%v want=%v err=%v", got, want, err)
			}
		}
		for _, raw := range []string{"null", "{}", "[", `[null]`, `[{}]`} {
			put(aSource, raw, 0, 1)
			check(f.owner, false)
		}
		put(aSource, "[]", 0, 0)
		check(f.owner, false) // Migrated generations without verified lineage stay hidden.
		put(aSource, "[]", 0, 1)
		check(f.owner, true)
		rawB, _ := json.Marshal([]searchDocumentSource{bSource})
		put(aSource, string(rawB), 0, 1)
		put(bSource, "[]", 0, 1)
		check(f.owner, true)
		rawA, _ := json.Marshal([]searchDocumentSource{aSource})
		put(bSource, string(rawA), 0, 1)
		check(f.owner, false) // A -> B -> A cannot recurse forever.
		c, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "beta", DataID: "blob"}, "a", accesspkg.DocumentInherit)
		if err != nil {
			t.Fatal(err)
		}
		cSource := searchDocumentSource{DatasetID: c.DatasetID, DocumentID: c.DataID, ContentRevision: c.ContentRevision, Collection: "docs", Generation: "c", Derived: true}
		rawC, _ := json.Marshal([]searchDocumentSource{cSource})
		put(bSource, string(rawC), 0, 1)
		put(cSource, "[]", 0, 1)
		check(f.owner, true)
		if _, err := f.p.AdvanceDocumentContent(ctx, f.owner, c.DocumentRef, c.ACLRevision, c.ContentRevision); err != nil {
			t.Fatal(err)
		}
		check(f.owner, false) // A -> B -> C must retain C's revision.
		put(bSource, "[]", 0, 1)
		if _, err := f.p.AdvanceDocumentContent(ctx, f.owner, b.DocumentRef, b.ACLRevision, b.ContentRevision); err != nil {
			t.Fatal(err)
		}
		check(f.owner, false) // Transitive revision changes invalidate A.
		oversized := make([]searchDocumentSource, 257)
		for i := range oversized {
			oversized[i] = aSource
			oversized[i].Derived = false
		}
		tooMany, _ := json.Marshal(oversized)
		put(aSource, string(tooMany), 0, 1)
		check(f.owner, false)
		put(aSource, "[]", 1, 1)
		f.exec("UPDATE users SET is_superuser=true WHERE id='owner'")
		check(accesspkg.Actor{UserID: "owner"}, true)
		f.exec("UPDATE users SET is_superuser=false WHERE id='owner'")
		check(f.owner, false)
		raw := aSource
		raw.Derived = false
		if got, err := searchDocumentAllowed(sessionHistoryContext(f.owner), f.cfg, f.owner, raw); err != nil || !got {
			t.Fatalf("demotion must preserve ordinary raw access: %v %v", got, err)
		}
	})
}

func TestDatasetMetadataProofCannotBeForgedByIndexedContent(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		actor := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
		ctx := sessionHistoryContext(actor)
		proof := searchDocumentSource{DatasetID: "alpha", DatasetMetadataOnly: true}
		if allowed, err := searchDocumentAllowed(ctx, f.cfg, actor, proof); err != nil || !allowed {
			t.Fatalf("dataset name denied: %v %v", allowed, err)
		}
		raw, _ := json.Marshal(proof)
		forged, err := decodeSearchDocumentSource(raw)
		if err != nil {
			t.Fatal(err)
		}
		forged.Derived = true
		if allowed, err := searchDocumentAllowed(ctx, f.cfg, actor, forged); err != nil || allowed {
			t.Fatalf("indexed content forged name-only proof: %v %v", allowed, err)
		}
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		if allowed, err := searchDocumentAllowed(ctx, f.cfg, actor, proof); err != nil || allowed {
			t.Fatalf("metadata proof ignored revoke: %v %v", allowed, err)
		}
	})
}
