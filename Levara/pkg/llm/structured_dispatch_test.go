package llm

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Unit tests for the endpoint-aware dispatch in StructuredCall — no network.
//
// Validates:
//   - DeepSeek endpoint (seeded in init) triggers fast path: ZERO schema
//     attempt, one plain-JSON call.
//   - Learned endpoint (after a simulated 400 response) triggers fast path
//     on the NEXT call.
//   - Provider without Endpoint() method falls back to the legacy path.

type fakeProvider struct {
	name      string
	endpoint  string
	calls     atomic.Int32
	schemaReq atomic.Int32 // count requests that included response_format
	errOnce   atomic.Bool  // if true, first call returns a 400-like error
	reply     string
}

func (f *fakeProvider) Name() string     { return f.name }
func (f *fakeProvider) Endpoint() string { return f.endpoint }

func (f *fakeProvider) ChatCompletion(_ context.Context, req CompletionRequest) (*CompletionResponse, error) {
	f.calls.Add(1)
	if req.ResponseFormat != nil {
		f.schemaReq.Add(1)
	}
	if f.errOnce.Load() && req.ResponseFormat != nil {
		f.errOnce.Store(false)
		return nil, errors.New(`OpenAI status 400: {"error":{"message":"This response_format type is unavailable now"}}`)
	}
	resp := &CompletionResponse{Content: f.reply, Model: req.Model}
	return resp, nil
}

func TestStructuredCall_DeepSeekFastPath_NoSchemaAttempt(t *testing.T) {
	p := &fakeProvider{
		name:     "openai",
		endpoint: "https://api.deepseek.com/v1",
		reply:    `{"nodes":[],"edges":[]}`,
	}
	out, err := StructuredCall(context.Background(), StructuredRequest{
		Model:          "deepseek-chat",
		SystemPrompt:   "s",
		UserPrompt:     "u",
		ResponseSchema: KnowledgeGraphSchema,
		MaxRetries:     2,
		Provider:       p,
	})
	if err != nil {
		t.Fatalf("StructuredCall: %v", err)
	}
	if out == "" {
		t.Fatal("empty output")
	}
	if got := p.schemaReq.Load(); got != 0 {
		t.Errorf("schema-attempted calls = %d, want 0 (fast path should skip)", got)
	}
	if got := p.calls.Load(); got != 1 {
		t.Errorf("total calls = %d, want 1 (single plain call)", got)
	}
}

func TestStructuredCall_LearnsFromRejection(t *testing.T) {
	// Use an endpoint NOT in the seeded list; simulate a 400 on schema.
	const learnedEP = "https://example-unknown-provider.test/v1"

	// Reset cache entry before/after — sync.Map is process-global.
	schemaUnsupportedEndpoints.Delete(learnedEP)
	t.Cleanup(func() { schemaUnsupportedEndpoints.Delete(learnedEP) })

	p := &fakeProvider{
		name:     "openai",
		endpoint: learnedEP,
		reply:    `{"nodes":[],"edges":[]}`,
	}
	p.errOnce.Store(true)

	// First call: initial schema attempt rejected (errOnce), then retry
	// succeeds via plain-JSON path. Endpoint should be memoized.
	_, err := StructuredCall(context.Background(), StructuredRequest{
		Model:          "some-model",
		ResponseSchema: KnowledgeGraphSchema,
		MaxRetries:     2,
		Provider:       p,
	})
	if err != nil {
		t.Fatalf("first StructuredCall: %v", err)
	}
	// First call: 1 schema attempt (rejected) + at least 1 plain retry.
	if got := p.schemaReq.Load(); got != 1 {
		t.Errorf("first-call schema attempts = %d, want 1", got)
	}
	if _, ok := schemaUnsupportedEndpoints.Load(learnedEP); !ok {
		t.Fatal("endpoint was not memoized after 400 rejection")
	}

	// Second call with a fresh provider on the same endpoint: must skip
	// schema entirely thanks to memoization.
	p2 := &fakeProvider{
		name:     "openai",
		endpoint: learnedEP,
		reply:    `{"nodes":[],"edges":[]}`,
	}
	_, err = StructuredCall(context.Background(), StructuredRequest{
		Model:          "some-model",
		ResponseSchema: KnowledgeGraphSchema,
		MaxRetries:     2,
		Provider:       p2,
	})
	if err != nil {
		t.Fatalf("second StructuredCall: %v", err)
	}
	if got := p2.schemaReq.Load(); got != 0 {
		t.Errorf("second-call schema attempts = %d, want 0 (should hit fast path via memo)", got)
	}
	if got := p2.calls.Load(); got != 1 {
		t.Errorf("second-call total = %d, want 1", got)
	}
}

func TestStructuredCall_UnknownEndpointTriesSchemaFirst(t *testing.T) {
	// Provider that accepts schema and returns good JSON on first try.
	// No memo, no seeded-bad list match → must attempt schema path.
	const freshEP = "https://fresh-endpoint.test/v1"
	schemaUnsupportedEndpoints.Delete(freshEP)
	t.Cleanup(func() { schemaUnsupportedEndpoints.Delete(freshEP) })

	p := &fakeProvider{
		name:     "openai",
		endpoint: freshEP,
		reply:    `{"nodes":[],"edges":[]}`,
	}
	_, err := StructuredCall(context.Background(), StructuredRequest{
		Model:          "some-model",
		ResponseSchema: KnowledgeGraphSchema,
		MaxRetries:     1,
		Provider:       p,
	})
	if err != nil {
		t.Fatalf("StructuredCall: %v", err)
	}
	if got := p.schemaReq.Load(); got != 1 {
		t.Errorf("schema attempts = %d, want 1", got)
	}
}

func TestStructuredCall_ProviderWithoutEndpoint_NoFastPath(t *testing.T) {
	// A Provider that does NOT implement Endpoint() must still work via the
	// legacy path (try schema first). Covers Anthropic and custom providers.
	type noEndpointProvider struct{ fakeProvider }
	// Shadow Endpoint() to hide it via method-set gymnastics: wrap it.
	p := &struct{ Provider }{Provider: (func() Provider {
		m := &fakeProvider{name: "anthropic", endpoint: "ignored", reply: `{"nodes":[],"edges":[]}`}
		return onlyCompletionProvider{m}
	})()}

	_, err := StructuredCall(context.Background(), StructuredRequest{
		Model:          "claude-x",
		ResponseSchema: KnowledgeGraphSchema,
		MaxRetries:     1,
		Provider:       p.Provider,
	})
	if err != nil {
		t.Fatalf("StructuredCall: %v", err)
	}
}

// onlyCompletionProvider hides Endpoint() from the wrapped provider so the
// endpointer type assertion in providerEndpoint returns false.
type onlyCompletionProvider struct{ inner Provider }

func (o onlyCompletionProvider) Name() string { return o.inner.Name() }
func (o onlyCompletionProvider) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	return o.inner.ChatCompletion(ctx, req)
}

func TestLooksLikeResponseFormatReject(t *testing.T) {
	cases := map[string]bool{
		"OpenAI status 400: response_format unavailable":                                 true,
		"OpenAI status 400: {\"error\":{\"message\":\"This response_format ...\"}}":      true,
		"OpenAI status 400: {\"message\":\"unavailable now\"}":                            true,
		"status 400 invalid parameter: json_schema":                                      true,
		"OpenAI status 500: internal":                                                    false,
		"timeout":                                                                        false,
	}
	for msg, want := range cases {
		if got := looksLikeResponseFormatReject(msg); got != want {
			t.Errorf("looksLikeResponseFormatReject(%q) = %v, want %v", msg, got, want)
		}
	}
}

// Concurrent access safety: many goroutines calling endpointSkipsJSONSchema
// and rememberEndpointUnsupportsSchema together must not race.
func TestSchemaCache_ConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p := &fakeProvider{endpoint: "https://concurrent-test.example/v1"}
			for j := 0; j < 50; j++ {
				_ = endpointSkipsJSONSchema(p)
				if id%2 == 0 {
					rememberEndpointUnsupportsSchema(p)
				}
			}
		}(i)
	}
	wg.Wait()
	schemaUnsupportedEndpoints.Delete("https://concurrent-test.example/v1")
}

// Smoke: ensure the seeded DeepSeek entry is present.
func TestSeededEndpoints_DeepSeek(t *testing.T) {
	if _, ok := schemaUnsupportedEndpoints.Load("api.deepseek.com"); !ok {
		t.Fatal("api.deepseek.com not seeded")
	}
	if !strings.Contains("https://api.deepseek.com/v1", "api.deepseek.com") {
		t.Fatal("sanity check failed")
	}
}
