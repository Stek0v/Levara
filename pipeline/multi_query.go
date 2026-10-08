package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Always include original query
	allQueries := append([]string{queryText}, variants...)

	// Search each variant (overfetch 2x per query for RRF headroom)
	perQueryLimit := limit * 2
	if perQueryLimit < 10 {
		perQueryLimit = 10
	}

	// Collect results per query with rank
	idBestRank := make(map[string]int)           // ID → best rank across all queries
	idResult := make(map[string]ScoredResult)     // ID → best-scored result
	idFusedScore := make(map[string]float64)      // ID → RRF fused score

	const k = 60.0 // RRF constant

	for _, q := range allQueries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		results, err := p.SearchByText(ctx, collection, q, perQueryLimit)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if errors.Is(err, embed.ErrGuardRejected) || errors.Is(err, ErrResultFilterRejected) {
			return nil, err
		}
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

	if err := ctx.Err(); err != nil {
		return nil, err
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

	// Resolve all overfetched child hits before limiting: missing or denied
	// parents must not consume the result budget.
	parentScores := make(map[string]float32)
	for _, child := range children {
		id := extractParentID(child.Metadata)
		if id == "" {
			continue
		}
		if score, seen := parentScores[id]; !seen || child.Score > score {
			parentScores[id] = child.Score
		}
	}
	if len(parentScores) == 0 {
		children, err = p.filterResults(ctx, collection, children)
		if err != nil {
			return nil, err
		}
		if len(children) > limit {
			children = children[:limit]
		}
		return children, nil
	}
	db, err := p.collections.Get(collection)
	if err != nil {
		return nil, fmt.Errorf("parent collection: %w", err)
	}
	parents := make([]ScoredResult, 0, len(parentScores))
	for id, score := range parentScores {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		_, metadata, found := db.Get(id)
		if found && len(metadata) > 0 {
			parents = append(parents, ScoredResult{ID: id, Score: score, Metadata: metadata})
		}
	}
	parents, err = p.filterResults(ctx, collection, parents)
	if err != nil {
		return nil, err
	}
	sort.Slice(parents, func(i, j int) bool {
		if parents[i].Score == parents[j].Score {
			return parents[i].ID < parents[j].ID
		}
		return parents[i].Score > parents[j].Score
	})
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
