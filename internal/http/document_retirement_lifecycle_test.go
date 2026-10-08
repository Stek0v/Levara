package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/orchestrator"
)

// HTTP raw/artifact/RAG requests use the real handlers and verified fixture
// middleware. query_entity uses its public tool adapter with verified context;
// graph assertions are fixtures of actual publication metadata, not LLM output.
func TestDocumentRetirementFourChannelLifecycle(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		native, closeNative := newWorkspaceTestConfig(t)
		defer closeNative()
		cfg := f.cfg
		cfg.RequireAuth = true
		cfg.Collections, cfg.BM25Indexes = native.Collections, native.BM25Indexes
		cfg.EmbedEndpoint, cfg.EmbedModel = native.EmbedEndpoint, native.EmbedModel
		cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 1, 1)
		model := &recordingLLM{responses: []string{
			"Lifecycle evidence [1]", "Lifecycle evidence [1]",
			"Lifecycle evidence [1]", "Lifecycle evidence [1]",
		}}
		cfg.LLMProvider = model
		t.Setenv("LLM_ENDPOINT", cfg.EmbedEndpoint)
		t.Setenv("LLM_MODEL", "fixture")
		t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0")
		f.app.Get("/api/v1/datasets/:id/data/:dataId/structured-artifacts/:artifactId", structuredArtifactHandler(cfg))
		f.app.Post("/api/v1/search/text", searchHandler(cfg))
		f.app.Get("/api/v1/graph/path", graphPathHandler(cfg))
		h := &mcpHandler{cfg: cfg}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		expiry := time.Now().Add(time.Hour).Unix()
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: expiry}}
		ownerCtx := context.WithValue(ctx, searchActorKey{}, f.owner)
		ownerCtx = context.WithValue(ownerCtx, searchEgressKey{}, searchEgress{cfg: cfg, actor: f.owner, kind: "jwt", expiresAt: expiry})
		viewer := accesspkg.Actor{UserID: "viewer", TenantID: "a"}
		viewerCtx := context.WithValue(ctx, searchActorKey{}, viewer)
		viewerCtx = context.WithValue(viewerCtx, searchEgressKey{}, searchEgress{cfg: cfg, actor: viewer, kind: "jwt", expiresAt: expiry})
		viewerCtx = context.WithValue(viewerCtx, mcp.UserIDKey, viewer.UserID)
		viewerCtx = context.WithValue(viewerCtx, mcp.TenantIDKey, viewer.TenantID)
		writer, err := ingest.NewMetadataWriterForStorage(f.db, nil)
		if err != nil {
			t.Fatal(err)
		}
		const dataID = "retirement-source"
		const textA = "LifecycleAmber is the confidential source generation A. Its documented service port is 4242."
		const textB = "LifecycleCobalt is the confidential replacement generation B. Its documented service port is 5252."
		item := func(text string) ingest.Item {
			return ingest.Item{ID: dataID, Filename: "retirement.txt", Text: text, StructuredArtifact: []byte(fmt.Sprintf("{%q:%q}", "text", text))}
		}
		ingestSource := func(dataset, text string, expected *ingest.SourceCAS) ingest.Result {
			t.Helper()
			var results []ingest.Result
			var err error
			if expected == nil {
				results, _, err = writer.IngestAuthorized(ctx, []ingest.Item{item(text)}, nil, cfg.StoragePath, nil, actor, dataset, map[string]string{"alpha": "Alpha", "beta": "Beta"}[dataset])
			} else {
				results, _, err = writer.ReplaceAuthorized(ctx, []ingest.Item{item(text)}, nil, cfg.StoragePath, nil, actor, dataset, map[string]string{"alpha": "Alpha", "beta": "Beta"}[dataset], *expected)
			}
			if err != nil || len(results) != 1 || results[0].StructuredArtifactID == "" {
				t.Fatalf("ingest %s: results=%+v err=%v", dataset, results, err)
			}
			return results[0]
		}
		resource := func(dataset string) accesspkg.DocumentResource {
			t.Helper()
			r, err := f.p.GetDocumentResource(ctx, accesspkg.DocumentRef{DatasetID: dataset, DataID: dataID})
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		register := func(dataset string) {
			t.Helper()
			r, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: dataset, DataID: dataID}, "a", accesspkg.DocumentRestricted)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.p.GrantDocument(ctx, f.owner, r.DocumentRef, r.ACLRevision, accesspkg.DocumentPrincipal{Kind: accesspkg.DocumentUser, ID: "viewer"}, accesspkg.RoleViewer); err != nil {
				t.Fatal(err)
			}
		}
		publish := func(dataset, text, graphID string) {
			t.Helper()
			r := resource(dataset)
			revision, hash, err := f.p.SourceVersion(ctx, r.DocumentRef)
			if err != nil {
				t.Fatal(err)
			}
			collection := "lifecycle_" + dataset
			source := cognifySource{datasetID: dataset, documentID: dataID, contentRevision: r.ContentRevision, sourceRevision: revision, rawContentHash: hash, texts: []string{text}}
			pipe := orchestrator.Config{Collection: collection, Collections: cfg.Collections, BM25Indexes: cfg.BM25Indexes, EmbedEndpoint: cfg.EmbedEndpoint, EmbedModel: cfg.EmbedModel, DB: f.db, SkipGraph: true, MinChunkChars: 1}
			if err := runCognifySources(ownerCtx, []cognifySource{source}, cfg, pipe, make(chan orchestrator.Progress, 100)); err != nil {
				t.Fatal(err)
			}
			hits := cfg.BM25Indexes.Get(collection).Search(strings.Fields(text)[0], 10)
			if len(hits) != 1 {
				t.Fatalf("native publication %s hits=%+v", dataset, hits)
			}
			proof, err := decodeSearchDocumentSource(hits[0].Metadata)
			if err != nil || proof.Generation == "" || proof.DatasetID != dataset || proof.DocumentID != dataID || proof.ContentRevision != r.ContentRevision {
				t.Fatalf("actual publication proof=%+v err=%v", proof, err)
			}
			// Independent endpoints/edge for each association and generation.
			for _, id := range []string{graphID, graphID + "-target"} {
				f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES($1,$1,'Entity',$2,$3)", id, dataset, string(hits[0].Metadata))
			}
			f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES($1,$2,$3,'KNOWS',$4,$5)", graphID+"-edge", graphID, graphID+"-target", dataset, string(hits[0].Metadata))
		}
		base := func(dataset string) string { return "/datasets/" + dataset + "/data/" + dataID }
		artifact := func(dataset string, result ingest.Result) string {
			return base(dataset) + "/structured-artifacts/" + result.StructuredArtifactID
		}
		checkGraph := func(dataset, graphID string, want bool) {
			t.Helper()
			result := h.toolQueryEntity(viewerCtx, map[string]any{"name": graphID, "dataset_id": dataset})
			got := !result.IsError && len(result.Content) > 0 && strings.Contains(result.Content[0].Text, graphID+"-edge")
			if got != want {
				t.Fatalf("query_entity %s visible=%v want=%v result=%+v", graphID, got, want, result)
			}
			status, body, _ := f.request("viewer", "GET", "/graph/path?from="+graphID+"&to="+graphID+"-target", "", "X-Tenant-ID", "a")
			var path struct {
				Edges []json.RawMessage `json:"edges"`
			}
			wantEdges := 0
			if want {
				wantEdges = 1
			}
			if status != 200 || json.Unmarshal(body, &path) != nil || len(path.Edges) != wantEdges {
				t.Fatalf("public graph path %s status=%d body=%s want_edges=%d", graphID, status, body, wantEdges)
			}
		}
		checkRAG := func(dataset, text string, want bool) {
			t.Helper()
			before := len(model.promptsSnapshot())
			request, _ := json.Marshal(map[string]any{"query_text": "What is the documented service port?", "query_type": "RAG_COMPLETION", "collection": "lifecycle_" + dataset, "strict_grounded": true, "top_k": 10})
			status, body, _ := f.request("viewer", "POST", "/search/text", string(request))
			var result struct {
				Abstained   bool              `json:"abstained"`
				Chunks      []json.RawMessage `json:"chunks"`
				EvidenceIDs []string          `json:"evidence_ids"`
				Answer      string            `json:"answer"`
			}
			if status != 200 || json.Unmarshal(body, &result) != nil {
				t.Fatalf("RAG %s status=%d body=%s", dataset, status, body)
			}
			prompts := model.promptsSnapshot()
			if want {
				if result.Abstained || len(result.Chunks) == 0 || len(result.EvidenceIDs) == 0 || result.Answer == "" || len(prompts) != before+1 || !strings.Contains(prompts[len(prompts)-1], text) || !bytes.Contains(body, []byte(strings.Fields(text)[0])) {
					t.Fatalf("RAG lost native source %s: body=%s prompts=%v", dataset, body, prompts)
				}
			} else if !result.Abstained || len(result.Chunks) != 0 || len(result.EvidenceIDs) != 0 || len(prompts) != before || bytes.Contains(body, []byte(strings.Fields(text)[0])) {
				t.Fatalf("RAG retired source visible %s: body=%s prompts=%v", dataset, body, prompts)
			}
		}
		checkLive := func(dataset, text, graphID string, result ingest.Result) {
			t.Helper()
			status, body, _ := f.request("viewer", "GET", base(dataset)+"/raw", "")
			if status != 200 || string(body) != text {
				t.Fatalf("raw %s status=%d body=%s", dataset, status, body)
			}
			status, body, _ = f.request("viewer", "GET", artifact(dataset, result), "")
			if status != 200 || !bytes.Contains(body, []byte(text)) {
				t.Fatalf("artifact %s status=%d body=%s", dataset, status, body)
			}
			checkRAG(dataset, text, true)
			checkGraph(dataset, graphID, true)
		}
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		a := ingestSource("alpha", textA, nil)
		register("alpha")
		publish("alpha", textA, "alpha-A")
		checkLive("alpha", textA, "alpha-A", a)
		var revision int64
		var hash string
		if err := f.db.QueryRow(Q("SELECT source_revision,raw_content_hash FROM data WHERE id=$1"), dataID).Scan(&revision, &hash); err != nil {
			t.Fatal(err)
		}
		b := ingestSource("alpha", textB, &ingest.SourceCAS{Revision: revision, RawContentHash: hash})
		if b.StructuredArtifactID == a.StructuredArtifactID {
			t.Fatal("replacement reused artifact identity")
		}
		f.expect("viewer", "GET", artifact("alpha", a), "", 404)
		status, body, _ := f.request("viewer", "GET", base("alpha")+"/raw", "")
		if status != 200 || string(body) != textB {
			t.Fatalf("replacement raw before publication status=%d body=%s", status, body)
		}
		checkRAG("alpha", textA, false)
		checkGraph("alpha", "alpha-A", false)
		publish("alpha", textB, "alpha-B")
		checkLive("alpha", textB, "alpha-B", b)
		alias := ingestSource("beta", textB, nil)
		if alias.StructuredArtifactID != b.StructuredArtifactID {
			t.Fatal("identical alias duplicated source artifact")
		}
		register("beta")
		publish("beta", textB, "beta-B")
		checkLive("beta", textB, "beta-B", alias)
		f.expect("owner", "DELETE", base("alpha"), "", 200, "If-Match", documentETag(resource("alpha")))
		f.expect("viewer", "GET", base("alpha")+"/raw", "", 403)
		f.expect("viewer", "GET", artifact("alpha", b), "", 403)
		checkRAG("alpha", textB, false)
		checkGraph("alpha", "alpha-B", false)
		checkLive("beta", textB, "beta-B", alias)
		if _, err := os.Stat(strings.TrimPrefix(b.StructuredArtifactPath, "file://")); err != nil {
			t.Fatalf("live alias lost artifact bytes: %v", err)
		}
		f.expect("owner", "DELETE", base("beta"), "", 200, "If-Match", documentETag(resource("beta")))
		f.expect("viewer", "GET", base("beta")+"/raw", "", 403)
		f.expect("viewer", "GET", artifact("beta", alias), "", 403)
		checkRAG("beta", textB, false)
		checkGraph("beta", "beta-B", false)
		if _, err := os.Stat(strings.TrimPrefix(b.StructuredArtifactPath, "file://")); !os.IsNotExist(err) {
			t.Fatalf("last alias left artifact bytes: %v", err)
		}
		var inventory int
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM document_structured_artifacts WHERE data_id=$1"), dataID).Scan(&inventory); err != nil || inventory != 0 {
			t.Fatalf("last alias artifact inventory=%d err=%v", inventory, err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatalf("lifecycle leaked SQL fence: %+v", f.db.Stats())
		}
	})
}

// The provider fence must serialize a real independent source mutation, while
// the fresh publication fence must still reject or retire the late generation.
// This is a logical visibility oracle; native vectors may remain physically.
func TestDocumentRetirementWaitsForLateEmbedding(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(3)
		native, closeNative := newWorkspaceTestConfig(t)
		defer closeNative()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		entered, release := make(chan struct{}), make(chan struct{})
		var block atomic.Bool
		var enteredOnce, releaseOnce sync.Once
		releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
		endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Input []string `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil {
				http.Error(w, "invalid embedding request", 400)
				return
			}
			if block.Load() {
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-release:
				case <-ctx.Done():
					return
				}
			}
			rows := make([]any, len(request.Input))
			for i := range rows {
				rows[i] = map[string]any{"index": i, "embedding": []float32{1, 0}}
			}
			json.NewEncoder(w).Encode(map[string]any{"data": rows})
		}))
		defer endpoint.Close()
		defer releaseProvider()
		cfg := f.cfg
		cfg.RequireAuth, cfg.Collections, cfg.BM25Indexes, cfg.EmbedEndpoint = true, native.Collections, native.BM25Indexes, endpoint.URL
		f.app.Post("/api/v1/search/text", searchHandler(cfg))
		expiry := time.Now().Add(time.Hour).Unix()
		ownerCtx := context.WithValue(ctx, searchActorKey{}, f.owner)
		ownerCtx = context.WithValue(ownerCtx, searchEgressKey{}, searchEgress{cfg: cfg, actor: f.owner, kind: "jwt", expiresAt: expiry})
		// Native embedding intentionally skips fragments of 30 bytes or less.
		const text = "LateLifecycleAmber is the confidential source used to prove retirement after an in-flight embedding provider returns."
		var location string
		if err := f.db.QueryRow("SELECT raw_data_location FROM data WHERE id='blob'").Scan(&location); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(strings.TrimPrefix(location, "file://"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		f.exec("UPDATE data SET raw_content_hash=$1 WHERE id='blob'", fmt.Sprintf("%x", sha256.Sum256([]byte(text))))
		revision, hash, err := f.p.SourceVersion(ctx, f.r.DocumentRef)
		if err != nil {
			t.Fatal(err)
		}
		source := cognifySource{datasetID: "alpha", documentID: "blob", contentRevision: f.r.ContentRevision, sourceRevision: revision, rawContentHash: hash, texts: []string{text}}
		pipe := orchestrator.Config{Collection: "late_lifecycle", Collections: cfg.Collections, BM25Indexes: cfg.BM25Indexes, EmbedEndpoint: endpoint.URL, DB: f.db, SkipGraph: true, MinChunkChars: 1}
		run := func() error {
			return runCognifySources(ownerCtx, []cognifySource{source}, cfg, pipe, make(chan orchestrator.Progress, 100))
		}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		search := func(want bool) {
			t.Helper()
			status, body, _ := f.request("owner", "POST", "/search/text", `{"query_text":"LateLifecycleAmber","query_type":"CHUNKS_LEXICAL","collection":"late_lifecycle"}`)
			if status != 200 || bytes.Contains(body, []byte(text)) != want {
				t.Fatalf("public lexical visible=%v want=%v status=%d body=%s", bytes.Contains(body, []byte(text)), want, status, body)
			}
		}
		search(true) // Establish a real public positive before the race.
		hits := cfg.BM25Indexes.Get("late_lifecycle").Search("LateLifecycleAmber", 10)
		if len(hits) != 1 {
			t.Fatalf("initial native source missing: %+v", hits)
		}
		proof, err := decodeSearchDocumentSource(hits[0].Metadata)
		if err != nil || proof.Generation == "" {
			t.Fatalf("initial generation proof=%+v err=%v", proof, err)
		}
		for _, id := range []string{"late-source", "late-target"} {
			f.exec("INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES($1,$1,'Entity','alpha',$2)", id, string(hits[0].Metadata))
		}
		f.exec("INSERT INTO graph_edges(id,source_id,target_id,relationship_name,dataset_id,properties) VALUES('late-edge','late-source','late-target','KNOWS','alpha',$1)", string(hits[0].Metadata))
		queryGraph := func(want bool) {
			t.Helper()
			queryCtx := context.WithValue(ownerCtx, mcp.UserIDKey, f.owner.UserID)
			queryCtx = context.WithValue(queryCtx, mcp.TenantIDKey, f.owner.TenantID)
			result := (&mcpHandler{cfg: cfg}).toolQueryEntity(queryCtx, map[string]any{"name": "late-source", "dataset_id": "alpha"})
			visible := !result.IsError && len(result.Content) > 0 && strings.Contains(result.Content[0].Text, "late-edge")
			if visible != want {
				t.Fatalf("direct query_entity visible=%v want=%v result=%+v", visible, want, result)
			}
		}
		queryGraph(true)
		block.Store(true)
		cognified := make(chan error, 1)
		go func() { cognified <- run() }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("late embedding never entered")
		}
		started, mutated := make(chan struct{}), make(chan error, 1)
		go func() {
			close(started)
			mutated <- f.p.DeleteDocumentAssociation(ctx, f.owner, f.r.DocumentRef, f.r.ACLRevision, f.r.ContentRevision)
		}()
		<-started
		select {
		case err := <-mutated:
			t.Fatalf("tombstone crossed blocked provider fence: %v", err)
		case <-time.After(70 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("mutation observation deadline")
		}
		releaseProvider()
		select {
		case err := <-mutated:
			if err != nil {
				t.Fatalf("real tombstone failed: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("tombstone did not finish after provider release")
		}
		select {
		case err := <-cognified:
			// Publication may win the released fence, or lose its final CAS.
			t.Logf("late cognify completion after tombstone race: %v", err)
		case <-ctx.Done():
			t.Fatal("late cognify did not finish")
		}
		search(false)
		queryGraph(false)
		if f.db.Stats().InUse != 0 {
			t.Fatalf("late lifecycle leaked SQL: %+v", f.db.Stats())
		}
	})
}

func TestDocumentRetiredArtifactBackendFailureRetry(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		ingest.SetSQLiteMode(GetDBProvider() == DBSQLite)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		backend := &artifactDeleteStorage{memStorage: newMemStorage()}
		cfg := f.cfg
		cfg.RequireAuth, cfg.FileStorage = true, backend
		f.app.Get("/api/v1/datasets/:id/data/:dataId/structured-artifacts/:artifactId", structuredArtifactHandler(cfg))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		writer, err := ingest.NewMetadataWriterForStorage(f.db, backend)
		if err != nil {
			t.Fatal(err)
		}
		actor := accesspkg.MetadataActor{Actor: f.owner, Credential: accesspkg.MetadataCredential{Kind: "jwt", ExpiresAt: time.Now().Add(time.Hour).Unix()}}
		const id = "artifact-retry-source"
		first, _, err := writer.IngestAuthorized(ctx, []ingest.Item{{ID: id, Text: "old source", StructuredArtifact: []byte(`{"version":"old"}`)}}, nil, cfg.StoragePath, backend, actor, "alpha", "Alpha")
		if err != nil || len(first) != 1 {
			t.Fatalf("initial artifact ingest=%+v err=%v", first, err)
		}
		if _, err := f.p.RegisterDocument(ctx, f.owner, accesspkg.DocumentRef{DatasetID: "alpha", DataID: id}, "a", accesspkg.DocumentRestricted); err != nil {
			t.Fatal(err)
		}
		path := func(artifactID string) string {
			return "/datasets/alpha/data/" + id + "/structured-artifacts/" + artifactID
		}
		f.expect("owner", "GET", path(first[0].StructuredArtifactID), "", 200)
		var revision int64
		var hash string
		if err := f.db.QueryRow(Q("SELECT source_revision,raw_content_hash FROM data WHERE id=$1"), id).Scan(&revision, &hash); err != nil {
			t.Fatal(err)
		}
		backend.fail.Store(true)
		second, _, err := writer.ReplaceAuthorized(ctx, []ingest.Item{{ID: id, Text: "new source", StructuredArtifact: []byte(`{"version":"new"}`)}}, nil, cfg.StoragePath, backend, actor, "alpha", "Alpha", ingest.SourceCAS{Revision: revision, RawContentHash: hash})
		if err != nil || len(second) != 1 || second[0].StructuredArtifactID == first[0].StructuredArtifactID {
			t.Fatalf("replacement artifact=%+v err=%v", second, err)
		}
		oldKey := strings.TrimPrefix(first[0].StructuredArtifactPath, "storage://")
		checkInventory := func(want int) {
			t.Helper()
			var retired int
			if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM document_structured_artifacts WHERE data_id=$1 AND state='retired'"), id).Scan(&retired); err != nil || retired != want {
				t.Fatalf("retired inventory=%d want=%d err=%v", retired, want, err)
			}
		}
		if !cleanupRetiredStructuredArtifacts(ctx, cfg, id) {
			t.Fatal("backend failure did not report pending cleanup")
		}
		checkInventory(1)
		if _, exists := backend.objects[oldKey]; !exists {
			t.Fatal("failed cleanup lost retired bytes")
		}
		f.expect("owner", "GET", path(first[0].StructuredArtifactID), "", 404)
		f.expect("owner", "GET", path(second[0].StructuredArtifactID), "", 200)
		backend.fail.Store(false)
		if cleanupRetiredStructuredArtifacts(ctx, cfg, id) {
			t.Fatal("cleanup retry remained pending")
		}
		checkInventory(0)
		if _, exists := backend.objects[oldKey]; exists {
			t.Fatal("retry left retired artifact bytes")
		}
		if cleanupRetiredStructuredArtifacts(ctx, cfg, id) {
			t.Fatal("idempotent cleanup became pending")
		}
		f.expect("owner", "GET", path(first[0].StructuredArtifactID), "", 404)
		f.expect("owner", "GET", path(second[0].StructuredArtifactID), "", 200)
		if f.db.Stats().InUse != 0 {
			t.Fatalf("artifact retry leaked SQL: %+v", f.db.Stats())
		}
	})
}
