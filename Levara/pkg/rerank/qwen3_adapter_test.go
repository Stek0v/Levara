package rerank

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeChatServer replies with a /v1/chat/completions-shaped response
// whose logprob comes from scorer(query, doc). Lets us simulate "doc 3
// is the best match" without running an actual LLM.
func fakeChatServer(t *testing.T, scorer func(query, doc string) float64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		var req qwen3ChatReq
		_ = json.Unmarshal(body, &req)

		// Last user message holds the document text (Qwen3 format).
		var query, doc string
		for _, m := range req.Messages {
			if m.Role == "user" {
				// Structure: "<Instruct>: ...\n\n<Query>: Q\n\n<Document>: D"
				if idx := strings.Index(m.Content, "<Query>: "); idx >= 0 {
					rest := m.Content[idx+len("<Query>: "):]
					if dIdx := strings.Index(rest, "\n\n<Document>: "); dIdx >= 0 {
						query = rest[:dIdx]
						doc = rest[dIdx+len("\n\n<Document>: "):]
					}
				}
			}
		}
		score := scorer(query, doc)
		// Clamp score into (0, 1) and convert to logprob.
		if score <= 0 {
			score = 0.0001
		}
		if score >= 1 {
			score = 0.9999
		}
		logp := math.Log(score)
		notLogp := math.Log(1 - score)

		resp := map[string]any{
			"choices": []map[string]any{
				{
					"message": map[string]any{"content": "yes"},
					"logprobs": map[string]any{
						"content": []map[string]any{
							{
								"token":   "yes",
								"logprob": logp,
								"top_logprobs": []map[string]any{
									{"token": "yes", "logprob": logp},
									{"token": "no", "logprob": notLogp},
								},
							},
						},
					},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	return srv, &calls
}

// TestQwen3Client_OrdersByScore verifies the adapter calls the server
// per document, reads P("yes") from logprobs, and sorts by score desc.
func TestQwen3Client_OrdersByScore(t *testing.T) {
	// Doc index 1 should win (highest score).
	scores := map[string]float64{
		"A": 0.2,
		"B": 0.95,
		"C": 0.5,
	}
	srv, calls := fakeChatServer(t, func(_, doc string) float64 {
		return scores[doc]
	})
	defer srv.Close()

	q := NewQwen3Client(srv.URL, "qwen3-reranker-0.6b", 0, 2000, 2)
	results, err := q.Rerank(context.Background(), "what's B about?", []string{"A", "B", "C"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("len = %d, want 3", len(results))
	}
	if results[0].Index != 1 {
		t.Errorf("top result index = %d, want 1 (B)", results[0].Index)
	}
	if results[0].Score <= results[1].Score {
		t.Errorf("scores not descending: %+v", results)
	}
	if calls.Load() != 3 {
		t.Errorf("server calls = %d, want 3 (one per document)", calls.Load())
	}
}

// TopN truncation happens after ranking, not before — so the three
// documents all get scored, then only the top 2 are returned.
func TestQwen3Client_TopNTruncates(t *testing.T) {
	scores := map[string]float64{"A": 0.9, "B": 0.1, "C": 0.5}
	srv, _ := fakeChatServer(t, func(_, doc string) float64 { return scores[doc] })
	defer srv.Close()

	q := NewQwen3Client(srv.URL, "qwen3-reranker-0.6b", 2, 2000, 2)
	results, err := q.Rerank(context.Background(), "q", []string{"A", "B", "C"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	if results[0].Index != 0 || results[1].Index != 2 {
		t.Errorf("expected [A, C], got %+v", results)
	}
}

// Disabled client (empty URL) returns nil, nil — matches Client.Rerank.
func TestQwen3Client_DisabledNoop(t *testing.T) {
	q := NewQwen3Client("", "", 5, 0, 0)
	if q.Enabled() {
		t.Error("Enabled() should be false with empty endpoint")
	}
	results, err := q.Rerank(context.Background(), "q", []string{"doc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil results, got %+v", results)
	}
}

// When every pair errors (bad endpoint), Rerank surfaces the first
// error instead of silently returning a zero-scored ordering.
func TestQwen3Client_AllErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "always fails", http.StatusInternalServerError)
	}))
	defer srv.Close()

	q := NewQwen3Client(srv.URL, "qwen3-reranker-0.6b", 0, 2000, 2)
	_, err := q.Rerank(context.Background(), "q", []string{"A", "B"})
	if err == nil {
		t.Fatal("expected error when every pair fails")
	}
}

// HTTP front — POST Cohere-shaped body, get Cohere-shaped response.
// This is what Levara's pkg/rerank.Client will speak to.
func TestQwenRerankHTTPHandler_Roundtrip(t *testing.T) {
	chatSrv, _ := fakeChatServer(t, func(_, doc string) float64 {
		// Very simple: longer doc = more relevant, ceiling at 0.95.
		s := float64(len(doc)) / 20.0
		if s > 0.95 {
			s = 0.95
		}
		return s
	})
	defer chatSrv.Close()

	q := NewQwen3Client(chatSrv.URL, "qwen3-reranker-0.6b", 0, 2000, 2)
	front := httptest.NewServer(QwenRerankHTTPHandler(q))
	defer front.Close()

	// Client-side: call exactly like Levara's pkg/rerank.Client would.
	c := NewClient(front.URL, "qwen3-reranker-0.6b", 0, 5000)
	results, err := c.Rerank(context.Background(), "anything",
		[]string{"short", "this document is quite a bit longer"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2", len(results))
	}
	// Longer doc (index 1) must win.
	if results[0].Index != 1 {
		t.Errorf("top index = %d, want 1 (longer doc)", results[0].Index)
	}
}
