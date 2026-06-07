package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
)

func postSearchAny(t *testing.T, env *searchTestEnv, body map[string]any) (int, any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/search/text", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := env.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal response: %v (raw=%s)", err, string(raw))
	}
	return resp.StatusCode, out
}

func TestChunksSearch_LegacyArrayWhenIncludeDebugFalse(t *testing.T) {
	env := newSearchTestEnv(t)
	env.start()
	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	status, out := postSearchAny(t, env, map[string]any{
		"query_text": "hello",
		"query_type": "CHUNKS",
		"collection": "entities",
		"top_k":      5,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if _, ok := out.([]any); !ok {
		t.Fatalf("expected legacy []any response, got %T", out)
	}
}

func TestChunksSearch_EnvelopeWhenIncludeDebugTrue(t *testing.T) {
	env := newSearchTestEnv(t)
	env.start()
	vec := []float32{1, 0, 0, 0}
	env.insertVector("entities", "e1", vec, map[string]any{"text": "hello"})

	status, out := postSearchAny(t, env, map[string]any{
		"query_text":    "hello",
		"query_type":    "CHUNKS",
		"collection":    "entities",
		"top_k":         5,
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	obj, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("expected object envelope, got %T", out)
	}
	if obj["search_type"] != "CHUNKS" {
		t.Fatalf("search_type=%v, want CHUNKS", obj["search_type"])
	}
	if _, ok := obj["items"].([]any); !ok {
		t.Fatalf("items missing or wrong type: %#v", obj["items"])
	}
	debug, ok := obj["debug"].(map[string]any)
	if !ok || debug["source"] != "explicit" {
		t.Fatalf("debug=%v, want source=explicit", obj["debug"])
	}
}

