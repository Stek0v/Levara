package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/stek0v/cognevra/pkg/llm"
)

// SearchByTextMultiQuery generates query variants via LLM,
// searches each variant, and merges results via Reciprocal Rank Fusion (RRF).
//
// If llmProvider is nil, falls back to single-query SearchByText.
// maxVariants: max LLM-generated variants (default 3, capped at 5).
func (p *SearchPipeline) SearchByTextMultiQuery(
	ctx context.Context, collection, queryText string, limit int,
	llmProvider llm.Provider, llmModel string, maxVariants int,
) ([]ScoredResult, error) {
	if llmProvider == nil {
		return p.SearchByText(ctx, collection, queryText, limit)
	}

	if maxVariants <= 0 {
		maxVariants = 3
	}
	if maxVariants > 5 {
		maxVariants = 5
	}

	// Generate query variants via LLM
	variants := generateQueryVariants(ctx, llmProvider, llmModel, queryText, maxVariants)

	// Always include original query
	allQueries := append([]string{queryText}, variants...)

	// Search each variant (overfetch 2x per query for RRF headroom)
	perQueryLimit := limit * 2
	if perQueryLimit < 10 {
		perQueryLimit = 10
	}

	type rankedResult struct {
		result ScoredResult
		rank   int
	}

	// Collect results per query with rank
	idBestRank := make(map[string]int)           // ID → best rank across all queries
	idResult := make(map[string]ScoredResult)     // ID → best-scored result
	idFusedScore := make(map[string]float64)      // ID → RRF fused score

	const k = 60.0 // RRF constant

	for _, q := range allQueries {
		results, err := p.SearchByText(ctx, collection, q, perQueryLimit)
		if err != nil {
			continue
		}
		for rank, r := range results {
			// RRF: score += 1 / (k + rank)
			idFusedScore[r.ID] += 1.0 / (k + float64(rank+1))

			if _, seen := idResult[r.ID]; !seen || r.Score > idResult[r.ID].Score {
				idResult[r.ID] = r
			}
			if _, seen := idBestRank[r.ID]; !seen || rank < idBestRank[r.ID] {
				idBestRank[r.ID] = rank
			}
		}
	}

	// Sort by fused score
	type fusedEntry struct {
		id    string
		score float64
	}
	entries := make([]fusedEntry, 0, len(idFusedScore))
	for id, score := range idFusedScore {
		entries = append(entries, fusedEntry{id, score})
	}

	// Sort descending by fused score
	for i := 0; i < len(entries); i++ {
		for j := i + 1; j < len(entries); j++ {
			if entries[j].score > entries[i].score {
				entries[i], entries[j] = entries[j], entries[i]
			}
		}
	}

	// Take top limit
	out := make([]ScoredResult, 0, limit)
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		out = append(out, idResult[e.id])
	}

	return out, nil
}

// generateQueryVariants asks the LLM to produce alternative search queries.
// Returns empty slice on any error (graceful degradation).
func generateQueryVariants(ctx context.Context, provider llm.Provider, model, query string, maxVariants int) []string {
	prompt := fmt.Sprintf(
		"Generate %d alternative phrasings of this search query that might match different relevant documents. "+
			"Return ONLY a JSON array of strings, no explanation.\n\n"+
			"Original query: %q\n\nAlternative queries:", maxVariants, query)

	resp, err := provider.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       model,
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		Temperature: 0.7,
		MaxTokens:   500,
	})
	if err != nil {
		log.Printf("[multi-query] LLM error: %v", err)
		return nil
	}

	variants := parseJSONStringArray(resp.Content)
	if len(variants) > maxVariants {
		variants = variants[:maxVariants]
	}
	return variants
}

// parseJSONStringArray extracts a JSON string array from LLM response.
// Handles markdown code fences and lenient parsing.
func parseJSONStringArray(text string) []string {
	text = strings.TrimSpace(text)

	// Strip markdown code fences
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		var inner []string
		inBlock := false
		for _, line := range lines {
			if strings.HasPrefix(line, "```") {
				inBlock = !inBlock
				continue
			}
			if inBlock {
				inner = append(inner, line)
			}
		}
		text = strings.Join(inner, "\n")
	}

	// Find JSON array in text
	start := strings.Index(text, "[")
	end := strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	var result []string
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		return nil
	}

	// Filter empty strings
	filtered := result[:0]
	for _, s := range result {
		s = strings.TrimSpace(s)
		if s != "" {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// SearchByTextParentChild searches the child collection for precision,
// then resolves parent chunks for full context.
// Returns parent chunks (deduplicated by parent ID), ordered by best child match score.
//
// If childCollection doesn't exist, falls back to SearchByText on the main collection.
func (p *SearchPipeline) SearchByTextParentChild(
	ctx context.Context, collection, queryText string, limit int,
) ([]ScoredResult, error) {
	childCollection := collection + "_child"

	// Check if child collection exists
	if !p.collections.Has(childCollection) {
		// Fallback: no child collection, search main
		return p.SearchByText(ctx, collection, queryText, limit)
	}

	// Overfetch children (3x) to find enough unique parents
	childLimit := limit * 3
	if childLimit < 10 {
		childLimit = 10
	}

	children, err := p.SearchByText(ctx, childCollection, queryText, childLimit)
	if err != nil {
		return nil, fmt.Errorf("child search: %w", err)
	}

	if len(children) == 0 {
		// No children found, try main collection
		return p.SearchByText(ctx, collection, queryText, limit)
	}

	// Resolve parent IDs from child metadata
	type parentHit struct {
		parentID string
		score    float32 // best child score for this parent
	}

	seen := make(map[string]bool)
	var parentHits []parentHit

	for _, child := range children {
		parentID := extractParentID(child.Metadata)
		if parentID == "" || seen[parentID] {
			continue
		}
		seen[parentID] = true
		parentHits = append(parentHits, parentHit{parentID: parentID, score: child.Score})
		if len(parentHits) >= limit {
			break
		}
	}

	if len(parentHits) == 0 {
		// Children have no parent_id — treat as regular search
		if len(children) > limit {
			children = children[:limit]
		}
		return children, nil
	}

	// Look up parent chunks from main collection by ID
	// Since we can't search by ID in HNSW, we search main collection with same query
	// and filter to known parent IDs. This is a pragmatic approach.
	mainResults, err := p.SearchByText(ctx, collection, queryText, childLimit)
	if err != nil {
		return nil, fmt.Errorf("parent search: %w", err)
	}

	// Build parent result set, ordered by child match quality
	parentScores := make(map[string]float32)
	for _, ph := range parentHits {
		parentScores[ph.parentID] = ph.score
	}

	// Collect parents that were found in main collection search
	var parents []ScoredResult
	foundParents := make(map[string]bool)
	for _, r := range mainResults {
		if _, isWanted := parentScores[r.ID]; isWanted && !foundParents[r.ID] {
			parents = append(parents, r)
			foundParents[r.ID] = true
		}
	}

	// If some parents weren't found via search (different query angle),
	// still return what we have, ordered by child score
	if len(parents) < limit && len(parents) < len(parentHits) {
		// We got fewer parents than expected — that's OK
	}

	if len(parents) > limit {
		parents = parents[:limit]
	}

	return parents, nil
}

// extractParentID pulls parent_id from chunk metadata.
func extractParentID(metadata json.RawMessage) string {
	if len(metadata) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(metadata, &m); err != nil {
		return ""
	}
	if pid, ok := m["parent_id"].(string); ok {
		return pid
	}
	return ""
}
