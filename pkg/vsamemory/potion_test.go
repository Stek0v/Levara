package vsamemory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/embed"
)

const (
	potionModel = "potion-code-16M"
	potionDim   = 256
)

func TestVSAPotionOfflineCompatibility(t *testing.T) {
	ctx := context.Background()
	server := fakePotionEmbeddingServer(t)
	defer server.Close()

	client := embed.NewClient(server.URL+"/v1/embeddings", potionModel, 8, 1)
	texts := []string{
		"func QueryObject(ctx context.Context, datasetID, subjectID, predicate string, topK int)",
		"api-service calls sqlite graph store",
		"api-service emits audit-event",
	}
	vecs, err := client.EmbedTexts(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedTexts potion fake: %v", err)
	}
	assertPotionVectors(t, vecs, len(texts))

	db := newVSATestDB(t)
	seedPotionGraph(t, db)

	store := NewStore(db, Config{Dim: potionDim, ShardSize: 2, Dialect: DialectSQLite})
	if err := store.RebuildFromGraph(ctx, "potion-ds"); err != nil {
		t.Fatalf("RebuildFromGraph potion-ds: %v", err)
	}

	stats, err := store.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.MaxDim != potionDim {
		t.Fatalf("stats max dim=%d, want %d", stats.MaxDim, potionDim)
	}
	if stats.FactCount != 3 {
		t.Fatalf("stats fact count=%d, want 3 active potion facts", stats.FactCount)
	}

	got, err := store.QueryObject(ctx, "potion-ds", "svc:api", "calls", 5)
	if err != nil {
		t.Fatalf("QueryObject calls: %v", err)
	}
	if len(got) == 0 || got[0].TargetID != "store:sqlite" {
		t.Fatalf("QueryObject calls = %+v, want store:sqlite first", got)
	}
}

func TestVSAPotionQueryUsesPersistedShardDimension(t *testing.T) {
	ctx := context.Background()
	db := newVSATestDB(t)
	seedPotionGraph(t, db)

	buildStore := NewStore(db, Config{Dim: potionDim, ShardSize: 4, Dialect: DialectSQLite})
	if err := buildStore.RebuildFromGraph(ctx, "potion-ds"); err != nil {
		t.Fatalf("RebuildFromGraph potion-ds: %v", err)
	}

	queryStoreWithDefaultDim := NewStore(db, Config{Dialect: DialectSQLite})
	got, err := queryStoreWithDefaultDim.QueryObject(ctx, "potion-ds", "svc:api", "calls", 1)
	if err != nil {
		t.Fatalf("QueryObject with default-dim store: %v", err)
	}
	if len(got) != 1 || got[0].TargetID != "store:sqlite" {
		t.Fatalf("QueryObject with default-dim store = %+v, want store:sqlite", got)
	}
}

func TestVSAPotionLiveEmbeddingContract(t *testing.T) {
	endpoint := potionTestEndpoint()
	if endpoint == "" {
		t.Skip("set POTION_EMBED_TEST_URL or EMBEDDING_ENDPOINT to run live potion-code-16M contract test")
	}
	model := os.Getenv("POTION_EMBED_TEST_MODEL")
	if model == "" {
		model = os.Getenv("EMBEDDING_MODEL")
	}
	if model == "" {
		model = potionModel
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client := embed.NewClient(endpoint, model, 8, 1)
	texts := []string{
		"func QueryObject(ctx context.Context, datasetID, subjectID, predicate string, topK int)",
		"func QueryObject(ctx context.Context, datasetID, subjectID, predicate string, topK int)",
		"banana bread recipe with walnuts",
	}
	vecs, err := client.EmbedTexts(ctx, texts)
	if err != nil {
		t.Fatalf("EmbedTexts live %s at %s: %v", model, endpoint, err)
	}
	assertPotionVectors(t, vecs, len(texts))
	if same, unrelated := cosine(vecs[0], vecs[1]), cosine(vecs[0], vecs[2]); same <= unrelated {
		t.Fatalf("semantic sanity failed: same-text cosine %.4f <= unrelated cosine %.4f", same, unrelated)
	}

	db := newVSATestDB(t)
	seedPotionGraph(t, db)
	store := NewStore(db, Config{Dim: len(vecs[0]), ShardSize: 4, Dialect: DialectSQLite})
	if err := store.RebuildFromGraph(ctx, "potion-ds"); err != nil {
		t.Fatalf("RebuildFromGraph live potion dim: %v", err)
	}
	got, err := store.QueryObject(ctx, "potion-ds", "svc:api", "calls", 1)
	if err != nil {
		t.Fatalf("QueryObject live potion dim: %v", err)
	}
	if len(got) != 1 || got[0].TargetID != "store:sqlite" {
		t.Fatalf("QueryObject live potion dim = %+v, want store:sqlite", got)
	}
}

func fakePotionEmbeddingServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"model":"potion-code-16M","dim":256,"backend":"model2vec"}`))
			return
		}
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if req.Model != potionModel {
			http.Error(w, "unexpected model", http.StatusBadRequest)
			return
		}
		data := make([]struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}, len(req.Input))
		for i, text := range req.Input {
			data[i] = struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: deterministicPotionVector(text)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  potionModel,
			"data":   data,
		})
	}))
}

func seedPotionGraph(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO graph_edges(id, source_id, target_id, relationship_name, valid_until, dataset_id) VALUES
			('p1', 'svc:api', 'store:sqlite', 'calls', NULL, 'potion-ds'),
			('p2', 'svc:api', 'event:audit', 'emits', NULL, 'potion-ds'),
			('p3', 'svc:worker', 'store:sqlite', 'calls', NULL, 'potion-ds'),
			('p4', 'svc:api', 'store:neo4j', 'calls', '2026-01-01T00:00:00Z', 'potion-ds');
	`); err != nil {
		t.Fatalf("seed potion graph: %v", err)
	}
}

func deterministicPotionVector(text string) []float32 {
	vec := make([]float32, potionDim)
	var sum float64
	for i := range vec {
		h := sha256.Sum256([]byte(text + "\x00" + string(rune(i))))
		v := (float64(int(h[0]))/127.5 - 1) + (float64(int(h[1]))/32512.5 - 0.0039)
		vec[i] = float32(v)
		sum += v * v
	}
	norm := math.Sqrt(sum)
	for i := range vec {
		vec[i] = float32(float64(vec[i]) / norm)
	}
	return vec
}

func assertPotionVectors(t *testing.T, vecs [][]float32, wantCount int) {
	t.Helper()
	if len(vecs) != wantCount {
		t.Fatalf("vectors len=%d, want %d", len(vecs), wantCount)
	}
	for i, vec := range vecs {
		if len(vec) != potionDim {
			t.Fatalf("vector %d dim=%d, want %d for potion-code-16M", i, len(vec), potionDim)
		}
		var norm float64
		for j, v := range vec {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				t.Fatalf("vector %d contains invalid value at %d: %v", i, j, v)
			}
			norm += float64(v) * float64(v)
		}
		if norm == 0 {
			t.Fatalf("vector %d is all zeros", i)
		}
	}
}

func potionTestEndpoint() string {
	endpoint := os.Getenv("POTION_EMBED_TEST_URL")
	if endpoint == "" {
		endpoint = os.Getenv("EMBEDDING_ENDPOINT")
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	if strings.HasSuffix(endpoint, "/v1/embeddings") {
		return endpoint
	}
	return strings.TrimRight(endpoint, "/") + "/v1/embeddings"
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}
