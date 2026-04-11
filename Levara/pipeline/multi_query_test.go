package pipeline

import (
	"testing"
)

func TestParseJSONStringArray_Valid(t *testing.T) {
	input := `["hello", "world", "foo"]`
	got := parseJSONStringArray(input)
	if len(got) != 3 {
		t.Fatalf("Expected 3, got %d: %v", len(got), got)
	}
	if got[0] != "hello" {
		t.Errorf("got[0] = %q, want 'hello'", got[0])
	}
}

func TestParseJSONStringArray_WithCodeFence(t *testing.T) {
	input := "```json\n[\"hello\", \"world\"]\n```"
	got := parseJSONStringArray(input)
	if len(got) != 2 {
		t.Fatalf("Expected 2, got %d: %v", len(got), got)
	}
}

func TestParseJSONStringArray_WithPreamble(t *testing.T) {
	input := "Here are the variants:\n[\"query1\", \"query2\", \"query3\"]"
	got := parseJSONStringArray(input)
	if len(got) != 3 {
		t.Fatalf("Expected 3, got %d: %v", len(got), got)
	}
}

func TestParseJSONStringArray_Empty(t *testing.T) {
	got := parseJSONStringArray("[]")
	if len(got) != 0 {
		t.Errorf("Expected 0, got %d", len(got))
	}
}

func TestParseJSONStringArray_Invalid(t *testing.T) {
	got := parseJSONStringArray("not json at all")
	if got != nil {
		t.Errorf("Expected nil for invalid JSON, got %v", got)
	}
}

func TestParseJSONStringArray_EmptyStringsFiltered(t *testing.T) {
	input := `["hello", "", "  ", "world"]`
	got := parseJSONStringArray(input)
	if len(got) != 2 {
		t.Errorf("Expected 2 (empty strings filtered), got %d: %v", len(got), got)
	}
}

func TestParseJSONStringArray_TooMany(t *testing.T) {
	input := `["a", "b", "c", "d", "e", "f", "g", "h"]`
	got := parseJSONStringArray(input)
	// parseJSONStringArray doesn't cap — that's done in generateQueryVariants
	if len(got) != 8 {
		t.Errorf("Expected 8 (no cap in parser), got %d", len(got))
	}
}

func TestExtractParentID(t *testing.T) {
	tests := []struct {
		name     string
		metadata string
		want     string
	}{
		{"with parent_id", `{"text":"hello","parent_id":"abc-123"}`, "abc-123"},
		{"without parent_id", `{"text":"hello"}`, ""},
		{"empty", `{}`, ""},
		{"nil", "", ""},
		{"invalid json", "not json", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractParentID([]byte(tc.metadata))
			if got != tc.want {
				t.Errorf("extractParentID = %q, want %q", got, tc.want)
			}
		})
	}
}
