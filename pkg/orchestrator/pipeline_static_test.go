package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/graph"
)

func staticFixtureGraph() *ExtractedGraph {
	return &ExtractedGraph{
		Nodes: []graph.DedupNode{{ID: "file", Name: "main.go", Type: "module"}, {ID: "hello", Name: "main.go::Hello", Type: "function"}},
		Edges: []graph.DedupEdge{{SourceID: "file", TargetID: "hello", RelationshipName: "CALLS", EdgeText: "main.go calls Hello"}},
	}
}

func TestStaticPipelineBypassesAllModelExtraction(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unexpected model", 500) }))
	defer endpoint.Close()
	for _, empty := range []bool{false, true} {
		cfg := Config{StaticGraph: staticFixtureGraph(), LLMEndpoint: endpoint.URL, MinChunkChars: 1}
		if empty {
			cfg.StaticGraph = &ExtractedGraph{}
		}
		updates := make(chan Progress, 100)
		if err := Run(context.Background(), []string{"package main\n// 2026-01-01\nfunc Hello() {}"}, cfg, updates); err != nil {
			t.Fatal(err)
		}
		var final Progress
		for u := range updates {
			final = u
		}
		want := 2
		if empty {
			want = 0
		}
		if final.EntitiesExtracted != want || final.Stage != "complete" {
			t.Fatalf("unexpected static output %+v", final)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("static or empty graph invoked model")
	}
}

func TestStaticPipelineSourceScopedVectorAssertions(t *testing.T) {
	var embeds, models atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "embeddings") {
			models.Add(1)
			http.Error(w, "unexpected LLM", 500)
			return
		}
		embeds.Add(1)
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		rows := []any{}
		for i := range req.Input {
			rows = append(rows, map[string]any{"index": i, "embedding": []float32{1, 0}})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": rows})
	}))
	defer endpoint.Close()
	cm, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	cfg := Config{StaticGraph: staticFixtureGraph(), Collection: "code", Collections: cm, EmbedEndpoint: endpoint.URL + "/embeddings", LLMEndpoint: endpoint.URL,
		DatasetID: "dataset", DocumentID: "document", ContentRevision: 3, Generation: "generation", MinChunkChars: 1}
	updates := make(chan Progress, 100)
	if err := Run(context.Background(), []string{"package main\nfunc Hello() { /* long enough raw source fixture */ }"}, cfg, updates); err != nil {
		t.Fatal(err)
	}
	records, err := cm.Search("code", []float32{1, 0}, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, r := range records {
		var meta map[string]any
		if err := json.Unmarshal([]byte(r.Data), &meta); err != nil {
			t.Fatal(err)
		}
		if meta["dataset_id"] != "dataset" || meta["document_id"] != "document" || meta["generation"] != "generation" || meta["collection"] != "code" || meta["content_revision"] != float64(3) {
			t.Fatalf("missing source stamp: %s", r.Data)
		}
		if meta["name"] != nil {
			found++
			if r.ID != "file" && r.ID != "hello" && (r.ID == documentScopedID(cfg, "file") || r.ID == documentScopedID(cfg, "hello")) {
				continue
			}
			t.Fatalf("entity ID not scoped: %s", r.ID)
		}
	}
	if found != 2 || embeds.Load() == 0 || models.Load() != 0 {
		t.Fatalf("entities=%d embeds=%d model=%d", found, embeds.Load(), models.Load())
	}
	if cfg.StaticGraph.Nodes[0].ID != "file" {
		t.Fatal("pipeline mutated supplied static graph")
	}
}

func TestStaticPipelineFailuresAreExplicit(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "configured embedding failed", 400) }))
	defer endpoint.Close()
	cm, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	for _, tc := range []string{"embedding", "missing_collections", "guard", "dangling", "canceled"} {
		t.Run(tc, func(t *testing.T) {
			cfg := Config{StaticGraph: staticFixtureGraph(), Collection: "code", MinChunkChars: 1}
			ctx := context.Background()
			switch tc {
			case "embedding":
				cfg.EmbedEndpoint, cfg.Collections = endpoint.URL, cm
			case "missing_collections":
				cfg.EmbedEndpoint = endpoint.URL
			case "guard":
				cfg.CheckWrite = func(context.Context) error { return errors.New("source revoked") }
			case "dangling":
				cfg.StaticGraph.Edges[0].TargetID = "absent"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			updates := make(chan Progress, 100)
			if err := Run(ctx, []string{"package main\nfunc Hello() { /* static failure fixture */ }"}, cfg, updates); err == nil {
				t.Fatal("failed static pipeline reported success")
			}
		})
	}
}
