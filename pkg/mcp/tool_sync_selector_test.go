package mcp

import (
	"context"
	"reflect"
	"testing"
)

func TestToolSyncSelectorValidation(t *testing.T) {
	invalid := []struct {
		name   string
		fields map[string]any
	}{
		{"unknown_only", map[string]any{"types": []any{"unknown"}}},
		{"mixed_unknown", map[string]any{"types": []any{"memories", "unknown"}}},
		{"blank_type", map[string]any{"types": []any{""}}},
		{"case_is_exact", map[string]any{"types": []any{"Memories"}}},
		{"types_scalar", map[string]any{"types": "memories"}},
		{"types_null", map[string]any{"types": nil}},
		{"types_object", map[string]any{"types": map[string]any{}}},
		{"types_bad_element", map[string]any{"types": []any{"memories", 1}}},
		{"types_bad_only", map[string]any{"types": []any{true}}},
		{"collections_missing", map[string]any{"types": []any{"collections"}}},
		{"collections_empty", map[string]any{"types": []any{"collections"}, "collections": []any{}}},
		{"collections_scalar", map[string]any{"collections": "docs"}},
		{"collections_null", map[string]any{"collections": nil}},
		{"collections_bad_element", map[string]any{"types": []any{"collections"}, "collections": []any{"docs", false}}},
		{"collections_blank", map[string]any{"types": []any{"collections"}, "collections": []any{"docs", " \t "}}},
		{"collections_empty_name", map[string]any{"collections": []any{""}}},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			deps := setupCodifyDB(t)
			calls := 0
			deps.doSyncFn = func(context.Context, string, string, []string, string, []string) (map[string]any, map[string]any, error) {
				calls++
				return map[string]any{"status": "ok"}, nil, nil
			}
			args := map[string]any{"remote_url": "http://selector.test"}
			for key, value := range tc.fields {
				args[key] = value
			}
			result := ToolSync(context.Background(), deps, args)
			if !result.IsError || calls != 0 {
				t.Fatalf("invalid selector reached sync: error=%v calls=%d result=%+v", result.IsError, calls, result)
			}
		})
	}
	valid := []struct {
		name         string
		fields       map[string]any
		types, names []string
	}{
		{"omitted", nil, nil, nil},
		{"empty_lists", map[string]any{"types": []any{}, "collections": []any{}}, nil, nil},
		{"names_are_not_opt_in", map[string]any{"collections": []any{"docs"}}, nil, []string{"docs"}},
		{"duplicate_types", map[string]any{"types": []any{"graph", "memories", "graph", "interactions"}}, []string{"graph", "memories", "interactions"}, nil},
		{"explicit_collections", map[string]any{"types": []any{"collections", "collections"}, "collections": []any{"docs", "docs", "other"}}, []string{"collections"}, []string{"docs", "other"}},
		{"typed_arrays", map[string]any{"types": []string{"collections", "graph"}, "collections": []string{"docs", "docs"}}, []string{"collections", "graph"}, []string{"docs"}},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			deps := setupCodifyDB(t)
			calls := 0
			deps.doSyncFn = func(_ context.Context, _ string, _ string, types []string, _ string, names []string) (map[string]any, map[string]any, error) {
				calls++
				if !reflect.DeepEqual(types, tc.types) || !reflect.DeepEqual(names, tc.names) {
					t.Fatalf("selectors changed: types=%v names=%v", types, names)
				}
				return map[string]any{"status": "ok"}, map[string]any{}, nil
			}
			args := map[string]any{"remote_url": "http://selector.test"}
			for key, value := range tc.fields {
				args[key] = value
			}
			if result := ToolSync(context.Background(), deps, args); result.IsError || calls != 1 {
				t.Fatalf("valid selector rejected: calls=%d result=%+v", calls, result)
			}
		})
	}
}
