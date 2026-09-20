package http

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bytes"
	"github.com/stek0v/levara/internal/store"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/orchestrator"
)

func TestDocumentPublicationAttemptCASIsAtomic(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		revision, hash, err := f.p.SourceVersion(ctx, f.r.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}
		claim := []pipelineAttemptSource{{datasetID: f.r.DatasetID, dataID: f.r.DataID, sourceRevision: revision, rawContentHash: hash}}
		for _, attemptID := range []string{"attempt-a", "attempt-b"} {
			if err := claimPipelineAttempts(ctx, f.db, claim, "docs", attemptID); err != nil {
				t.Fatal(err)
			}
		}
		publish := func(attemptID, generation string, chunks int) error {
			locked, release, err := f.p.BeginReadFence(ctx, GetDBProvider() == DBSQLite)
			if err != nil {
				return err
			}
			defer release()
			return locked.CommitDocumentIndexVersioned(ctx, f.owner, f.r.DocumentRef, f.r.ContentRevision, "docs", generation, accesspkg.DocumentPublicationLineage{
				SourcesJSON: "[]", SourceRevision: revision, RawContentHash: hash, AttemptID: attemptID,
				PipelineStatusJSON: pipelineStatusJSON("COMPLETED", chunks, 0, 0, 1),
			})
		}
		if err := publish("attempt-b", "generation-b", 2); err != nil {
			t.Fatal(err)
		}
		if err := publish("attempt-a", "generation-a", 1); !errors.Is(err, accesspkg.ErrDocumentVersionConflict) {
			t.Fatalf("late attempt error=%v", err)
		}
		var generation, attemptID, state, raw string
		if err := f.db.QueryRow(Q(`SELECT p.generation,s.attempt_id,s.pipeline_state,s.status_json
			FROM document_index_publications p JOIN document_pipeline_statuses s
			ON s.dataset_id=p.dataset_id AND s.data_id=p.data_id AND s.collection_name=p.collection_name
			WHERE p.dataset_id=$1 AND p.data_id=$2 AND p.collection_name=$3`), f.r.DatasetID, f.r.DataID, "docs").Scan(&generation, &attemptID, &state, &raw); err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		if json.Unmarshal([]byte(raw), &status) != nil || generation != "generation-b" || attemptID != "attempt-b" || state != "COMPLETED" || status["chunks"] != float64(2) {
			t.Fatalf("publication/status diverged: generation=%s attempt=%s state=%s status=%s", generation, attemptID, state, raw)
		}
	})
}

// The personal preset runs a metadata DB with -require-auth=false, so search
// fences stay no-ops for the anonymous actor. Dataset cognify must still
// publish: extraction is authorized through dataset dev-mode grants and the
// publication opens its own snapshot fence for the versioned commit.
func TestDocumentPublicationNoAuthPersonalMode(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = false
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Close()
		index := bm25.NewIndexRegistry()
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				http.Error(w, "invalid", 400)
				return
			}
			rows := []any{}
			for i := range req.Input {
				rows = append(rows, map[string]any{"index": i, "embedding": []float32{1, 0}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		}))
		defer endpoint.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		anonymous := accesspkg.Actor{}
		ctx = context.WithValue(ctx, searchActorKey{}, anonymous)
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: f.cfg, actor: anonymous})
		// 'visible' keeps its unregistered dataset association: no document_resources row.
		body := "Anonymous personal mode must still publish dataset documents through a dedicated snapshot fence."
		var location string
		if err := f.db.QueryRow("SELECT raw_data_location FROM data WHERE id='visible'").Scan(&location); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(strings.TrimPrefix(location, "file://"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='visible'", fmt.Sprintf("%x", sha256.Sum256([]byte(body))))
		ref := accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}
		revision, hash, err := f.p.SourceVersion(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if revision <= 0 || !strings.EqualFold(hash, fmt.Sprintf("%x", sha256.Sum256([]byte(body)))) {
			t.Fatalf("unexpected source version: revision=%d hash=%s", revision, hash)
		}
		source := cognifySource{datasetID: "alpha", documentID: "visible", sourceRevision: revision, rawContentHash: hash, texts: []string{body}}
		if err := claimPipelineAttempts(ctx, f.db, []pipelineAttemptSource{{datasetID: "alpha", dataID: "visible", sourceRevision: revision, rawContentHash: hash}}, "docs", "run-noauth"); err != nil {
			t.Fatal(err)
		}
		cfg := orchestrator.Config{Collection: "docs", Collections: cm, BM25Indexes: index, EmbedEndpoint: endpoint.URL, DB: f.db, SkipGraph: true, MinChunkChars: 1, AttemptID: "run-noauth"}
		if err := runCognifySources(ctx, []cognifySource{source}, f.cfg, cfg, make(chan orchestrator.Progress, 100)); err != nil {
			t.Fatalf("no-auth publication failed: %v", err)
		}
		var generation string
		var verified int
		var contentRevision int64
		if err := f.db.QueryRow(Q(`SELECT generation,lineage_verified,content_revision FROM document_index_publications
			WHERE dataset_id='alpha' AND data_id='visible' AND collection_name='docs'`)).Scan(&generation, &verified, &contentRevision); err != nil {
			t.Fatalf("publication row missing: %v", err)
		}
		if generation == "" || verified != 1 || contentRevision != 0 {
			t.Fatalf("publication row: generation=%q verified=%d revision=%d", generation, verified, contentRevision)
		}
		var state string
		if err := f.db.QueryRow(Q(`SELECT pipeline_state FROM document_pipeline_statuses
			WHERE dataset_id='alpha' AND data_id='visible' AND collection_name='docs' AND attempt_id='run-noauth'`)).Scan(&state); err != nil || state != "COMPLETED" {
			t.Fatalf("pipeline state=%q err=%v", state, err)
		}
		if hits := index.Get("docs").Search("snapshot", 10); len(hits) != 1 {
			t.Fatalf("hits=%+v", hits)
		}
	})
}

func TestDocumentPublicationRequiresCompleteLiveSource(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.cfg.RequireAuth = true
		cm, err := store.NewCollectionManager(2, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer cm.Close()
		index := bm25.NewIndexRegistry()
		var failed atomic.Bool
		var calls atomic.Int32
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if failed.Load() {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			var req struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				http.Error(w, "invalid", 400)
				return
			}
			rows := []any{}
			for i := range req.Input {
				rows = append(rows, map[string]any{"index": i, "embedding": []float32{1, 0}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		}))
		defer endpoint.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, searchActorKey{}, f.owner)
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: f.cfg, actor: f.owner, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
		source := cognifySource{datasetID: "alpha", documentID: "blob", contentRevision: f.r.ContentRevision, texts: []string{"Publication must require a complete document generation before its confidential content can appear in search results."}}
		var location string
		if err := f.db.QueryRow("SELECT raw_data_location FROM data WHERE id='blob'").Scan(&location); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(strings.TrimPrefix(location, "file://"), []byte(source.texts[0]), 0600); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", fmt.Sprintf("%x", sha256.Sum256([]byte(source.texts[0]))))
		source.sourceRevision, source.rawContentHash, err = f.p.SourceVersion(ctx, f.r.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}

		cfg := orchestrator.Config{Collection: "docs", Collections: cm, BM25Indexes: index, EmbedEndpoint: endpoint.URL, DB: f.db, SkipGraph: true, MinChunkChars: 1}
		run := func() error {
			return runCognifySources(ctx, []cognifySource{source}, f.cfg, cfg, make(chan orchestrator.Progress, 100))
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		hits := index.Get("docs").Search("confidential", 10)
		if len(hits) != 1 {
			t.Fatalf("hits=%+v", hits)
		}
		proof, err := decodeSearchDocumentSource(hits[0].Metadata)
		if err != nil {
			t.Fatal(err)
		}
		proof.Derived = true
		if proof.Generation == "" || proof.Collection != "docs" {
			t.Fatalf("missing generation: %+v", proof)
		}
		check := func(want bool) {
			t.Helper()
			allowed, err := searchDocumentAllowed(ctx, f.cfg, f.owner, proof)
			if err != nil || allowed != want {
				t.Fatalf("allowed=%v want=%v err=%v", allowed, want, err)
			}
		}
		check(true)
		// MCP lexical search must use document grants even without dataset read.
		grant, err := f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		f.r = grant
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		mcpCfg := f.cfg
		mcpCfg.BM25Indexes = index
		h := &mcpHandler{cfg: mcpCfg}
		viewerCtx := context.WithValue(ctx, mcpUserIDKey, "viewer")
		viewerCtx = context.WithValue(viewerCtx, mcp.TenantIDKey, "a")
		args := map[string]any{"search_query": "confidential", "search_type": "CHUNKS_LEXICAL", "collection": "docs"}
		visible := h.toolSearch(viewerCtx, args)
		if visible.IsError || len(visible.Content) == 0 || !bytes.Contains([]byte(visible.Content[0].Text), []byte("confidential")) {
			t.Fatalf("MCP exact grant unavailable: %+v", visible)
		}
		revoked, err := f.p.RevokeDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"})
		if err != nil {
			t.Fatal(err)
		}
		f.r = revoked
		hidden := h.toolSearch(viewerCtx, args)
		if len(hidden.Content) > 0 && bytes.Contains([]byte(hidden.Content[0].Text), []byte("confidential")) {
			t.Fatalf("MCP exposed revoked source: %+v", hidden)
		}
		// Materialize A while its context includes B, then discard all request
		// evidence. SQL lineage must still gate lexical, vector and graph reads.
		f.r, err = f.p.GrantDocument(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		b, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: "visible"}, "a", accesspkg.DocumentRestricted)
		if err != nil {
			t.Fatal(err)
		}
		b, err = f.p.GrantDocument(ctx, f.owner, b.DocumentRef, b.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		trackSearchSource(ctx, searchDocumentSource{DatasetID: b.DatasetID, DocumentID: b.DataID, ContentRevision: b.ContentRevision})
		if err := run(); err != nil {
			t.Fatal(err)
		}
		if err := f.db.QueryRow(Q("SELECT generation FROM document_index_publications WHERE dataset_id='alpha' AND data_id='blob' AND collection_name='docs'")).Scan(&proof.Generation); err != nil {
			t.Fatal(err)
		}
		check(true)
		viewerCtx = context.WithValue(context.Background(), mcpUserIDKey, "viewer")
		viewerCtx = context.WithValue(viewerCtx, mcp.TenantIDKey, "a")
		visible = h.toolSearch(viewerCtx, args)
		if visible.IsError || len(visible.Content) == 0 || !bytes.Contains([]byte(visible.Content[0].Text), []byte("confidential")) {
			t.Fatalf("published lineage unavailable: %+v", visible)
		}
		vectorHits, err := cm.Search("docs", []float32{1, 0}, 10)
		if err != nil || len(vectorHits) == 0 {
			t.Fatalf("vector generation missing: %v %v", vectorHits, err)
		}
		if _, err := f.p.RevokeDocument(ctx, f.owner, b.DocumentRef, b.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}); err != nil {
			t.Fatal(err)
		}
		hidden = h.toolSearch(viewerCtx, args)
		if len(hidden.Content) > 0 && bytes.Contains([]byte(hidden.Content[0].Text), []byte("confidential")) {
			t.Fatalf("MCP lineage exposed revoked B: %+v", hidden)
		}
		freshCfg := APIConfig{DB: f.db, RequireAuth: true}
		viewer := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
		freshCtx := context.WithValue(context.Background(), searchActorKey{}, viewer)
		for _, hit := range vectorHits {
			vectorSource, err := decodeSearchDocumentSource(hit.Data)
			if err != nil {
				t.Fatal(err)
			}
			vectorSource.Derived = true
			if allowed, err := searchDocumentAllowed(freshCtx, freshCfg, viewer, vectorSource); err != nil || allowed {
				t.Fatalf("vector lineage exposed revoked B: %v %v", allowed, err)
			}
		}
		if graphSourcesAllowed(freshCtx, freshCfg, []searchDocumentSource{proof}, nil) {
			t.Fatal("graph source accepted revoked inherited document")
		}
		failed.Store(true)
		if err := run(); err == nil {
			t.Fatal("failed embedding published success")
		}
		check(true) // A failed replacement leaves the prior complete generation readable.
		unpublished := proof
		unpublished.Generation = "partial"
		if allowed, err := searchDocumentAllowed(ctx, f.cfg, f.owner, unpublished); err != nil || allowed {
			t.Fatalf("partial source allowed=%v err=%v", allowed, err)
		}
		if _, err := f.p.AdvanceDocumentContent(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, f.r.ContentRevision); err != nil {
			t.Fatal(err)
		}
		check(false)
		before := calls.Load()
		if err := run(); err == nil {
			t.Fatal("old source revision processed after content change")
		}
		if calls.Load() != before {
			t.Fatal("stale source reached embed service")
		}
		f.exec("UPDATE users SET is_active=false WHERE id='owner'")
		if err := run(); err == nil {
			t.Fatal("deactivated owner continued background processing")
		}
		if calls.Load() != before {
			t.Fatal("revoked owner reached embed service")
		}
	})
}
