package bm25

import "sort"

// HybridResult is a fused result from vector + BM25 search.
type HybridResult struct {
	ID             string
	VectorScore    float32 // lower = more similar (distance)
	BM25Score      float64 // higher = more relevant
	FusedScore     float64 // RRF combined score (higher = better)
	VectorRank     int
	BM25Rank       int
	Metadata       string
}

// HybridSearch fuses vector search results with BM25 results using
// Reciprocal Rank Fusion (RRF): score(d) = Σ 1/(k + rank(d))
// where k=60 (standard RRF constant).
//
// vectorResults: from Cognevra Search RPC (sorted by distance ASC)
// bm25Results: from BM25 index (sorted by score DESC)
// topK: number of final results
// vectorWeight: relative weight for vector results (default 1.0)
// bm25Weight: relative weight for BM25 results (default 1.0)
func HybridSearch(
	vectorResults []VectorResult,
	bm25Results []Result,
	topK int,
	vectorWeight, bm25Weight float64,
) []HybridResult {
	if topK <= 0 {
		topK = 10
	}
	if vectorWeight <= 0 {
		vectorWeight = 1.0
	}
	if bm25Weight <= 0 {
		bm25Weight = 1.0
	}

	const k = 60.0 // RRF constant

	// Build fused score map
	type fusedEntry struct {
		vectorScore float32
		bm25Score   float64
		vectorRank  int
		bm25Rank    int
		fusedScore  float64
		metadata    string
	}

	fused := make(map[string]*fusedEntry)

	// Add vector results (rank by position, lower distance = better rank)
	for rank, vr := range vectorResults {
		e, ok := fused[vr.ID]
		if !ok {
			e = &fusedEntry{metadata: vr.Metadata}
			fused[vr.ID] = e
		}
		e.vectorScore = vr.Score
		e.vectorRank = rank + 1
		e.fusedScore += vectorWeight / (k + float64(rank+1))
	}

	// Add BM25 results (already sorted by score DESC)
	for rank, br := range bm25Results {
		e, ok := fused[br.ID]
		if !ok {
			e = &fusedEntry{metadata: br.Metadata}
			fused[br.ID] = e
		}
		e.bm25Score = br.Score
		e.bm25Rank = rank + 1
		e.fusedScore += bm25Weight / (k + float64(rank+1))
	}

	// Convert to slice and sort by fused score DESC
	results := make([]HybridResult, 0, len(fused))
	for id, e := range fused {
		results = append(results, HybridResult{
			ID:          id,
			VectorScore: e.vectorScore,
			BM25Score:   e.bm25Score,
			FusedScore:  e.fusedScore,
			VectorRank:  e.vectorRank,
			BM25Rank:    e.bm25Rank,
			Metadata:    e.metadata,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].FusedScore > results[j].FusedScore
	})

	if len(results) > topK {
		results = results[:topK]
	}

	return results
}

// VectorResult matches what Cognevra Search returns.
type VectorResult struct {
	ID       string
	Score    float32 // distance (lower = more similar)
	Metadata string
}
