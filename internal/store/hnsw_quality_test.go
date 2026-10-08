package store

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
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

func TestHNSWEfConstructionExpandsCandidatesWithinDegreeBounds(t *testing.T) {
	build := func() (*HNSWIndex, []string) {
		cfg := DefaultHNSWConfig()
		cfg.M, cfg.M0, cfg.EfConstruction = 2, 2, 8
		arena := NewVectorArena(4)
		index := NewHNSWIndex(arena, cfg)
		index.randFloat64 = func() float64 { return 1 }
		for i := 0; i < 24; i++ {
			vector := []float32{float32(i + 1), float32(24 - i), float32(i%3 + 1), 1}
			offset, err := arena.Add(vector)
			if err != nil {
				t.Fatal(err)
			}
			index.Add(vector, fmt.Sprintf("node-%02d", i), offset)
		}
		for _, node := range index.nodesByIdx {
			if node == nil {
				continue
			}
			for _, connections := range node.Connections {
				if len(connections) > 2 {
					t.Fatalf("node %s degree=%d exceeds 2", node.ID, len(connections))
				}
			}
		}
		query := normalizeVec([]float32{8, 16, 2, 1})
		entry := index.Nodes[index.EntryNodeID]
		arena.mu.RLock()
		narrow := index.searchLayerTopK(query, entry, 0, 2, index.vecNoLock)
		wide := index.searchLayerTopK(query, entry, 0, index.constructionBreadth(2), index.vecNoLock)
		arena.mu.RUnlock()
		if len(wide) <= len(narrow) || len(wide) != 8 {
			t.Fatalf("construction candidates narrow=%d wide=%d want wide=8", len(narrow), len(wide))
		}
		results := index.Search(query, 10)
		ids := make([]string, len(results))
		for i := range results {
			ids[i] = results[i].ID
		}
		return index, ids
	}
	_, first := build()
	_, second := build()
	if strings.Join(first, "\x00") != strings.Join(second, "\x00") {
		t.Fatalf("deterministic construction result changed: first=%v second=%v", first, second)
	}

	cfg := DefaultHNSWConfig()
	cfg.M, cfg.M0, cfg.EfConstruction = 4, 8, 2
	if got := NewHNSWIndex(NewVectorArena(2), cfg).constructionBreadth(cfg.M0); got != cfg.M0 {
		t.Fatalf("efConstruction below M normalized to %d, want layer capacity %d", got, cfg.M0)
	}
}
