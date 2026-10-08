package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
)

func TestT10DualSearchCollectionEncoderContracts(t *testing.T) {
	var mu sync.Mutex
	var models []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		models = append(models, req.Model)
		mu.Unlock()
		vec := []float32{1, 0, 0}
		if req.Model == "encoder-b:query" {
			vec = []float32{0, 1, 0}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec}})
	}))
	defer srv.Close()
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(3, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := cm.CreateWithDim(name, 3, "encoder-"+name, "cosine"); err != nil {
			t.Fatal(err)
		}
		vec := []float32{1, 0, 0}
		if name == "b" {
			vec = []float32{0, 1, 0}
		}
		if err := cm.Insert(name, name+"-answer", vec, map[string]any{"text": name}); err != nil {
			t.Fatal(err)
		}
	}
	cm.SetDefaultEmbeddingContract(embcontract.Contract{Encoder: "encoder-a", Tokenizer: "different-tokenizer", Pooling: "mean", Normalization: "l2", Dim: 3, Metric: "cosine"})
	if err := cm.CreateWithDim("incompatible", 3, "encoder-a", "cosine"); err != nil {
		t.Fatal(err)
	}
	if err := cm.Insert("incompatible", "must-not-return", []float32{1, 0, 0}, map[string]any{"text": "incompatible"}); err != nil {
		t.Fatal(err)
	}
	cm.Close()
	cm, err = store.NewCollectionManager(3, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	cfg := APIConfig{Collections: cm, EmbedEndpoint: srv.URL, EmbedModel: "encoder-a", EmbedClient: embed.NewClient(srv.URL, "encoder-a", 16, 1).WithQueryAlias()}
	for _, order := range [][]string{{"a", "b"}, {"b", "a"}, {"incompatible", "a", "b"}} {
		mu.Lock()
		models = nil
		mu.Unlock()
		app := fiber.New()
		app.Post("/search/dual", dualSearchHandler(cfg))
		body, _ := json.Marshal(dualUnifiedSearchRequest{QueryText: "question", Collections: order, TopK: 10, Rerank: true})
		req := httptest.NewRequest(http.MethodPost, "/search/dual", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var got []dualSearchResult
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || len(got) != 2 {
			t.Fatalf("order=%v status=%d results=%v", order, resp.StatusCode, got)
		}
		seen := map[string]bool{}
		for _, result := range got {
			if result.Score < .99 || result.Model != "encoder-"+result.Collection || result.ID != result.Collection+"-answer" {
				t.Fatalf("wrong vector space: %+v", result)
			}
			seen[result.Collection] = true
		}
		if !seen["a"] || !seen["b"] {
			t.Fatalf("missing collection: %v", got)
		}
		mu.Lock()
		sent := append([]string(nil), models...)
		mu.Unlock()
		wire := map[string]bool{}
		for _, model := range sent {
			wire[model] = true
		}
		if !wire["encoder-a:query"] || !wire["encoder-b:query"] {
			t.Fatalf("query model aliases not preserved: %v", sent)
		}
	}
}

func TestT10DualSearchInheritsProviderDeadline(t *testing.T) {
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		close(entered)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	cm, err := store.NewCollectionManager(3, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("a", 3, "encoder-a", "cosine"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.SetUserContext(ctx); return c.Next() })
	app.Post("/search/dual", dualSearchHandler(APIConfig{Collections: cm, EmbedEndpoint: srv.URL, EmbedModel: "encoder-a", EmbedClient: embed.NewClient(srv.URL, "encoder-a", 16, 1)}))
	done := make(chan error, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/search/dual", strings.NewReader(`{"query_text":"deadline"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req, 2000)
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never entered")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not observe inherited deadline")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish after deadline")
	}
}

func TestT10DualSearchRespectsCanceledRequestAndMissingClient(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{1, 0, 0}}})
	}))
	defer srv.Close()
	cm, err := store.NewCollectionManager(3, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("a", 3, "encoder-a", "cosine"); err != nil {
		t.Fatal(err)
	}
	if err := cm.Insert("a", "answer", []float32{1, 0, 0}, map[string]any{"text": "answer"}); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []bool{false, true} {
		cfg := APIConfig{Collections: cm, EmbedEndpoint: srv.URL, EmbedModel: "encoder-a"}
		if !missing {
			cfg.EmbedClient = embed.NewClient(srv.URL, "encoder-a", 16, 1)
		}
		app := fiber.New()
		app.Use(func(c *fiber.Ctx) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			c.SetUserContext(ctx)
			return c.Next()
		})
		app.Post("/search/dual", dualSearchHandler(cfg))
		req := httptest.NewRequest(http.MethodPost, "/search/dual", strings.NewReader(`{"query_text":"question"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		var got []dualSearchResult
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if len(got) != 0 {
			t.Fatalf("missing=%v returned results: %v", missing, got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("canceled or unavailable request reached provider %d times", calls)
	}
}
