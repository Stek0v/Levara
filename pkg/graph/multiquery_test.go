package graph

import (
	"testing"
)

func TestDecomposeQueryConjunction(t *testing.T) {
	tests := []struct {
		query    string
		minParts int
	}{
		{"Кто такая Эмбер и как она связана с Лукасом", 3}, // 2 parts + original
		{"quantum computing and machine learning", 3},
		{"Расскажи про HNSW, а также про WAL", 3},
		{"simple query", 1}, // no decomposition
	}

	for _, tt := range tests {
		parts := DecomposeQuery(tt.query)
		if len(parts) < tt.minParts {
			t.Errorf("DecomposeQuery(%q): got %d parts, want >= %d: %v",
				tt.query, len(parts), tt.minParts, parts)
		}
	}
}

func TestDecomposeQueryRussian(t *testing.T) {
	parts := DecomposeQuery("Кто такая Эмбер и как она связана с Лукасом")
	if len(parts) < 2 {
		t.Fatalf("expected >= 2 parts, got %d: %v", len(parts), parts)
	}
	// Should contain "Эмбер" in one part and "Лукас" in another
	hasEmber, hasLucas := false, false
	for _, p := range parts {
		if containsIgnoreCase(p, "Эмбер") {
			hasEmber = true
		}
		if containsIgnoreCase(p, "Лукас") {
			hasLucas = true
		}
	}
	if !hasEmber || !hasLucas {
		t.Errorf("expected parts to contain Эмбер and Лукас: %v", parts)
	}
}

func TestDecomposeQueryQuestion(t *testing.T) {
	parts := DecomposeQuery("Кто такая Эмбер? Как она связана с Лукасом?")
	if len(parts) < 2 {
		t.Errorf("expected >= 2 parts for multi-question, got %d: %v", len(parts), parts)
	}
}

func TestDecomposeQuerySimple(t *testing.T) {
	parts := DecomposeQuery("телепат Эмбер")
	if len(parts) != 1 {
		t.Errorf("simple query should return 1 part, got %d: %v", len(parts), parts)
	}
}

func TestMergeResults(t *testing.T) {
	sub1 := []SearchResultEntry{
		{ID: "a", Score: 0.1, Metadata: "meta-a"},
		{ID: "b", Score: 0.3, Metadata: "meta-b"},
		{ID: "c", Score: 0.5, Metadata: "meta-c"},
	}
	sub2 := []SearchResultEntry{
		{ID: "b", Score: 0.2, Metadata: "meta-b"},
		{ID: "d", Score: 0.4, Metadata: "meta-d"},
		{ID: "a", Score: 0.15, Metadata: "meta-a"},
	}

	merged := MergeResults([][]SearchResultEntry{sub1, sub2}, 5)

	if len(merged) == 0 {
		t.Fatal("expected merged results")
	}

	// "a" and "b" appear in both → should be boosted
	topIDs := map[string]bool{}
	for _, r := range merged[:2] {
		topIDs[r.ID] = true
	}
	if !topIDs["a"] || !topIDs["b"] {
		t.Errorf("'a' and 'b' should be top-2 (in both sub-queries), top: %v", merged[:2])
	}

	// Check appearances
	for _, r := range merged {
		if r.ID == "a" && r.Appearances != 2 {
			t.Errorf("'a' should have 2 appearances, got %d", r.Appearances)
		}
		if r.ID == "d" && r.Appearances != 1 {
			t.Errorf("'d' should have 1 appearance, got %d", r.Appearances)
		}
	}
}

func TestMergeResultsBestScore(t *testing.T) {
	sub1 := []SearchResultEntry{{ID: "x", Score: 0.5}}
	sub2 := []SearchResultEntry{{ID: "x", Score: 0.2}}

	merged := MergeResults([][]SearchResultEntry{sub1, sub2}, 5)
	if merged[0].BestScore != 0.2 {
		t.Errorf("best score should be 0.2, got %f", merged[0].BestScore)
	}
}

func containsIgnoreCase(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
