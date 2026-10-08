package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
)

func TestT10SearchTerminalErrorsAcrossNativeBranches(t *testing.T) {
	for _, typ := range []string{"CHUNKS", "RERANK", "PARENT_CHILD", "MULTI_QUERY", "GRAPH_RERANK", "HYBRID"} {
		for _, failure := range []string{"cancel", "embed-guard", "result-filter", "provider"} {
			t.Run(typ+"/"+failure, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				search := func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
					switch failure {
					case "cancel":
						cancel()
						return []pipeline.ScoredResult{scoredRes("partial", 1)}, nil
					case "embed-guard":
						return nil, fmt.Errorf("wrapped: %w", embed.ErrGuardRejected)
					case "result-filter":
						return nil, fmt.Errorf("wrapped: %w", pipeline.ErrResultFilterRejected)
					default:
						return nil, errors.New("provider unavailable")
					}
				}
				sp := &fakeSearchPipeline{rerankEnabled: true, byText: search, byTextParentChild: search, byTextMultiQuery: func(ctx context.Context, coll, q string, k int, _ llm.Provider, _ string, _ int) ([]pipeline.ScoredResult, error) {
					return search(ctx, coll, q, k)
				}, applyRerank: func(context.Context, string, []pipeline.ScoredResult, int) (bool, []pipeline.ScoredResult) {
					t.Fatal("terminal retrieval reached reranker")
					return false, nil
				}}
				deps := &fakeDeps{collections: []string{"kb"}, searchPipelineFn: func(bool) SearchPipeline { return sp }, llmProvider: &stubDistillProvider{content: `[]`}, lexicalFn: func(string, string, int) ([]LexicalResult, error) {
					return []LexicalResult{{ID: "lexical", Score: 1, Metadata: []byte(`{"text":"lexical"}`)}}, nil
				}}
				if typ == "GRAPH_RERANK" {
					db, err := sql.Open("sqlite3", t.TempDir()+"/graph.db")
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					deps.db = db
				}
				result := ToolSearch(ctx, deps, map[string]any{"search_query": "q", "search_type": typ})
				if failure == "provider" {
					if result.IsError {
						t.Fatalf("ordinary provider failure changed: %+v", result)
					}
					return
				}
				if !result.IsError {
					t.Fatalf("terminal failure returned success: %+v", result)
				}
			})
		}
	}
}
