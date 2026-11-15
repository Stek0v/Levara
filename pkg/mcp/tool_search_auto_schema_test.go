package mcp

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/router"
)

func TestToolSearch_AUTOOutputSchema(t *testing.T) {
	const query = "routing"
	for _, tt := range []struct {
		name    string
		hasBM25 bool
	}{
		{name: "with_alternatives", hasBM25: true},
		{name: "without_alternatives"},
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
			response := ToolSearch(context.Background(), deps, map[string]any{
				"search_query": query,
				"search_type":  "AUTO",
			})
			if response.IsError {
				t.Fatalf("AUTO search failed: %+v", response)
			}
			payload := decodeSearchResp(t, response)
			decision := router.Route(query, caps)
			if (len(decision.Alternatives) > 0) != tt.hasBM25 {
				t.Fatalf("fixture produced unexpected router alternatives: %+v", decision.Alternatives)
			}
			if payload["search_type"] != decision.SearchType || decision.SearchType == "AUTO" {
				t.Fatalf("search_type = %v, want routed type %q", payload["search_type"], decision.SearchType)
			}
			wantJSON, err := json.Marshal(map[string]any{
				"selected_type": decision.SearchType,
				"reason":        decision.Reason,
				"confidence":    decision.Confidence,
				"alternatives":  decision.Alternatives,
				"source":        "routed",
			})
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]any
			if err := json.Unmarshal(wantJSON, &want); err != nil {
				t.Fatal(err)
			}
			if got := payload["routing"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("routing = %#v, want the router decision %#v", got, want)
			}
			checkAutoRoutingSchema(t)
		})
	}
}

func checkAutoRoutingSchema(t *testing.T) {
	t.Helper()
	var registered map[string]any
	for _, tool := range ToolDescriptors() {
		if tool.Name == "search" {
			data, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &registered); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	properties, _ := registered["properties"].(map[string]any)
	routing, ok := properties["routing"].(map[string]any)
	if !ok {
		t.Fatal("search OutputSchema is missing routing")
	}
	if routing["type"] != "object" {
		t.Fatalf("routing schema type = %v, want object", routing["type"])
	}
	fields, _ := routing["properties"].(map[string]any)
	for field, want := range map[string]string{
		"selected_type": "string", "reason": "string", "confidence": "number", "source": "string",
	} {
		schema, _ := fields[field].(map[string]any)
		if schema["type"] != want {
			t.Errorf("routing.%s schema type = %v, want %s", field, schema["type"], want)
		}
	}
	alternatives, _ := fields["alternatives"].(map[string]any)
	types, _ := alternatives["type"].([]any)
	if len(types) != 2 || !slices.Contains(types, any("array")) || !slices.Contains(types, any("null")) {
		t.Fatalf("routing.alternatives schema type = %v, want array or null", alternatives["type"])
	}
	items, _ := alternatives["items"].(map[string]any)
	if items["type"] != "object" {
		t.Fatalf("routing.alternatives item schema type = %v, want object", items["type"])
	}
	itemFields, _ := items["properties"].(map[string]any)
	for field, want := range map[string]string{"search_type": "string", "score": "number"} {
		schema, _ := itemFields[field].(map[string]any)
		if schema["type"] != want {
			t.Errorf("routing.alternatives item %s schema type = %v, want %s", field, schema["type"], want)
		}
	}
}
