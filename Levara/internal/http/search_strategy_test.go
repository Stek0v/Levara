package http

import (
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
)

// Stub strategy — records invocation via an atomic counter. Lets tests
// assert that the registry dispatched to the right Name without having
// to stand up a real search pipeline.
type countingStrategy struct {
	name  string
	calls int64
}

func (s *countingStrategy) Name() string { return s.name }
func (s *countingStrategy) Execute(_ *fiber.Ctx, _ APIConfig, _ CogneeSearchRequest) error {
	atomic.AddInt64(&s.calls, 1)
	return nil
}

func TestStrategyRegistry_GetKnown(t *testing.T) {
	r := NewDefaultStrategyRegistry()

	cases := []struct {
		queryType string
		wantName  string
	}{
		{"CHUNKS", "CHUNKS"},
		{"RAG_COMPLETION", "RAG_COMPLETION"},
		{"GRAPH_COMPLETION", "GRAPH_COMPLETION"},
		{"GRAPH_SUMMARY_COMPLETION", "GRAPH_SUMMARY_COMPLETION"},
		{"CYPHER", "CYPHER"},
		{"CODE", "CODE"},
		{"CODING_RULES", "CODING_RULES"},
		{"COMMUNITY_LOCAL", "COMMUNITY_LOCAL"},
		{"COMMUNITY_GLOBAL", "COMMUNITY_GLOBAL"},
	}
	for _, tc := range cases {
		s := r.Get(tc.queryType)
		if s.Name() != tc.wantName {
			t.Errorf("Get(%q).Name() = %q, want %q", tc.queryType, s.Name(), tc.wantName)
		}
	}
}

func TestStrategyRegistry_FallbackOnUnknown(t *testing.T) {
	// Unknown query_type must fall through to the default (CHUNKS). This
	// preserves the pre-T5 switch behaviour where the default case in the
	// dispatch also invoked chunksSearch. Clients that misspell or send a
	// new query_type the server doesn't know get a chunks response, not
	// a 400 or 500.
	r := NewDefaultStrategyRegistry()

	s := r.Get("definitely-not-a-real-strategy")
	if s == nil {
		t.Fatal("Get(unknown) returned nil")
	}
	if s.Name() != "CHUNKS" {
		t.Errorf("Get(unknown).Name() = %q, want CHUNKS", s.Name())
	}
}

func TestStrategyRegistry_OverrideReplacesExisting(t *testing.T) {
	// Register with a duplicate name must win — this is how tests stub
	// out a strategy after NewDefaultStrategyRegistry wires the defaults.
	r := NewDefaultStrategyRegistry()
	stub := &countingStrategy{name: "CHUNKS"}
	r.Register(stub)

	s := r.Get("CHUNKS")
	if s != stub {
		t.Errorf("Get(CHUNKS) returned the original strategy, not the override")
	}
}
