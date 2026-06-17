package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
)

func TestEmbeddingShadowReadReport(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	if err := cm.CreateWithDim("shadow", 2, "shadow-model", "cosine"); err != nil {
		t.Fatalf("Create shadow: %v", err)
	}
	if err := cm.Insert("live", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert live alpha: %v", err)
	}
	if err := cm.Insert("live", "beta-doc", []float32{0, 1}, map[string]any{"text": "beta"}); err != nil {
		t.Fatalf("insert live beta: %v", err)
	}
	if err := cm.Insert("shadow", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert shadow alpha: %v", err)
	}
	if err := cm.Insert("shadow", "gamma-doc", []float32{0, 1}, map[string]any{"text": "gamma"}); err != nil {
		t.Fatalf("insert shadow gamma: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	embedSrv := fakeShadowReadEmbedServer(t)
	defer embedSrv.Close()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), APIConfig{
		Collections:   cm,
		EmbedEndpoint: embedSrv.URL + "/v1/embeddings",
		StoragePath:   dir,
	})

	status, body := postShadowRead(t, app, embeddingShadowReadRequest{
		SourceCollection: "live",
		ShadowCollection: "shadow",
		Queries:          []string{"alpha"},
		TopK:             2,
	})
	if status != 200 {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if got := body["mean_jaccard_at_k"].(float64); got <= 0 {
		t.Fatalf("mean_jaccard_at_k=%v, want > 0", got)
	}
	if got := body["top1_stability"].(float64); got != 1 {
		t.Fatalf("top1_stability=%v, want 1", got)
	}
	if got := body["shadow_empty_rate"].(float64); got != 0 {
		t.Fatalf("shadow_empty_rate=%v, want 0", got)
	}
}

func TestEmbeddingShadowReadRequiresQueries(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), APIConfig{})

	status, _ := postShadowRead(t, app, embeddingShadowReadRequest{
		SourceCollection: "live",
		ShadowCollection: "shadow",
	})
	if status != 400 {
		t.Fatalf("status=%d, want 400", status)
	}
}

func TestEmbeddingMigrationManagedJobCompletes(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	if err := cm.Insert("live", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert live alpha: %v", err)
	}
	if err := cm.Insert("live", "beta-doc", []float32{0, 1}, map[string]any{"text": "beta"}); err != nil {
		t.Fatalf("insert live beta: %v", err)
	}

	embedSrv := fakeShadowReadEmbedServer(t)
	defer embedSrv.Close()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), APIConfig{
		Collections:   cm,
		EmbedEndpoint: embedSrv.URL + "/v1/embeddings",
	})

	status, body := postMigrationJSON(t, app, "/api/v1/embedding-migrations", embeddingMigrationRequest{
		SourceCollection: "live",
		TargetCollection: "live__shadow",
		TargetModel:      "shadow-model",
		TargetDim:        2,
		BatchSize:        1,
	})
	if status != 200 {
		t.Fatalf("start status=%d body=%v", status, body)
	}
	runID, _ := body["run_id"].(string)
	if runID == "" {
		t.Fatalf("missing run_id: %v", body)
	}

	final := pollMigrationStatus(t, app, runID)
	if got, _ := final["status"].(string); got != "COMPLETED" {
		t.Fatalf("final status=%v body=%v", got, final)
	}
	if got, _ := final["processed"].(float64); int(got) != 2 {
		t.Fatalf("processed=%v, want 2", got)
	}
	if got := cm.GetMeta("live__shadow"); got == nil || got.EmbeddingVersion == "" {
		t.Fatalf("target embedding contract missing: %+v", got)
	}
	ids, _, _, err := cm.AllRecords("live__shadow")
	if err != nil {
		t.Fatalf("AllRecords: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("target records=%d, want 2", len(ids))
	}
}

func TestEmbeddingMigrationDualWriteCopiesNewSourceWrites(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	if err := cm.Insert("live", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert live alpha: %v", err)
	}

	embedSrv := fakeShadowReadEmbedServer(t)
	defer embedSrv.Close()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), APIConfig{
		Collections:   cm,
		EmbedEndpoint: embedSrv.URL + "/v1/embeddings",
		StoragePath:   dir,
	})

	status, body := postMigrationJSON(t, app, "/api/v1/embedding-migrations", embeddingMigrationRequest{
		SourceCollection: "live",
		TargetCollection: "live__shadow",
		TargetModel:      "shadow-model",
		TargetDim:        2,
		BatchSize:        1,
		EnableDualWrite:  true,
	})
	if status != 200 {
		t.Fatalf("start status=%d body=%v", status, body)
	}
	runID, _ := body["run_id"].(string)
	final := pollMigrationStatus(t, app, runID)
	if got, _ := final["status"].(string); got != "COMPLETED" {
		t.Fatalf("final status=%v body=%v", got, final)
	}

	if err := cm.Insert("live", "delta-doc", []float32{0, 1}, map[string]any{"text": "delta"}); err != nil {
		t.Fatalf("insert live delta: %v", err)
	}
	if !cm.HasRecord("live__shadow", "delta-doc") {
		t.Fatal("dual-write did not copy new source record to shadow")
	}
}

func TestEmbeddingMigrationStatusSurvivesRegistryLoss(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	if err := cm.Insert("live", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert live alpha: %v", err)
	}

	embedSrv := fakeShadowReadEmbedServer(t)
	defer embedSrv.Close()
	cfg := APIConfig{
		Collections:   cm,
		EmbedEndpoint: embedSrv.URL + "/v1/embeddings",
		StoragePath:   dir,
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), cfg)

	status, body := postMigrationJSON(t, app, "/api/v1/embedding-migrations", embeddingMigrationRequest{
		SourceCollection: "live",
		TargetCollection: "live__shadow",
		TargetModel:      "shadow-model",
		TargetDim:        2,
		BatchSize:        1,
	})
	if status != 200 {
		t.Fatalf("start status=%d body=%v", status, body)
	}
	runID, _ := body["run_id"].(string)
	final := pollMigrationStatus(t, app, runID)
	if got, _ := final["status"].(string); got != "COMPLETED" {
		t.Fatalf("final status=%v body=%v", got, final)
	}

	embeddingMigrationRuns.Delete(runID)
	restarted := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(restarted.Group("/api/v1"), cfg)
	restored := getMigrationStatus(t, restarted, runID)
	if got, _ := restored["status"].(string); got != "COMPLETED" {
		t.Fatalf("restored status=%v body=%v", got, restored)
	}
	if got, _ := restored["checkpoint_id"].(string); got != "alpha-doc" {
		t.Fatalf("checkpoint_id=%q, want alpha-doc", got)
	}
}

func TestEmbeddingMigrationRetrySurvivesRegistryLoss(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}
	if err := cm.Insert("live", "alpha-doc", []float32{1, 0}, map[string]any{"text": "alpha"}); err != nil {
		t.Fatalf("insert live alpha: %v", err)
	}

	embedSrv := fakeFlakyEmbedServer(t)
	defer embedSrv.Close()
	cfg := APIConfig{
		Collections:   cm,
		EmbedEndpoint: embedSrv.URL + "/v1/embeddings",
		StoragePath:   dir,
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), cfg)

	status, body := postMigrationJSON(t, app, "/api/v1/embedding-migrations", embeddingMigrationRequest{
		SourceCollection: "live",
		TargetCollection: "live__shadow",
		TargetModel:      "shadow-model",
		TargetDim:        2,
		BatchSize:        1,
		MaxAttempts:      3,
	})
	if status != 200 {
		t.Fatalf("start status=%d body=%v", status, body)
	}
	runID, _ := body["run_id"].(string)
	dead := pollMigrationStatus(t, app, runID)
	if got, _ := dead["status"].(string); got != "DEAD_LETTER" {
		t.Fatalf("status=%v body=%v, want DEAD_LETTER", got, dead)
	}

	embeddingMigrationRuns.Delete(runID)
	restarted := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(restarted.Group("/api/v1"), cfg)
	status, retryBody := postMigrationJSON(t, restarted, "/api/v1/embedding-migrations/"+runID+"/retry", nil)
	if status != 200 {
		t.Fatalf("retry status=%d body=%v", status, retryBody)
	}
	final := pollMigrationStatus(t, restarted, runID)
	if got, _ := final["status"].(string); got != "COMPLETED" {
		t.Fatalf("final status=%v body=%v", got, final)
	}
	if got, _ := final["processed"].(float64); int(got) != 1 {
		t.Fatalf("processed=%v, want 1", got)
	}
}

func TestEmbeddingMigrationDryRunDoesNotCreateTarget(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(2, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.CreateWithDim("live", 2, "source-model", "cosine"); err != nil {
		t.Fatalf("Create live: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterEmbeddingMigrationAPI(app.Group("/api/v1"), APIConfig{
		Collections:   cm,
		EmbedEndpoint: "http://embed.example/v1/embeddings",
	})
	status, body := postMigrationJSON(t, app, "/api/v1/embedding-migrations", embeddingMigrationRequest{
		SourceCollection: "live",
		TargetCollection: "live__shadow",
		TargetModel:      "shadow-model",
		DryRun:           true,
	})
	if status != 200 {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if got, _ := body["status"].(string); got != "DRY_RUN" {
		t.Fatalf("status=%q, want DRY_RUN", got)
	}
	if cm.Has("live__shadow") {
		t.Fatal("dry run created target collection")
	}
}

func postShadowRead(t *testing.T, app *fiber.App, req any) (int, map[string]any) {
	return postMigrationJSON(t, app, "/api/v1/embedding-migrations/shadow-read", req)
}

func postMigrationJSON(t *testing.T, app *fiber.App, path string, req any) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(httpReq, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(buf, &body)
	return resp.StatusCode, body
}

func pollMigrationStatus(t *testing.T, app *fiber.App, runID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		body := getMigrationStatus(t, app, runID)
		switch body["status"] {
		case "COMPLETED", "FAILED", "DEAD_LETTER":
			return body
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("migration status timeout")
	return nil
}

func getMigrationStatus(t *testing.T, app *fiber.App, runID string) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/v1/embedding-migrations/"+runID+"/status", nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("status request: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var body map[string]any
	_ = json.Unmarshal(buf, &body)
	return body
}

func fakeShadowReadEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var resp struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		for i, text := range req.Input {
			vec := []float32{1, 0}
			if text != "alpha" {
				vec = []float32{0, 1}
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

func fakeFlakyEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	var calls atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) == 1 {
			http.Error(w, "temporary embed failure", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		var resp struct {
			Data []struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		for i := range req.Input {
			resp.Data = append(resp.Data, struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}{Index: i, Embedding: []float32{1, 0}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}
