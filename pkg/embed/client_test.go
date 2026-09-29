package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync/atomic"
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

func TestEmbedRejectsIncompleteAndMisindexedResponses(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"data":[{"index":0,"embedding":[1]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"index":2,"embedding":[2]}]}`,
		`{"data":[{"index":0,"embedding":[1]},{"index":1,"embedding":[2]},{"index":2,"embedding":[3]}]}`,
		`{"embeddings":[[1],[]]}`, `{"embeddings":[[1],[1,2]]}`,
	} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer srv.Close()
			c := NewClient(srv.URL, "test", 0, 1)
			vectors, err := c.EmbedTexts(context.Background(), []string{"first", "second"})
			if err == nil || vectors != nil {
				t.Fatalf("invalid response accepted: %v %v", vectors, err)
			}
		})
	}
}

func TestQueryAliasPayloadAndImmutableCopies(t *testing.T) {
	requests := make(chan embeddingRequest, 12)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		requests <- req
		vectors := make([][]float32, len(req.Input))
		for i := range vectors {
			vectors[i] = []float32{float32(i + 1)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
	}))
	defer srv.Close()

	var guarded, released atomic.Int32
	base := NewClient(srv.URL, "encoder", 2, 1).
		WithPriorityGate(NewPriorityGate(2)).WithBackground().
		WithGuard(func(context.Context) (func(), error) {
			guarded.Add(1)
			return func() { released.Add(1) }, nil
		})
	alias := base.WithQueryAlias()
	query := alias.AsQuery()
	if base == alias || alias == query || base.queryAlias || base.query || alias.query {
		t.Fatal("query configuration mutated the document client")
	}
	if query.httpClient != base.httpClient || query.breaker != base.breaker ||
		query.gate != base.gate || !query.background || query.concurrency != base.concurrency {
		t.Fatal("query copy lost shared transport, breaker or QoS")
	}
	for _, tc := range []struct {
		name   string
		client *Client
		model  string
	}{
		{"default document", base, "encoder"},
		{"default query", base.AsQuery(), "encoder"},
		{"opt-in document", alias, "encoder"},
		{"opt-in query", query, "encoder:query"},
		{"idempotent query", query.AsQuery(), "encoder:query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.client.Model() != "encoder" {
				t.Fatalf("canonical model changed: %q", tc.client.Model())
			}
			if _, err := tc.client.EmbedSingle(context.Background(), "same text"); err != nil {
				t.Fatal(err)
			}
			if got := <-requests; got.Model != tc.model || !reflect.DeepEqual(got.Input, []string{"same text"}) {
				t.Fatalf("single request: %+v", got)
			}
		})
	}
	if _, err := query.EmbedTexts(context.Background(), []string{"first", "second", "third"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range [][]string{{"first", "second"}, {"third"}} {
		if got := <-requests; got.Model != "encoder:query" || !reflect.DeepEqual(got.Input, want) {
			t.Fatalf("batch request: %+v, want %v", got, want)
		}
	}
	if guarded.Load() != 7 || released.Load() != 7 {
		t.Fatalf("guard/release calls: %d/%d, want 7/7", guarded.Load(), released.Load())
	}
}

func TestQueryAliasFailureDoesNotFallBackToDocumentModel(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req embeddingRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		if req.Model != "encoder:query" {
			t.Errorf("unexpected fallback model %q", req.Model)
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1}}})
			return
		}
		http.Error(w, "unsupported query alias", http.StatusBadRequest)
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "encoder", 1, 1).WithQueryAlias().AsQuery()
	if _, err := client.EmbedSingle(context.Background(), "query"); err == nil {
		t.Fatal("unsupported alias must fail")
	}
	if calls.Load() != 1 {
		t.Fatalf("got %d requests, want one failed alias request", calls.Load())
	}
}

func TestEmbeddingCacheIsolatesQueryRoleAndProviderAcrossReload(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{float32(calls.Add(1))}}})
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "cache.jsonl")
	cache, err := NewPersistentCache(30, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cache.Close() }()
	text := "same\x00text\n\"query\""
	cache.Put(text, []float32{-1}) // legacy text-only entry must not be reused.
	base := NewClient(srv.URL, "encoder", 2, 1)
	clients := []*Client{
		base,
		base.AsQuery(),
		base.WithQueryAlias().AsQuery(),
		NewClient(srv.URL+"/other", "encoder", 2, 1),
		NewClient(srv.URL, "other-encoder", 2, 1),
		// Same wire model and role as the alias, different canonical model.
		NewClient(srv.URL, "encoder:query", 2, 1).AsQuery(),
	}
	for pass := 0; pass < 2; pass++ {
		for i, client := range clients {
			vec, err := client.WithCache(cache).EmbedSingle(context.Background(), text)
			if err != nil || !reflect.DeepEqual(vec, []float32{float32(i + 1)}) {
				t.Fatalf("pass %d client %d: vector=%v error=%v", pass, i, vec, err)
			}
		}
		if calls.Load() != int32(len(clients)) {
			t.Fatalf("pass %d: %d HTTP calls, want %d", pass, calls.Load(), len(clients))
		}
		if pass == 0 {
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
			cache, err = NewPersistentCache(30, path)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := base.EmbedSingle(context.Background(), text+"different"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != int32(len(clients)+1) {
		t.Fatal("different text reused a cached vector")
	}
	// A delimiter in one field cannot move data into an adjacent field.
	a := NewClient("endpoint\x00model", "encoder", 1, 1)
	b := NewClient("endpoint", "model\x00encoder", 1, 1)
	if a.cacheKey(text) == b.cacheKey(text) {
		t.Fatal("ambiguous cache namespace framing")
	}
}
