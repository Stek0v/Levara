package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/router"
)

// scoredRes builds a pipeline.ScoredResult with a {"text":"..."} metadata
// blob so tests can eyeball IDs in the JSON response.
func scoredRes(id string, score float32) pipeline.ScoredResult {
	return pipeline.ScoredResult{
		ID:       id,
		Score:    score,
		Metadata: json.RawMessage(`{"text":"` + id + `"}`),
	}
}

// decodeSearchResp unmarshals the JSON text the tool returns. Used
// instead of string-matching so test failures point at the offending
// field rather than a hash of text.
func decodeSearchResp(t *testing.T, res ToolResult) map[string]any {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("no content in ToolResult")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("response is not JSON: %s", res.Content[0].Text)
	}
	return out
}

func TestToolSearch_MissingQuery(t *testing.T) {
	deps := &fakeDeps{}
	res := ToolSearch(context.Background(), deps, map[string]any{})
	if !res.IsError {
		t.Fatal("want IsError when search_query missing")
	}
	if !strings.Contains(res.Content[0].Text, "'search_query' required") {
		t.Errorf("unexpected error text: %q", res.Content[0].Text)
	}
}

func TestToolSearch_EmbedNotConfigured(t *testing.T) {
	// searchPipelineFn not set → NewSearchPipeline returns nil →
	// tool returns the "not configured" text, not an IsError.
	deps := &fakeDeps{}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "what is love",
	})
	if res.IsError {
		t.Errorf("unexpected IsError; text=%q", res.Content[0].Text)
	}
	if res.Content[0].Text != "No results (embedding service not configured)" {
		t.Errorf("wrong missing-embed text: %q", res.Content[0].Text)
	}
}

func TestToolSearch_DefaultBranchReturnsResults(t *testing.T) {
	// No flags → SearchByText path, single collection, 3 results.
	var gotColl, gotQuery string
	var gotK int32
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			gotColl = coll
			gotQuery = query
			atomic.StoreInt32(&gotK, int32(topK))
			return []pipeline.ScoredResult{
				scoredRes("a", 0.9),
				scoredRes("b", 0.7),
				scoredRes("c", 0.5),
			}, nil
		},
	}
	deps := &fakeDeps{
		collections: []string{"default"},
		hasColls:    true,
		searchPipelineFn: func(doRerank bool) SearchPipeline {
			if doRerank {
				t.Error("doRerank should be false for default branch")
			}
			return fakePipe
		},
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "BASIC", // skip AUTO → skip router
	})
	if res.IsError {
		t.Fatalf("unexpected IsError: %q", res.Content[0].Text)
	}
	if gotColl != "default" {
		t.Errorf("SearchByText coll=%q, want default", gotColl)
	}
	if gotQuery != "q" {
		t.Errorf("SearchByText query=%q, want q", gotQuery)
	}
	if atomic.LoadInt32(&gotK) != int32(searchDefaultTopK) {
		t.Errorf("SearchByText topK=%d, want %d", gotK, searchDefaultTopK)
	}

	resp := decodeSearchResp(t, res)
	results := resp["results"].([]any)
	if len(results) != 3 {
		t.Errorf("got %d results, want 3", len(results))
	}
	if resp["search_type"] != "BASIC" {
		t.Errorf("search_type=%v, want BASIC", resp["search_type"])
	}
	if resp["reranked"] != false {
		t.Errorf("reranked=%v, want false", resp["reranked"])
	}
}

func TestToolSearch_TopKCaps(t *testing.T) {
	results := []pipeline.ScoredResult{}
	for i := 0; i < 20; i++ {
		results = append(results, scoredRes("r"+string(rune('a'+i)), 1.0-float32(i)*0.01))
	}
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			return results, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "BASIC",
		"top_k":        float64(5),
	})
	resp := decodeSearchResp(t, res)
	out := resp["results"].([]any)
	if len(out) != 5 {
		t.Errorf("got %d results, want top_k=5", len(out))
	}
}

func TestToolSearch_RerankBranch(t *testing.T) {
	var byTextCalled, byTextWithRerankCalled int32
	fakePipe := &fakeSearchPipeline{
		rerankEnabled: true,
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			atomic.AddInt32(&byTextCalled, 1)
			return nil, nil
		},
		byTextWithRerank: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, bool, error) {
			atomic.AddInt32(&byTextWithRerankCalled, 1)
			return []pipeline.ScoredResult{scoredRes("r1", 0.95)}, true, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(doRerank bool) SearchPipeline {
			if !doRerank {
				t.Error("doRerank should be true when rerank:true")
			}
			return fakePipe
		},
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "RERANK",
	})
	if atomic.LoadInt32(&byTextCalled) != 0 {
		t.Error("SearchByText should not be called in rerank branch")
	}
	if atomic.LoadInt32(&byTextWithRerankCalled) != 1 {
		t.Errorf("SearchByTextWithRerank called %d times, want 1", byTextWithRerankCalled)
	}
	resp := decodeSearchResp(t, res)
	if resp["reranked"] != true {
		t.Errorf("reranked=%v, want true", resp["reranked"])
	}
}

func TestToolSearch_RerankFallsBackWhenDisabled(t *testing.T) {
	// doRerank=true but RerankEnabled()=false → fall through to
	// default SearchByText branch. reranked stays false.
	var byTextCalled, byTextWithRerankCalled int32
	fakePipe := &fakeSearchPipeline{
		rerankEnabled: false,
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			atomic.AddInt32(&byTextCalled, 1)
			return []pipeline.ScoredResult{scoredRes("a", 0.9)}, nil
		},
		byTextWithRerank: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, bool, error) {
			atomic.AddInt32(&byTextWithRerankCalled, 1)
			return nil, true, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "RERANK",
	})
	if atomic.LoadInt32(&byTextWithRerankCalled) != 0 {
		t.Error("SearchByTextWithRerank called despite RerankEnabled=false")
	}
	if atomic.LoadInt32(&byTextCalled) != 1 {
		t.Errorf("SearchByText called %d times, want 1 (fallback)", byTextCalled)
	}
	resp := decodeSearchResp(t, res)
	if resp["reranked"] != false {
		t.Errorf("reranked=%v, want false (fallback)", resp["reranked"])
	}
}

func TestToolSearch_ParentChildBranch(t *testing.T) {
	var parentChildCalled int32
	fakePipe := &fakeSearchPipeline{
		byTextParentChild: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			atomic.AddInt32(&parentChildCalled, 1)
			return []pipeline.ScoredResult{scoredRes("pc1", 0.8)}, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "PARENT_CHILD",
	})
	if atomic.LoadInt32(&parentChildCalled) != 1 {
		t.Errorf("SearchByTextParentChild called %d times, want 1", parentChildCalled)
	}
}

func TestToolSearch_MultiQueryBranchSkippedWhenNoLLMProvider(t *testing.T) {
	// MULTI_QUERY with LLMProvider=nil should fall through to
	// default SearchByText branch (matches pre-refactor case guard).
	var byTextCalled, multiCalled int32
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			atomic.AddInt32(&byTextCalled, 1)
			return []pipeline.ScoredResult{scoredRes("fallback", 0.5)}, nil
		},
		byTextMultiQuery: func(ctx context.Context, coll, query string, topK int, p llm.Provider, model string, n int) ([]pipeline.ScoredResult, error) {
			atomic.AddInt32(&multiCalled, 1)
			return nil, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
		llmProvider:      nil,
	}
	ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "MULTI_QUERY",
	})
	if atomic.LoadInt32(&multiCalled) != 0 {
		t.Error("MultiQuery called despite LLMProvider=nil")
	}
	if atomic.LoadInt32(&byTextCalled) != 1 {
		t.Errorf("SearchByText called %d times, want 1 (fallback when LLMProvider=nil)", byTextCalled)
	}
}

func TestToolSearch_AUTORoutesThroughRouter(t *testing.T) {
	// AUTO with minimal caps → router.Route runs. We can't predict
	// the exact output without duplicating router logic, so just
	// assert: (a) searchType changed from AUTO, (b) routing metadata
	// is present with source="routed".
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			return []pipeline.ScoredResult{scoredRes("a", 0.9)}, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
		capabilities: router.Capabilities{
			HasEmbedding: true,
		},
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		// "search_type" omitted → default AUTO
	})
	resp := decodeSearchResp(t, res)
	if resp["search_type"] == "AUTO" {
		t.Errorf("AUTO should be resolved by the router, still %v", resp["search_type"])
	}
	routing, ok := resp["routing"].(map[string]any)
	if !ok {
		t.Fatal("AUTO should produce routing metadata in response")
	}
	if routing["source"] != "routed" {
		t.Errorf("routing.source=%v, want 'routed'", routing["source"])
	}
}

func TestToolSearch_ModeRagCoercesGraphType(t *testing.T) {
	// mode=rag + search_type=GRAPH_COMPLETION → coerce to CHUNKS.
	// No router (mode gating happens before routing). Observe via
	// the final searchType in the response.
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			return []pipeline.ScoredResult{scoredRes("a", 0.5)}, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "GRAPH_COMPLETION",
		"mode":         "rag",
	})
	resp := decodeSearchResp(t, res)
	if resp["search_type"] != "CHUNKS" {
		t.Errorf("search_type=%v, want CHUNKS (mode=rag coerces graph types)", resp["search_type"])
	}
}

func TestToolSearch_MetadataFilterOverfetchAndDrop(t *testing.T) {
	// topK=2 with room filter → fetchK = 6. Return 6 results, 4
	// matching room="alpha" → final 2 returned (capped at topK).
	var gotFetchK int32
	fakePipe := &fakeSearchPipeline{
		byText: func(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
			atomic.StoreInt32(&gotFetchK, int32(topK))
			return []pipeline.ScoredResult{
				{ID: "1", Score: 0.9, Metadata: json.RawMessage(`{"room":"alpha"}`)},
				{ID: "2", Score: 0.8, Metadata: json.RawMessage(`{"room":"beta"}`)},
				{ID: "3", Score: 0.7, Metadata: json.RawMessage(`{"room":"alpha"}`)},
				{ID: "4", Score: 0.6, Metadata: json.RawMessage(`{"room":"beta"}`)},
				{ID: "5", Score: 0.5, Metadata: json.RawMessage(`{"room":"alpha"}`)},
				{ID: "6", Score: 0.4, Metadata: json.RawMessage(`{"room":"alpha"}`)},
			}, nil
		},
	}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "BASIC",
		"top_k":        float64(2),
		"room":         "alpha",
	})
	if atomic.LoadInt32(&gotFetchK) != int32(2*searchMetaOverfetchFactor) {
		t.Errorf("fetchK=%d, want %d (overfetch factor)", gotFetchK, 2*searchMetaOverfetchFactor)
	}
	resp := decodeSearchResp(t, res)
	out := resp["results"].([]any)
	if len(out) != 2 {
		t.Errorf("got %d results, want 2 (topK cap)", len(out))
	}
	for _, r := range out {
		id := r.(map[string]any)["id"].(string)
		if id == "2" || id == "4" {
			t.Errorf("result %s (room=beta) leaked past filter", id)
		}
	}
}

func TestToolSearch_EmptyResultsEncodesAsEmptyArray(t *testing.T) {
	// No results → response.results must be [], not omitted / null.
	fakePipe := &fakeSearchPipeline{}
	deps := &fakeDeps{
		collections:      []string{"default"},
		hasColls:         true,
		searchPipelineFn: func(bool) SearchPipeline { return fakePipe },
	}
	res := ToolSearch(context.Background(), deps, map[string]any{
		"search_query": "q",
		"search_type":  "BASIC",
	})
	resp := decodeSearchResp(t, res)
	arr, ok := resp["results"].([]any)
	if !ok {
		t.Fatalf("results should be []any, got %T", resp["results"])
	}
	if len(arr) != 0 {
		t.Errorf("want empty array, got %d items", len(arr))
	}
}

// ── pure-helper tests ──

func TestSearchTypesForMode(t *testing.T) {
	if rag := searchTypesForMode("rag"); !rag["CHUNKS"] || !rag["HYBRID"] || rag["GRAPH_COMPLETION"] {
		t.Errorf("rag whitelist wrong: %v", rag)
	}
	if graph := searchTypesForMode("graph"); !graph["GRAPH_COMPLETION"] || graph["CHUNKS"] {
		t.Errorf("graph whitelist wrong: %v", graph)
	}
	if searchTypesForMode("auto") != nil {
		t.Error("auto mode should be unrestricted (nil)")
	}
}

func TestDefaultTypeForMode(t *testing.T) {
	if defaultTypeForMode("rag") != "CHUNKS" {
		t.Errorf("rag default = %q, want CHUNKS", defaultTypeForMode("rag"))
	}
	if defaultTypeForMode("graph") != "GRAPH_COMPLETION" {
		t.Errorf("graph default = %q, want GRAPH_COMPLETION", defaultTypeForMode("graph"))
	}
	if defaultTypeForMode("auto") != "AUTO" {
		t.Errorf("auto default = %q, want AUTO", defaultTypeForMode("auto"))
	}
}

func TestApplyModeGating(t *testing.T) {
	cases := []struct {
		mode, in, want string
	}{
		{"auto", "RERANK", "RERANK"},
		{"full", "RERANK", "RERANK"},
		{"rag", "GRAPH_COMPLETION", "CHUNKS"}, // outside whitelist → coerce
		{"rag", "CHUNKS", "CHUNKS"},           // in whitelist → keep
		{"rag", "AUTO", "AUTO"},               // AUTO passes through
		{"rag", "", ""},                       // empty passes through
		{"graph", "CHUNKS", "GRAPH_COMPLETION"},
		{"graph", "GRAPH_COMPLETION", "GRAPH_COMPLETION"},
	}
	for _, c := range cases {
		got := applyModeGating(c.mode, c.in)
		if got != c.want {
			t.Errorf("applyModeGating(%q, %q) = %q, want %q", c.mode, c.in, got, c.want)
		}
	}
}

func TestApplyTypeFlags(t *testing.T) {
	cases := []struct {
		searchType               string
		wantParent, wantMulti    bool
		wantRerank, wantGraphRer bool
	}{
		{"PARENT_CHILD", true, false, false, false},
		{"MULTI_QUERY", false, true, false, false},
		{"RERANK", false, false, true, false},
		{"GRAPH_RERANK", false, false, false, true},
		{"BASIC", false, false, false, false},
		{"CHUNKS", false, false, false, false},
		{"AUTO", false, false, false, false},
		{"parent_child", true, false, false, false}, // case insensitive
	}
	for _, c := range cases {
		var a searchArgs
		applyTypeFlags(c.searchType, &a)
		if a.doParentChild != c.wantParent || a.doMultiQuery != c.wantMulti ||
			a.doRerank != c.wantRerank || a.doGraphRerank != c.wantGraphRer {
			t.Errorf("applyTypeFlags(%q) flags = {pc:%v mq:%v r:%v gr:%v}, want {pc:%v mq:%v r:%v gr:%v}",
				c.searchType,
				a.doParentChild, a.doMultiQuery, a.doRerank, a.doGraphRerank,
				c.wantParent, c.wantMulti, c.wantRerank, c.wantGraphRer)
		}
	}
}

func TestParseSearchArgs_Defaults(t *testing.T) {
	a := parseSearchArgs(map[string]any{"search_query": "hello"})
	if a.query != "hello" {
		t.Errorf("query=%q", a.query)
	}
	if a.searchType != "AUTO" {
		t.Errorf("searchType=%q, want AUTO", a.searchType)
	}
	if a.mode != "auto" {
		t.Errorf("mode=%q, want auto", a.mode)
	}
	if a.topK != searchDefaultTopK {
		t.Errorf("topK=%d, want %d", a.topK, searchDefaultTopK)
	}
	if !a.doDedup {
		t.Error("doDedup should default to true")
	}
}

func TestParseSearchArgs_Overrides(t *testing.T) {
	a := parseSearchArgs(map[string]any{
		"search_query": "q",
		"search_type":  "RERANK",
		"mode":         "rag",
		"top_k":        float64(25),
		"collection":   "levara",
		"room":         "auth",
		"tags":         []any{"sec", "", "prod"},
		"rerank":       true,
		"parent_child": true,
		"multi_query":  true,
		"dedup":        false,
		"graph_rerank": true,
	})
	if a.searchType != "RERANK" || a.mode != "rag" || a.topK != 25 || a.collection != "levara" {
		t.Errorf("basic overrides wrong: %+v", a)
	}
	if a.roomFilter != "auth" {
		t.Errorf("roomFilter=%q, want auth", a.roomFilter)
	}
	if len(a.tagFilters) != 2 || a.tagFilters[0] != "sec" || a.tagFilters[1] != "prod" {
		t.Errorf("tagFilters=%v, want [sec prod] (empty strings dropped)", a.tagFilters)
	}
	if !a.doRerank || !a.doParentChild || !a.doMultiQuery || !a.doGraphRerank {
		t.Errorf("flags not all set: %+v", a)
	}
	if a.doDedup {
		t.Error("dedup:false should override default")
	}
}

