package bm25

import (
	"fmt"
	"testing"
)

func TestTokenize(t *testing.T) {
	tokens := tokenize("Levara is a Vector Database! Uses HNSW.")
	expected := []string{"levara", "is", "vector", "database", "uses", "hnsw"}
	if len(tokens) != len(expected) {
		t.Fatalf("expected %d tokens, got %d: %v", len(expected), len(tokens), tokens)
	}
	for i, tok := range tokens {
		if tok != expected[i] {
			t.Errorf("token[%d]: expected %q, got %q", i, expected[i], tok)
		}
	}
}

func TestTokenizeRussian(t *testing.T) {
	tokens := tokenize("Телепат Эмбер читает мысли")
	if len(tokens) != 4 {
		t.Fatalf("expected 4 tokens, got %d: %v", len(tokens), tokens)
	}
	if tokens[0] != "телепат" {
		t.Errorf("expected 'телепат', got %q", tokens[0])
	}
}

func TestAddAndSearch(t *testing.T) {
	idx := NewIndex()
	idx.Add("d1", "quantum computers use qubits for superposition", "")
	idx.Add("d2", "natural language processing analyzes text data", "")
	idx.Add("d3", "HNSW algorithm provides fast vector search", "")

	results := idx.Search("quantum qubits", 3)
	if len(results) == 0 {
		t.Fatal("expected results for 'quantum qubits'")
	}
	if results[0].ID != "d1" {
		t.Errorf("expected d1 as top result, got %s", results[0].ID)
	}
}

func TestSearchRussian(t *testing.T) {
	idx := NewIndex()
	idx.Add("r1", "Телепат Эмбер читает мысли других людей", "")
	idx.Add("r2", "Лукас командир ударной группы", "")
	idx.Add("r3", "Ураган изменил всё в городе-улье", "")

	results := idx.Search("телепат мысли", 3)
	if len(results) == 0 {
		t.Fatal("expected results for Russian query")
	}
	if results[0].ID != "r1" {
		t.Errorf("expected r1, got %s", results[0].ID)
	}
}

func TestSearchNoMatch(t *testing.T) {
	idx := NewIndex()
	idx.Add("d1", "quantum computers", "")
	results := idx.Search("basketball game", 3)
	if len(results) != 0 {
		t.Errorf("expected 0 results for unrelated query, got %d", len(results))
	}
}

func TestRemove(t *testing.T) {
	idx := NewIndex()
	idx.Add("d1", "quantum qubits superposition", "")
	idx.Add("d2", "quantum entanglement", "")

	idx.Remove("d1")

	results := idx.Search("quantum", 3)
	if len(results) != 1 || results[0].ID != "d2" {
		t.Errorf("after remove d1, expected only d2, got %v", results)
	}
	if idx.Size() != 1 {
		t.Errorf("size after remove: expected 1, got %d", idx.Size())
	}
}

func TestClear(t *testing.T) {
	idx := NewIndex()
	idx.Add("d1", "text", "")
	idx.Clear()
	if idx.Size() != 0 {
		t.Errorf("size after clear: expected 0, got %d", idx.Size())
	}
}

func TestHybridSearch(t *testing.T) {
	vectorResults := []VectorResult{
		{ID: "d1", Score: 0.1, Metadata: "v-d1"},
		{ID: "d2", Score: 0.3, Metadata: "v-d2"},
		{ID: "d3", Score: 0.5, Metadata: "v-d3"},
	}
	bm25Results := []Result{
		{ID: "d2", Score: 5.0, Metadata: "b-d2"},
		{ID: "d4", Score: 3.0, Metadata: "b-d4"},
		{ID: "d1", Score: 1.0, Metadata: "b-d1"},
	}

	results := HybridSearch(vectorResults, bm25Results, 5, 1.0, 1.0)

	if len(results) == 0 {
		t.Fatal("expected hybrid results")
	}

	// d1 and d2 appear in BOTH lists → should score highest
	topIDs := map[string]bool{}
	for _, r := range results[:2] {
		topIDs[r.ID] = true
	}
	if !topIDs["d1"] || !topIDs["d2"] {
		t.Errorf("d1 and d2 should be top-2 (in both lists), got %v", results[:2])
	}

	// All results should have positive fused score
	for _, r := range results {
		if r.FusedScore <= 0 {
			t.Errorf("result %s has non-positive fused score: %f", r.ID, r.FusedScore)
		}
	}
}

func TestHybridWeights(t *testing.T) {
	vectorResults := []VectorResult{
		{ID: "vec-only", Score: 0.1},
	}
	bm25Results := []Result{
		{ID: "bm25-only", Score: 5.0},
	}

	// Heavy vector weight
	r1 := HybridSearch(vectorResults, bm25Results, 2, 10.0, 1.0)
	if r1[0].ID != "vec-only" {
		t.Errorf("with high vector weight, vec-only should be first, got %s", r1[0].ID)
	}

	// Heavy BM25 weight
	r2 := HybridSearch(vectorResults, bm25Results, 2, 1.0, 10.0)
	if r2[0].ID != "bm25-only" {
		t.Errorf("with high bm25 weight, bm25-only should be first, got %s", r2[0].ID)
	}
}

func BenchmarkBM25Search(b *testing.B) {
	idx := NewIndex()
	for i := 0; i < 10000; i++ {
		idx.Add(fmt.Sprintf("d%d", i),
			fmt.Sprintf("document %d about quantum computing and machine learning algorithms number %d", i, i%100), "")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Search("quantum computing algorithms", 10)
	}
}

func BenchmarkBM25Add(b *testing.B) {
	idx := NewIndex()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx.Add(fmt.Sprintf("d%d", i), "quantum computing machine learning algorithms", "")
	}
}
