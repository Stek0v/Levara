package http

import "testing"

func TestEvaluateNeo4jSchema_OK(t *testing.T) {
	obs := neo4jObservedSchema{
		constraints: map[string]bool{
			"__Node__:id:UNIQUE": true,
		},
		indexes: map[string]bool{
			"__Node__:name":       true,
			"__Node__:dataset_id": true,
			"__Node__:type":       true,
		},
	}
	status, msg, remediation := evaluateNeo4jSchema(obs)
	if status != "ok" {
		t.Fatalf("status=%s want ok", status)
	}
	if msg == "" {
		t.Fatalf("expected non-empty message")
	}
	if remediation != "" {
		t.Fatalf("expected empty remediation for ok, got %q", remediation)
	}
}

func TestEvaluateNeo4jSchema_Missing(t *testing.T) {
	obs := neo4jObservedSchema{
		constraints: map[string]bool{},
		indexes: map[string]bool{
			"__Node__:name": true,
		},
	}
	status, msg, remediation := evaluateNeo4jSchema(obs)
	if status != "warn" {
		t.Fatalf("status=%s want warn", status)
	}
	if msg == "" || remediation == "" {
		t.Fatalf("expected message+remediation for warn; msg=%q remediation=%q", msg, remediation)
	}
}

func TestNormalizeNeo4jConstraintRows(t *testing.T) {
	rows := []map[string]any{
		{
			"type":          "UNIQUE",
			"labelsOrTypes": []any{"__Node__"},
			"properties":    []any{"id"},
		},
	}
	got := normalizeNeo4jConstraintRows(rows)
	if !got["__Node__:id:UNIQUE"] {
		t.Fatalf("expected __Node__:id:UNIQUE to be present; got=%v", got)
	}
}

func TestNormalizeNeo4jIndexRows(t *testing.T) {
	rows := []map[string]any{
		{
			"labelsOrTypes": []any{"__Node__"},
			"properties":    []any{"name", "dataset_id"},
		},
	}
	got := normalizeNeo4jIndexRows(rows)
	if !got["__Node__:name"] || !got["__Node__:dataset_id"] {
		t.Fatalf("expected name+dataset_id indexes; got=%v", got)
	}
}
