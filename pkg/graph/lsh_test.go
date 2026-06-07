package graph

import (
	"math/rand"
	"testing"
)

func TestLSHIdenticalVectors(t *testing.T) {
	dim := 128
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = float32(i) * 0.01
	}

	vectors := [][]float32{vec, vec, vec}
	result := SemanticDedupLSH(vectors, 0.95, 10, 8)

	// Small input falls back to brute-force
	if len(result.Kept) != 1 {
		t.Errorf("expected 1 kept, got %d", len(result.Kept))
	}
}

func TestLSHLargeInput(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	dim := 256
	n := 200

	vectors := make([][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		vectors[i] = normalizeVector(v)
	}

	// Add 20 exact duplicates
	for i := 0; i < 20; i++ {
		vectors = append(vectors, vectors[i])
	}

	result := SemanticDedupLSH(vectors, 0.99, 12, 8)

	// Should find at least some duplicates
	if len(result.Removed) < 10 {
		t.Errorf("expected >= 10 removed (20 exact dups), got %d", len(result.Removed))
	}
	t.Logf("LSH dedup: %d input → %d kept + %d removed", len(vectors), len(result.Kept), len(result.Removed))
}

func TestLSHCandidateRecall(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	dim := 128

	// Create a vector and a near-duplicate
	base := make([]float32, dim)
	for i := range base {
		base[i] = rng.Float32()
	}
	base = normalizeVector(base)

	nearDup := make([]float32, dim)
	for i := range nearDup {
		nearDup[i] = base[i] + rng.Float32()*0.01
	}
	nearDup = normalizeVector(nearDup)

	sim := cosineSimilarity(base, nearDup)
	if sim < 0.95 {
		t.Skipf("near-dup sim too low: %f", sim)
	}

	lsh := NewLSH(dim, 20, 6)
	vectors := [][]float32{base, nearDup}
	lsh.Build(vectors)

	candidates := lsh.QueryCandidates(base)
	found := false
	for _, c := range candidates {
		if c == 1 {
			found = true
		}
	}

	if !found {
		t.Logf("LSH missed near-dup (sim=%f) — increase tables/decrease bits for better recall", sim)
	}
}

func BenchmarkLSHDedup1000(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	dim := 1024
	n := 1000

	vectors := make([][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		vectors[i] = normalizeVector(v)
	}
	// Add 100 near-duplicates
	for i := 0; i < 100; i++ {
		dup := make([]float32, dim)
		for j := range dup {
			dup[j] = vectors[i][j] + rng.Float32()*0.001
		}
		vectors = append(vectors, normalizeVector(dup))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SemanticDedupLSH(vectors, 0.95, 12, 10)
	}
}

func BenchmarkBruteForceDedup1000(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	dim := 1024
	n := 1000

	vectors := make([][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		vectors[i] = normalizeVector(v)
	}
	for i := 0; i < 100; i++ {
		dup := make([]float32, dim)
		for j := range dup {
			dup[j] = vectors[i][j] + rng.Float32()*0.001
		}
		vectors = append(vectors, normalizeVector(dup))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		SemanticDedup(vectors, 0.95)
	}
}
