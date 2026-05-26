package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
)

// reembed_test.go — validation-path coverage for POST /reembed. The async
// embedder goroutine spawned on the happy path is intentionally NOT
// exercised here (it needs a live embed endpoint); these tests pin the
// synchronous request-validation contract that returns 4xx/5xx before any
// background work begins.

func newReembedApp(t *testing.T, cfg APIConfig) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api := app.Group("/api/v1")
	RegisterReembedAPI(api, cfg)
	return app
}

func reembedPost(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if raw, ok := body.([]byte); ok {
		reader = bytes.NewReader(raw)
	} else {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(b)
	}
	r := httptest.NewRequest("POST", "/api/v1/reembed", reader)
	r.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(buf, &out)
	return resp.StatusCode, out
}

func TestReembed_RejectsInvalidJSON(t *testing.T) {
	app := newReembedApp(t, APIConfig{})
	status, body := reembedPost(t, app, []byte("{not json"))
	if status != 400 {
		t.Errorf("status = %d, want 400; body=%v", status, body)
	}
}

func TestReembed_RequiresSourceAndTarget(t *testing.T) {
	app := newReembedApp(t, APIConfig{})
	for _, payload := range []reembedRequest{
		{TargetCollection: "t"},
		{SourceCollection: "s"},
		{},
	} {
		status, _ := reembedPost(t, app, payload)
		if status != 400 {
			t.Errorf("payload=%+v: status = %d, want 400", payload, status)
		}
	}
}

func TestReembed_RejectsSameSourceAndTarget(t *testing.T) {
	app := newReembedApp(t, APIConfig{})
	status, body := reembedPost(t, app, reembedRequest{
		SourceCollection: "same",
		TargetCollection: "same",
	})
	if status != 400 {
		t.Errorf("status = %d, want 400; body=%v", status, body)
	}
}

func TestReembed_NoCollectionsConfiguredReturns503(t *testing.T) {
	// cfg.Collections is nil — handler should fail fast with 503 before
	// even consulting the embed model defaults.
	app := newReembedApp(t, APIConfig{})
	status, _ := reembedPost(t, app, reembedRequest{
		SourceCollection: "src",
		TargetCollection: "tgt",
	})
	if status != 503 {
		t.Errorf("status = %d, want 503", status)
	}
}

func TestReembed_SourceCollectionMissingReturns404(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(8, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()

	app := newReembedApp(t, APIConfig{
		Collections:   cm,
		EmbedEndpoint: "http://embed.example",
		EmbedModel:    "test-model",
	})
	status, _ := reembedPost(t, app, reembedRequest{
		SourceCollection: "does-not-exist",
		TargetCollection: "tgt",
	})
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestReembed_RequiresEmbedEndpointAndModel(t *testing.T) {
	dir := t.TempDir()
	cm, err := store.NewCollectionManager(8, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()
	if err := cm.Create("src"); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No EmbedEndpoint/EmbedModel on cfg, no overrides on request → 400.
	app := newReembedApp(t, APIConfig{Collections: cm})
	status, _ := reembedPost(t, app, reembedRequest{
		SourceCollection: "src",
		TargetCollection: "tgt",
	})
	if status != 400 {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestReembedStatus_UnknownRunReturns404(t *testing.T) {
	app := newReembedApp(t, APIConfig{})
	resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/reembed/no-such-run/status", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// fakeReembedEmbedServer mints deterministic dim=targetDim vectors for any
// /v1/embeddings request. Pinned to OpenAI response shape so embed.Client
// decodes cleanly. The vectors are non-zero and per-text distinguishable so
// the test can later assert "target collection contains an embedding we
// produced", not just "an embedding".
func fakeReembedEmbedServer(t *testing.T, targetDim int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			http.NotFound(w, r)
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
		for i, text := range req.Input {
			vec := make([]float32, targetDim)
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

// TestReembed_HappyPath_DimChange exercises the full async re-embed flow:
// source coll has dim=8 records; reembed writes target with dim=4 from a
// fake embed server. Verifies (a) target was created with the new dim,
// (b) record count matches source, (c) source remains untouched (delete=false),
// (d) status transitions RUNNING → COMPLETED with no failures.
func TestReembed_HappyPath_DimChange(t *testing.T) {
	const (
		sourceDim = 8
		targetDim = 4
		numRecs   = 12
	)

	dir := t.TempDir()
	cm, err := store.NewCollectionManager(sourceDim, dir)
	if err != nil {
		t.Fatalf("NewCollectionManager: %v", err)
	}
	defer cm.Close()

	if err := cm.CreateWithMeta("src", "nomic-old", "cosine"); err != nil {
		t.Fatalf("Create src: %v", err)
	}
	for i := 0; i < numRecs; i++ {
		vec := make([]float32, sourceDim)
		for j := range vec {
			vec[j] = float32(i + 1)
		}
		meta := map[string]any{"text": "sample text " + string(rune('A'+i))}
		metaBytes, _ := json.Marshal(meta)
		if err := cm.Insert("src", "rec-"+string(rune('A'+i)), vec, json.RawMessage(metaBytes)); err != nil {
			t.Fatalf("Insert rec-%d: %v", i, err)
		}
	}

	srv := fakeReembedEmbedServer(t, targetDim)
	defer srv.Close()

	app := newReembedApp(t, APIConfig{
		Collections:   cm,
		EmbedEndpoint: srv.URL + "/v1/embeddings",
		EmbedModel:    "potion-new",
	})

	status, body := reembedPost(t, app, reembedRequest{
		SourceCollection: "src",
		TargetCollection: "tgt",
		BatchSize:        5,
	})
	if status != 200 {
		t.Fatalf("POST /reembed status = %d, body=%v", status, body)
	}
	runID, _ := body["run_id"].(string)
	if runID == "" {
		t.Fatalf("missing run_id in response: %v", body)
	}

	// Poll status until COMPLETED or timeout.
	deadline := time.Now().Add(5 * time.Second)
	var final map[string]any
	for time.Now().Before(deadline) {
		resp, err := app.Test(httptest.NewRequest("GET", "/api/v1/reembed/"+runID+"/status", nil), -1)
		if err != nil {
			t.Fatalf("status GET: %v", err)
		}
		buf, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = json.Unmarshal(buf, &final)
		if s, _ := final["status"].(string); s == "COMPLETED" || s == "FAILED" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got, _ := final["status"].(string); got != "COMPLETED" {
		t.Fatalf("final status = %v, want COMPLETED; full=%v", got, final)
	}
	if got, _ := final["failed"].(float64); got != 0 {
		t.Errorf("failed=%v, want 0", got)
	}
	if got, _ := final["processed"].(float64); int(got) != numRecs {
		t.Errorf("processed=%v, want %d", got, numRecs)
	}

	if d := cm.Dim("tgt"); d != targetDim {
		t.Errorf("target dim = %d, want %d", d, targetDim)
	}
	if d := cm.Dim("src"); d != sourceDim {
		t.Errorf("source dim mutated: %d, want %d (delete_source=false)", d, sourceDim)
	}
	if !cm.Has("src") {
		t.Errorf("source collection was dropped despite delete_source=false")
	}

	tgtIDs, _, _, err := cm.AllRecords("tgt")
	if err != nil {
		t.Fatalf("AllRecords tgt: %v", err)
	}
	if len(tgtIDs) != numRecs {
		t.Errorf("target record count = %d, want %d", len(tgtIDs), numRecs)
	}
}
