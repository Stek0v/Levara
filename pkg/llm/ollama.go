package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OllamaNativeProvider speaks Ollama's native /api/chat protocol. It exists
// because the OpenAI-compatible /v1 endpoint silently ignores the "think"
// flag — a thinking-default model (granite4.2, gemma4 thinking variants)
// burns the whole completion budget in the reasoning field and returns an
// empty content. The native protocol is the only documented way to disable
// thinking per request.
type OllamaNativeProvider struct {
	endpoint string // base URL without /v1, e.g. http://127.0.0.1:11434
	apiKey   string
	client   *http.Client
}

// NewOllamaNativeProvider creates a native Ollama /api/chat provider.
// endpoint accepts the bare base ("http://127.0.0.1:11434") or an
// OpenAI-style suffix (".../v1"), which is stripped.
func NewOllamaNativeProvider(endpoint, apiKey string) *OllamaNativeProvider {
	base := strings.TrimSuffix(endpoint, "/")
	base = strings.TrimSuffix(base, "/v1")
	return &OllamaNativeProvider{
		endpoint: base,
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 600 * time.Second},
	}
}

func (p *OllamaNativeProvider) Name() string { return "ollama-native" }

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Think    *bool           `json:"think,omitempty"`
	Options  *ollamaOptions  `json:"options,omitempty"`
	Format   json.RawMessage `json:"format,omitempty"`
}

type ollamaOptions struct {
	Temperature float32 `json:"temperature,omitempty"`
	NumPredict  int     `json:"num_predict,omitempty"`
}

type ollamaChatResponse struct {
	Message struct {
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	PromptEvalCount int `json:"prompt_eval_count"`
	EvalCount       int `json:"eval_count"`
}

func (p *OllamaNativeProvider) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	body := ollamaChatRequest{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   false,
		Think:    req.Think,
	}
	if req.Temperature != 0 || req.MaxTokens != 0 {
		body.Options = &ollamaOptions{Temperature: req.Temperature, NumPredict: req.MaxTokens}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", p.endpoint+"/api/chat", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama chat: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama chat: HTTP %d", resp.StatusCode)
	}
	var d ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("ollama chat decode: %w", err)
	}
	out := &CompletionResponse{Content: strings.TrimSpace(d.Message.Content), Model: req.Model}
	out.Usage.PromptTokens = d.PromptEvalCount
	out.Usage.CompletionTokens = d.EvalCount
	return out, nil
}
