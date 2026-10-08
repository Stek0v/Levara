package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
)

func TestT10SingleAndBatchComposeOperationFilterWithACL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		vectors := make([][]float32, len(request.Input))
		for i := range vectors {
			vectors[i] = []float32{1, 0, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
	}))
	defer server.Close()
	cm, err := store.NewCollectionManager(3, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("docs", 3, "encoder", "cosine"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"denied", "wrong-room", "wanted"} {
		if err := cm.Insert("docs", id, []float32{1, 0, 0}, map[string]any{"text": id}); err != nil {
			t.Fatal(err)
		}
	}
	p := NewSearchPipeline(embed.NewClient(server.URL, "encoder", 16, 1), cm, nil).WithResultFilter(func(ctx context.Context, results []ScoredResult) ([]ScoredResult, error) {
		var allowed []ScoredResult
		for _, result := range results {
			if result.ID != "denied" {
				allowed = append(allowed, result)
			}
		}
		return allowed, nil
	})
	ctx := WithSearchResultFilter(context.Background(), func(ctx context.Context, collection string, results []ScoredResult) ([]ScoredResult, error) {
		if collection != "docs" {
			t.Fatalf("wrong collection %q", collection)
		}
		var matching []ScoredResult
		for _, result := range results {
			if result.ID == "denied" {
				t.Fatal("operation filter ran before ACL")
			}
			if result.ID == "wanted" {
				matching = append(matching, result)
			}
		}
		return matching, nil
	})
	t.Run("single", func(t *testing.T) {
		single, err := p.SearchByText(ctx, "docs", "query", 3)
		if err != nil || len(single) != 1 || single[0].ID != "wanted" {
			t.Fatalf("single results=%v error=%v", single, err)
		}
	})
	t.Run("batch", func(t *testing.T) {
		batch, err := p.BatchSearchByText(ctx, "docs", []string{"one", "two"}, 3)
		if err != nil || len(batch) != 2 {
			t.Fatalf("batch=%v error=%v", batch, err)
		}
		for _, results := range batch {
			if len(results) != 1 || results[0].ID != "wanted" {
				t.Fatalf("unfiltered batch=%v", batch)
			}
		}
	})
	t.Run("operation-local", func(t *testing.T) {
		unscoped, err := p.SearchByText(context.Background(), "docs", "other", 3)
		if err != nil || len(unscoped) != 2 {
			t.Fatalf("operation filter escaped request: %v error=%v", unscoped, err)
		}
	})
}
