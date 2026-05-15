package pipeline

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/rerank"
)

// SearchPipeline orchestrates the full search path:
// embed query → vector search (in-process) → return results.
// No HTTP round-trip — embed client calls embed-server directly,
// vector search calls CollectionManager in-process.
type SearchPipeline struct {
	embedClient *embed.Client
	collections *store.CollectionManager
	reranker    *rerank.Client // nil = no reranking
}

// NewSearchPipeline creates a pipeline backed by CollectionManager + embed client.
// reranker is optional (nil = disabled).
func NewSearchPipeline(embedClient *embed.Client, collections *store.CollectionManager, reranker *rerank.Client) *SearchPipeline {
	return &SearchPipeline{
		embedClient: embedClient,
		collections: collections,
		reranker:    reranker,
	}
}

// SearchByText embeds the query text and searches the collection.
// This is the Go equivalent of Python:
//
//	query_vec = await embed_data([query_text])     # 5-15ms HTTP
//	results = await _post("/api/v1/search", ...)   # 2.6ms HTTP+JSON
//	filtered = [r for r in results if ...]          # 0.1ms
//
// In Go, vector search is in-process (~0.3ms), no filtering needed (native collections).
func (p *SearchPipeline) SearchByText(ctx context.Context, collection, queryText string, limit int) ([]ScoredResult, error) {
	// Step 1: Embed query (HTTP to embed-server, same as Python)
	vec, err := p.embedClient.EmbedSingle(ctx, queryText)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	// Step 2: Vector search (IN-PROCESS — 0ms transport!)
	return p.SearchByVector(collection, vec, limit)
}

// SearchByVector searches with a pre-computed vector (no embedding step).
func (p *SearchPipeline) SearchByVector(collection string, vector []float32, limit int) ([]ScoredResult, error) {
	results, err := p.collections.Search(collection, vector, limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}

	scored := make([]ScoredResult, len(results))
	for i, r := range results {
		scored[i] = ScoredResult{
			ID:       r.ID,
			Score:    r.Score,
			Metadata: r.Data,
		}
	}
	return scored, nil
}

// extractText pulls the "text" field from metadata JSON.
// ExtractText pulls a usable text payload out of a result's metadata
// (looks for "text" first, then "name"). Returns "" when neither field
// is present or the JSON fails to parse. Exported so other search paths
// (e.g. hybridSearch) can mirror chunksSearch's rerank input shape
// without duplicating the parsing rules.
func ExtractText(metadata json.RawMessage) string { return extractText(metadata) }

func extractText(metadata json.RawMessage) string {
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
	// Fallback: try "name" field (entity metadata)
	if name, ok := m["name"].(string); ok {
		return name
	}
	return ""
}

// BatchSearchByText embeds multiple queries and searches concurrently.
func (p *SearchPipeline) BatchSearchByText(ctx context.Context, collection string, queries []string, limit int) ([][]ScoredResult, error) {
	// Embed all queries in one batch
	vecs, err := p.embedClient.EmbedTexts(ctx, queries)
	if err != nil {
		return nil, fmt.Errorf("batch embed: %w", err)
	}

	// Search each query (in-process, fast)
	results := make([][]ScoredResult, len(queries))
	for i, vec := range vecs {
		res, err := p.SearchByVector(collection, vec, limit)
		if err != nil {
			return nil, fmt.Errorf("search query %d: %w", i, err)
		}
		results[i] = res
	}

	return results, nil
}
