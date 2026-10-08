package store

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func TestHNSWRecallAt10Quality(t *testing.T) {
	const n, dim, queries, k = 3000, 128, 50, 10
	rng := rand.New(rand.NewSource(20261007))
	arena := NewVectorArena(dim)
	index := NewHNSWIndex(arena, DefaultHNSWConfig())
	levelRNG := rand.New(rand.NewSource(7))
	index.randFloat64 = levelRNG.Float64
	vectors := make([][]float32, n)
	for i := range vectors {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		offset, err := arena.Add(v)
		if err != nil {
			t.Fatal(err)
		}
		vectors[i] = append([]float32(nil), v...)
		index.Add(v, fmt.Sprintf("id-%d", i), offset)
	}
	queryVectors := make([][]float32, queries)
	exact := make([]map[string]struct{}, queries)
	for q := range queryVectors {
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		queryVectors[q] = normalizeVec(v)
		type scored struct {
			id string
			d  float32
		}
		all := make([]scored, n)
		for i := range vectors {
			all[i] = scored{fmt.Sprintf("id-%d", i), dist(queryVectors[q], vectors[i])}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].d < all[j].d })
		exact[q] = make(map[string]struct{}, k)
		for i := 0; i < k; i++ {
			exact[q][all[i].id] = struct{}{}
		}
	}
	hits := 0
	for q, query := range queryVectors {
		for _, result := range index.Search(query, k) {
			if _, ok := exact[q][result.ID]; ok {
				hits++
			}
		}
	}
	recall := float64(hits) / float64(queries*k)
	if recall < 0.90 {
		t.Fatalf("recall@10=%.3f, want at least 0.90", recall)
	}
}
