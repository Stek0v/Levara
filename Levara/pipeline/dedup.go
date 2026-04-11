package pipeline

import (
	"encoding/json"
	"strings"
)

// DeduplicateResults removes near-duplicate results by Jaccard similarity
// on word sets. Keeps the result with the higher score from each duplicate pair.
//
// threshold: Jaccard similarity above which two results are considered duplicates.
// Typical value: 0.85 (85% word overlap).
//
// O(n²) but n = topK (typically 10-30), so ~100-900 comparisons.
func DeduplicateResults(results []ScoredResult, threshold float64) []ScoredResult {
	if len(results) <= 1 || threshold <= 0 {
		return results
	}

	// Extract word sets for each result
	wordSets := make([]map[string]struct{}, len(results))
	for i, r := range results {
		wordSets[i] = tokenize(extractTextForDedup(r.Metadata))
	}

	// Mark duplicates: for each pair, if similarity > threshold, mark lower-scored as duplicate
	removed := make([]bool, len(results))
	for i := 0; i < len(results); i++ {
		if removed[i] {
			continue
		}
		for j := i + 1; j < len(results); j++ {
			if removed[j] {
				continue
			}
			// Skip dedup if either has no text (can't compare)
			if len(wordSets[i]) == 0 || len(wordSets[j]) == 0 {
				continue
			}
			sim := jaccardSimilarity(wordSets[i], wordSets[j])
			if sim > threshold {
				// Keep the one with higher score, remove the other
				if results[i].Score >= results[j].Score {
					removed[j] = true
				} else {
					removed[i] = true
					break // i is removed, skip remaining j comparisons
				}
			}
		}
	}

	// Collect survivors
	out := make([]ScoredResult, 0, len(results))
	for i, r := range results {
		if !removed[i] {
			out = append(out, r)
		}
	}
	return out
}

// jaccardSimilarity computes |A ∩ B| / |A ∪ B| for two word sets.
func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}

	intersection := 0
	for w := range a {
		if _, ok := b[w]; ok {
			intersection++
		}
	}

	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}

	return float64(intersection) / float64(union)
}

// tokenize splits text into a set of lowercase words.
func tokenize(text string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(text))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		// Strip common punctuation from word edges
		w = strings.Trim(w, ".,;:!?\"'()[]{}«»—–-")
		if len(w) > 0 {
			set[w] = struct{}{}
		}
	}
	return set
}

// extractTextForDedup pulls text from metadata for comparison.
func extractTextForDedup(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	if text, ok := m["text"].(string); ok {
		return text
	}
	if name, ok := m["name"].(string); ok {
		return name
	}
	return ""
}
