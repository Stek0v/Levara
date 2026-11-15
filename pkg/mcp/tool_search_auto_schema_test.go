package mcp

import (
	"context"
	"reflect"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/router"
)

func TestToolSearch_AUTOOutputSchema(t *testing.T) {
	var search Tool
	for _, tool := range ToolDescriptors() {
		if tool.Name == "search" {
			search = tool
			break
		}
	}
	if search.OutputSchema == nil {
		t.Fatal("search has no registered output schema")
	}

	const query = "routing"
	for _, tt := range []struct {
		name         string
		hasBM25      bool
		selectedType string
		alternatives []any
	}{
		{"with_alternatives", true, "HYBRID", []any{"CHUNKS", "CHUNKS_LEXICAL"}},
		{"without_alternatives", false, "CHUNKS", []any{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			caps := router.Capabilities{HasEmbedding: true, HasBM25: tt.hasBM25}
			deps := &fakeDeps{
				collections:  []string{"default"},
				capabilities: caps,
				searchPipelineFn: func(bool) SearchPipeline {
					return &fakeSearchPipeline{
						byText: func(context.Context, string, string, int) ([]pipeline.ScoredResult, error) {
							return []pipeline.ScoredResult{scoredRes("a", 0.9)}, nil
						},
					}
				},
			}
			result := ToolSearch(context.Background(), deps, map[string]any{
				"search_query": query,
				"search_type":  "AUTO",
			})
			if result.IsError {
				t.Fatalf("AUTO search failed: %+v", result)
			}
			assertOutputSchema(t, search, result)

			payload := decodeSearchResp(t, result)
			routing, ok := payload["routing"].(map[string]any)
			if !ok {
				t.Fatalf("routing = %#v, want an object", payload["routing"])
			}
			if payload["search_type"] != tt.selectedType || routing["selected_type"] != tt.selectedType {
				t.Errorf("search_type = %v, routing.selected_type = %v, want %s",
					payload["search_type"], routing["selected_type"], tt.selectedType)
			}
			decision := router.Route(query, caps)
			if routing["source"] != "routed" || routing["reason"] != decision.Reason || routing["confidence"] != float64(decision.Confidence) {
				t.Errorf("routing metadata = %#v, want the router's source, reason and confidence", routing)
			}
			if !reflect.DeepEqual(routing["alternatives"], tt.alternatives) {
				t.Errorf("routing.alternatives = %#v, want %#v", routing["alternatives"], tt.alternatives)
			}
			reranked, _ := schemaProps(t, search.OutputSchema)["reranked"].(map[string]any)
			if reranked["description"] != "Whether reranking was applied." {
				t.Error("reranked description must only report whether reranking was applied")
			}
		})
	}
}
