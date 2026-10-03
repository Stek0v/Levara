package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The native provider must address /api/chat, pass the think toggle through,
// and map message.content back — the OpenAI-compatible endpoint ignores
// "think" entirely, which is why this provider exists.
func TestOllamaNativeProvider_ThinkPassthrough(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var body struct {
			Model   string `json:"model"`
			Stream  bool   `json:"stream"`
			Think   *bool  `json:"think"`
			Options *struct {
				Temperature float32 `json:"temperature"`
				NumPredict  int     `json:"num_predict"`
			} `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Think == nil {
			t.Error("request must carry the think toggle")
		} else if *body.Think {
			t.Error("think must be false")
		}
		if body.Options == nil || body.Options.NumPredict != 128 {
			t.Errorf("options = %+v, want num_predict 128", body.Options)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"model":             body.Model,
			"prompt_eval_count": 11,
			"eval_count":        22,
			"message": map[string]string{
				"content":  "combined statement",
				"thinking": "chain of thought that must not leak into content",
			},
		})
	}))
	defer srv.Close()

	p, err := NewProvider("ollama-native", srv.URL, "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	got, err := p.ChatCompletion(context.Background(), CompletionRequest{
		Model:     "granite4.2:3b",
		Messages:  []Message{{Role: "user", Content: "hi"}},
		MaxTokens: 128,
		Think:     BoolPtr(false),
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if gotPath != "/api/chat" {
		t.Errorf("path = %q, want /api/chat", gotPath)
	}
	if got.Content != "combined statement" {
		t.Errorf("content = %q", got.Content)
	}
	if got.Usage.PromptTokens != 11 || got.Usage.CompletionTokens != 22 {
		t.Errorf("usage = %+v", got.Usage)
	}
}

// NewProvider must keep routing the legacy "ollama" name to the
// OpenAI-compatible provider — existing deployments depend on that.
func TestNewProvider_BackCompat(t *testing.T) {
	p, err := NewProvider("ollama", "http://x", "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, ok := p.(*OllamaNativeProvider); ok {
		t.Error(`"ollama" must stay OpenAI-compatible, not native`)
	}
	p, err = NewProvider("ollama-native", "http://x", "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, ok := p.(*OllamaNativeProvider); !ok {
		t.Error(`"ollama-native" must build the native provider`)
	}
}
