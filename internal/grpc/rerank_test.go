package grpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stek0v/levara/internal/metrics"
	pb "github.com/stek0v/levara/proto/pb"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// embedStub answers OpenAI-compatible /v1/embeddings with a fixed vector.
func newEmbedStub(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		vec := make([]float32, dim)
		vec[0] = 1
		data := make([]map[string]any, len(req.Input))
		for i := range req.Input {
			data[i] = map[string]any{"embedding": vec, "index": i}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "model": "stub"})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestGRPC_SearchByText_Rerank verifies that supplying rerank_endpoint
// invokes the sidecar and bumps the ok outcome counter.
func TestGRPC_SearchByText_Rerank(t *testing.T) {
	const dim = 4
	client, cleanup := startTestServer(t, dim)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "c"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	vec := []float32{1, 0, 0, 0}
	for _, id := range []string{"a", "b"} {
		md, _ := json.Marshal(map[string]any{"text": id})
		if _, err := client.Insert(ctx, &pb.InsertReq{
			Collection: "c", Id: id, Vector: vec, MetadataJson: string(md),
		}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}

	embed := newEmbedStub(t, dim)

	hit := false
	rerank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`))
	}))
	defer rerank.Close()

	okBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))

	resp, err := client.SearchByText(ctx, &pb.SearchByTextReq{
		Collection:     "c",
		QueryText:      "x",
		TopK:           5,
		EmbedEndpoint:  embed.URL + "/v1/embeddings",
		EmbedModel:     "stub",
		RerankEndpoint: rerank.URL,
		RerankModel:    "test",
		RerankBudgetMs: 5000,
	})
	if err != nil {
		t.Fatalf("SearchByText: %v", err)
	}
	if !hit {
		t.Fatal("rerank sidecar not called")
	}
	if okAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok")); okAfter <= okBefore {
		t.Fatalf("ok counter did not increment: before=%v after=%v", okBefore, okAfter)
	}
	if len(resp.Results) == 0 {
		t.Fatal("empty results")
	}
}

func TestGRPC_HybridSearch_Rerank(t *testing.T) {
	const dim = 4
	client, cleanup := startTestServer(t, dim)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.CreateCollection(ctx, &pb.CreateCollectionReq{Name: "h"}); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	vec := []float32{1, 0, 0, 0}
	for _, id := range []string{"a", "b"} {
		md, _ := json.Marshal(map[string]any{"text": id + " content"})
		if _, err := client.Insert(ctx, &pb.InsertReq{
			Collection: "h", Id: id, Vector: vec, MetadataJson: string(md),
		}); err != nil {
			t.Fatalf("Insert %s: %v", id, err)
		}
	}
	embed := newEmbedStub(t, dim)

	hit := false
	rerank := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.9},{"index":0,"relevance_score":0.1}]}`))
	}))
	defer rerank.Close()

	okBefore := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok"))

	resp, err := client.HybridSearch(ctx, &pb.HybridSearchReq{
		Collection:     "h",
		QueryText:      "content",
		TopK:           5,
		EmbedEndpoint:  embed.URL + "/v1/embeddings",
		EmbedModel:     "stub",
		VectorWeight:   1.0,
		Bm25Weight:     1.0,
		RerankEndpoint: rerank.URL,
		RerankModel:    "test",
		RerankBudgetMs: 5000,
	})
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if !hit {
		t.Fatal("rerank sidecar not called for HybridSearch")
	}
	if okAfter := testutil.ToFloat64(metrics.RerankInvocations.WithLabelValues("ok")); okAfter <= okBefore {
		t.Fatalf("ok counter did not increment: before=%v after=%v", okBefore, okAfter)
	}
	if len(resp.Results) == 0 {
		t.Fatal("empty results")
	}
}
