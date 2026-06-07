package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	embedModel  = "pplx-embed-context-v1-0.6b"
	expectedDim = 1024
)

func isEmbedServerAvailable() bool {
	resp, err := http.Get("http://localhost:9001/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func fakeEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp := embeddingResponse{}
		for i, text := range req.Input {
			vec := make([]float32, expectedDim)
			seed := float32(len(text) + i + 1)
			for j := range vec {
				vec[j] = seed + float32(j)/1000
			}
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: vec})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestEmbedSingle(t *testing.T) {
	srv := fakeEmbedServer(t)
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/embeddings", embedModel, 16, 1)
	ctx := context.Background()

	vec, err := client.EmbedSingle(ctx, "тестовый текст для эмбеддинга")
	if err != nil {
		t.Fatalf("EmbedSingle: %v", err)
	}

	if len(vec) != expectedDim {
		t.Fatalf("Expected dim=%d, got %d", expectedDim, len(vec))
	}

	// Sanity: vector should not be all zeros
	allZero := true
	for _, v := range vec {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("Vector is all zeros")
	}

	t.Logf("EmbedSingle OK: dim=%d, first 3 values: [%.4f, %.4f, %.4f]",
		len(vec), vec[0], vec[1], vec[2])
}

func TestEmbedBatch(t *testing.T) {
	srv := fakeEmbedServer(t)
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/embeddings", embedModel, 16, 1)
	ctx := context.Background()

	texts := []string{
		"Первый текст для эмбеддинга",
		"Второй текст, совершенно другой",
		"Третий текст — ещё один вариант",
		"Fourth text in English for testing",
		"Пятый текст с кириллицей и цифрами 123",
	}

	vecs, err := client.EmbedTexts(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedTexts: %v", err)
	}

	if len(vecs) != len(texts) {
		t.Fatalf("Expected %d vectors, got %d", len(texts), len(vecs))
	}

	for i, v := range vecs {
		if len(v) != expectedDim {
			t.Errorf("Vector %d: expected dim=%d, got %d", i, expectedDim, len(v))
		}
	}

	t.Logf("EmbedBatch OK: %d texts → %d vectors, dim=%d", len(texts), len(vecs), expectedDim)
}

func TestEmbedLargeBatch(t *testing.T) {
	srv := fakeEmbedServer(t)
	defer srv.Close()

	client := NewClient(srv.URL+"/v1/embeddings", embedModel, 16, 1)
	ctx := context.Background()

	// 50 texts → split into 4 batches of 16+16+16+2
	texts := make([]string, 50)
	for i := range texts {
		texts[i] = "Текст номер " + string(rune('а'+i%26))
	}

	vecs, err := client.EmbedTexts(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedTexts large batch: %v", err)
	}

	if len(vecs) != 50 {
		t.Fatalf("Expected 50 vectors, got %d", len(vecs))
	}

	t.Logf("EmbedLargeBatch OK: 50 texts in %d batches of %d", (50+15)/16, 16)
}

func TestEmbedEmpty(t *testing.T) {
	client := NewClient("http://localhost:9001/v1/embeddings", embedModel, 16, 1)
	ctx := context.Background()

	vecs, err := client.EmbedTexts(ctx, nil)
	if err != nil {
		t.Fatalf("EmbedTexts nil: %v", err)
	}
	if vecs != nil {
		t.Fatalf("Expected nil, got %d vectors", len(vecs))
	}

	vecs, err = client.EmbedTexts(ctx, []string{})
	if err != nil {
		t.Fatalf("EmbedTexts empty: %v", err)
	}
	if vecs != nil {
		t.Fatalf("Expected nil, got %d vectors", len(vecs))
	}
}

func BenchmarkEmbedSingle(b *testing.B) {
	if !isEmbedServerAvailable() {
		b.Skip("embed-server not available")
	}

	client := NewClient("http://localhost:9001/v1/embeddings", embedModel, 16, 1)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.EmbedSingle(ctx, "тестовый текст для бенчмарка эмбеддинга")
	}
}

func TestWithTimeoutOverridesDefault(t *testing.T) {
	c := NewClient("http://localhost:9001/v1/embeddings", embedModel, 16, 1)
	if c.httpClient.Timeout != 30*time.Second {
		t.Fatalf("default timeout = %v, want 30s", c.httpClient.Timeout)
	}
	if got := c.WithTimeout(5 * time.Minute); got != c {
		t.Errorf("WithTimeout should return the same client for chaining")
	}
	if c.httpClient.Timeout != 5*time.Minute {
		t.Errorf("timeout = %v, want 5m after WithTimeout", c.httpClient.Timeout)
	}
	// Non-positive duration must leave the timeout unchanged.
	c.WithTimeout(0)
	if c.httpClient.Timeout != 5*time.Minute {
		t.Errorf("timeout = %v, want unchanged 5m after WithTimeout(0)", c.httpClient.Timeout)
	}
}
