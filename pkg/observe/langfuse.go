// Package observe — Langfuse LLM tracing integration.
//
// Sends LLM generation traces to Langfuse (https://langfuse.com) for observability.
// Uses the Langfuse public ingestion API with Basic auth (publicKey:secretKey).
//
// Usage:
//
//	tracer := observe.NewLangfuseTracer("https://cloud.langfuse.com", pubKey, secKey)
//	tracer.TraceGeneration(ctx, observe.TraceData{
//	    TraceID: "abc", Name: "entity_extraction", Model: "gemma3:4b",
//	    Input: prompt, Output: resp, LatencyMs: 120, TokensIn: 100, TokensOut: 50,
//	})
package observe

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LangfuseTracer sends LLM call traces to Langfuse for observability.
type LangfuseTracer struct {
	endpoint  string // https://cloud.langfuse.com or self-hosted
	publicKey string
	secretKey string
	client    *http.Client
	enabled   bool
}

// TraceData represents a single LLM generation trace.
type TraceData struct {
	TraceID   string
	Name      string // "entity_extraction", "graph_completion", etc.
	Model     string
	Input     string
	Output    string
	LatencyMs int64
	TokensIn  int
	TokensOut int
	Status    string // "success", "error"
	Metadata  map[string]any
}

// NewLangfuseTracer creates a Langfuse tracer. If publicKey or secretKey is empty,
// the tracer is created in disabled mode (all calls are no-ops).
func NewLangfuseTracer(endpoint, publicKey, secretKey string) *LangfuseTracer {
	if endpoint == "" {
		endpoint = "https://cloud.langfuse.com"
	}
	endpoint = strings.TrimSuffix(endpoint, "/")

	return &LangfuseTracer{
		endpoint:  endpoint,
		publicKey: publicKey,
		secretKey: secretKey,
		client:    &http.Client{Timeout: 10 * time.Second},
		enabled:   publicKey != "" && secretKey != "",
	}
}

// Enabled returns whether the tracer is configured and active.
func (lt *LangfuseTracer) Enabled() bool {
	return lt.enabled
}

// Endpoint returns the configured Langfuse API endpoint.
func (lt *LangfuseTracer) Endpoint() string {
	return lt.endpoint
}

// TraceGeneration records an LLM generation (input, output, model, latency, tokens).
// Returns nil immediately if the tracer is disabled.
func (lt *LangfuseTracer) TraceGeneration(ctx context.Context, trace TraceData) error {
	if !lt.enabled {
		return nil
	}

	now := time.Now().UTC()
	startTime := now.Add(-time.Duration(trace.LatencyMs) * time.Millisecond)

	// Build generation body
	genBody := map[string]any{
		"traceId":             trace.TraceID,
		"name":                trace.Name,
		"model":               trace.Model,
		"input":               trace.Input,
		"output":              trace.Output,
		"completionStartTime": startTime.Format(time.RFC3339Nano),
		"endTime":             now.Format(time.RFC3339Nano),
		"usage": map[string]any{
			"input":  trace.TokensIn,
			"output": trace.TokensOut,
		},
	}
	if trace.Status != "" {
		genBody["statusMessage"] = trace.Status
	}
	if trace.Metadata != nil {
		genBody["metadata"] = trace.Metadata
	}

	// Langfuse batch ingestion envelope
	payload := map[string]any{
		"batch": []map[string]any{
			{
				"id":        trace.TraceID + "-gen",
				"type":      "generation-create",
				"timestamp": now.Format(time.RFC3339Nano),
				"body":      genBody,
			},
		},
	}

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("langfuse: marshal: %w", err)
	}

	url := lt.endpoint + "/api/public/ingestion"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("langfuse: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Basic auth: base64(publicKey:secretKey)
	creds := base64.StdEncoding.EncodeToString([]byte(lt.publicKey + ":" + lt.secretKey))
	req.Header.Set("Authorization", "Basic "+creds)

	resp, err := lt.client.Do(req)
	if err != nil {
		return fmt.Errorf("langfuse: HTTP call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("langfuse: status %d: %s", resp.StatusCode, truncLF(string(body), 200))
	}

	// Drain body to allow connection reuse
	io.Copy(io.Discard, resp.Body)
	return nil
}

// truncLF truncates a string to n bytes.
func truncLF(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
