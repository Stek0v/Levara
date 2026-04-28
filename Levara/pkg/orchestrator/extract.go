// extract.go — LLM-side helpers split out of pipeline.go (ARCH-2).
//
// The orchestrator pipeline.go file is the canonical god-object of this
// package — Run + RunWithItems span hundreds of lines because the five
// stages share a lot of intermediate state. We don't unwind that here;
// instead we lift out the four self-contained LLM-extraction helpers
// (useStructuredOutput, extractEntities, parseEntities, extractJSON) so
// the file with the actual pipeline is shorter and the LLM-IO surface
// has its own home.
//
// Behaviour is byte-for-byte the same as before the move; the only
// difference is which file `git blame` will point at.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/graph"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
)

// useStructuredOutput is the convenience accessor for the optional bool
// pointer in Config. nil means "default true" — every caller wants
// JSON-Schema mode unless they explicitly opt out.
func useStructuredOutput(cfg Config) bool {
	if cfg.UseStructuredOutput == nil {
		return true
	}
	return *cfg.UseStructuredOutput
}

// extractEntities calls the configured LLM to produce a knowledge-graph
// payload (nodes + edges) from a chunk of text. The flow:
//
//  1. Cache lookup keyed on (model, text, sysPrompt, temperature). A hit
//     short-circuits the HTTP call entirely.
//  2. Structured-output (JSON Schema) attempt when enabled — production
//     LLM endpoints (Ollama, vLLM, OpenAI) all support response_format.
//     Falls back to the raw-content path when the structured call errors.
//  3. Provider abstraction (cfg.LLMProvider) when wired; otherwise raw
//     OpenAI-compatible POST. The legacy raw path stays so deployments
//     without a Provider still work.
//
// The returned content is fed through parseEntities — the LLM is trusted
// to produce JSON, but parseEntities tolerates markdown wrappers via
// extractJSON.
func extractEntities(ctx context.Context, client *http.Client, cfg Config, text string) ([]graph.DedupNode, []graph.DedupEdge, error) {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = defaultExtractionPrompt
	}

	var cacheKey string
	if cfg.LLMCache != nil {
		cacheKey = llmcache.Key(cfg.LLMModel, text, sysPrompt, cfg.Temperature)
		if cached, ok := cfg.LLMCache.Get(cacheKey); ok {
			return parseEntities(cached)
		}
	}

	if useStructuredOutput(cfg) && cfg.LLMEndpoint != "" && cfg.LLMModel != "" {
		result, err := llm.StructuredCall(ctx, llm.StructuredRequest{
			Endpoint:       cfg.LLMEndpoint,
			Model:          cfg.LLMModel,
			SystemPrompt:   sysPrompt,
			UserPrompt:     text,
			Temperature:    cfg.Temperature,
			ResponseSchema: llm.KnowledgeGraphSchema,
			MaxRetries:     3,
			Client:         client,
			Provider:       cfg.LLMProvider, // nil → legacy HTTP, non-nil → use provider
		})
		if err == nil {
			if cfg.LLMCache != nil && cacheKey != "" {
				cfg.LLMCache.Put(cacheKey, result, cfg.LLMModel)
			}
			return parseEntities(result)
		}
		log.Printf("[pipeline] structured output failed, fallback to regex: %v", err)
	}

	// Provider abstraction wins when wired — gives us tracing + rate
	// limiting wrappers configured in main.
	if cfg.LLMProvider != nil {
		provResp, provErr := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{
			Model: cfg.LLMModel,
			Messages: []llm.Message{
				{Role: "system", Content: sysPrompt},
				{Role: "user", Content: text},
			},
			Temperature: cfg.Temperature,
		})
		if provErr != nil {
			return nil, nil, fmt.Errorf("LLM provider call: %w", provErr)
		}
		content := provResp.Content
		if cfg.LLMCache != nil && cacheKey != "" {
			cfg.LLMCache.Put(cacheKey, content, cfg.LLMModel)
		}
		return parseEntities(content)
	}

	// Raw OpenAI-compatible HTTP path for deployments without a Provider.
	reqBody, _ := json.Marshal(map[string]any{
		"model": cfg.LLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": text},
		},
		"temperature": cfg.Temperature,
		"stream":      false,
	})

	endpoint := cfg.LLMEndpoint
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint = strings.TrimSuffix(endpoint, "/") + "/chat/completions"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM call: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("LLM status %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &llmResp); err != nil {
		return nil, nil, fmt.Errorf("parse LLM response: %w", err)
	}
	if len(llmResp.Choices) == 0 {
		return nil, nil, fmt.Errorf("LLM returned no choices")
	}

	content := llmResp.Choices[0].Message.Content
	if cfg.LLMCache != nil && cacheKey != "" {
		cfg.LLMCache.Put(cacheKey, content, cfg.LLMModel)
	}
	return parseEntities(content)
}

// parseEntities turns LLM output text into DedupNode + DedupEdge slices.
// Tolerates markdown-wrapped JSON via extractJSON. Per-node confidence
// defaults to 1.0 — a richer pipeline could pull confidence from the LLM
// when models support it.
func parseEntities(content string) ([]graph.DedupNode, []graph.DedupEdge, error) {
	var kg struct {
		Nodes []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"nodes"`
		Edges []struct {
			Source           string `json:"source"`
			Target           string `json:"target"`
			Relationship     string `json:"relationship"`
			RelationshipName string `json:"relationship_name"`
			EdgeText         string `json:"edge_text"`
		} `json:"edges"`
	}

	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return nil, nil, fmt.Errorf("no JSON found in LLM response")
	}

	if err := json.Unmarshal([]byte(jsonStr), &kg); err != nil {
		return nil, nil, fmt.Errorf("parse entities JSON: %w", err)
	}

	nodes := make([]graph.DedupNode, len(kg.Nodes))
	for i, n := range kg.Nodes {
		id := n.ID
		if id == "" {
			id = graph.GenerateNodeID(n.Name)
		}
		nodes[i] = graph.DedupNode{
			ID: id, Name: n.Name, Type: n.Type, Description: n.Description,
			Confidence:  1.0,
			ExtractedAt: time.Now().UTC().Format(time.RFC3339),
		}
	}

	edges := make([]graph.DedupEdge, len(kg.Edges))
	for i, e := range kg.Edges {
		relName := e.Relationship
		if relName == "" {
			relName = e.RelationshipName
		}
		edges[i] = graph.DedupEdge{
			SourceID:         e.Source,
			TargetID:         e.Target,
			RelationshipName: relName,
			EdgeText:         e.EdgeText,
		}
	}
	return nodes, edges, nil
}

// extractJSON finds the first {...} object in s, tolerating markdown
// fences and surrounding prose. Brace-counting is good enough — the LLM
// occasionally produces a } inside a string field, but real-world output
// from gpt-4o / claude / llama-class models has been clean enough that a
// proper JSON tokeniser hasn't been worth the dependency.
func extractJSON(s string) string {
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
