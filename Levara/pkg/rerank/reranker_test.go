package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRerank_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// Return documents in reverse order of index (simulate reranking)
		results := make([]rerankResultItem, len(req.Documents))
		for i := range req.Documents {
			score := float64(len(req.Documents)-i) / float64(len(req.Documents))
			results[i] = rerankResultItem{Index: i, RelevanceScore: &score}
		}
		json.NewEncoder(w).Encode(rerankResponse{Results: results})
	}))
	defer server.Close()

	c := NewClient(server.URL, "test-model", 0, 5000)
	results, err := c.Rerank(context.Background(), "test query", []string{"doc A", "doc B", "doc C"})
	if err != nil {
		t.Fatalf("Rerank error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	// First result should have highest score (index 0, score 1.0)
	if results[0].Index != 0 {
		t.Errorf("Expected index 0 first (highest score), got index %d", results[0].Index)
	}
	// Last result should have lowest score
	if results[2].Index != 2 {
		t.Errorf("Expected index 2 last (lowest score), got index %d", results[2].Index)
	}
}

func TestRerank_EmptyEndpoint(t *testing.T) {
	c := NewClient("", "model", 10, 5000)
	if c.Enabled() {
		t.Error("Empty URL should not be enabled")
	}
	results, err := c.Rerank(context.Background(), "query", []string{"doc"})
	if err != nil {
		t.Fatalf("Expected no error for no-op, got: %v", err)
	}
	if results != nil {
		t.Errorf("Expected nil results for no-op, got: %v", results)
	}
}

func TestRerank_NilClient(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("Nil client should not be enabled")
	}
	results, err := c.Rerank(context.Background(), "query", []string{"doc"})
	if err != nil {
		t.Fatalf("Expected no error for nil client, got: %v", err)
	}
	if results != nil {
		t.Errorf("Expected nil results for nil client, got: %v", results)
	}
}

func TestRerank_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond) // Exceed client timeout
		json.NewEncoder(w).Encode(rerankResponse{})
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 50) // 50ms timeout
	_, err := c.Rerank(context.Background(), "query", []string{"doc A"})
	if err == nil {
		t.Error("Expected timeout error")
	}
}

func TestRerank_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", 500)
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 5000)
	_, err := c.Rerank(context.Background(), "query", []string{"doc"})
	if err == nil {
		t.Error("Expected error on 500")
	}
}

func TestRerank_EmptyDocuments(t *testing.T) {
	c := NewClient("http://localhost:1234", "model", 10, 5000)
	results, err := c.Rerank(context.Background(), "query", []string{})
	if err != nil {
		t.Fatalf("Expected no error for empty docs, got: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected empty results, got %d", len(results))
	}
}

func TestRerank_SingleDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		score := 0.99
		json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResultItem{{Index: 0, RelevanceScore: &score}},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 5000)
	results, err := c.Rerank(context.Background(), "query", []string{"single doc"})
	if err != nil {
		t.Fatalf("Rerank error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].Index != 0 {
		t.Errorf("Expected index 0, got %d", results[0].Index)
	}
}

func TestRerank_CohereFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s1, s2 := 0.95, 0.30
		json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResultItem{
				{Index: 0, RelevanceScore: &s1},
				{Index: 1, RelevanceScore: &s2},
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 5000)
	results, err := c.Rerank(context.Background(), "query", []string{"relevant", "irrelevant"})
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	if results[0].Score < results[1].Score {
		t.Errorf("Scores not sorted: %f < %f", results[0].Score, results[1].Score)
	}
}

func TestRerank_AlternativeScoreField(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Use "score" instead of "relevance_score" (non-Cohere format)
		s1, s2 := 0.90, 0.10
		json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResultItem{
				{Index: 0, Score: &s1},
				{Index: 1, Score: &s2},
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 5000)
	results, err := c.Rerank(context.Background(), "query", []string{"good", "bad"})
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	if results[0].Score != 0.90 {
		t.Errorf("Expected 0.90, got %f", results[0].Score)
	}
}

func TestRerank_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 10, 5000)
	_, err := c.Rerank(context.Background(), "query", []string{"doc"})
	if err == nil {
		t.Error("Expected error on malformed JSON")
	}
}

func TestRerank_TopNRespected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rerankRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.TopN != 2 {
			t.Errorf("Expected top_n=2 in request, got %d", req.TopN)
		}
		s1, s2 := 0.9, 0.5
		json.NewEncoder(w).Encode(rerankResponse{
			Results: []rerankResultItem{
				{Index: 0, RelevanceScore: &s1},
				{Index: 2, RelevanceScore: &s2},
			},
		})
	}))
	defer server.Close()

	c := NewClient(server.URL, "model", 2, 5000)
	results, err := c.Rerank(context.Background(), "query", []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("Error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results (top_n=2), got %d", len(results))
	}
}
