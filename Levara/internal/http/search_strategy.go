// search_strategy.go — strategy pattern wrapper for the query_type
// dispatch in searchHandler (T5).
//
// Before T5 the dispatch was a hard-coded switch over query_type strings
// calling free-standing functions defined in api_search.go and
// graph_search.go. That worked but had three downsides:
//
//   1. Adding a new query_type required editing the central switch AND
//      hoping nobody else was relying on the default-to-CHUNKS fallback.
//   2. Tests couldn't substitute a stub strategy — every search test had
//      to stand up the real pipeline.
//   3. The contract (what does Execute receive, what does it return) was
//      implicit: spread across 13 function signatures.
//
// This file introduces a minimal SearchStrategy interface and a registry
// wired once during RegisterAPI. The existing handler functions
// stay in place — each is wrapped in a small struct so we don't have to
// move ~1300 lines of graph_search.go in one step. Follow-up work (still
// under T5 in 20.04-tasks.md) can extract each wrapped function into its
// own strategy_<name>.go file without touching the dispatch.
package http

import (
	"sync"

	"github.com/gofiber/fiber/v2"
)

// SearchStrategy is the narrow contract every query_type handler obeys.
// Execute returns nil on success (response already written into c) and a
// non-nil error when fiber should surface the failure. The Name method
// exists so the registry + tests can assert which strategy was chosen
// without comparing function pointers.
type SearchStrategy interface {
	Name() string
	Execute(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error
}

// StrategyRegistry maps query_type strings to strategy implementations.
// Lookups fall back to a default strategy (CHUNKS) when the query_type
// is unknown, matching the pre-T5 switch behaviour.
//
// All access goes through the embedded RWMutex (BL-5). In practice
// Register is only called at startup from main and Get is hot-path
// read-only, but treating the registry as genuinely concurrent-safe
// removes a class of "works on my laptop, races on CI" bugs and lets
// tests do `r.Register(stub)` from goroutines without ceremony.
type StrategyRegistry struct {
	mu       sync.RWMutex
	m        map[string]SearchStrategy
	fallback SearchStrategy
}

// NewDefaultStrategyRegistry wires the 13 built-in strategies. Each
// strategy is a thin adapter over the existing *Search functions —
// keeping the registry a single source of truth for query_type strings
// without rewriting the search logic in-place.
func NewDefaultStrategyRegistry() *StrategyRegistry {
	chunks := funcStrategy{name: "CHUNKS", fn: chunksSearch}
	r := &StrategyRegistry{
		m:        make(map[string]SearchStrategy, 16),
		fallback: chunks,
	}
	// Non-graph strategies — defined in api_search.go.
	r.Register(chunks)
	r.Register(funcStrategy{name: "RAG_COMPLETION", fn: ragCompletionSearch})
	r.Register(funcStrategy{name: "SUMMARIES", fn: summariesSearch})
	r.Register(funcStrategy{name: "CHUNKS_LEXICAL", fn: bm25Search})
	r.Register(funcStrategy{name: "HYBRID", fn: hybridSearch})
	r.Register(funcStrategy{name: "WEIGHTED_HYBRID", fn: hybridSearch})
	r.Register(funcStrategy{name: "TEMPORAL", fn: temporalSearch})
	// Graph strategies — defined in graph_search.go.
	r.Register(funcStrategy{name: "GRAPH_COMPLETION", fn: graphCompletionSearch})
	r.Register(funcStrategy{name: "GRAPH_SUMMARY_COMPLETION", fn: graphCompletionSearch})
	r.Register(funcStrategy{name: "GRAPH_COMPLETION_CONTEXT_EXTENSION", fn: contextExtensionSearch})
	r.Register(funcStrategy{name: "GRAPH_COMPLETION_COT", fn: cotSearch})
	r.Register(funcStrategy{name: "TRIPLET_COMPLETION", fn: tripletCompletionSearch})
	r.Register(funcStrategy{name: "NATURAL_LANGUAGE", fn: naturalLanguageSearch})
	r.Register(funcStrategy{name: "CYPHER", fn: cypherSearch})
	r.Register(funcStrategy{name: "CODE", fn: codingRulesSearch})
	r.Register(funcStrategy{name: "CODING_RULES", fn: codingRulesSearch})
	r.Register(funcStrategy{name: "COMMUNITY_LOCAL", fn: communityLocalSearch})
	r.Register(funcStrategy{name: "COMMUNITY_GLOBAL", fn: communityGlobalSearch})
	return r
}

// Register adds or replaces a strategy. Duplicate names overwrite; the
// most recent registration wins, which lets tests substitute stubs by
// calling Register after NewDefaultStrategyRegistry.
func (r *StrategyRegistry) Register(s SearchStrategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[s.Name()] = s
}

// Get returns the strategy for queryType, falling back to the default
// (CHUNKS) when unknown. Never nil.
func (r *StrategyRegistry) Get(queryType string) SearchStrategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if s, ok := r.m[queryType]; ok {
		return s
	}
	return r.fallback
}

// funcStrategy is a thin adapter that lifts a bare func into a
// SearchStrategy. Used by NewDefaultStrategyRegistry so we don't have to
// define nine different named types to wrap nine functions; the Name is
// the registry key, not a per-type identity.
type funcStrategy struct {
	name string
	fn   func(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error
}

func (f funcStrategy) Name() string { return f.name }
func (f funcStrategy) Execute(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	return f.fn(c, cfg, req)
}
