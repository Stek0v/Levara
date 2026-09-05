package bm25

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"
)

// Independently score document snapshots, then fully sort them. Equal-score
// documents are interchangeable in Search's existing ranking contract.
func fullSortBM25Reference(idx *Index, query string) []Result {
	docs := idx.Documents()
	terms := tokenize(query)
	if len(docs) == 0 || len(terms) == 0 {
		return nil
	}
	tokens := make([][]string, len(docs))
	frequencies := make([]map[string]int, len(docs))
	var total int
	for i, doc := range docs {
		tokens[i] = tokenize(doc.Text)
		total += len(tokens[i])
		frequencies[i] = make(map[string]int)
		for _, term := range tokens[i] {
			frequencies[i][term]++
		}
	}
	n := float64(len(docs))
	avg := float64(total) / n
	scores := make(map[int]float64)
	for _, term := range terms {
		var matches int
		for _, freq := range frequencies {
			if freq[term] > 0 {
				matches++
			}
		}
		idf := math.Log((n-float64(matches)+0.5)/(float64(matches)+0.5) + 1)
		for i, freq := range frequencies {
			if freq[term] == 0 {
				continue
			}
			f, dl := float64(freq[term]), float64(len(tokens[i]))
			tf := (f * (idx.k1 + 1)) / (f + idx.k1*(1-idx.b+idx.b*dl/avg))
			scores[i] += idf * tf
		}
	}
	results := make([]Result, 0, len(scores))
	for i, score := range scores {
		results = append(results, Result{ID: docs[i].ID, Score: score, Metadata: docs[i].Metadata})
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	return results
}

func TestSearchTopKMatchesFullSortAcrossMutations(t *testing.T) {
	idx := NewIndexWithParams(1.7, 0.6)
	check := func() {
		t.Helper()
		for _, query := range []string{"", "x", "missing", "common", "common rare", "common common rare", "русский текст"} {
			all := fullSortBM25Reference(idx, query)
			byID := make(map[string]Result, len(all))
			for _, result := range all {
				byID[result.ID] = result
			}
			for _, k := range []int{-5, 0, 1, 2, 10, 50, 100, 179, 180, 1000} {
				limit := k
				if limit <= 0 {
					limit = 10
				}
				limit = min(limit, len(all))
				got := idx.Search(query, k)
				if len(got) != limit {
					t.Fatalf("query=%q k=%d got %d results, want %d", query, k, len(got), limit)
				}
				seen := make(map[string]bool, len(got))
				for i, result := range got {
					want, found := byID[result.ID]
					if !found || seen[result.ID] || result.Score != all[i].Score || result.Score != want.Score || result.Metadata != want.Metadata {
						t.Fatalf("query=%q k=%d position=%d result=%+v reference score=%v ID result=%+v", query, k, i, result, all[i].Score, want)
					}
					seen[result.ID] = true
				}
			}
		}
	}
	check()
	for i := 0; i < 180; i++ {
		text := strings.Repeat("common ", i%7+1) + strings.Repeat("filler ", i%4)
		if i%5 == 0 {
			text += "rare русский текст"
		}
		idx.Add(fmt.Sprintf("doc-%03d", i), text, fmt.Sprintf("metadata-%d", i))
	}
	idx.Add("empty", "", "empty metadata")
	check()
	idx.Add("doc-000", "rare rare rare common", "replaced metadata")
	idx.Remove("doc-007")
	idx.Remove("missing")
	check()
	idx.Clear()
	check()
	for _, id := range []string{"tie-z", "tie-a", "tie-b"} {
		idx.Add(id, "common", id)
	}
	check()
}

func TestSearchTopKConcurrentMutations(t *testing.T) {
	idx := NewIndex()
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		for i := 0; i < 300; i++ {
			id := fmt.Sprintf("doc-%d", i%32)
			idx.Add(id, strings.Repeat("common ", i%5+1), id)
			if i%3 == 0 {
				idx.Remove(fmt.Sprintf("doc-%d", (i+5)%32))
			}
			if i%50 == 0 {
				idx.Clear()
			}
		}
	}()
	for _, k := range []int{10, 100} {
		go func() {
			defer workers.Done()
			for i := 0; i < 300; i++ {
				results := idx.Search("common", k)
				if len(results) > k {
					t.Errorf("got %d results for k=%d", len(results), k)
				}
				for j, result := range results {
					if result.Metadata != result.ID || result.Score < 0 || math.IsNaN(result.Score) || (j > 0 && result.Score > results[j-1].Score) {
						t.Errorf("inconsistent result during mutation: %+v", result)
					}
				}
			}
		}()
	}
	workers.Wait()
}

func BenchmarkBM25SearchTopK(b *testing.B) {
	idx := NewIndex()
	for i := 0; i < 10000; i++ {
		text := strings.Repeat("common ", i%7+1) + "additional varied document text"
		if i%100 == 0 {
			text += " needle"
		}
		idx.Add(fmt.Sprintf("doc-%05d", i), text, "metadata")
	}
	for _, query := range []struct{ name, text string }{{"dense", "common additional"}, {"sparse", "needle"}} {
		for _, k := range []int{1, 10, 100, 1000, 5000, 10000} {
			b.Run(fmt.Sprintf("%s/k%d", query.name, k), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					idx.Search(query.text, k)
				}
			})
		}
	}
}
