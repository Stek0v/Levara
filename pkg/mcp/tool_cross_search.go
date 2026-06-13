package mcp

// Cross-project / cross-collection search. Wraps vector search over
// multiple collections (capped at 5 per call) and optionally blends
// in SQL-LIKE matches from the memories table. Unlike ToolSearch it
// doesn't support rerank / graph / multi-query variants — the use
// case is a wide scan across collections, not deep single-collection
// ranking. Migrated in F-4 wave 3l with zero new Deps methods;
// reuses NewSearchPipeline + DB + Q + extractOwnerID.

import (
	"context"
	"fmt"
	"log"
	"strings"
)

const (
	// crossSearchCollectionCap limits the number of collections per
	// cross-search call — matches the pre-refactor "max 5" guard.
	// Wider scans are a smell; callers should do their own union.
	crossSearchCollectionCap = 5
	// crossSearchDefaultTopK is the per-collection result cap when
	// "top_k" is not supplied. Matches pre-refactor.
	crossSearchDefaultTopK = 5
	// crossSearchMemoryValueMaxLen truncates memory values in the
	// response so large blobs don't dominate the JSON payload.
	crossSearchMemoryValueMaxLen = 200
)

// sensitiveKeyPatterns names memory keys whose values must not be
// returned in cross-project search results — the cross-project scope
// means a key leaking credentials from collection A would show up
// unexpectedly when searching collection B. isSensitiveKey() matches
// case-insensitively on substring so variants ("API_KEY", "apikey",
// "apiKey") are all covered.
var sensitiveKeyPatterns = []string{
	"api_key", "apikey", "password", "passwd", "secret", "token", "credential", "private_key",
}

// isSensitiveKey reports whether a memory key contains any pattern
// from sensitiveKeyPatterns. Used by ToolCrossSearch to drop
// credential-like rows from the response.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// ToolCrossSearch runs vector search across up to 5 named
// collections and — when include_memories is enabled — also does a
// SQL-LIKE scan of the memories table scoped by collection_name.
//
// Validation:
//   - Missing search_query → IsError.
//   - Missing / empty / oversized collections list → IsError.
//
// Embed-unconfigured returns a plain "No results ..." text, not an
// error — matches ToolSearch's contract for the same branch.
//
// Sensitive keys (isSensitiveKey) are silently dropped from the
// memories section; the caller doesn't learn the key existed.
func ToolCrossSearch(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	query, _ := args["search_query"].(string)
	if query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'search_query' required"}},
			IsError: true,
		}
	}

	collectionsRaw, _ := args["collections"].([]any)
	if len(collectionsRaw) == 0 {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'collections' array required"}},
			IsError: true,
		}
	}
	if len(collectionsRaw) > crossSearchCollectionCap {
		return ToolResult{
			Content: []Content{{Type: "text", Text: fmt.Sprintf("Error: max %d collections per cross-search", crossSearchCollectionCap)}},
			IsError: true,
		}
	}

	var collections []string
	for _, c := range collectionsRaw {
		if s, ok := c.(string); ok && s != "" {
			collections = append(collections, s)
		}
	}

	topK := crossSearchDefaultTopK
	if tk, ok := args["top_k"].(float64); ok && tk > 0 {
		topK = int(tk)
	}
	includeMemories := true
	if im, ok := args["include_memories"].(bool); ok {
		includeMemories = im
	}

	sp := deps.NewSearchPipeline(false)
	if sp == nil {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: "No results (embedding service not configured)",
		}}}
	}

	type collResult struct {
		Collection string           `json:"collection"`
		Vectors    []map[string]any `json:"vectors,omitempty"`
		Memories   []map[string]any `json:"memories,omitempty"`
	}

	var results []collResult
	for _, coll := range collections {
		cr := collResult{Collection: coll}

		if res, err := sp.SearchByText(ctx, coll, query, topK); err == nil {
			for _, r := range res {
				cr.Vectors = append(cr.Vectors, map[string]any{
					"id":       r.ID,
					"score":    r.Score,
					"metadata": string(r.Metadata),
				})
			}
		}

		if includeMemories {
			if db := deps.DB(); db != nil {
				cr.Memories = crossSearchMemoriesFor(ctx, deps, coll, query, topK)
			}
		}

		results = append(results, cr)
	}

	// Observability: log the scan. Matches pre-refactor. Uses the
	// stdlib logger rather than a Deps method because log.Printf is
	// inert in tests and pkg/mcp already depends on it transitively.
	log.Printf("[cross-project] searched %d collections for query: %s", len(collections), Truncate(query, 50))

	return jsonResult(map[string]any{
		"results":     results,
		"collections": collections,
		"query":       query,
	})
}

// crossSearchMemoriesFor runs the SQL-LIKE scan of the memories table
// scoped to collection + caller ownership. Caller-owned rows OR
// shared (owner_id=”) rows match. Sensitive keys are dropped.
// Errors surface as an empty slice — cross-search is best-effort per
// collection.
func crossSearchMemoriesFor(ctx context.Context, deps Deps, collection, query string, topK int) []map[string]any {
	db := deps.DB()
	if db == nil {
		return nil
	}
	pattern := "%" + query + "%"
	ownerID := extractOwnerID(ctx)

	rows, err := db.QueryContext(ctx, deps.Q(`
		SELECT key, value, type FROM memories
		WHERE (key LIKE $1 OR value LIKE $2)
		AND (collection_name = $3 OR collection_name = '')
		AND (owner_id = $4 OR owner_id = '')
		ORDER BY updated_at DESC LIMIT $5
	`), pattern, pattern, collection, ownerID, topK)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []map[string]any
	for rows.Next() {
		var key, value, typ string
		if err := rows.Scan(&key, &value, &typ); err != nil {
			continue
		}
		if isSensitiveKey(key) {
			continue
		}
		out = append(out, map[string]any{
			"key":   key,
			"value": Truncate(value, crossSearchMemoryValueMaxLen),
			"type":  typ,
		})
	}
	return out
}
