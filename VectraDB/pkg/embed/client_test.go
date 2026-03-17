package embed

import (
	"context"
	"net/http"
	"testing"
)

const (
	embedURL   = "http://localhost:9001/v1/embeddings"
	embedModel = "pplx-embed-context-v1-0.6b"
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

func TestEmbedSingle(t *testing.T) {
	if !isEmbedServerAvailable() {
		t.Skip("embed-server not available at localhost:9001")
	}

	client := NewClient(embedURL, embedModel, 16)
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
	if !isEmbedServerAvailable() {
		t.Skip("embed-server not available at localhost:9001")
	}

	client := NewClient(embedURL, embedModel, 16)
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
	if !isEmbedServerAvailable() {
		t.Skip("embed-server not available at localhost:9001")
	}

	client := NewClient(embedURL, embedModel, 16)
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
	client := NewClient(embedURL, embedModel, 16)
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

	client := NewClient(embedURL, embedModel, 16)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client.EmbedSingle(ctx, "тестовый текст для бенчмарка эмбеддинга")
	}
}
