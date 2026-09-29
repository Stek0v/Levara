package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
)

func randomVec(dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = rand.Float32()*2 - 1
	}
	return v
}

func setupTestPipeline(t *testing.T, dim int) (*SearchPipeline, *store.CollectionManager, func()) {
	t.Helper()

	dir, _ := os.MkdirTemp("", "levara-pipeline-test-*")
	cm, err := store.NewCollectionManager(dim, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}

	embedClient := embed.NewClient(
		"http://localhost:9001/v1/embeddings",
		"pplx-embed-context-v1-0.6b",
		16,
		1,
	)

	pipeline := NewSearchPipeline(embedClient, cm, nil)

	cleanup := func() {
		cm.Close()
		os.RemoveAll(dir)
	}

	return pipeline, cm, cleanup
}

func TestSearchByVector(t *testing.T) {
	dim := 64
	p, cm, cleanup := setupTestPipeline(t, dim)
	defer cleanup()

	// Insert test data
	cm.Create("test")
	targetVec := randomVec(dim)
	cm.Insert("test", "target-1", targetVec, map[string]any{"text": "target document"})

	for i := 0; i < 99; i++ {
		cm.Insert("test", fmt.Sprintf("noise-%d", i), randomVec(dim),
			map[string]any{"text": fmt.Sprintf("noise %d", i)})
	}

	// Wait for HNSW indexer
	time.Sleep(200 * time.Millisecond)

	// Search with the same vector — should find target-1 as top result
	results, err := p.SearchByVector("test", targetVec, 10)
	if err != nil {
		t.Fatalf("SearchByVector: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("No results returned")
	}

	if results[0].ID != "target-1" {
		t.Errorf("Top result: got %q, want target-1", results[0].ID)
	}

	if results[0].Score < 0.9 {
		t.Errorf("Top result score: got %.4f, want >= 0.9", results[0].Score)
	}

	t.Logf("SearchByVector: top=%s score=%.4f, %d results", results[0].ID, results[0].Score, len(results))
}

func TestSearchCollectionIsolation(t *testing.T) {
	dim := 64
	p, cm, cleanup := setupTestPipeline(t, dim)
	defer cleanup()

	cm.Create("books")
	cm.Create("movies")

	bookVec := randomVec(dim)
	movieVec := randomVec(dim)

	cm.Insert("books", "book-1", bookVec, map[string]any{"title": "Go Book"})
	cm.Insert("movies", "movie-1", movieVec, map[string]any{"title": "Go Movie"})

	time.Sleep(100 * time.Millisecond)

	// Search books — should NOT return movie
	bookResults, err := p.SearchByVector("books", bookVec, 10)
	if err != nil {
		t.Fatalf("Search books: %v", err)
	}

	for _, r := range bookResults {
		if r.ID == "movie-1" {
			t.Fatal("Cross-collection leakage: movie found in books")
		}
	}
}

func TestSearchNonExistentCollection(t *testing.T) {
	dim := 64
	p, _, cleanup := setupTestPipeline(t, dim)
	defer cleanup()

	_, err := p.SearchByVector("nonexistent", randomVec(dim), 10)
	if err == nil {
		t.Fatal("Expected error for non-existent collection")
	}
}

func TestSearchByTextRejectsQueryEmbeddingContractMismatch(t *testing.T) {
	dim := 64
	p, cm, cleanup := setupTestPipeline(t, dim)
	defer cleanup()

	indexContract := embcontract.Contract{Encoder: "index-encoder", Tokenizer: "tok-a", Pooling: "mean", Normalization: "l2", Dim: dim, Metric: "cosine"}
	cm.SetDefaultEmbeddingContract(indexContract)
	if err := cm.CreateWithDim("docs", dim, "index-encoder", "cosine"); err != nil {
		t.Fatalf("CreateWithDim: %v", err)
	}

	err := p.validateQueryContract("docs", dim)
	if !errors.Is(err, store.ErrEmbeddingContractMismatch) {
		t.Fatalf("validateQueryContract error=%v, want ErrEmbeddingContractMismatch", err)
	}
}

func TestSearchPipelineUsesQueryAliasWithCanonicalContract(t *testing.T) {
	models := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		models <- req.Model
		vectors := make([][]float32, len(req.Input))
		for i := range vectors {
			vectors[i] = []float32{1, 0, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
	}))
	defer srv.Close()
	cm, err := store.NewCollectionManager(3, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	cm.SetDefaultEmbeddingContract(embcontract.FromEnv("encoder", 3, "cosine"))
	if err := cm.CreateWithDim("docs", 3, "encoder", "cosine"); err != nil {
		t.Fatal(err)
	}
	if err := cm.Insert("docs", "answer", []float32{1, 0, 0}, map[string]any{"text": "answer"}); err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		documentClient := embed.NewClient(srv.URL, "encoder", 16, 1)
		wantModel := "encoder"
		if enabled {
			documentClient = documentClient.WithQueryAlias()
			wantModel += ":query"
		}
		p := NewSearchPipeline(documentClient, cm, nil)
		results, err := p.SearchByText(context.Background(), "docs", "question", 1)
		if err != nil || len(results) != 1 || results[0].ID != "answer" {
			t.Fatalf("single search alias=%v: results=%v error=%v", enabled, results, err)
		}
		batch, err := p.BatchSearchByText(context.Background(), "docs", []string{"q1", "q2"}, 1)
		if err != nil || len(batch) != 2 || len(batch[0]) != 1 || len(batch[1]) != 1 {
			t.Fatalf("batch search alias=%v: results=%v error=%v", enabled, batch, err)
		}
		for i := 0; i < 2; i++ {
			if got := <-models; got != wantModel {
				t.Fatalf("alias=%v: sent %q, want %q", enabled, got, wantModel)
			}
		}
		if _, err := documentClient.EmbedSingle(context.Background(), "document"); err != nil {
			t.Fatal(err)
		}
		if got := <-models; got != "encoder" || documentClient.Model() != "encoder" {
			t.Fatalf("document client changed: sent %q, canonical %q", got, documentClient.Model())
		}
	}
	wrongEncoder := embed.NewClient(srv.URL, "different-encoder", 16, 1).WithQueryAlias()
	p := NewSearchPipeline(wrongEncoder, cm, nil)
	if err := p.validateQueryContract("docs", 3); !errors.Is(err, store.ErrEmbeddingContractMismatch) {
		t.Fatalf("different encoder bypassed contract validation: %v", err)
	}
}

func BenchmarkSearchByVector(b *testing.B) {
	dim := 64
	dir, _ := os.MkdirTemp("", "levara-bench-pipeline-*")
	defer os.RemoveAll(dir)

	cm, _ := store.NewCollectionManager(dim, dir)
	defer cm.Close()

	embedClient := embed.NewClient("http://localhost:9001/v1/embeddings", "test", 16, 1)
	p := NewSearchPipeline(embedClient, cm, nil)

	// Insert 500 vectors
	cm.Create("bench")
	for i := 0; i < 500; i++ {
		cm.Insert("bench", fmt.Sprintf("v-%d", i), randomVec(dim),
			map[string]any{"i": i})
	}
	time.Sleep(500 * time.Millisecond)

	queryVec := randomVec(dim)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p.SearchByVector("bench", queryVec, 10)
	}
}
