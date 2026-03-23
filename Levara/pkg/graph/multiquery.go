// Package graph multiquery decomposes complex queries into sub-queries,
// runs them in parallel, and merges results with deduplication.
//
// Example: "Кто такая Эмбер и как она связана с Лукасом?"
// → sub-query 1: "Кто такая Эмбер"
// → sub-query 2: "связь Эмбер Лукас"
// → parallel search → merge + dedup by ID → re-rank
package graph

import (
	"sort"
	"strings"
	"unicode"
)

// MultiQueryResult holds merged results from multiple sub-queries.
type MultiQueryResult struct {
	SubQueries []string
	Results    []MergedResult
}

// MergedResult is a search result that appeared in one or more sub-queries.
type MergedResult struct {
	ID          string
	BestScore   float32 // best (lowest) score across sub-queries
	Appearances int     // how many sub-queries found this result
	FusedScore  float64 // RRF-style fused score (higher = better)
	Metadata    string
}

// DecomposeQuery splits a complex query into sub-queries.
// Strategies:
//   - Split on conjunctions: "и", "а также", "and", "or", ","
//   - Split on question words: "кто", "как", "что", "где", "когда", "why", "how", "what"
//   - Keep original as fallback
func DecomposeQuery(query string) []string {
	// Normalize
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}

	// Try splitting on conjunctions
	conjunctions := []string{" и ", " а также ", " and ", " or ", ", а ", ", и "}
	for _, conj := range conjunctions {
		if strings.Contains(strings.ToLower(q), conj) {
			parts := splitOnConjunction(q, conj)
			if len(parts) >= 2 {
				return dedupStrings(append(parts, q)) // include original
			}
		}
	}

	// Try splitting on question patterns
	questionWords := []string{"кто ", "как ", "что ", "где ", "когда ", "почему ",
		"who ", "how ", "what ", "where ", "when ", "why "}
	lower := strings.ToLower(q)
	qCount := 0
	for _, qw := range questionWords {
		if strings.Contains(lower, qw) {
			qCount++
		}
	}
	if qCount >= 2 {
		// Multiple question words — try to split
		parts := splitOnQuestions(q)
		if len(parts) >= 2 {
			return dedupStrings(append(parts, q))
		}
	}

	// No decomposition possible — return original
	return []string{q}
}

// MergeResults combines results from multiple sub-queries using RRF fusion.
// Results that appear in more sub-queries get boosted.
func MergeResults(subResults [][]SearchResultEntry, topK int) []MergedResult {
	if topK <= 0 {
		topK = 10
	}

	const k = 60.0 // RRF constant

	type entry struct {
		bestScore   float32
		appearances int
		fusedScore  float64
		metadata    string
	}

	merged := make(map[string]*entry)

	for _, results := range subResults {
		for rank, r := range results {
			e, ok := merged[r.ID]
			if !ok {
				e = &entry{bestScore: r.Score, metadata: r.Metadata}
				merged[r.ID] = e
			}
			if r.Score < e.bestScore {
				e.bestScore = r.Score
			}
			e.appearances++
			e.fusedScore += 1.0 / (k + float64(rank+1))
		}
	}

	// Boost items appearing in multiple sub-queries
	for _, e := range merged {
		if e.appearances > 1 {
			e.fusedScore *= float64(e.appearances) * 0.5 // boost factor
		}
	}

	results := make([]MergedResult, 0, len(merged))
	for id, e := range merged {
		results = append(results, MergedResult{
			ID: id, BestScore: e.bestScore, Appearances: e.appearances,
			FusedScore: e.fusedScore, Metadata: e.metadata,
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

// SearchResultEntry is a single search result for merging.
type SearchResultEntry struct {
	ID       string
	Score    float32
	Metadata string
}

func splitOnConjunction(q, conj string) []string {
	lower := strings.ToLower(q)
	idx := strings.Index(lower, conj)
	if idx < 0 {
		return []string{q}
	}
	part1 := strings.TrimSpace(q[:idx])
	part2 := strings.TrimSpace(q[idx+len(conj):])

	var parts []string
	if len(part1) >= 5 {
		parts = append(parts, part1)
	}
	if len(part2) >= 5 {
		parts = append(parts, part2)
	}
	return parts
}

func splitOnQuestions(q string) []string {
	// Split on "?" if present
	if strings.Contains(q, "?") {
		raw := strings.Split(q, "?")
		var parts []string
		for _, p := range raw {
			p = strings.TrimSpace(p)
			if len(p) >= 5 {
				parts = append(parts, p)
			}
		}
		if len(parts) >= 2 {
			return parts
		}
	}

	// Split by sentence-like boundaries
	var parts []string
	var current strings.Builder
	words := strings.Fields(q)
	questionWords := map[string]bool{
		"кто": true, "как": true, "что": true, "где": true, "когда": true, "почему": true,
		"who": true, "how": true, "what": true, "where": true, "when": true, "why": true,
	}

	for i, w := range words {
		clean := strings.TrimFunc(strings.ToLower(w), unicode.IsPunct)
		if i > 0 && questionWords[clean] && current.Len() >= 10 {
			parts = append(parts, strings.TrimSpace(current.String()))
			current.Reset()
		}
		if current.Len() > 0 {
			current.WriteRune(' ')
		}
		current.WriteString(w)
	}
	if current.Len() >= 5 {
		parts = append(parts, strings.TrimSpace(current.String()))
	}

	return parts
}

func dedupStrings(ss []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
