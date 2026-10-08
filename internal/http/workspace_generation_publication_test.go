package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
	"github.com/valyala/fasthttp"
)

func workspaceGenerationModes(t *testing.T, run func(*testing.T, APIConfig, accesspkg.MetadataActor, context.Context)) {
	t.Helper()
	workspaceIntegrityModes(t, func(t *testing.T, base APIConfig, actor accesspkg.MetadataActor) {
		cfg, closeCfg := newWorkspaceTestConfig(t)
		defer closeCfg()
		cfg.DB, cfg.RequireAuth = base.DB, base.RequireAuth
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, mcp.UserIDKey, actor.UserID)
		ctx = context.WithValue(ctx, mcp.TenantIDKey, actor.TenantID)
		ctx = context.WithValue(ctx, searchActorKey{}, actor.Actor)
		ctx = context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		ctx = context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: actor.Actor, kind: actor.Credential.Kind, expiresAt: actor.Credential.ExpiresAt})
		run(t, cfg, actor, ctx)
	})
}

func workspaceGenerationRequest() workspaceReindexRequest {
	return workspaceReindexRequest{ProjectID: "alpha", Branch: "main", Generation: "stable", Collection: "generation-control", Paths: []string{"a.md", "b.md"}, ChunkStrategy: "paragraph", MinChunkChars: 1, MaxChunkChars: 500, ActivateGeneration: true}
}

func workspaceGenerationManifest(t *testing.T, cfg APIConfig) (*workspace.Manifest, []byte) {
	t.Helper()
	manifest, _, err := loadWorkspaceManifest(cfg, "alpha", "main")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, raw
}

func workspaceGenerationRecords(t *testing.T, cfg APIConfig, manifest *workspace.Manifest) []pipeline.ScoredResult {
	t.Helper()
	var records []pipeline.ScoredResult
	for _, chunk := range manifest.Chunks {
		db, err := cfg.Collections.Get(chunk.Collection)
		if err != nil {
			t.Fatal(err)
		}
		_, metadata, found := db.Get(chunk.VectorID)
		if !found {
			t.Fatalf("authoritative vector missing %s", chunk.VectorID)
		}
		records = append(records, pipeline.ScoredResult{ID: chunk.VectorID, Collection: chunk.Collection, Metadata: metadata, Score: 1})
	}
	return records
}

func TestWorkspaceGenerationLaterFailurePreservesPublication(t *testing.T) {
	for _, failure := range []string{"second-read", "second-embed"} {
		t.Run(failure, func(t *testing.T) {
			workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
				workspaceIntegrityPut(t, cfg, "a.md", []byte("Original copper service."))
				b := workspaceIntegrityPut(t, cfg, "b.md", []byte("Original zinc service."))
				request := workspaceGenerationRequest()
				if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err != nil {
					t.Fatal(err)
				}
				prior, priorJSON := workspaceGenerationManifest(t, cfg)
				records := workspaceGenerationRecords(t, cfg, prior)
				if len(records) != 2 {
					t.Fatalf("baseline chunk count=%d", len(records))
				}
				type stored struct {
					Vector   []float32
					Metadata []byte
				}
				before := map[string]stored{}
				for _, record := range records {
					db, _ := cfg.Collections.Get(record.Collection)
					vector, metadata, _ := db.Get(record.ID)
					before[record.ID] = stored{append([]float32(nil), vector...), append([]byte(nil), metadata...)}
				}
				workspaceIntegrityPut(t, cfg, "a.md", []byte("Prepared replacement copper."))
				var prepared atomic.Int32
				if failure == "second-read" {
					if err := os.Remove(b); err != nil {
						t.Fatal(err)
					}
				} else {
					workspaceIntegrityPut(t, cfg, "b.md", []byte("second-file-fail zinc"))
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						var input struct {
							Input []string `json:"input"`
						}
						if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
							http.Error(w, err.Error(), 400)
							return
						}
						for _, text := range input.Input {
							if strings.Contains(text, "second-file-fail") {
								http.Error(w, "forced second file", 400)
								return
							}
						}
						prepared.Add(1)
						values := make([]map[string]any, len(input.Input))
						for i := range values {
							values[i] = map[string]any{"index": i, "embedding": []float32{1, 2}}
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"data": values})
					}))
					defer server.Close()
					cfg.EmbedEndpoint = server.URL
					cfg.EmbedClient = nil
				}
				if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err == nil {
					t.Fatalf("%s batch falsely succeeded", failure)
				}
				if failure == "second-embed" && prepared.Load() == 0 {
					t.Fatal("first file never prepared before second-file failure")
				}
				_, afterJSON := workspaceGenerationManifest(t, cfg)
				if !bytes.Equal(priorJSON, afterJSON) {
					t.Fatalf("failed batch changed exact prior manifest\nbefore=%s\nafter=%s", priorJSON, afterJSON)
				}
				for _, record := range records {
					db, _ := cfg.Collections.Get(record.Collection)
					vector, metadata, found := db.Get(record.ID)
					if !found || !reflect.DeepEqual(vector, before[record.ID].Vector) || !bytes.Equal(metadata, before[record.ID].Metadata) {
						t.Fatalf("failed same-generation attempt overwrote prior vector %s", record.ID)
					}
				}
				admitted, err := filterMCPDocumentResults(ctx, cfg, records)
				if err != nil || len(admitted) != len(records) {
					t.Fatalf("prior searchable records lost: admitted=%d want=%d err=%v", len(admitted), len(records), err)
				}
			})
		})
	}
}

func TestWorkspaceGenerationSameLogicalRechunkPublishesFreshIDs(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		text := strings.Repeat("Copper protects published source boundaries. ", 15)
		if len(text) <= 90*3 {
			t.Fatal("rechunk source must span several sliding windows")
		}
		workspaceIntegrityPut(t, cfg, "a.md", []byte(text))
		workspaceIntegrityPut(t, cfg, "b.md", []byte("Zinc remains independent."))
		request := workspaceGenerationRequest()
		request.MaxChunkChars = 2000
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err != nil {
			t.Fatal(err)
		}
		prior, _ := workspaceGenerationManifest(t, cfg)
		old := workspaceGenerationRecords(t, cfg, prior)
		if len(prior.Chunks) != 2 {
			t.Fatalf("paragraph baseline must have exactly one chunk per source, got %d", len(prior.Chunks))
		}
		request.ChunkStrategy = "sliding"
		request.MaxChunkChars = 90
		request.MinChunkChars = 1
		request.OverlapChars = 10
		snap := false
		request.SnapToSentence = &snap
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err != nil {
			t.Fatal(err)
		}
		current, _ := workspaceGenerationManifest(t, cfg)
		if current.ActiveGeneration != "stable" {
			t.Fatalf("logical generation changed: %s", current.ActiveGeneration)
		}
		if len(current.Chunks) <= len(prior.Chunks) {
			t.Fatalf("rechunk control did not change layout before=%d after=%d", len(prior.Chunks), len(current.Chunks))
		}
		for _, record := range old {
			if _, found := current.Chunks[record.ID]; found {
				t.Fatalf("same-generation prepared vector reused old physical ID %s", record.ID)
			}
		}
		admitted, err := filterMCPDocumentResults(ctx, cfg, old)
		if err != nil || len(admitted) != 0 {
			t.Fatalf("retired rechunk results admitted=%d err=%v", len(admitted), err)
		}
		currentRecords := workspaceGenerationRecords(t, cfg, current)
		admitted, err = filterMCPDocumentResults(ctx, cfg, currentRecords)
		if err != nil || len(admitted) != len(currentRecords) {
			t.Fatalf("fresh publication missing context records=%d admitted=%d err=%v", len(currentRecords), len(admitted), err)
		}
	})
}

func workspaceGenerationInventory(t *testing.T, manifest *workspace.Manifest, generation string) map[string]string {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var disk struct {
		Files map[string]map[string]string `json:"files"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	inventory, exists := disk.Files[generation]
	if !exists || inventory == nil {
		t.Fatalf("committed inventory is unknown rather than explicit: %s", raw)
	}
	return inventory
}

func TestWorkspaceGenerationInventoryAndSelectedMissingScope(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		a := workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper searchable control."))
		b := workspaceIntegrityPut(t, cfg, "b.md", []byte("Zinc unrelated control."))
		request := workspaceGenerationRequest()
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: request, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(a); err != nil {
			t.Fatal(err)
		}
		request.Paths = []string{"a.md"}
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: request, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ := workspaceGenerationManifest(t, cfg)
		inventory := workspaceGenerationInventory(t, manifest, "stable")
		if _, exists := inventory["a.md"]; exists || inventory["b.md"] != digestBytes([]byte("Zinc unrelated control.")) {
			t.Fatalf("selected deletion broadened scope: %v", inventory)
		}
		if len(manifest.ListChunks(workspace.ChunkFilter{Path: "b.md", ActiveOnly: true})) == 0 {
			t.Fatal("selected deleteMissing retired unrelated path")
		}
		workspaceIntegrityPut(t, cfg, "b.md", []byte(" \r\n\t"))
		request.Paths = []string{"b.md"}
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: request, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ = workspaceGenerationManifest(t, cfg)
		if len(manifest.ListChunks(workspace.ChunkFilter{Path: "b.md", ActiveOnly: true})) != 0 || workspaceGenerationInventory(t, manifest, "stable")["b.md"] != digestBytes([]byte(" \r\n\t")) {
			t.Fatalf("zero-chunk file did not commit actual digest: %+v", manifest)
		}
		if err := os.Remove(b); err != nil {
			t.Fatal(err)
		}
		request.Paths = nil
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: request, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ = workspaceGenerationManifest(t, cfg)
		if len(workspaceGenerationInventory(t, manifest, "stable")) != 0 || len(manifest.ListChunks(workspace.ChunkFilter{ActiveOnly: true})) != 0 {
			t.Fatalf("last-file deletion failed explicit empty publication: %+v", manifest)
		}
	})
}

func TestWorkspaceGenerationContextMembershipAndSourceCitationRead(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		const sourceText = "Copper ledger authorization preserves exact source citations."
		workspaceIntegrityPut(t, cfg, "a.md", []byte(sourceText))
		workspaceIntegrityPut(t, cfg, "b.md", []byte("Zinc separate control."))
		request := workspaceGenerationRequest()
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, request, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ := workspaceGenerationManifest(t, cfg)
		records := workspaceGenerationRecords(t, cfg, manifest)
		for _, record := range records {
			db, _ := cfg.Collections.Get(record.Collection)
			vector, metadata, _ := db.Get(record.ID)
			wrongCollection := "wrong-physical-collection"
			var meta any
			if err := json.Unmarshal(metadata, &meta); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Collections.Insert(wrongCollection, record.ID, vector, meta); err != nil {
				t.Fatal(err)
			}
			wrongDB, err := cfg.Collections.Get(wrongCollection)
			if err != nil {
				t.Fatal(err)
			}
			_, wrongMetadata, found := wrongDB.Get(record.ID)
			if !found {
				t.Fatal("wrong collection physical control absent")
			}
			unpublishedID := "unpublished-" + record.ID
			if err := cfg.Collections.Insert(record.Collection, unpublishedID, vector, meta); err != nil {
				t.Fatal(err)
			}
			_, unpublishedMetadata, found := db.Get(unpublishedID)
			if !found {
				t.Fatal("unpublished physical control absent")
			}
			var malformed map[string]any
			if err := json.Unmarshal(metadata, &malformed); err != nil {
				t.Fatal(err)
			}
			delete(malformed, "chunk_id")
			malformed["text"] = "Copper ledger authorization malformed workspace"
			malformedID := "malformed-" + record.ID
			if err := cfg.Collections.Insert(record.Collection, malformedID, vector, malformed); err != nil {
				t.Fatal(err)
			}
			_, malformedMetadata, exists := db.Get(malformedID)
			if !exists {
				t.Fatal("malformed workspace physical control absent")
			}
			workspaceLexicalIndex(cfg, record.Collection).Add(malformedID, malformed["text"].(string), string(malformedMetadata))
			bad := []pipeline.ScoredResult{{ID: record.ID, Collection: wrongCollection, Metadata: wrongMetadata}, {ID: unpublishedID, Collection: record.Collection, Metadata: unpublishedMetadata}, {ID: malformedID, Collection: record.Collection, Metadata: malformedMetadata}}
			admitted, err := filterMCPDocumentResults(ctx, cfg, bad)
			if err != nil || len(admitted) != 0 {
				t.Fatalf("wrong collection/unpublished entered context: %v err=%v", admitted, err)
			}
		}
		h := &mcpHandler{cfg: cfg}
		result := h.toolWorkspaceSearch(ctx, map[string]any{"project_id": "alpha", "branch": "main", "search_query": "Copper ledger authorization", "search_type": "CHUNKS_LEXICAL", "top_k": 10})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("actual source search failed: %+v", result)
		}
		var response struct {
			Results []map[string]any `json:"results"`
			Status  string           `json:"generic_search_status"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range response.Results {
			raw, _ := json.Marshal(hit)
			if strings.Contains(string(raw), "malformed-") {
				t.Fatalf("actual workspace context admitted missing chunk_id: %s", raw)
			}
			if strings.Contains(string(raw), "a.md") && strings.Contains(string(raw), workspaceFileCitation("alpha", "main", "a.md").SourceURI) {
				found = true
			}
		}
		if !found {
			t.Fatalf("actual search lacks source citation status=%s results=%s", response.Status, result.Content[0].Text)
		}
		exact, err := readWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReadRequest{ProjectID: "alpha", Branch: "main", Path: "a.md"}, actor)
		if err != nil || exact.Text != sourceText {
			t.Fatalf("citation exact read=%q err=%v", exact.Text, err)
		}
		if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
			t.Fatalf("native generation controls leaked SQL: %+v", cfg.DB.Stats())
		}
	})
}

func TestWorkspaceGenerationLegacyUnknownInventorySelectedMutation(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper known indexed source."))
		whitespace := []byte(" \r\n\t")
		workspaceIntegrityPut(t, cfg, "b.md", whitespace)
		req := workspaceGenerationRequest()
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: req, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ := workspaceGenerationManifest(t, cfg)
		if workspaceGenerationInventory(t, manifest, "stable")["b.md"] != digestBytes(whitespace) {
			t.Fatal("zero-chunk inventory baseline missing")
		}
		// Legacy files omitted the inventory entirely. Chunk rows cannot reconstruct
		// a committed whitespace file, so a selected mutation must retain unknown.
		delete(manifest.Files, "stable")
		if err := saveWorkspaceManifest(ctx, cfg, workspaceManifestPath(cfg, "alpha", "main"), manifest); err != nil {
			t.Fatal(err)
		}
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper selected legacy replacement."))
		req.Paths = []string{"a.md"}
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		current, _ := workspaceGenerationManifest(t, cfg)
		if _, known := current.Files["stable"]; known {
			t.Fatalf("selected legacy mutation fabricated complete inventory: %#v", current.Files["stable"])
		}
		if len(current.ListChunks(workspace.ChunkFilter{Path: "a.md", ActiveOnly: true})) == 0 {
			t.Fatal("unknown-inventory handling prevented selected publication")
		}
		req.Paths = nil
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: req, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		current, _ = workspaceGenerationManifest(t, cfg)
		inventory := workspaceGenerationInventory(t, current, "stable")
		if len(inventory) != 2 || inventory["b.md"] != digestBytes(whitespace) || inventory["a.md"] != digestBytes([]byte("Copper selected legacy replacement.")) {
			t.Fatalf("full reconcile did not establish complete actual inventory: %#v", inventory)
		}
	})
}

func TestWorkspaceGenerationLexicalRestartRefreshPreservesSharedCollection(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper lexical restart recovery control."))
		req := workspaceGenerationRequest()
		req.Paths = []string{"a.md"}
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ := workspaceGenerationManifest(t, cfg)
		if len(workspaceGenerationRecords(t, cfg, manifest)) != 1 {
			t.Fatal("durable published vector precondition failed")
		}
		// A fresh server can load a stale lexical snapshot: keep another project's
		// document in this shared collection, but omit the target's published ID.
		cfg.BM25Indexes = bm25.NewIndexRegistry()
		foreign := map[string]any{"text": "Quartz foreign project lexical control", "project_id": "beta", "dataset_id": "beta", "branch": "main", "generation": "foreign", "chunk_id": "foreign-chunk", "path": "other.md", "file_digest": digestBytes([]byte("foreign")), "document_id": "foreign-document"}
		if err := cfg.Collections.Insert(req.Collection, "foreign-lexical-control", []float32{1, 2}, foreign); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(foreign)
		if err != nil {
			t.Fatal(err)
		}
		lexical := workspaceLexicalIndex(cfg, req.Collection)
		lexical.Add("foreign-lexical-control", foreign["text"].(string), string(raw))
		if len(lexical.Search("Copper", 10)) != 0 || len(lexical.Search("Quartz", 10)) != 1 {
			t.Fatal("stale lexical restart precondition failed")
		}
		h := &mcpHandler{cfg: cfg}
		result := h.toolWorkspaceSearch(ctx, map[string]any{"project_id": "alpha", "branch": "main", "search_query": "Copper lexical restart", "search_type": "CHUNKS_LEXICAL", "top_k": 10})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("actual restart search failed: %+v", result)
		}
		var response struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range response.Results {
			data, _ := json.Marshal(hit)
			if strings.Contains(string(data), workspaceFileCitation("alpha", "main", "a.md").SourceURI) {
				found = true
			}
			if strings.Contains(string(data), "foreign-lexical-control") {
				t.Fatalf("another project entered target context: %s", data)
			}
		}
		if !found {
			t.Fatalf("published vectors did not refresh missing lexical context: %s", result.Content[0].Text)
		}
		shared := cfg.BM25Indexes.Get(req.Collection)
		if shared == nil {
			t.Fatal("shared lexical index disappeared")
		}
		foreignHits := shared.Search("Quartz", 10)
		if len(foreignHits) != 1 || foreignHits[0].ID != "foreign-lexical-control" {
			t.Fatalf("target refresh cleared another project's lexical document: %+v", foreignHits)
		}
		if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
			t.Fatal("lexical refresh leaked native SQL lease")
		}
	})
}

func TestWorkspaceGenerationNullInventoryAndSelectedNewActivation(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper null inventory source."))
		whitespace := []byte(" \r\n\t")
		workspaceIntegrityPut(t, cfg, "b.md", whitespace)
		req := workspaceGenerationRequest()
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: req, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		prior, _ := workspaceGenerationManifest(t, cfg)
		if workspaceGenerationInventory(t, prior, "stable")["b.md"] != digestBytes(whitespace) {
			t.Fatal("committed whitespace baseline missing")
		}
		prior.Files["stable"] = nil
		if err := saveWorkspaceManifest(ctx, cfg, workspaceManifestPath(cfg, "alpha", "main"), prior); err != nil {
			t.Fatal(err)
		}
		// Check persisted JSON null rather than silently treating an absent map as it.
		raw, err := os.ReadFile(workspaceManifestPath(cfg, "alpha", "main"))
		if err != nil {
			t.Fatal(err)
		}
		var persisted struct {
			Files map[string]json.RawMessage `json:"files"`
		}
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(persisted.Files["stable"], []byte("null")) {
			t.Fatalf("null fixture was not persisted: %s", raw)
		}
		req.Paths = []string{"a.md"}
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper null selected replacement."))
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		current, _ := workspaceGenerationManifest(t, cfg)
		if inventory, exists := current.Files["stable"]; exists && inventory != nil {
			t.Fatalf("null legacy inventory became known after selected mutation: %#v", inventory)
		}
		if len(current.ListChunks(workspace.ChunkFilter{Path: "a.md", ActiveOnly: true})) != 1 {
			t.Fatal("selected null-inventory publication missing")
		}
		req.Generation = "next-from-unknown"
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper new active selected replacement."))
		if _, err := reconcileWorkspaceMarkdownAuthorized(ctx, cfg, workspaceReconcileRequest{workspaceReindexRequest: req, DeleteMissing: true}, actor); err != nil {
			t.Fatal(err)
		}
		current, _ = workspaceGenerationManifest(t, cfg)
		if current.ActiveGeneration != req.Generation {
			t.Fatalf("new generation not active: %s", current.ActiveGeneration)
		}
		inventory := workspaceGenerationInventory(t, current, req.Generation)
		if len(inventory) != 2 || inventory["b.md"] != digestBytes(whitespace) || inventory["a.md"] != digestBytes([]byte("Copper new active selected replacement.")) {
			t.Fatalf("selected new activation lost committed zero-chunk file from unknown legacy state: %#v", inventory)
		}
		if len(current.ListChunks(workspace.ChunkFilter{Generation: req.Generation, Path: "b.md"})) != 0 {
			t.Fatal("whitespace carry-forward created synthetic chunks")
		}
	})
}

func TestWorkspaceGenerationSearchScopesActiveOtherBranch(t *testing.T) {
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper branch isolation main control."))
		req := workspaceGenerationRequest()
		req.Paths = []string{"a.md"}
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		indexed := true
		dev := workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Branch: "dev", Generation: "dev-active", Collection: req.Collection, Path: "dev.md", Text: "Copper branch isolation dev control.", ChunkStrategy: "paragraph", MinChunkChars: 1, MaxChunkChars: 500, ActivateGeneration: true}, Index: &indexed}
		if _, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, dev, actor); err != nil {
			t.Fatal(err)
		}
		devManifest, _, err := loadWorkspaceManifest(cfg, "alpha", "dev")
		if err != nil {
			t.Fatal(err)
		}
		if devManifest.ActiveGeneration != "dev-active" || len(devManifest.Chunks) != 1 {
			t.Fatalf("other branch must be genuinely active: %+v", devManifest)
		}
		var devRecord pipeline.ScoredResult
		var devVector []float32
		db, err := cfg.Collections.Get(req.Collection)
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range devManifest.Chunks {
			vector, metadata, found := db.Get(chunk.VectorID)
			if !found {
				t.Fatal("active dev physical record missing")
			}
			devVector = append([]float32(nil), vector...)
			devRecord = pipeline.ScoredResult{ID: chunk.VectorID, Collection: chunk.Collection, Metadata: append([]byte(nil), metadata...), Score: 1}
		}
		admitted, err := filterMCPDocumentResults(ctx, cfg, []pipeline.ScoredResult{devRecord})
		if err != nil || len(admitted) != 1 {
			t.Fatalf("general policy must admit authorized active dev control: %d %v", len(admitted), err)
		}
		beforeLexical := workspaceLexicalIndex(cfg, req.Collection).Search("Copper branch isolation", 10)
		if len(beforeLexical) != 2 {
			t.Fatalf("same query must nominate both active branches: %+v", beforeLexical)
		}
		h := &mcpHandler{cfg: cfg}
		// The general-policy probe and this workspace search are separate requests;
		// preserve verified authority but give the actual search its own evidence.
		searchCtx := context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
		result := h.toolWorkspaceSearch(searchCtx, map[string]any{"project_id": "alpha", "branch": "main", "search_query": "Copper branch isolation", "search_type": "CHUNKS_LEXICAL", "top_k": 10})
		if result.IsError || len(result.Content) == 0 {
			t.Fatalf("actual scoped search failed: %+v", result)
		}
		var response struct {
			Results []map[string]any `json:"results"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].Text), &response); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, hit := range response.Results {
			raw, _ := json.Marshal(hit)
			if strings.Contains(string(raw), devRecord.ID) || strings.Contains(string(raw), "dev.md") {
				t.Fatalf("active other branch entered main context: %s", raw)
			}
			if strings.Contains(string(raw), workspaceFileCitation("alpha", "main", "a.md").SourceURI) {
				found = true
			}
		}
		if !found {
			t.Fatalf("target main citation missing: %s", result.Content[0].Text)
		}
		vector, metadata, present := db.Get(devRecord.ID)
		if !present || !reflect.DeepEqual(vector, devVector) || !bytes.Equal(metadata, devRecord.Metadata) {
			t.Fatal("main search modified active other branch physical record")
		}
		afterLexical := workspaceLexicalIndex(cfg, req.Collection).Search("Copper branch isolation", 10)
		retained := false
		for _, hit := range afterLexical {
			if hit.ID == devRecord.ID {
				retained = true
			}
		}
		if !retained {
			t.Fatal("main refresh erased active dev lexical record")
		}
		if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
			t.Fatal("branch-scoped search leaked SQL lease")
		}
	})
}

func TestWorkspaceGenerationRetainedHistoricalScopeAndGenericExclusion(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	workspaceGenerationModes(t, func(t *testing.T, cfg APIConfig, actor accesspkg.MetadataActor, ctx context.Context) {
		const oldText = "Copper retained historical publication control."
		workspaceIntegrityPut(t, cfg, "a.md", []byte(oldText))
		req := workspaceGenerationRequest()
		req.Paths = []string{"a.md"}
		req.Generation = "historical-old"
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		oldManifest, _ := workspaceGenerationManifest(t, cfg)
		oldRecords := workspaceGenerationRecords(t, cfg, oldManifest)
		if len(oldRecords) != 1 {
			t.Fatalf("old publication count=%d", len(oldRecords))
		}
		old := oldRecords[0]
		req.Generation = "historical-current"
		workspaceIntegrityPut(t, cfg, "a.md", []byte("Copper current publication replacement."))
		if _, err := reindexWorkspaceMarkdownAuthorized(ctx, cfg, req, actor); err != nil {
			t.Fatal(err)
		}
		manifest, _ := workspaceGenerationManifest(t, cfg)
		if manifest.ActiveGeneration != req.Generation {
			t.Fatal("new generation activation precondition failed")
		}
		if _, retained := manifest.Chunks[old.ID]; !retained {
			t.Fatal("old publication was not retained in manifest")
		}
		general, err := filterMCPDocumentResults(ctx, cfg, []pipeline.ScoredResult{old})
		if err != nil || len(general) != 0 {
			t.Fatalf("unscoped generic search admitted inactive old publication: %d %v", len(general), err)
		}
		db, err := cfg.Collections.Get(req.Collection)
		if err != nil {
			t.Fatal(err)
		}
		vector, metadata, found := db.Get(old.ID)
		if !found {
			t.Fatal("retained old physical record absent")
		}
		var pending map[string]any
		if err := json.Unmarshal(metadata, &pending); err != nil {
			t.Fatal(err)
		}
		pending["text"] = "Copper pending unpublished historical control."
		if err := cfg.Collections.Insert(req.Collection, "historical-pending", vector, pending); err != nil {
			t.Fatal(err)
		}
		pendingRaw, err := json.Marshal(pending)
		if err != nil {
			t.Fatal(err)
		}
		foreign := map[string]any{}
		for key, value := range pending {
			foreign[key] = value
		}
		foreign["project_id"], foreign["dataset_id"], foreign["text"] = "beta", "beta", "Copper foreign project historical control."
		if err := cfg.Collections.Insert(req.Collection, "historical-foreign", vector, foreign); err != nil {
			t.Fatal(err)
		}
		foreignRaw, err := json.Marshal(foreign)
		if err != nil {
			t.Fatal(err)
		}
		index := true
		dev := workspaceWriteRequest{workspaceIndexRequest: workspaceIndexRequest{ProjectID: "alpha", Branch: "dev", Generation: "historical-dev", Collection: req.Collection, Path: "dev.md", Text: "Copper other branch historical control.", ChunkStrategy: "paragraph", MinChunkChars: 1, ActivateGeneration: true}, Index: &index}
		if _, err := writeWorkspaceMarkdownAuthorized(ctx, cfg, dev, actor); err != nil {
			t.Fatal(err)
		}
		devManifest, _, err := loadWorkspaceManifest(cfg, "alpha", "dev")
		if err != nil {
			t.Fatal(err)
		}
		if devManifest.ActiveGeneration != "historical-dev" || len(devManifest.Chunks) != 1 {
			t.Fatal("other branch active publication precondition failed")
		}
		var devID string
		for id := range devManifest.Chunks {
			devID = id
		}
		lexical := workspaceLexicalIndex(cfg, req.Collection)
		lexical.Add("historical-pending", pending["text"].(string), string(pendingRaw))
		lexical.Add("historical-foreign", foreign["text"].(string), string(foreignRaw))
		if len(lexical.Search("Copper", 20)) < 5 {
			t.Fatal("historical scope negative candidates were not physically indexed")
		}
		bad := []pipeline.ScoredResult{{ID: "historical-pending", Collection: req.Collection, Metadata: pendingRaw}, {ID: "historical-foreign", Collection: req.Collection, Metadata: foreignRaw}}
		general, err = filterMCPDocumentResults(ctx, cfg, bad)
		if err != nil || len(general) != 0 {
			t.Fatalf("pending/foreign records entered generic context: %d %v", len(general), err)
		}
		cfg.EmbedClient = embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 1)
		cfg.JWTSecret = "historical-transport-test-secret"
		h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
		assertHistorical := func(t *testing.T, body []byte) {
			t.Helper()
			var response struct {
				Results    []map[string]any `json:"results"`
				Collection string           `json:"collection"`
				Freshness  struct {
					Stale  bool   `json:"stale"`
					Reason string `json:"reason"`
				} `json:"freshness"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Collection != req.Collection || !response.Freshness.Stale || response.Freshness.Reason != "requested_generation_is_not_active" {
				t.Fatalf("historical scope response incompatible: %s", body)
			}
			hit := response.Results[0]
			if hit["id"] != old.ID || hit["text"] != oldText || hit["generation"] != "historical-old" || hit["branch"] != "main" {
				t.Fatalf("historical lookup returned another scope: %+v", hit)
			}
			citation, ok := hit["citation"].(map[string]any)
			if !ok || citation["stale"] != true || citation["potentially_stale"] != false || citation["generation"] != "historical-old" || citation["vector_id"] != old.ID {
				t.Fatalf("historical citation flags/identity changed: %+v", citation)
			}
		}
		for _, strategy := range []string{"CHUNKS", "CHUNKS_LEXICAL", "HYBRID"} {
			t.Run("direct_"+strategy, func(t *testing.T) {
				searchCtx := context.WithValue(ctx, searchEvidenceKey{}, &searchEvidence{sources: make(map[searchDocumentSource]struct{})})
				result := h.toolWorkspaceSearch(searchCtx, map[string]any{"project_id": "alpha", "branch": "main", "generation": "historical-old", "collection": req.Collection, "search_query": "Copper", "search_type": strategy, "top_k": 20})
				if result.IsError || len(result.Content) == 0 {
					t.Fatalf("explicit historical %s failed: %+v", strategy, result)
				}
				assertHistorical(t, []byte(result.Content[0].Text))
			})
		}
		app := fiber.New()
		app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: cfg.DB}); return c.Next() })
		app.Use(JWTMiddleware(cfg.JWTSecret, cfg.RequireAuth))
		app.Use(TenantMiddleware(AccessConfig{DB: cfg.DB}))
		app.Post("/workspace/search", workspaceSearchHandler(cfg))
		app.Post("/search/text", searchHandler(cfg))
		app.Post("/mcp", h.handleRPC)
		app.Post(latestMCPPath, h.handleLatestRPC)
		for _, path := range []string{"/workspace/search", "/mcp", latestMCPPath} {
			t.Run("retained_"+path, func(t *testing.T) {
				// Real JWT and tenant middleware run before each actual handler.
				for _, historical := range []bool{true, false} {
					tool := "workspace_search"
					args := map[string]any{"project_id": "alpha", "branch": "main", "generation": "historical-old", "collection": req.Collection, "search_query": "Copper", "search_type": "CHUNKS_LEXICAL", "top_k": 20}
					if !historical {
						tool = "search"
						args = map[string]any{"collection": req.Collection, "search_query": "Copper", "search_type": "CHUNKS_LEXICAL", "top_k": 20}
					}
					raw := &fasthttp.RequestCtx{}
					raw.Request.Header.SetMethod("POST")
					raw.Request.Header.SetContentType("application/json")
					raw.Request.SetRequestURI(path)
					encoded, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					if path == "/workspace/search" {
						if !historical {
							raw.Request.SetRequestURI("/search/text")
							encoded, err = json.Marshal(map[string]any{"collection": req.Collection, "query_text": "Copper", "query_type": "CHUNKS_LEXICAL", "top_k": 20})
							if err != nil {
								t.Fatal(err)
							}
						}
						raw.Request.SetBody(encoded)
					} else {
						for key, value := range codifyRPCHeaders(h, path, actor.UserID) {
							if key != "Authorization" || !actor.TrustedLocal {
								raw.Request.Header.Set(key, value)
							}
						}
						raw.Request.Header.Set("X-Tenant-Id", actor.TenantID)
						meta := ""
						if path == latestMCPPath {
							meta = "," + latestMCPMetaParams()
							raw.Request.Header.Set("Mcp-Name", tool)
						}
						raw.Request.SetBodyString(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s%s}}`, tool, encoded, meta))
					}
					if !actor.TrustedLocal {
						raw.Request.Header.Set("Authorization", "Bearer "+createJWT(actor.UserID, actor.UserID+"@test.invalid", cfg.JWTSecret))
					}
					raw.Request.Header.Set("X-Tenant-Id", actor.TenantID)
					// Actual route/middleware dispatch retains the completed stream
					// without an HTTP client draining it automatically.
					app.Handler()(raw)
					stream, ok := raw.Response.BodyStream().(io.ReadCloser)
					if !ok {
						t.Fatalf("transport historical=%v status=%d lacks completed body fence: %s", historical, raw.Response.StatusCode(), raw.Response.Body())
					}
					if cfg.DB != nil && cfg.DB.Stats().InUse != 1 {
						_ = stream.Close()
						t.Fatal("completed response lacks retained SQL authority")
					}
					prefix := make([]byte, 8)
					n, readErr := stream.Read(prefix)
					rest, drainErr := io.ReadAll(stream)
					body := append(prefix[:n], rest...)
					if readErr != nil || n == 0 || drainErr != nil {
						_ = stream.Close()
						t.Fatalf("retained body read n=%d first=%v drain=%v body=%s", n, readErr, drainErr, body)
					}
					if cfg.DB != nil && cfg.DB.Stats().InUse != 1 {
						_ = stream.Close()
						t.Fatal("body drain released SQL before actual Close")
					}
					closeErr := stream.Close()
					secondCloseErr := stream.Close()
					if closeErr != nil || secondCloseErr != nil || (cfg.DB != nil && cfg.DB.Stats().InUse != 0) {
						t.Fatalf("actual Close leaked SQL: %v %v", closeErr, secondCloseErr)
					}
					if path != "/workspace/search" {
						var envelope struct {
							Result *mcp.ToolResult `json:"result"`
							Error  any             `json:"error"`
						}
						if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error != nil || envelope.Result == nil || envelope.Result.IsError || len(envelope.Result.Content) == 0 {
							t.Fatalf("actual RPC response failed: %s err=%v", body, err)
						}
						body = []byte(envelope.Result.Content[0].Text)
					}
					if historical {
						assertHistorical(t, body)
					} else if bytes.Contains(body, []byte(old.ID)) || bytes.Contains(body, []byte(oldText)) {
						t.Fatalf("following generic request inherited historical eligibility: %s", body)
					}
				}
			})
		}
		for _, id := range []string{"historical-pending", "historical-foreign", devID} {
			if !cfg.Collections.HasRecord(req.Collection, id) {
				t.Fatalf("scoped read deleted physical control %s", id)
			}
		}
		// Historical admission is confined to the verified requested generation;
		// it must not change subsequent unscoped eligibility.
		general, err = filterMCPDocumentResults(ctx, cfg, []pipeline.ScoredResult{old})
		if err != nil || len(general) != 0 {
			t.Fatalf("historical lookup broadened generic eligibility: %d %v", len(general), err)
		}
		if cfg.DB != nil && cfg.DB.Stats().InUse != 0 {
			t.Fatal("historical scoped read leaked SQL lease")
		}
	})
}
