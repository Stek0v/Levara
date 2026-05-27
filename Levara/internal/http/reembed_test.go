package http

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"

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

func TestUnwrapMem0Envelope(t *testing.T) {
	mkEnvelope := func(value string) []byte {
		inner, _ := json.Marshal(map[string]string{
			"collection": "", "key": "k", "memory_id": "id", "type": "t", "value": value,
		})
		b64 := base64.StdEncoding.EncodeToString(inner)
		quoted, _ := json.Marshal(b64)
		return quoted
	}

	t.Run("envelope with value extracts inner", func(t *testing.T) {
		got, ok := unwrapMem0Envelope(mkEnvelope("hello world"))
		if !ok || got != "hello world" {
			t.Fatalf("got=%q ok=%v, want hello world/true", got, ok)
		}
	})
	t.Run("empty value falls through", func(t *testing.T) {
		if _, ok := unwrapMem0Envelope(mkEnvelope("")); ok {
			t.Fatal("empty value must return ok=false")
		}
	})
	t.Run("plain json object falls through", func(t *testing.T) {
		if _, ok := unwrapMem0Envelope([]byte(`{"text":"hi"}`)); ok {
			t.Fatal("plain map must return ok=false")
		}
	})
	t.Run("plain string non-base64 falls through", func(t *testing.T) {
		if _, ok := unwrapMem0Envelope([]byte(`"not base64!!"`)); ok {
			t.Fatal("non-b64 string must return ok=false")
		}
	})
	t.Run("base64 of non-envelope json falls through", func(t *testing.T) {
		b64 := base64.StdEncoding.EncodeToString([]byte(`{"other":"x"}`))
		quoted, _ := json.Marshal(b64)
		if _, ok := unwrapMem0Envelope(quoted); ok {
			t.Fatal("envelope without value must return ok=false")
		}
	})
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
