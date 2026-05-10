package graphdb

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSerializeProperties(t *testing.T) {
	props := map[string]any{
		"name":    "Levara",
		"version": 1,
		"active":  true,
		"metadata": map[string]any{
			"index_fields": []any{"text"},
		},
		"tags": []any{"db", "vector"},
	}

	out := serializeProperties(props)

	if out["name"] != "Levara" {
		t.Errorf("name: expected Levara, got %v", out["name"])
	}
	if out["version"] != 1 {
		t.Errorf("version: expected 1, got %v", out["version"])
	}
	if out["active"] != true {
		t.Errorf("active: expected true, got %v", out["active"])
	}

	// Dict → JSON string
	meta, ok := out["metadata"].(string)
	if !ok {
		t.Fatalf("metadata: expected string, got %T", out["metadata"])
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(meta), &parsed); err != nil {
		t.Fatalf("metadata JSON parse: %v", err)
	}
	if parsed["index_fields"] == nil {
		t.Error("metadata should contain index_fields")
	}

	// List → JSON string
	tags, ok := out["tags"].(string)
	if !ok {
		t.Fatalf("tags: expected string, got %T", out["tags"])
	}
	if tags != `["db","vector"]` {
		t.Errorf("tags: expected [\"db\",\"vector\"], got %s", tags)
	}
}

func TestFlattenEdgeProperties(t *testing.T) {
	props := map[string]any{
		"source_node_id": "n1",
		"target_node_id": "n2",
		"weights": map[string]any{
			"semantic": 0.8,
			"temporal": 0.3,
		},
		"edge_text":  "uses",
		"extra_data": map[string]any{"foo": "bar"},
		"tag_list":   []any{"a", "b"},
	}

	out := flattenEdgeProperties(props)

	// Primitives pass through
	if out["source_node_id"] != "n1" {
		t.Errorf("source_node_id: expected n1, got %v", out["source_node_id"])
	}
	if out["edge_text"] != "uses" {
		t.Errorf("edge_text: expected uses, got %v", out["edge_text"])
	}

	// Weights → weight_X prefix
	if out["weight_semantic"] != 0.8 {
		t.Errorf("weight_semantic: expected 0.8, got %v", out["weight_semantic"])
	}
	if out["weight_temporal"] != 0.3 {
		t.Errorf("weight_temporal: expected 0.3, got %v", out["weight_temporal"])
	}
	// Original "weights" key should NOT be present
	if _, ok := out["weights"]; ok {
		t.Error("weights key should be flattened, not preserved")
	}

	// Other dicts → _json suffix
	if _, ok := out["extra_data_json"].(string); !ok {
		t.Errorf("extra_data_json: expected string, got %T", out["extra_data_json"])
	}

	// Lists → _json suffix
	if _, ok := out["tag_list_json"].(string); !ok {
		t.Errorf("tag_list_json: expected string, got %T", out["tag_list_json"])
	}
}

func TestSerializePropertiesNil(t *testing.T) {
	out := serializeProperties(nil)
	if out == nil || len(out) != 0 {
		t.Errorf("nil props should return empty map, got %v", out)
	}
}

func TestFlattenEdgePropertiesNil(t *testing.T) {
	out := flattenEdgeProperties(nil)
	if out == nil || len(out) != 0 {
		t.Errorf("nil props should return empty map, got %v", out)
	}
}

func TestRequiredNeo4jSchemaStatements(t *testing.T) {
	stmts := requiredNeo4jSchemaStatements(baseLabel)
	if len(stmts) != 4 {
		t.Fatalf("expected 4 schema statements, got %d", len(stmts))
	}
	wantContains := []string{
		"CREATE CONSTRAINT IF NOT EXISTS",
		"ON (n.name)",
		"ON (n.dataset_id)",
		"ON (n.type)",
	}
	for _, want := range wantContains {
		found := false
		for _, stmt := range stmts {
			if strings.Contains(stmt, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("schema statements missing fragment %q; got %v", want, stmts)
		}
	}
}

func TestSafeLabel(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"Entity", false},
		{"_internal", false},
		{"Node_42", false},
		{"a", false},
		{"", true},
		{"42Node", true},
		{"a-b", true},
		{"a b", true},
		{"a`b", true},
		{"a\nMATCH (x) DETACH DELETE x", true},
		{"a;DROP", true},
		{strings.Repeat("a", 65), true},
	}
	for _, tc := range cases {
		got, err := safeLabel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("safeLabel(%q) expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("safeLabel(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.in {
			t.Errorf("safeLabel(%q) = %q, want %q", tc.in, got, tc.in)
		}
	}
}

func TestSafeRelType(t *testing.T) {
	if _, err := safeRelType("RELATED_TO"); err != nil {
		t.Errorf("RELATED_TO should be valid: %v", err)
	}
	if _, err := safeRelType("a`b"); err == nil {
		t.Error("backtick should be rejected")
	}
}
