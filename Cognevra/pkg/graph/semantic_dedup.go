package graph

import (
	"math"
)

// SemanticDedupResult holds deduplicated items and stats.
type SemanticDedupResult struct {
	Kept    []int // indices of kept items
	Removed []int // indices of removed duplicates
	Pairs   []DedupPair // which items were considered duplicates
}

// DedupPair records a duplicate pair.
type DedupPair struct {
	KeptIdx    int
	RemovedIdx int
	Similarity float32
}

// SemanticDedup removes near-duplicate vectors by cosine similarity.
// Items with similarity > threshold to an already-kept item are removed.
// Returns indices of kept and removed items.
//
// Algorithm: greedy — iterate items in order, keep item if no kept item
// has similarity > threshold. O(n*k) where k = kept items.
// For 1000 items with 50% dedup: ~500K comparisons (fast with SIMD).
func SemanticDedup(vectors [][]float32, threshold float32) SemanticDedupResult {
	if threshold <= 0 {
		threshold = 0.95
	}

	n := len(vectors)
	if n == 0 {
		return SemanticDedupResult{}
	}

	kept := make([]int, 0, n)
	removed := make([]int, 0)
	var pairs []DedupPair

	for i := 0; i < n; i++ {
		isDup := false
		var bestMatch int
		var bestSim float32

		for _, k := range kept {
			sim := cosineSimilarity(vectors[i], vectors[k])
			if sim > threshold && sim > bestSim {
				isDup = true
				bestMatch = k
				bestSim = sim
			}
		}

		if isDup {
			removed = append(removed, i)
			pairs = append(pairs, DedupPair{
				KeptIdx: bestMatch, RemovedIdx: i, Similarity: bestSim,
			})
		} else {
			kept = append(kept, i)
		}
	}

	return SemanticDedupResult{Kept: kept, Removed: removed, Pairs: pairs}
}

// cosineSimilarity computes cosine similarity between two vectors.
// Returns value in [-1, 1] where 1 = identical direction.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	denom := float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB)))
	if denom == 0 {
		return 0
	}
	return dot / denom
}
