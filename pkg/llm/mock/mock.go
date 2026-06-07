// Package mock provides a deterministic llm.Provider implementation for tests.
//
// Usage:
//
//	m := mock.New().
//	    On("query about X").Reply(`{"nodes":[...], "edges":[...]}`).
//	    OnAny().Reply("default response")
//	provider := m.Provider()
//	// Pass `provider` to code under test; inspect `m.Calls()` afterwards.
package mock

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/stek0v/levara/pkg/llm"
)

// Mock is a test double for llm.Provider with scripted responses.
//
// Matching rules (first match wins):
//  1. Exact match on last user message content.
//  2. Substring match (via On).
//  3. Any-match fallback (via OnAny).
//
// If no rule matches, ChatCompletion returns an error — tests fail loudly rather
// than silently picking zero-value responses.
type Mock struct {
	mu       sync.Mutex
	rules    []rule
	calls    []llm.CompletionRequest
	name     string
}

type rule struct {
	match    func(string) bool
	response string
	err      error
}

// New creates a fresh mock provider.
func New() *Mock {
	return &Mock{name: "mock"}
}

// WithName sets the provider name (returned from Provider.Name()).
func (m *Mock) WithName(n string) *Mock { m.name = n; return m }

// On matches requests whose last user message contains substr.
// Returns a builder; call .Reply or .Fail to finish the rule.
func (m *Mock) On(substr string) *ruleBuilder {
	return &ruleBuilder{m: m, match: func(s string) bool { return strings.Contains(s, substr) }}
}

// OnAny matches any request (fallback). Chain with .Reply or .Fail.
func (m *Mock) OnAny() *ruleBuilder {
	return &ruleBuilder{m: m, match: func(string) bool { return true }}
}

type ruleBuilder struct {
	m     *Mock
	match func(string) bool
}

// Reply registers a scripted text response for the current matcher.
func (b *ruleBuilder) Reply(content string) *Mock {
	b.m.mu.Lock()
	b.m.rules = append(b.m.rules, rule{match: b.match, response: content})
	b.m.mu.Unlock()
	return b.m
}

// Fail registers a scripted error for the current matcher.
func (b *ruleBuilder) Fail(err error) *Mock {
	b.m.mu.Lock()
	b.m.rules = append(b.m.rules, rule{match: b.match, err: err})
	b.m.mu.Unlock()
	return b.m
}

// Calls returns a copy of all ChatCompletion requests observed.
func (m *Mock) Calls() []llm.CompletionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]llm.CompletionRequest, len(m.calls))
	copy(out, m.calls)
	return out
}

// Provider returns the mock wrapped as llm.Provider.
func (m *Mock) Provider() llm.Provider { return &mockProvider{m: m} }

type mockProvider struct{ m *Mock }

func (p *mockProvider) Name() string { return p.m.name }

func (p *mockProvider) ChatCompletion(ctx context.Context, req llm.CompletionRequest) (*llm.CompletionResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p.m.mu.Lock()
	p.m.calls = append(p.m.calls, req)
	rules := p.m.rules // copy slice header; safe for read since we hold lock
	p.m.mu.Unlock()

	last := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			last = req.Messages[i].Content
			break
		}
	}

	for _, r := range rules {
		if !r.match(last) {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		resp := &llm.CompletionResponse{
			Content: r.response,
			Model:   req.Model,
		}
		resp.Usage.PromptTokens = len(last) / 4
		resp.Usage.CompletionTokens = len(r.response) / 4
		return resp, nil
	}

	return nil, fmt.Errorf("mock: no matching rule for user message: %q", last)
}
