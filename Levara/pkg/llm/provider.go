// Package llm provides multi-provider LLM abstraction.
//
// Supported providers:
//   - "openai" / "ollama" / "" — OpenAI-compatible API (POST /chat/completions)
//   - "anthropic" / "claude"  — Anthropic Messages API (POST /v1/messages)
//
// Usage:
//
//	p, _ := llm.NewProvider("anthropic", "", "sk-ant-...")
//	resp, _ := p.ChatCompletion(ctx, llm.CompletionRequest{
//	    Model: "claude-sonnet-4-20250514", Messages: msgs, MaxTokens: 2000,
//	})
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/stek0v/cognevra/internal/metrics"
)

// Message is a chat message (role + content).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// CompletionRequest is a provider-agnostic chat completion request.
type CompletionRequest struct {
	Model          string
	Messages       []Message
	Temperature    float32
	MaxTokens      int
	ResponseFormat map[string]any // optional JSON Schema (OpenAI json_schema mode)
}

// CompletionResponse is a provider-agnostic chat completion response.
type CompletionResponse struct {
	Content string
	Model   string
	Usage   struct {
		PromptTokens     int
		CompletionTokens int
	}
}

// Provider is the LLM provider interface.
type Provider interface {
	ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
	Name() string
}

// NewProvider creates a Provider by name.
//
//	"openai", "ollama", "" → OpenAIProvider (endpoint required)
//	"anthropic", "claude"  → AnthropicProvider (apiKey required)
//	anything else          → OpenAIProvider (treat as OpenAI-compatible)
func NewProvider(providerName, endpoint, apiKey string) (Provider, error) {
	switch strings.ToLower(providerName) {
	case "anthropic", "claude":
		if apiKey == "" {
			return nil, fmt.Errorf("anthropic provider requires API key")
		}
		return NewAnthropicProvider(apiKey), nil
	case "openai", "ollama", "":
		return NewOpenAIProvider(endpoint, apiKey), nil
	default:
		// Default to OpenAI-compatible
		return NewOpenAIProvider(endpoint, apiKey), nil
	}
}

// ──────────────────────────────────────────────
// OpenAI Provider (covers OpenAI, Ollama, vLLM, etc.)
// ──────────────────────────────────────────────

// OpenAIProvider implements Provider for any OpenAI-compatible API.
type OpenAIProvider struct {
	endpoint string
	apiKey   string
	client   *http.Client
}

// NewOpenAIProvider creates an OpenAI-compatible provider.
// endpoint example: "http://localhost:11434/v1" or "https://api.openai.com/v1"
func NewOpenAIProvider(endpoint, apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		apiKey:   apiKey,
		client:   &http.Client{Timeout: 600 * time.Second},
	}
}

func (p *OpenAIProvider) Name() string { return "openai" }

func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	llmStart := time.Now()
	defer func() {
		metrics.LLMDuration.Observe(time.Since(llmStart).Seconds())
	}()
	url := p.endpoint
	if !strings.HasSuffix(url, "/chat/completions") {
		url = url + "/chat/completions"
	}

	body := map[string]any{
		"model":       req.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
		"stream":      false,
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if req.ResponseFormat != nil {
		body["response_format"] = req.ResponseFormat
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		metrics.LLMRequests.WithLabelValues(req.Model, "error").Inc()
		return nil, fmt.Errorf("HTTP call: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		metrics.LLMRequests.WithLabelValues(req.Model, "error").Inc()
		return nil, fmt.Errorf("OpenAI status %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var oaiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	out := &CompletionResponse{
		Content: oaiResp.Choices[0].Message.Content,
		Model:   oaiResp.Model,
	}
	out.Usage.PromptTokens = oaiResp.Usage.PromptTokens
	out.Usage.CompletionTokens = oaiResp.Usage.CompletionTokens
	metrics.LLMRequests.WithLabelValues(req.Model, "ok").Inc()
	return out, nil
}

// ──────────────────────────────────────────────
// Anthropic Provider
// ──────────────────────────────────────────────

const anthropicAPIURL = "https://api.anthropic.com/v1/messages"

// AnthropicProvider implements Provider for the Anthropic Messages API.
type AnthropicProvider struct {
	apiKey string
	client *http.Client
}

// NewAnthropicProvider creates an Anthropic provider.
func NewAnthropicProvider(apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		apiKey: apiKey,
		client: &http.Client{Timeout: 600 * time.Second},
	}
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// ──────────────────────────────────────────────
// TracedProvider — wraps any Provider with Langfuse tracing
// ──────────────────────────────────────────────

// Tracer is the interface for LLM call tracing (implemented by observe.LangfuseTracer).
type Tracer interface {
	TraceGeneration(ctx context.Context, trace TracerData) error
	Enabled() bool
}

// TracerData mirrors observe.TraceData to avoid circular imports.
type TracerData struct {
	TraceID   string
	Name      string
	Model     string
	Input     string
	Output    string
	LatencyMs int64
	TokensIn  int
	TokensOut int
	Status    string
	Metadata  map[string]any
}

// TracedProvider wraps a Provider with LLM call tracing.
type TracedProvider struct {
	provider Provider
	tracer   Tracer
}

// NewTracedProvider wraps provider with tracing. If tracer is nil or disabled,
// returns the original provider unwrapped.
func NewTracedProvider(provider Provider, tracer Tracer) Provider {
	if tracer == nil || !tracer.Enabled() {
		return provider
	}
	return &TracedProvider{provider: provider, tracer: tracer}
}

func (tp *TracedProvider) Name() string {
	return "traced:" + tp.provider.Name()
}

func (tp *TracedProvider) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	start := time.Now()
	resp, err := tp.provider.ChatCompletion(ctx, req)
	latency := time.Since(start).Milliseconds()

	status := "success"
	output := ""
	tokensIn, tokensOut := 0, 0
	if err != nil {
		status = "error"
		output = err.Error()
	} else if resp != nil {
		output = resp.Content
		tokensIn = resp.Usage.PromptTokens
		tokensOut = resp.Usage.CompletionTokens
	}

	// Build input summary from messages
	var inputParts []string
	for _, m := range req.Messages {
		inputParts = append(inputParts, fmt.Sprintf("[%s] %s", m.Role, m.Content))
	}
	inputStr := strings.Join(inputParts, "\n")

	// Trace asynchronously — don't block the caller on Langfuse latency
	traceData := TracerData{
		TraceID:   fmt.Sprintf("gen-%d", start.UnixNano()),
		Name:      "chat_completion",
		Model:     req.Model,
		Input:     inputStr,
		Output:    output,
		LatencyMs: latency,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Status:    status,
		Metadata:  map[string]any{"provider": tp.provider.Name()},
	}
	go tp.tracer.TraceGeneration(context.Background(), traceData)

	return resp, err
}

func (p *AnthropicProvider) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	llmStart := time.Now()
	defer func() {
		metrics.LLMDuration.Observe(time.Since(llmStart).Seconds())
	}()
	// Anthropic format: separate system from user/assistant messages
	var systemPrompt string
	var messages []map[string]string

	for _, m := range req.Messages {
		if m.Role == "system" {
			systemPrompt = m.Content
			continue
		}
		messages = append(messages, map[string]string{
			"role":    m.Role,
			"content": m.Content,
		})
	}

	// Anthropic requires at least one user message
	if len(messages) == 0 {
		return nil, fmt.Errorf("anthropic: at least one non-system message required")
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	body := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": maxTokens,
	}
	if systemPrompt != "" {
		body["system"] = systemPrompt
	}
	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", anthropicAPIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		metrics.LLMRequests.WithLabelValues(req.Model, "error").Inc()
		return nil, fmt.Errorf("HTTP call: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		metrics.LLMRequests.WithLabelValues(req.Model, "error").Inc()
		return nil, fmt.Errorf("Anthropic status %d: %s", resp.StatusCode, truncate(string(respBody), 300))
	}

	var antResp struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &antResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Extract text from content blocks
	var textParts []string
	for _, block := range antResp.Content {
		if block.Type == "text" {
			textParts = append(textParts, block.Text)
		}
	}
	if len(textParts) == 0 {
		return nil, fmt.Errorf("no text content in Anthropic response")
	}

	out := &CompletionResponse{
		Content: strings.Join(textParts, ""),
		Model:   antResp.Model,
	}
	out.Usage.PromptTokens = antResp.Usage.InputTokens
	out.Usage.CompletionTokens = antResp.Usage.OutputTokens
	metrics.LLMRequests.WithLabelValues(req.Model, "ok").Inc()
	return out, nil
}
