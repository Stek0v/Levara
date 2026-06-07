// Package llm provides structured LLM calls with JSON Schema enforcement and retry.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stek0v/levara/internal/metrics"
)

// schemaUnsupportedEndpoints remembers endpoints that rejected json_schema
// response_format so we don't waste another round-trip next time. Populated
// when the first schema attempt returns HTTP 400 with a response_format-shaped
// error message. Seeded with known offenders below.
var schemaUnsupportedEndpoints sync.Map

func init() {
	// DeepSeek: api.deepseek.com returns HTTP 400 with
	//   "This response_format type is unavailable now"
	// for json_schema. Plain json_object works, but plain fallback works too.
	schemaUnsupportedEndpoints.Store("api.deepseek.com", struct{}{})
}

// endpointSkipsJSONSchema reports whether the provider's endpoint is known to
// reject response_format: json_schema. Matches by substring on the endpoint
// host so any path suffix (/v1, /v2/...) works. Only inspects providers that
// expose an Endpoint() string method.
func endpointSkipsJSONSchema(p Provider) bool {
	ep, ok := providerEndpoint(p)
	if !ok {
		return false
	}
	hit := false
	schemaUnsupportedEndpoints.Range(func(k, _ any) bool {
		if host, ok := k.(string); ok && strings.Contains(ep, host) {
			hit = true
			return false
		}
		return true
	})
	return hit
}

// rememberEndpointUnsupportsSchema records that this endpoint rejected
// json_schema. Idempotent; safe to call from concurrent goroutines.
func rememberEndpointUnsupportsSchema(p Provider) {
	if ep, ok := providerEndpoint(p); ok && ep != "" {
		schemaUnsupportedEndpoints.Store(ep, struct{}{})
	}
}

// providerEndpoint pulls the endpoint URL out of providers that expose it.
func providerEndpoint(p Provider) (string, bool) {
	type endpointer interface{ Endpoint() string }
	if e, ok := p.(endpointer); ok {
		return e.Endpoint(), true
	}
	return "", false
}

// looksLikeResponseFormatReject heuristically detects an HTTP-400 response
// that complains about response_format. We look for either the OpenAI-style
// "Invalid parameter: response_format" or DeepSeek's wording.
func looksLikeResponseFormatReject(errMsg string) bool {
	s := strings.ToLower(errMsg)
	if !strings.Contains(s, "status 400") && !strings.Contains(s, "400") {
		return false
	}
	return strings.Contains(s, "response_format") ||
		strings.Contains(s, "response format") ||
		strings.Contains(s, "json_schema") ||
		strings.Contains(s, "json schema") ||
		strings.Contains(s, "unavailable now")
}

// StructuredRequest configures a structured output LLM call.
type StructuredRequest struct {
	Endpoint     string
	Model        string
	SystemPrompt string
	UserPrompt   string
	Temperature  float32
	// JSON Schema for response_format (OpenAI JSON mode)
	ResponseSchema map[string]any
	// Retry config
	MaxRetries int // default: 3
	// HTTP client (optional, uses default if nil)
	Client *http.Client
	// Provider (optional): if set, uses Provider.ChatCompletion instead of raw HTTP.
	// Supports non-OpenAI providers (e.g. Anthropic) transparently.
	Provider Provider
}

// KnowledgeGraphSchema is the JSON Schema for knowledge graph extraction.
var KnowledgeGraphSchema = map[string]any{
	"name":   "knowledge_graph",
	"strict": false,
	"schema": map[string]any{
		"type": "object",
		"properties": map[string]any{
			"nodes": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":        map[string]any{"type": "string"},
						"type":        map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
					},
					"required": []string{"name", "type"},
				},
			},
			"edges": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source":            map[string]any{"type": "string"},
						"target":            map[string]any{"type": "string"},
						"relationship_name": map[string]any{"type": "string"},
					},
					"required": []string{"source", "target", "relationship_name"},
				},
			},
		},
		"required": []string{"nodes", "edges"},
	},
}

var defaultClient = &http.Client{Timeout: 600 * time.Second}

// StructuredCall makes an LLM call with JSON Schema enforcement.
//  1. Tries response_format: {"type": "json_schema", "json_schema": {...}} (OpenAI)
//  2. If 400/unsupported -> fallback to regular call + parse
//  3. Retry up to MaxRetries on parse failure
//  4. Returns raw JSON string (caller parses into their struct)
func StructuredCall(ctx context.Context, req StructuredRequest) (string, error) {
	if req.MaxRetries <= 0 {
		req.MaxRetries = 3
	}

	// If a Provider is set, use provider-based path (works for any provider incl. Anthropic).
	if req.Provider != nil {
		return structuredCallViaProvider(ctx, req)
	}

	// Legacy path: raw HTTP to OpenAI-compatible endpoint.
	client := req.Client
	if client == nil {
		client = defaultClient
	}

	endpoint := req.Endpoint
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint = strings.TrimSuffix(endpoint, "/") + "/chat/completions"
	}

	// First attempt: with response_format json_schema
	result, err := doStructuredCall(ctx, client, endpoint, req, true)
	if err == nil {
		if isValidJSON(result) {
			log.Printf("[llm] structured output: got valid JSON via json_schema mode")
			return result, nil
		}
		// Valid HTTP response but invalid JSON — will retry below
		log.Printf("[llm] structured output: json_schema mode returned invalid JSON, retrying")
	} else if isUnsupportedError(err) {
		// Model doesn't support json_schema — fallback without it
		log.Printf("[llm] structured output: json_schema not supported (400), falling back to plain mode")
	} else {
		// Network/other error — still try retries without schema
		log.Printf("[llm] structured output: initial call failed: %v", err)
	}

	// Retry loop: without response_format, append JSON instruction to prompt
	var lastErr error
	for attempt := 1; attempt <= req.MaxRetries; attempt++ {
		retryReq := req
		retryReq.UserPrompt = req.UserPrompt + "\n\nPlease respond with valid JSON only."

		result, err := doStructuredCall(ctx, client, endpoint, retryReq, false)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt, err)
			log.Printf("[llm] structured retry %d/%d failed: %v", attempt, req.MaxRetries, err)
			continue
		}

		// Extract JSON from response (may be wrapped in markdown blocks)
		extracted := extractJSONFromResponse(result)
		if extracted != "" && isValidJSON(extracted) {
			log.Printf("[llm] structured output: got valid JSON on retry %d/%d", attempt, req.MaxRetries)
			return extracted, nil
		}

		lastErr = fmt.Errorf("attempt %d: response is not valid JSON", attempt)
		log.Printf("[llm] structured retry %d/%d: invalid JSON in response", attempt, req.MaxRetries)
	}

	return "", fmt.Errorf("structured call failed after %d retries: %w", req.MaxRetries, lastErr)
}

// structuredCallViaProvider uses the Provider interface for structured calls.
// First tries with ResponseFormat (json_schema), then retries with plain JSON instruction.
//
// Fast path: if the provider's endpoint is in schemaUnsupportedEndpoints
// (known offender like DeepSeek, or one that rejected schema earlier in the
// process) we skip the wasted schema attempt and go straight to plain JSON.
func structuredCallViaProvider(ctx context.Context, req StructuredRequest) (string, error) {
	// Fast path: skip schema entirely for known-unsupporting endpoints.
	if endpointSkipsJSONSchema(req.Provider) {
		return structuredCallPlainJSON(ctx, req)
	}

	msgs := []Message{
		{Role: "system", Content: req.SystemPrompt},
		{Role: "user", Content: req.UserPrompt},
	}

	// First attempt: with response_format (OpenAI providers support this; Anthropic ignores it)
	cr := CompletionRequest{
		Model:       req.Model,
		Messages:    msgs,
		Temperature: req.Temperature,
	}
	if req.ResponseSchema != nil {
		cr.ResponseFormat = map[string]any{
			"type":        "json_schema",
			"json_schema": req.ResponseSchema,
		}
	}

	resp, err := req.Provider.ChatCompletion(ctx, cr)
	if err == nil && isValidJSON(resp.Content) {
		log.Printf("[llm] structured via %s: got valid JSON", req.Provider.Name())
		return resp.Content, nil
	}
	if err == nil {
		log.Printf("[llm] structured via %s: invalid JSON, retrying plain mode", req.Provider.Name())
	} else {
		log.Printf("[llm] structured via %s: initial call failed: %v", req.Provider.Name(), err)
		// Learn: if this looks like a response_format rejection, remember it
		// so the next call for this endpoint skips the schema attempt.
		if looksLikeResponseFormatReject(err.Error()) {
			rememberEndpointUnsupportsSchema(req.Provider)
			log.Printf("[llm] structured: memoized endpoint as schema-unsupported")
		}
	}

	// Retry loop: without response_format, append JSON instruction
	var lastErr error
	for attempt := 1; attempt <= req.MaxRetries; attempt++ {
		retryMsgs := []Message{
			{Role: "system", Content: req.SystemPrompt},
			{Role: "user", Content: req.UserPrompt + "\n\nPlease respond with valid JSON only."},
		}
		cr := CompletionRequest{
			Model:       req.Model,
			Messages:    retryMsgs,
			Temperature: req.Temperature,
		}

		resp, err := req.Provider.ChatCompletion(ctx, cr)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt, err)
			log.Printf("[llm] structured via %s retry %d/%d failed: %v", req.Provider.Name(), attempt, req.MaxRetries, err)
			continue
		}

		extracted := extractJSONFromResponse(resp.Content)
		if extracted != "" && isValidJSON(extracted) {
			log.Printf("[llm] structured via %s: got valid JSON on retry %d/%d", req.Provider.Name(), attempt, req.MaxRetries)
			return extracted, nil
		}

		lastErr = fmt.Errorf("attempt %d: response is not valid JSON", attempt)
		log.Printf("[llm] structured via %s retry %d/%d: invalid JSON", req.Provider.Name(), attempt, req.MaxRetries)
	}

	return "", fmt.Errorf("structured call via %s failed after %d retries: %w", req.Provider.Name(), req.MaxRetries, lastErr)
}

// structuredCallPlainJSON is the fast path for providers that don't accept
// response_format: json_schema. Appends a "respond with valid JSON only"
// instruction and retries up to MaxRetries on parse failure. No schema attempt
// is made at all — use this only when you're sure schema is unsupported.
func structuredCallPlainJSON(ctx context.Context, req StructuredRequest) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= req.MaxRetries; attempt++ {
		cr := CompletionRequest{
			Model: req.Model,
			Messages: []Message{
				{Role: "system", Content: req.SystemPrompt},
				{Role: "user", Content: req.UserPrompt + "\n\nPlease respond with valid JSON only."},
			},
			Temperature: req.Temperature,
		}
		resp, err := req.Provider.ChatCompletion(ctx, cr)
		if err != nil {
			lastErr = fmt.Errorf("attempt %d: %w", attempt, err)
			log.Printf("[llm] structured plain via %s: attempt %d/%d failed: %v",
				req.Provider.Name(), attempt, req.MaxRetries, err)
			continue
		}
		extracted := extractJSONFromResponse(resp.Content)
		if extracted != "" && isValidJSON(extracted) {
			log.Printf("[llm] structured plain via %s: ok on attempt %d/%d",
				req.Provider.Name(), attempt, req.MaxRetries)
			return extracted, nil
		}
		lastErr = fmt.Errorf("attempt %d: response is not valid JSON", attempt)
	}
	return "", fmt.Errorf("structured plain via %s failed after %d retries: %w",
		req.Provider.Name(), req.MaxRetries, lastErr)
}

// doStructuredCall makes a single LLM HTTP call, optionally with response_format.
func doStructuredCall(ctx context.Context, client *http.Client, endpoint string, req StructuredRequest, withSchema bool) (_ string, err error) {
	defer metrics.ObserveExternalCall("llm", "complete", time.Now(), &err)
	body := map[string]any{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "system", "content": req.SystemPrompt},
			{"role": "user", "content": req.UserPrompt},
		},
		"temperature": req.Temperature,
		"stream":      false,
	}

	if withSchema && req.ResponseSchema != nil {
		body["response_format"] = map[string]any{
			"type":        "json_schema",
			"json_schema": req.ResponseSchema,
		}
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("HTTP call: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 400 && withSchema {
		return "", &unsupportedSchemaError{status: 400, body: string(respBody)}
	}
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM status %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	// Parse OpenAI-compatible response
	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &llmResp); err != nil {
		return "", fmt.Errorf("parse LLM response: %w", err)
	}
	if len(llmResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}

	return llmResp.Choices[0].Message.Content, nil
}

// unsupportedSchemaError indicates the model returned 400 for json_schema mode.
type unsupportedSchemaError struct {
	status int
	body   string
}

func (e *unsupportedSchemaError) Error() string {
	return fmt.Sprintf("json_schema unsupported (HTTP %d): %s", e.status, truncate(e.body, 100))
}

// isUnsupportedError checks if the error is due to json_schema not being supported.
func isUnsupportedError(err error) bool {
	_, ok := err.(*unsupportedSchemaError)
	return ok
}

// isValidJSON checks if the string is valid JSON.
func isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}

// extractJSONFromResponse finds a JSON object in text (handles ```json ... ``` blocks).
func extractJSONFromResponse(s string) string {
	// Find first opening brace
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}

	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
