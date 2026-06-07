package http

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
)

const (
	workspaceEvalProject    = "workspace-eval"
	workspaceEvalBranch     = "main"
	workspaceEvalGeneration = "eval-gen-1"
	workspaceEvalCollection = "workspace_eval_gen_1"
)

type workspaceEvalQuery struct {
	ID              string   `json:"id"`
	Query           string   `json:"query"`
	ExpectedPath    string   `json:"expected_path"`
	ExpectedHeading string   `json:"expected_heading"`
	Types           []string `json:"types"`
}

type workspaceEvalMetrics struct {
	Queries   int
	RecallAt1 float64
	RecallAt5 float64
	MRR       float64
}

func TestWorkspaceRetrievalQualityEval(t *testing.T) {
	root := workspaceEvalRoot(t)
	queries := loadWorkspaceEvalQueries(t, root)
	cfg, closeFn := newWorkspaceEvalConfig(t)
	defer closeFn()
	h := &mcpHandler{cfg: cfg}

	indexWorkspaceEvalCorpus(t, h, filepath.Join(root, "corpus"))

	ranksByType := map[string][]int{}
	for _, q := range queries {
		for _, searchType := range q.Types {
			resp := workspaceEvalSearch(t, h, q, searchType)
			results, _ := resp["results"].([]any)
			if q.ExpectedPath == "" {
				if len(results) != 0 {
					t.Fatalf("%s/%s: expected no hits, got %d: %+v", q.ID, searchType, len(results), results)
				}
				continue
			}

			rank, headingOK := workspaceEvalExpectedRank(results, q.ExpectedPath, q.ExpectedHeading)
			ranksByType[searchType] = append(ranksByType[searchType], rank)
			if rank == 0 {
				t.Fatalf("%s/%s: expected path %q not found in top results: %+v", q.ID, searchType, q.ExpectedPath, results)
			}
			if !headingOK {
				t.Fatalf("%s/%s: expected heading %q for path %q not found in matching result: %+v",
					q.ID, searchType, q.ExpectedHeading, q.ExpectedPath, results[rank-1])
			}
		}
	}

	thresholds := map[string]workspaceEvalMetrics{
		"CHUNKS_LEXICAL": {RecallAt1: 0.80, RecallAt5: 1.00, MRR: 0.85},
		"CHUNKS":         {RecallAt1: 0.80, RecallAt5: 1.00, MRR: 0.85},
		"HYBRID":         {RecallAt1: 0.80, RecallAt5: 1.00, MRR: 0.85},
	}
	for searchType, ranks := range ranksByType {
		got := workspaceEvalMetricsFor(ranks)
		want := thresholds[searchType]
		if got.RecallAt1 < want.RecallAt1 || got.RecallAt5 < want.RecallAt5 || got.MRR < want.MRR {
			t.Fatalf("%s metrics below threshold: got %+v want minimum %+v ranks=%v", searchType, got, want, ranks)
		}
		t.Logf("%s metrics: queries=%d recall@1=%.2f recall@5=%.2f mrr=%.2f ranks=%v",
			searchType, got.Queries, got.RecallAt1, got.RecallAt5, got.MRR, ranks)
	}
}

func TestWorkspaceEvalMetricsEmpty(t *testing.T) {
	got := workspaceEvalMetricsFor(nil)
	if got.Queries != 0 || got.RecallAt1 != 0 || got.RecallAt5 != 0 || got.MRR != 0 {
		t.Fatalf("empty metrics=%+v, want all zero", got)
	}
}

func newWorkspaceEvalConfig(t *testing.T) (APIConfig, func()) {
	t.Helper()
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(8, filepath.Join(dir, "vectors"))
	if err != nil {
		t.Fatal(err)
	}
	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		resp := struct {
			Data []item `json:"data"`
		}{Data: make([]item, len(req.Input))}
		for i, text := range req.Input {
			resp.Data[i] = item{Index: i, Embedding: workspaceEvalVector(text)}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	cfg := APIConfig{
		WorkspacePath: filepath.Join(dir, "workspace"),
		EmbedEndpoint: embedSrv.URL,
		EmbedModel:    "workspace-eval-embed",
		EmbedClient:   embed.NewClient(embedSrv.URL, "workspace-eval-embed", 16, 1),
		Collections:   cm,
		BM25Indexes:   map[string]*bm25.Index{},
	}
	return cfg, func() {
		embedSrv.Close()
		_ = cm.Close()
	}
}

func workspaceEvalRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		candidate := filepath.Join(dir, "testdata", "workspace-eval")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("testdata/workspace-eval not found")
		}
		dir = parent
	}
}

func loadWorkspaceEvalQueries(t *testing.T, root string) []workspaceEvalQuery {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "queries.json"))
	if err != nil {
		t.Fatal(err)
	}
	var queries []workspaceEvalQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		t.Fatal(err)
	}
	if len(queries) == 0 {
		t.Fatal("workspace eval has no queries")
	}
	return queries
}

func indexWorkspaceEvalCorpus(t *testing.T, h *mcpHandler, corpusRoot string) {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(corpusRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(corpusRoot, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatal("workspace eval corpus has no markdown files")
	}
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(corpusRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		res := h.executeToolInner(context.Background(), nil, "workspace_write", map[string]any{
			"project_id":          workspaceEvalProject,
			"branch":              workspaceEvalBranch,
			"generation":          workspaceEvalGeneration,
			"collection":          workspaceEvalCollection,
			"path":                rel,
			"text":                string(data),
			"chunk_strategy":      "paragraph",
			"min_chunk_chars":     1,
			"activate_generation": true,
		})
		if res.IsError {
			t.Fatalf("index %s failed: %+v", rel, res.Content)
		}
	}
}

func workspaceEvalSearch(t *testing.T, h *mcpHandler, q workspaceEvalQuery, searchType string) map[string]any {
	t.Helper()
	res := h.executeToolInner(context.Background(), nil, "workspace_search", map[string]any{
		"project_id":   workspaceEvalProject,
		"branch":       workspaceEvalBranch,
		"search_query": q.Query,
		"search_type":  searchType,
		"top_k":        5,
	})
	if res.IsError {
		t.Fatalf("%s/%s workspace_search failed: %+v", q.ID, searchType, res.Content)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &resp); err != nil {
		t.Fatalf("%s/%s response is not JSON: %v\n%s", q.ID, searchType, err, res.Content[0].Text)
	}
	freshness, _ := resp["freshness"].(map[string]any)
	if freshness["stale"] != false {
		t.Fatalf("%s/%s freshness stale: %+v", q.ID, searchType, freshness)
	}
	return resp
}

func workspaceEvalExpectedRank(results []any, expectedPath, expectedHeading string) (int, bool) {
	rank := 0
	headingOK := expectedHeading == ""
	for i, item := range results {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if m["path"] != expectedPath {
			continue
		}
		if rank == 0 {
			rank = i + 1
		}
		if expectedHeading != "" && workspaceEvalHeadingContains(m["heading_path"], expectedHeading) {
			headingOK = true
		}
	}
	return rank, headingOK
}

func workspaceEvalHeadingContains(raw any, expected string) bool {
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if item == expected {
			return true
		}
	}
	return false
}

func workspaceEvalMetricsFor(ranks []int) workspaceEvalMetrics {
	var out workspaceEvalMetrics
	out.Queries = len(ranks)
	if len(ranks) == 0 {
		return out
	}
	for _, rank := range ranks {
		if rank == 1 {
			out.RecallAt1++
		}
		if rank > 0 && rank <= 5 {
			out.RecallAt5++
			out.MRR += 1 / float64(rank)
		}
	}
	out.RecallAt1 /= float64(len(ranks))
	out.RecallAt5 /= float64(len(ranks))
	out.MRR /= float64(len(ranks))
	out.RecallAt1 = math.Round(out.RecallAt1*100) / 100
	out.RecallAt5 = math.Round(out.RecallAt5*100) / 100
	out.MRR = math.Round(out.MRR*100) / 100
	return out
}

func workspaceEvalVector(text string) []float32 {
	lower := strings.ToLower(text)
	vec := make([]float32, 8)
	addTopic := func(dim int, weight float32, terms ...string) {
		for _, term := range terms {
			if strings.Contains(lower, term) {
				vec[dim] += weight
			}
		}
	}
	addTopic(0, 1.0, "payment", "checkout", "authorization", "bank", "timeout", "gateway", "idempotency")
	addTopic(0, 0.7, "retry", "customer-facing", "waits too long", "slow deploys")
	addTopic(1, 1.0, "auth", "jwt", "jwks", "token", "login", "session", "signing key", "cookies")
	addTopic(2, 1.0, "canary", "deploy", "deployment", "feature flag", "release", "rollback", "traffic")
	addTopic(2, 0.5, "elevated errors", "stable")
	addTopic(3, 1.0, "postgres", "database", "schema", "migration", "nullable", "backfill", "orders")
	addTopic(4, 1.0, "openapi", "api", "endpoint", "invoices", "refunds", "contract", "sdk", "version")
	addTopic(5, 1.0, "search", "ranking", "worker pool", "queue", "saturation")
	addTopic(6, 0.2, "incident", "runbook", "playbook", "guide")
	if bytes.Contains([]byte(lower), []byte("неизвестный")) {
		vec[7] = 1
	}
	if allZero(vec) {
		vec[7] = 1
	}
	return vec
}

func allZero(vec []float32) bool {
	for _, v := range vec {
		if v != 0 {
			return false
		}
	}
	return true
}
