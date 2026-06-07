package graph

import (
	"math"
	"math/rand"
	"testing"
)

func normalize(v []float32) []float32 {
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	norm = float32(math.Sqrt(float64(norm)))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / norm
	}
	return out
}

func TestSemanticDedupIdentical(t *testing.T) {
	vec := normalize([]float32{1, 2, 3, 4})
	vectors := [][]float32{vec, vec, vec} // 3 identical

	result := SemanticDedup(vectors, 0.95)

	if len(result.Kept) != 1 {
		t.Errorf("expected 1 kept, got %d", len(result.Kept))
	}
	if len(result.Removed) != 2 {
		t.Errorf("expected 2 removed, got %d", len(result.Removed))
	}
}

func TestSemanticDedupOrthogonal(t *testing.T) {
	// Orthogonal vectors — no duplicates
	v1 := normalize([]float32{1, 0, 0, 0})
	v2 := normalize([]float32{0, 1, 0, 0})
	v3 := normalize([]float32{0, 0, 1, 0})

	result := SemanticDedup([][]float32{v1, v2, v3}, 0.95)

	if len(result.Kept) != 3 {
		t.Errorf("expected 3 kept (orthogonal), got %d", len(result.Kept))
	}
	if len(result.Removed) != 0 {
		t.Errorf("expected 0 removed, got %d", len(result.Removed))
	}
}

func TestSemanticDedupNearDuplicate(t *testing.T) {
	v1 := normalize([]float32{1.0, 0.5, 0.3, 0.1})
	v2 := normalize([]float32{1.0, 0.5, 0.3, 0.11}) // very similar to v1
	v3 := normalize([]float32{0.0, 1.0, 0.0, 0.0})  // different

	sim := cosineSimilarity(v1, v2)
	if sim < 0.99 {
		t.Fatalf("v1-v2 similarity should be >0.99, got %f", sim)
	}

	result := SemanticDedup([][]float32{v1, v2, v3}, 0.95)

	if len(result.Kept) != 2 {
		t.Errorf("expected 2 kept, got %d", len(result.Kept))
	}
	if len(result.Removed) != 1 {
		t.Errorf("expected 1 removed, got %d", len(result.Removed))
	}
	if result.Removed[0] != 1 {
		t.Errorf("expected index 1 removed, got %d", result.Removed[0])
	}
}

func TestSemanticDedupThreshold(t *testing.T) {
	v1 := normalize([]float32{1, 0, 0})
	v2 := normalize([]float32{0.9, 0.1, 0})

	sim := cosineSimilarity(v1, v2)

	// With high threshold — no dedup
	r1 := SemanticDedup([][]float32{v1, v2}, 0.999)
	if len(r1.Kept) != 2 {
		t.Errorf("high threshold: expected 2 kept, got %d (sim=%f)", len(r1.Kept), sim)
	}

	// With low threshold — dedup
	r2 := SemanticDedup([][]float32{v1, v2}, 0.5)
	if len(r2.Kept) != 1 {
		t.Errorf("low threshold: expected 1 kept, got %d (sim=%f)", len(r2.Kept), sim)
	}
}

func TestSemanticDedupEmpty(t *testing.T) {
	result := SemanticDedup(nil, 0.95)
	if len(result.Kept) != 0 {
		t.Error("expected empty result for nil input")
	}
}

func TestSemanticDedupPairs(t *testing.T) {
	vec := normalize([]float32{1, 2, 3})
	result := SemanticDedup([][]float32{vec, vec}, 0.95)

	if len(result.Pairs) != 1 {
		t.Fatalf("expected 1 pair, got %d", len(result.Pairs))
	}
	if result.Pairs[0].KeptIdx != 0 || result.Pairs[0].RemovedIdx != 1 {
		t.Errorf("pair: kept=%d removed=%d", result.Pairs[0].KeptIdx, result.Pairs[0].RemovedIdx)
	}
	if result.Pairs[0].Similarity < 0.99 {
		t.Errorf("identical vectors should have sim ~1.0, got %f", result.Pairs[0].Similarity)
	}
}

func BenchmarkSemanticDedup1000(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	dim := 1024
	n := 1000

	vectors := make([][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		vectors[i] = normalize(v)
	}
	// Add 100 near-duplicates
	for i := 0; i < 100; i++ {
		src := vectors[i]
		dup := make([]float32, dim)
		for j := range dup {
			dup[j] = src[j] + rng.Float32()*0.001 // tiny perturbation
		}
		vectors = append(vectors, normalize(dup))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SemanticDedup(vectors, 0.95)
	}
}
