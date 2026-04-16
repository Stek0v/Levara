package mcp

// Vector + hybrid search tool. Migrated from internal/http during
// F-4 wave 3k. Drives *pipeline.SearchPipeline (abstracted behind the
// SearchPipeline interface on Deps) with one of five strategies
// picked from a small flag matrix, then applies dedup + metadata
// post-filter + topK cap before marshaling the JSON response.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/stek0v/cognevra/pipeline"
	"github.com/stek0v/cognevra/pkg/graphrank"
	"github.com/stek0v/cognevra/pkg/router"
)

const (
	// searchDefaultTopK is the result cap when the caller omits top_k.
	searchDefaultTopK = 10
	// searchMetaOverfetchFactor controls how much extra we fetch when a
	// room/tags filter is active. Post-filter discards non-matching
	// rows so we need slack to still return topK.
	searchMetaOverfetchFactor = 3
	// searchDedupThreshold is the cosine threshold for merging near-
	// duplicate results. Matches pre-refactor (0.85).
	searchDedupThreshold = 0.85
	// searchMultiQueryN is the number of rewritten queries the
	// multi-query branch generates per call. Matches pre-refactor.
	searchMultiQueryN = 3
)

// searchTypesForMode returns the whitelist of search types allowed
// under a given mode. nil means "no restriction" (full / auto).
// Mirror of the pre-refactor helper in internal/http/mcp.go.
func searchTypesForMode(mode string) map[string]bool {
	switch mode {
	case "rag":
		return map[string]bool{
			"CHUNKS": true, "HYBRID": true, "CHUNKS_LEXICAL": true,
			"RAG_COMPLETION": true, "SUMMARIES": true, "WEIGHTED_HYBRID": true,
		}
	case "graph":
		return map[string]bool{
			"GRAPH_COMPLETION": true, "GRAPH_COMPLETION_COT": true,
			"GRAPH_COMPLETION_CONTEXT_EXTENSION": true, "GRAPH_SUMMARY_COMPLETION": true,
			"COMMUNITY_LOCAL": true, "COMMUNITY_GLOBAL": true,
			"CYPHER": true, "TRIPLET_COMPLETION": true, "TEMPORAL": true,
		}
	default:
		return nil
	}
}

// defaultTypeForMode returns the fallback search_type to coerce to
// when the caller asked for a type outside the mode's whitelist.
func defaultTypeForMode(mode string) string {
	switch mode {
	case "rag":
		return "CHUNKS"
	case "graph":
		return "GRAPH_COMPLETION"
	default:
		return "AUTO"
	}
}

// extractQueryEntitiesForSearch finds graph entity names whose
// lowercased `name` matches any whitespace-separated word of the
// query (length > 2, alphanumeric-cleaned). Returns up to 10 names.
// Used by the GRAPH_RERANK branch to feed graphrank.RerankWithGraph.
// Port of internal/http's extractQueryEntities.
func extractQueryEntitiesForSearch(ctx context.Context, deps Deps, query string) []string {
	db := deps.DB()
	if db == nil || query == "" {
		return nil
	}
	words := strings.Fields(strings.ToLower(query))
	var conditions []string
	var args []any
	for i, w := range words {
		cleaned := strings.TrimFunc(w, func(r rune) bool {
			return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9')
		})
		if len(cleaned) > 2 {
			conditions = append(conditions, fmt.Sprintf("LOWER(name) LIKE $%d", i+1))
			args = append(args, "%"+cleaned+"%")
		}
	}
	if len(conditions) == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx,
		deps.Q(fmt.Sprintf("SELECT name FROM graph_nodes WHERE %s LIMIT 10", strings.Join(conditions, " OR "))),
		args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	return names
}

// searchArgs captures all flag and parameter parsing for ToolSearch.
// Kept as a struct so the decision logic (mode gating, routing,
// type→flag mapping) stays testable without touching infrastructure.
type searchArgs struct {
	query         string
	searchType    string
	mode          string
	collection    string
	topK          int
	roomFilter    string
	tagFilters    []string
	doRerank      bool
	doParentChild bool
	doMultiQuery  bool
	doDedup       bool
	doGraphRerank bool
}

// parseSearchArgs pulls values from the args map, applying the
// pre-refactor defaults. Does NOT apply mode gating or AUTO routing —
// those touch deployment state (router.Capabilities).
func parseSearchArgs(args map[string]any) searchArgs {
	out := searchArgs{
		searchType: "AUTO",
		mode:       "auto",
		topK:       searchDefaultTopK,
		doDedup:    true, // enabled by default
	}
	out.query, _ = args["search_query"].(string)
	if st, _ := args["search_type"].(string); st != "" {
		out.searchType = st
	}
	if tk, ok := args["top_k"].(float64); ok {
		out.topK = int(tk)
	}
	out.collection, _ = args["collection"].(string)
	out.roomFilter, _ = args["room"].(string)
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				out.tagFilters = append(out.tagFilters, s)
			}
		}
	}
	out.doRerank, _ = args["rerank"].(bool)
	out.doParentChild, _ = args["parent_child"].(bool)
	out.doMultiQuery, _ = args["multi_query"].(bool)
	if dd, ok := args["dedup"].(bool); ok {
		out.doDedup = dd
	}
	out.doGraphRerank, _ = args["graph_rerank"].(bool)
	if m, _ := args["mode"].(string); m != "" {
		out.mode = m
	}
	return out
}

// applyModeGating coerces search_type when mode restricts it. Returns
// the (possibly updated) searchType. "AUTO" and empty types pass
// through untouched — the router handles them later.
func applyModeGating(mode, searchType string) string {
	if mode != "rag" && mode != "graph" {
		return searchType
	}
	allowed := searchTypesForMode(mode)
	if allowed == nil {
		return searchType
	}
	upper := strings.ToUpper(searchType)
	if upper == "AUTO" || upper == "" {
		return searchType
	}
	if allowed[upper] {
		return searchType
	}
	return defaultTypeForMode(mode)
}

// applyTypeFlags maps the (possibly routed) search_type into the
// corresponding feature flag. The flag is ORed with args-derived
// flags — a tool call with both rerank:true and search_type=BASIC
// still reranks.
func applyTypeFlags(searchType string, a *searchArgs) {
	switch strings.ToUpper(searchType) {
	case "PARENT_CHILD":
		a.doParentChild = true
	case "MULTI_QUERY":
		a.doMultiQuery = true
	case "RERANK":
		a.doRerank = true
	case "GRAPH_RERANK":
		a.doGraphRerank = true
	}
}

// ToolSearch runs a vector-based search (+ optional rerank / graph /
// multi-query variants) and returns a JSON payload with the top
// results plus routing metadata.
//
// High-level flow:
//  1. Parse args + apply mode gating (rag/graph modes restrict
//     search_type).
//  2. If search_type is AUTO/FEELING_LUCKY, consult the router with
//     the deployment's capabilities.
//  3. Build a SearchPipeline via Deps. nil → "embedding not
//     configured" short-circuit.
//  4. For each collection (configured filter or all), dispatch on the
//     flag combination and execute the matching strategy.
//  5. Dedup (when enabled), apply room/tags post-filter, cap at topK.
//  6. Marshal results + routing metadata to JSON.
func ToolSearch(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	a := parseSearchArgs(args)
	if a.query == "" {
		return ToolResult{
			Content: []Content{{Type: "text", Text: "Error: 'search_query' required"}},
			IsError: true,
		}
	}

	a.searchType = applyModeGating(a.mode, a.searchType)

	// AUTO / FEELING_LUCKY → consult router.
	var routingInfo *router.Decision
	upper := strings.ToUpper(a.searchType)
	if upper == "AUTO" || upper == "FEELING_LUCKY" {
		caps := deps.SearchCapabilities()
		// Mode-aware: suppress graph capabilities in rag mode so the
		// router doesn't pick a graph-backed search type.
		if a.mode == "rag" {
			caps.HasNeo4j = false
			caps.HasPostgres = false
			caps.HasCommunities = false
		}
		d := router.Route(a.query, caps)
		routingInfo = &d
		a.searchType = d.SearchType
	}

	applyTypeFlags(a.searchType, &a)

	sp := deps.NewSearchPipeline(a.doRerank)
	if sp == nil {
		return ToolResult{Content: []Content{{
			Type: "text",
			Text: "No results (embedding service not configured)",
		}}}
	}

	var colls []string
	if a.collection != "" {
		colls = []string{a.collection}
	} else {
		colls = deps.ListCollections()
	}

	hasMetaFilter := a.roomFilter != "" || len(a.tagFilters) > 0
	fetchK := a.topK
	if hasMetaFilter {
		fetchK = a.topK * searchMetaOverfetchFactor
	}

	var results []map[string]any
	wasReranked := false

	for _, coll := range colls {
		res, reranked := runSearchStrategy(ctx, deps, sp, coll, a.query, fetchK, a)
		if reranked {
			wasReranked = true
		}

		if a.doDedup && len(res) > 1 {
			res = pipeline.DeduplicateResults(res, searchDedupThreshold)
		}

		for _, r := range res {
			if hasMetaFilter && !ChunkMetaMatches(r.Metadata, a.roomFilter, a.tagFilters) {
				continue
			}
			results = append(results, map[string]any{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   string(r.Metadata),
			})
			if len(results) >= a.topK {
				break
			}
		}
		if len(results) >= a.topK {
			break
		}
	}

	response := map[string]any{
		"search_type": a.searchType,
		"reranked":    wasReranked,
	}
	if len(results) == 0 {
		response["results"] = []any{}
	} else {
		response["results"] = results
	}
	if routingInfo != nil {
		response["routing"] = map[string]any{
			"selected_type": routingInfo.SearchType,
			"reason":        routingInfo.Reason,
			"confidence":    routingInfo.Confidence,
			"alternatives":  routingInfo.Alternatives,
			"source":        "routed",
		}
	}

	out, _ := json.MarshalIndent(response, "", "  ")
	return ToolResult{Content: []Content{{Type: "text", Text: string(out)}}}
}

// runSearchStrategy dispatches to one of the five search branches
// based on the flag combination. Returns the results and whether a
// rerank actually ran (only the WithRerank branch may set this true).
// Errors from the pipeline are swallowed per branch — the caller
// continues to the next collection, matching pre-refactor behavior.
func runSearchStrategy(ctx context.Context, deps Deps, sp SearchPipeline, coll, query string, fetchK int, a searchArgs) (results []pipeline.ScoredResult, reranked bool) {
	switch {
	case a.doParentChild:
		res, err := sp.SearchByTextParentChild(ctx, coll, query, fetchK)
		if err != nil {
			return nil, false
		}
		return res, false
	case a.doMultiQuery && deps.LLMProvider() != nil:
		res, err := sp.SearchByTextMultiQuery(ctx, coll, query, fetchK,
			deps.LLMProvider(), deps.LLMModel(), searchMultiQueryN)
		if err != nil {
			return nil, false
		}
		return res, false
	case a.doRerank && sp.RerankEnabled():
		res, rr, err := sp.SearchByTextWithRerank(ctx, coll, query, fetchK)
		if err != nil {
			return nil, false
		}
		return res, rr
	case a.doGraphRerank && deps.DB() != nil:
		res, err := sp.SearchByText(ctx, coll, query, fetchK)
		if err != nil || len(res) == 0 {
			return res, false
		}
		// Convert to graphrank.ScoredResult, rerank, convert back.
		grRes := make([]graphrank.ScoredResult, len(res))
		for i, r := range res {
			grRes[i] = graphrank.ScoredResult{ID: r.ID, Score: r.Score, Metadata: r.Metadata}
		}
		queryEntities := extractQueryEntitiesForSearch(ctx, deps, query)
		grReranked := graphrank.RerankWithGraph(ctx, deps.DB(), queryEntities, grRes, graphrank.DefaultConfig())
		for i, r := range grReranked {
			if i >= len(res) {
				break
			}
			res[i] = pipeline.ScoredResult{ID: r.ID, Score: r.Score, Metadata: r.Metadata}
		}
		return res, false
	default:
		res, err := sp.SearchByText(ctx, coll, query, fetchK)
		if err != nil {
			return nil, false
		}
		return res, false
	}
}
