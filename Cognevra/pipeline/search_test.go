package pipeline

import (
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/rupamthxt/cognevra/internal/store"
	"github.com/rupamthxt/cognevra/pkg/embed"
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

	dir, _ := os.MkdirTemp("", "cognevra-pipeline-test-*")
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

	pipeline := NewSearchPipeline(embedClient, cm)

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

func BenchmarkSearchByVector(b *testing.B) {
	dim := 64
	dir, _ := os.MkdirTemp("", "cognevra-bench-pipeline-*")
	defer os.RemoveAll(dir)

	cm, _ := store.NewCollectionManager(dim, dir)
	defer cm.Close()

	embedClient := embed.NewClient("http://localhost:9001/v1/embeddings", "test", 16, 1)
	p := NewSearchPipeline(embedClient, cm)

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
