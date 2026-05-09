package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// memify_test.go — synchronous-path coverage for /memify. The async pipeline
// (entity_consolidation / triplet_embeddings / rule_associations / summary_generation)
// requires Neo4j + an LLM endpoint and is intentionally NOT exercised here.
// These tests pin the request-validation contract and the run-registry
// surfaces (status / stream 404), plus the pure extractJSON helper.

func newMemifyApp(t *testing.T, cfg APIConfig) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/memify", memifyHandler(cfg))
	app.Get("/memify/:runId/status", memifyStatusHandler())
	app.Get("/memify/:runId/stream", memifyStreamHandler())
	return app
}

func memifyPost(t *testing.T, app *fiber.App, body any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest("POST", "/memify", bytes.NewReader(b))
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

func TestMemify_RejectsWithoutNeo4jConfig(t *testing.T) {
	// cfg.Neo4jCfg.Neo4jURL == "" → handler must short-circuit with 400
	// before spinning up any goroutines.
	app := newMemifyApp(t, APIConfig{})
	status, body := memifyPost(t, app, memifyRequest{Dataset: "main"})
	if status != 400 {
		t.Errorf("status = %d, want 400; body=%v", status, body)
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Errorf("body.detail empty, want explanation")
	}
}

func TestMemifyStatus_UnknownRunReturns404(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	resp, err := app.Test(httptest.NewRequest("GET", "/memify/no-such-run/status", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestMemifyStatus_KnownRunReturnsJSON(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	runID := "test-run-known-123"
	want := &memifyRunStatus{
		RunID:     runID,
		Status:    "DONE",
		Stage:     "finished",
		Message:   "ok",
		Enriched:  7,
		ElapsedMs: 1234,
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	memifyRuns.Store(runID, want)
	defer memifyRuns.Delete(runID)

	resp, err := app.Test(httptest.NewRequest("GET", "/memify/"+runID+"/status", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got memifyRunStatus
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.RunID != want.RunID || got.Status != want.Status || got.Stage != want.Stage || got.Enriched != want.Enriched {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestMemifyStream_UnknownRunReturns404(t *testing.T) {
	app := newMemifyApp(t, APIConfig{})
	resp, err := app.Test(httptest.NewRequest("GET", "/memify/no-such-run/stream", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain object", `{"a":1}`, `{"a":1}`},
		{"plain array", `[1,2,3]`, `[1,2,3]`},
		{"object with prose prefix", "Sure, here you go: {\"k\":\"v\"} thanks!", `{"k":"v"}`},
		{"array with code fence", "```json\n[\"x\",\"y\"]\n```", `["x","y"]`},
		{"nested object", `prefix {"a":{"b":2},"c":[1,2]} suffix`, `{"a":{"b":2},"c":[1,2]}`},
		{"no JSON at all", "no brackets here", "no brackets here"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := extractJSON(c.in); got != c.want {
				t.Errorf("extractJSON(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
