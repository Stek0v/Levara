package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/embed"
)

func TestT10SearchMetadataBeforeDedupAndRerank(t *testing.T) {
	for _, typ := range []string{"CHUNKS", "RERANK", "HYBRID"} {
		t.Run(typ, func(t *testing.T) {
			rows := []pipeline.ScoredResult{
				{ID: "wrong", Score: .9, Metadata: json.RawMessage(`{"text":"same source","room":"other","tags":["x"]}`)},
				{ID: "match", Score: .8, Metadata: json.RawMessage(`{"text":"same source","room":"wanted","tags":["b"]}`)},
			}
			sp := &fakeSearchPipeline{rerankEnabled: true, byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
				return append([]pipeline.ScoredResult(nil), rows...), nil
			}, applyRerank: func(_ context.Context, _ string, in []pipeline.ScoredResult, _ int) (bool, []pipeline.ScoredResult) {
				if len(in) != 1 || in[0].ID != "match" {
					t.Fatalf("provider received nonmatching sources: %+v", in)
				}
				return true, in
			}}
			deps := &fakeDeps{collections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return sp }}
			args := map[string]any{"search_query": "q", "search_type": typ, "room": "wanted", "tags": []any{"a", "b"}, "top_k": 1, "rerank": typ != "CHUNKS"}
			body := decodeSearchResp(t, ToolSearch(context.Background(), deps, args))
			got := body["results"].([]any)
			if len(got) != 1 || got[0].(map[string]any)["id"] != "match" {
				t.Fatalf("results=%v", got)
			}
			args["tags"] = []any{"absent"}
			sp.applyRerank = func(context.Context, string, []pipeline.ScoredResult, int) (bool, []pipeline.ScoredResult) {
				t.Fatal("empty filter sent to provider")
				return false, nil
			}
			body = decodeSearchResp(t, ToolSearch(context.Background(), deps, args))
			if len(body["results"].([]any)) != 0 {
				t.Fatalf("zero-match results=%v", body)
			}
		})
	}
}

func TestT10HybridRechecksAuthorityBeforeFallback(t *testing.T) {
	sp := &fakeSearchPipeline{byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
		return nil, errors.New("provider unavailable")
	}}
	deps := &fakeDeps{collections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return sp }, lexicalFn: func(string, string, int) ([]LexicalResult, error) {
		return []LexicalResult{{ID: "secret", Score: 1, Metadata: []byte(`{"text":"secret"}`)}}, nil
	}}
	ctx := WithSearchAccess(context.Background(), SearchAccess{Filter: func(context.Context, []pipeline.ScoredResult) ([]pipeline.ScoredResult, error) {
		return nil, errors.New("authority unavailable")
	}})
	if result := ToolSearch(ctx, deps, map[string]any{"search_query": "q", "search_type": "HYBRID"}); !result.IsError {
		t.Fatalf("authority failure returned results: %+v", result)
	}
}

func TestT10HybridWithoutPipelineUsesLexical(t *testing.T) {
	for _, typ := range []string{"HYBRID", "WEIGHTED_HYBRID"} {
		t.Run(typ, func(t *testing.T) {
			calls := 0
			deps := &fakeDeps{lexicalCollections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return nil }, lexicalFn: func(string, string, int) ([]LexicalResult, error) {
				calls++
				return []LexicalResult{{ID: "lexical", Score: 1, Metadata: []byte(`{"text":"answer"}`)}}, nil
			}}
			body := decodeSearchResp(t, ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": typ, "rerank": true}))
			rows := body["results"].([]any)
			if body["search_type"] != "CHUNKS_LEXICAL" || body["reranked"] != false || calls != 1 || len(rows) != 1 || rows[0].(map[string]any)["id"] != "lexical" {
				t.Fatalf("body=%v lexical calls=%d", body, calls)
			}
		})
	}
}

func TestT10HybridWithoutPipelinePropagatesLexicalError(t *testing.T) {
	for _, typ := range []string{"HYBRID", "WEIGHTED_HYBRID"} {
		t.Run(typ, func(t *testing.T) {
			deps := &fakeDeps{lexicalCollections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return nil }, lexicalFn: func(string, string, int) ([]LexicalResult, error) {
				return nil, errors.New("lexical backend unavailable")
			}}
			if result := ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": typ}); !result.IsError {
				t.Fatalf("both unavailable returned success: %+v", result)
			}
		})
	}
}

func TestT10HybridRetainsIndependentLeg(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		vectorErr, lexicalErr error
		cancel                bool
		want                  string
	}{
		{"vector-fails", errors.New("provider unavailable"), nil, false, "lexical"},
		{"lexical-fails", nil, errors.New("index unavailable"), false, "vector"},
		{"both-fail", errors.New("provider unavailable"), errors.New("index unavailable"), false, ""},
		{"authorization-fails", embed.ErrGuardRejected, nil, false, ""},
		{"canceled", context.Canceled, nil, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sp := &fakeSearchPipeline{byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
				if tc.cancel {
					cancel()
				}
				if tc.vectorErr != nil {
					return nil, tc.vectorErr
				}
				return []pipeline.ScoredResult{scoredRes("vector", .9)}, nil
			}}
			deps := &fakeDeps{collections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return sp }, lexicalFn: func(string, string, int) ([]LexicalResult, error) {
				if tc.lexicalErr != nil {
					return nil, tc.lexicalErr
				}
				return []LexicalResult{{ID: "lexical", Score: 1, Metadata: []byte(`{"text":"lexical"}`)}}, nil
			}}
			result := ToolSearch(ctx, deps, map[string]any{"search_query": "q", "search_type": "HYBRID"})
			if tc.cancel || errors.Is(tc.vectorErr, embed.ErrGuardRejected) || (tc.vectorErr != nil && tc.lexicalErr != nil) {
				if !result.IsError {
					t.Fatalf("failed legs returned success: %+v", result)
				}
				return
			}
			got := decodeSearchResp(t, result)["results"].([]any)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("results=%v", got)
				}
				return
			}
			if len(got) != 1 || got[0].(map[string]any)["id"] != tc.want {
				t.Fatalf("results=%v want=%s", got, tc.want)
			}
		})
	}
}
