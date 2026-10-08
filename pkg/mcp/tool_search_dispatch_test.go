package mcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/router"
)

func TestToolSearchDispatchRejectsBeforeDependencies(t *testing.T) {
	for _, typ := range []string{"RAG_COMPLETION", "GRAPH_COMPLETION", "TEMPORAL", "SUMMARIES", "CODING_RULES", "CYPHER", "COMMUNITY_LOCAL", "typo"} {
		for _, mode := range []string{"auto", "rag", "full"} {
			t.Run(typ+"/"+mode, func(t *testing.T) {
				deps := &fakeDeps{searchPipelineFn: func(bool) SearchPipeline { t.Fatal("pipeline called"); return nil }, lexicalFn: func(string, string, int) ([]LexicalResult, error) { t.Fatal("lexical called"); return nil, nil }}
				res := ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": typ, "mode": mode})
				if !res.IsError {
					t.Fatalf("unsupported strategy succeeded: %+v", res)
				}
			})
		}
	}
	for _, mode := range []string{" graph ", "typo"} {
		t.Run(mode, func(t *testing.T) {
			deps := &fakeDeps{searchPipelineFn: func(bool) SearchPipeline { t.Fatal("pipeline called"); return nil }}
			if res := ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "mode": mode}); !res.IsError {
				t.Fatal("unsupported mode succeeded")
			}
		})
	}
}

func TestToolSearchDispatchAutoRetrievalOnly(t *testing.T) {
	queries := []string{"summary of project", "what is the product?", "events on 2025-01-01", "func foo()", "service connected to database", "plain keywords"}
	for i, caps := range []router.Capabilities{
		{HasEmbedding: true, HasBM25: true, HasLLM: true, HasNeo4j: true, HasPostgres: true, HasCommunities: true, AllowCypher: true},
		{HasEmbedding: true, HasLLM: true, HasNeo4j: true},
		{HasBM25: true, HasLLM: true, HasPostgres: true},
	} {
		for _, q := range queries {
			t.Run(fmt.Sprintf("%d/%s", i, q), func(t *testing.T) {
				vectors, lexical := 0, 0
				deps := &fakeDeps{collections: []string{"kb"}, hasColls: true, lexicalCollections: []string{"kb"}, capabilities: caps,
					searchPipelineFn: func(bool) SearchPipeline {
						return &fakeSearchPipeline{byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
							vectors++
							return nil, nil
						}}
					},
					lexicalFn: func(string, string, int) ([]LexicalResult, error) { lexical++; return nil, nil },
				}
				body := decodeSearchResp(t, ToolSearch(context.Background(), deps, map[string]any{"search_query": q}))
				want := "CHUNKS"
				if caps.HasBM25 {
					want = "CHUNKS_LEXICAL"
					if caps.HasEmbedding {
						want = "HYBRID"
					}
				}
				if body["search_type"] != want {
					t.Fatalf("type=%v want %s", body["search_type"], want)
				}
				routing := body["routing"].(map[string]any)
				if routing["selected_type"] != want {
					t.Fatalf("routing=%v", routing)
				}
				for _, alt := range routing["alternatives"].([]any) {
					if alt != "HYBRID" && alt != "CHUNKS" && alt != "CHUNKS_LEXICAL" {
						t.Fatalf("unsupported alternative=%v", alt)
					}
				}
				if caps.HasEmbedding != (vectors == 1) || caps.HasBM25 != (lexical == 1) {
					t.Fatalf("calls vector=%d lexical=%d", vectors, lexical)
				}
			})
		}
	}
}

func TestToolSearchDispatchAliasesAndEffectiveFallback(t *testing.T) {
	for _, tc := range []struct {
		typ, want   string
		graphDenied bool
	}{
		{" chunks ", "CHUNKS", false}, {" basic ", "BASIC", false}, {" bm25 ", "CHUNKS_LEXICAL", false},
		{" multi_query ", "CHUNKS", false}, {" rerank ", "CHUNKS", false}, {" graph_rerank ", "CHUNKS", true},
	} {
		t.Run(tc.typ, func(t *testing.T) {
			vectors, lexical := 0, 0
			deps := &fakeDeps{collections: []string{"kb"}, hasColls: true, lexicalCollections: []string{"kb"},
				searchPipelineFn: func(bool) SearchPipeline {
					return &fakeSearchPipeline{byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
						vectors++
						return nil, nil
					}}
				},
				lexicalFn: func(string, string, int) ([]LexicalResult, error) { lexical++; return nil, nil },
			}
			ctx := context.Background()
			if tc.graphDenied {
				ctx = WithSearchAccess(ctx, SearchAccess{GlobalGraph: false})
			}
			body := decodeSearchResp(t, ToolSearch(ctx, deps, map[string]any{"search_query": "q", "search_type": tc.typ, "mode": " RAG "}))
			if body["search_type"] != tc.want {
				t.Fatalf("type=%v want=%s", body["search_type"], tc.want)
			}
			if tc.want == "CHUNKS_LEXICAL" {
				if lexical != 1 || vectors != 0 {
					t.Fatalf("calls=%d/%d", vectors, lexical)
				}
			} else if vectors != 1 || lexical != 0 {
				t.Fatalf("calls=%d/%d", vectors, lexical)
			}
		})
	}
}

func TestToolSearchDispatchFlagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, typ, want                                       string
		parent, multi, rerank, graph, enabled, performed, llm bool
	}{
		{"parent-over-multi-rerank", "RERANK", "PARENT_CHILD", true, true, false, true, true, true, true},
		{"multi-over-rerank", "RERANK", "MULTI_QUERY", false, true, false, true, true, true, true},
		{"missing-multi-uses-rerank", "MULTI_QUERY", "RERANK", false, false, true, false, true, true, false},
		{"rerank-unavailable", "RERANK", "CHUNKS", false, false, false, false, false, false, false},
		{"rerank-declines", "RERANK", "CHUNKS", false, false, false, false, true, false, false},
		{"basic-flags-rerank", "BASIC", "RERANK", false, false, true, false, true, true, false},
		{"no-rerank-flag", "BASIC", "BASIC", false, false, false, false, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			branch, rerankCalls := "", 0
			pipe := &fakeSearchPipeline{
				rerankEnabled: tc.enabled,
				byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					branch = "vector"
					return []pipeline.ScoredResult{scoredRes("a", .9)}, nil
				},
				byTextParentChild: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					branch = "PARENT_CHILD"
					return nil, nil
				},
				byTextMultiQuery: func(context.Context, string, string, int, llm.Provider, string, int) ([]pipeline.ScoredResult, error) {
					branch = "MULTI_QUERY"
					return nil, nil
				},
				applyRerank: func(_ context.Context, _ string, in []pipeline.ScoredResult, _ int) (bool, []pipeline.ScoredResult) {
					rerankCalls++
					return tc.performed, in
				},
			}
			deps := &fakeDeps{collections: []string{"kb"}, hasColls: true, searchPipelineFn: func(bool) SearchPipeline { return pipe }}
			if tc.llm {
				deps.llmProvider = &stubDistillProvider{}
			}
			body := decodeSearchResp(t, ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": tc.typ, "parent_child": tc.parent, "multi_query": tc.multi, "rerank": tc.rerank, "graph_rerank": tc.graph}))
			if body["search_type"] != tc.want {
				t.Fatalf("type=%v want=%s", body["search_type"], tc.want)
			}
			if tc.want == "PARENT_CHILD" || tc.want == "MULTI_QUERY" {
				if branch != tc.want || rerankCalls != 0 {
					t.Fatalf("branch=%s rerank=%d", branch, rerankCalls)
				}
			} else {
				if branch != "vector" {
					t.Fatalf("branch=%s", branch)
				}
				wantCalls := 0
				if (tc.rerank || tc.typ == "RERANK") && tc.enabled {
					wantCalls = 1
				}
				if rerankCalls != wantCalls {
					t.Fatalf("rerank=%d want=%d", rerankCalls, wantCalls)
				}
			}
		})
	}
}

func TestToolSearchDispatchFeelingLuckyAndNoBackend(t *testing.T) {
	for _, typ := range []string{"AUTO", " feeling_lucky "} {
		res := ToolSearch(context.Background(), &fakeDeps{}, map[string]any{"search_query": "summary", "search_type": typ})
		if res.IsError || res.Content[0].Text != "No results (embedding service not configured)" {
			t.Fatalf("no-backend shape=%+v", res)
		}
	}
}

func TestToolSearchDispatchEmptyACLDoesNotClaimRerank(t *testing.T) {
	for _, typ := range []string{"RERANK", "BASIC"} {
		t.Run(typ, func(t *testing.T) {
			pipe := &fakeSearchPipeline{rerankEnabled: true,
				byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					t.Fatal("vector called")
					return nil, nil
				},
				applyRerank: func(context.Context, string, []pipeline.ScoredResult, int) (bool, []pipeline.ScoredResult) {
					t.Fatal("rerank called")
					return false, nil
				},
			}
			deps := &fakeDeps{collections: []string{"kb"}, hasColls: true, allowedDatasetIDs: []string{},
				searchPipelineFn: func(bool) SearchPipeline { return pipe },
				lexicalFn:        func(string, string, int) ([]LexicalResult, error) { t.Fatal("lexical called"); return nil, nil },
			}
			body := decodeSearchResp(t, ToolSearch(context.Background(), deps, map[string]any{"search_query": "q", "search_type": typ, "rerank": true}))
			if body["search_type"] != "CHUNKS" || body["reranked"] != false || len(body["results"].([]any)) != 0 {
				t.Fatalf("empty ACL response=%v", body)
			}
		})
	}
}
