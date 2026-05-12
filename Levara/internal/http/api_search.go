// api_search.go — Levara search endpoints split out of api.go
// (T4). Covers POST /search, /search/text, /search/ plus the handlers for
// the various query_type branches (CHUNKS, HYBRID, BM25, TEMPORAL,
// RAG_COMPLETION, SUMMARIES) and their shared helpers.
//
// Graph-based query types (GRAPH_COMPLETION, CYPHER, NATURAL_LANGUAGE,
// TRIPLET_COMPLETION, CODING_RULES, COMMUNITY_LOCAL/GLOBAL,
// GRAPH_COMPLETION_CONTEXT_EXTENSION, GRAPH_COMPLETION_COT) live in
// graph_search.go — those will be extracted into strategy objects by T5.
package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/graph"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/graphrank"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/rerank"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/temporal"
)

// ── U5: Levara Search ──

type UnifiedSearchRequest struct {
	QueryText         string   `json:"query_text"`
	QueryType         string   `json:"query_type"` // CHUNKS, GRAPH_COMPLETION, etc.
	TopK              int      `json:"top_k"`
	CypherQuery       string   `json:"cypher_query"`    // Raw Cypher for CYPHER search type
	Collection        string   `json:"collection"`      // Filter search to one collection (empty = all)
	Domain            string   `json:"domain"`          // Optional: filter to collections tagged with this domain
	SessionID         string   `json:"session_id"`      // Conversational memory: load prior interactions
	Tags              []string `json:"tags"`            // Optional: filter results by metadata tags
	Rerank            bool     `json:"rerank"`          // Optional: rerank results via cross-encoder
	IncludeDebug      bool     `json:"include_debug"`   // Optional: envelope list responses with debug metadata
	StrictGrounded    bool     `json:"strict_grounded"` // Optional: abstain if no evidence_ids
	VerifyResults     bool     `json:"verify_results"`  // Optional: verify metadata JSON and filter malformed rows
	MinScore          float64  `json:"min_score"`       // Optional: drop hits below this score
	AllowedDatasetIDs []string `json:"-"`               // RBAC: nil = no filtering (dev mode)
}

// isQueryWordRune reports whether r is part of a meaningful query token.
// BL-4: we used to filter with `'a' <= r && r <= 'z'`, which dropped every
// non-ASCII letter — Cyrillic, CJK, accented Latin — so Russian queries
// lost graph expansion entirely. unicode.IsLetter + IsDigit is the fix.
func isQueryWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// extractQueryEntities finds graph entity names that match query terms.
// Used by graphrank to compute proximity between query entities and result entities.
func extractQueryEntities(ctx context.Context, db *sql.DB, query string) []string {
	if db == nil || query == "" {
		return nil
	}
	words := strings.Fields(strings.ToLower(query))
	var conditions []string
	var args []any
	for i, w := range words {
		cleaned := strings.TrimFunc(w, func(r rune) bool { return !isQueryWordRune(r) })
		// Count runes, not bytes — "кот" has 3 runes but 6 bytes, and we
		// care about lexical length here.
		if len([]rune(cleaned)) > 2 {
			conditions = append(conditions, fmt.Sprintf("LOWER(name) LIKE $%d", i+1))
			args = append(args, "%"+cleaned+"%")
		}
	}
	if len(conditions) == 0 {
		return nil
	}
	rows, err := db.QueryContext(ctx,
		Q(fmt.Sprintf("SELECT name FROM graph_nodes WHERE %s LIMIT 10", strings.Join(conditions, " OR "))), args...)
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

// expandQueryFromGraph looks up query terms in graph_nodes and returns related entity names.
// This helps BM25 find documents containing synonyms/related terms not in the original query.
// Returns space-separated additional terms, or empty string if no expansion found.
func expandQueryFromGraph(ctx context.Context, db *sql.DB, query string) string {
	if db == nil || query == "" {
		return ""
	}
	// Extract meaningful words (>3 runes) as potential entity name fragments.
	// BL-4: unicode-aware filter so non-ASCII queries (Russian, CJK, etc.)
	// still produce expansion candidates.
	words := strings.Fields(strings.ToLower(query))
	var searchTerms []string
	for _, w := range words {
		cleaned := strings.TrimFunc(w, func(r rune) bool { return !isQueryWordRune(r) })
		if len([]rune(cleaned)) > 3 {
			searchTerms = append(searchTerms, cleaned)
		}
	}
	if len(searchTerms) == 0 {
		return ""
	}

	// Search graph_nodes for entities matching query words
	var conditions []string
	var args []any
	for i, term := range searchTerms {
		conditions = append(conditions, fmt.Sprintf("LOWER(name) LIKE $%d", i+1))
		args = append(args, "%"+term+"%")
	}

	rows, err := db.QueryContext(ctx,
		Q(fmt.Sprintf(`SELECT DISTINCT gn2.name FROM graph_edges ge
			JOIN graph_nodes gn ON ge.source_id = gn.id
			JOIN graph_nodes gn2 ON ge.target_id = gn2.id
			WHERE (%s) AND ge.relationship_name <> 'HAPPENED_AT'
			AND (gn2.type IS NULL OR gn2.type <> 'TemporalEvent')
			LIMIT 5`, strings.Join(conditions, " OR "))),
		args...)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var expanded []string
	seen := make(map[string]bool)
	for rows.Next() {
		var name string
		if rows.Scan(&name) == nil && !seen[strings.ToLower(name)] {
			seen[strings.ToLower(name)] = true
			expanded = append(expanded, name)
		}
	}
	return strings.Join(expanded, " ")
}

// resolveCollections returns the list of collections to search.
// If req.Collection is set, only that collection is searched.
// If req.Domain is set, only collections matching that domain are searched.
// Otherwise all collections are listed.
func resolveCollections(cfg APIConfig, req UnifiedSearchRequest) []string {
	if req.Collection != "" {
		return []string{req.Collection}
	}
	if req.Domain != "" {
		return cfg.Collections.ListByDomain(req.Domain)
	}
	return cfg.Collections.List()
}

// filterByTags post-filters search results by metadata tags.
// If tags is empty, no filtering is applied.
func filterByTags(results []fiber.Map, tags []string) []fiber.Map {
	if len(tags) == 0 {
		return results
	}
	wantTags := make(map[string]bool, len(tags))
	for _, t := range tags {
		wantTags[strings.ToLower(t)] = true
	}
	var filtered []fiber.Map
	for _, r := range results {
		meta, ok := r["metadata"]
		if !ok {
			continue
		}
		var metaMap map[string]any
		switch m := meta.(type) {
		case map[string]any:
			metaMap = m
		case json.RawMessage:
			json.Unmarshal(m, &metaMap)
		}
		if metaMap == nil {
			continue
		}
		// Check tags field in metadata
		if tagsVal, ok := metaMap["tags"]; ok {
			if tagsList, ok := tagsVal.([]any); ok {
				for _, t := range tagsList {
					if ts, ok := t.(string); ok && wantTags[strings.ToLower(ts)] {
						filtered = append(filtered, r)
						goto next
					}
				}
			}
		}
		// Check key field (for LongMemEval-style facts)
		if key, ok := metaMap["key"]; ok {
			if ks, ok := key.(string); ok && wantTags[strings.ToLower(ks)] {
				filtered = append(filtered, r)
			}
		}
	next:
	}
	if len(filtered) == 0 {
		return results // fallback: return unfiltered if no matches
	}
	return filtered
}

// filterByAllowedDatasets post-filters search results by allowed dataset IDs.
// If allowedIDs is nil, no filtering is applied (dev mode / backward compat).
func filterByAllowedDatasets(results []fiber.Map, allowedIDs []string) []fiber.Map {
	if allowedIDs == nil {
		return results
	}
	allowed := make(map[string]bool, len(allowedIDs))
	for _, id := range allowedIDs {
		allowed[id] = true
	}
	var filtered []fiber.Map
	for _, r := range results {
		dsID := extractDatasetID(r)
		if dsID == "" || allowed[dsID] {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// extractDatasetID extracts dataset_id from a result's metadata field.
func extractDatasetID(r fiber.Map) string {
	meta, ok := r["metadata"]
	if !ok {
		return ""
	}
	var m map[string]any
	switch v := meta.(type) {
	case json.RawMessage:
		json.Unmarshal(v, &m)
	case []byte:
		json.Unmarshal(v, &m)
	case string:
		json.Unmarshal([]byte(v), &m)
	case map[string]any:
		m = v
	}
	dsID, _ := m["dataset_id"].(string)
	if dsID == "" {
		dsID, _ = m["project_id"].(string)
	}
	return dsID
}

// searchHandler — POST /search, /search/text, /search/ (all aliases).
// Dispatches to the right strategy via the SearchStrategies registry
// (T5); unknown query_type falls back to CHUNKS.
//
// @Summary     Unified search entry point
// @Description query_type selects the strategy. AUTO routes via the adaptive router; explicit values pin a strategy. CHUNKS/HYBRID/BM25/TEMPORAL/RAG_COMPLETION/SUMMARIES are vector-side; GRAPH_COMPLETION, CYPHER, NATURAL_LANGUAGE, TRIPLET_COMPLETION, CODE, COMMUNITY_LOCAL/GLOBAL, GRAPH_COMPLETION_CONTEXT_EXTENSION/COT are graph-side.
// @Tags        search
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body UnifiedSearchRequest true "Query + options"
// @Success     200 {object} map[string]any "strategy-dependent shape; always includes search_type"
// @Router      /search [post]
// @Router      /search/text [post]
func searchHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req UnifiedSearchRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if req.QueryText == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "query_text required"})
		}
		if req.TopK <= 0 {
			req.TopK = 10
		}

		// Request-scoped deadline for all downstream operations in this call.
		reqCtx, cancel := searchRequestContext(c)
		defer cancel()

		// RBAC: resolve allowed dataset IDs for this user
		userID, _ := c.Locals("user_id").(string)
		req.AllowedDatasetIDs = GetAllowedDatasetIDs(cfg.DB, reqCtx, userID)

		queryType := strings.ToUpper(req.QueryType)

		// Smart routing: AUTO, FEELING_LUCKY, or empty → heuristic router
		var routingDecision *router.Decision
		source := "explicit"
		if queryType == "" || queryType == "AUTO" || queryType == "FEELING_LUCKY" {
			source = "routed"
			routeStart := time.Now()
			caps := capabilitiesFromConfig(cfg)
			d := router.Route(req.QueryText, caps)
			metrics.RouterDecisionDuration.Observe(time.Since(routeStart).Seconds())

			// Apply adaptive weight adjustments from feedback history
			if cfg.AdaptiveWeights != nil && len(d.Alternatives) > 0 {
				bestType := d.SearchType
				bestScore := cfg.AdaptiveWeights.AdjustScore(d.SearchType, float64(d.Confidence))
				for _, alt := range d.Alternatives {
					adjusted := cfg.AdaptiveWeights.AdjustScore(alt.SearchType, float64(alt.Score))
					if adjusted > bestScore {
						bestScore = adjusted
						bestType = alt.SearchType
					}
				}
				if bestType != d.SearchType {
					log.Printf("[router] adaptive override: %s (%.2f) → %s (%.2f)", d.SearchType, d.Confidence, bestType, bestScore)
					d.SearchType = bestType
					d.Confidence = float32(bestScore)
					d.Reason = "adaptive: " + d.Reason
				}
			}

			routingDecision = &d
			queryType = d.SearchType
		}

		metrics.SearchRequestsByType.WithLabelValues(queryType, source).Inc()
		c.Locals("routing_source", source)

		// Store routing metadata for response enrichment
		if routingDecision != nil {
			c.Locals("routing_decision", routingDecision)
		}

		// T5: strategy dispatch through the registry. Unknown query_type
		// falls through to the registry's default strategy (CHUNKS).
		registry := cfg.SearchStrategies
		if registry == nil {
			registry = NewDefaultStrategyRegistry()
		}
		return registry.Get(queryType).Execute(c, cfg, req)
	}
}

// capabilitiesFromConfig derives router.Capabilities from the current APIConfig.
func capabilitiesFromConfig(cfg APIConfig) router.Capabilities {
	hasCommunities := false
	if cfg.DB != nil {
		var count int
		if err := cfg.DB.QueryRow("SELECT COUNT(*) FROM graph_communities LIMIT 1").Scan(&count); err == nil {
			hasCommunities = count > 0
		}
	}
	return router.Capabilities{
		HasEmbedding:   cfg.EmbedEndpoint != "" && cfg.Collections != nil,
		HasBM25:        len(cfg.BM25Indexes) > 0,
		HasNeo4j:       cfg.Neo4jCfg.Neo4jURL != "",
		HasLLM:         cfg.LLMProvider != nil,
		HasPostgres:    cfg.DB != nil,
		AllowCypher:    os.Getenv("ALLOW_CYPHER_QUERY") == "true",
		HasCommunities: hasCommunities,
	}
}

func chunksSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return respondSearchItems(c, req, "CHUNKS", []any{})
	}
	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// Create reranker if requested and endpoint configured
	var rerankClient *rerank.Client
	if req.Rerank && cfg.RerankEndpoint != "" {
		rerankClient = rerank.NewClient(cfg.RerankEndpoint, cfg.RerankModel, 0, cfg.RerankTimeoutMs)
	}

	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, rerankClient)
	colls := resolveCollections(cfg, req)

	// Multi-query: decompose complex queries and merge results
	subQueries := graph.DecomposeQuery(req.QueryText)
	if len(subQueries) <= 1 {
		subQueries = []string{req.QueryText}
	}

	var allResults []fiber.Map
	seen := map[string]bool{}
	// A.2 (20.04 review): track per-result reranked status. The previous
	// global wasReranked bool flipped on the first sub-query that managed
	// a rerank pass and then mislabelled every later result that DIDN'T
	// go through rerank as `"reranked": true`. Per-result tracking keeps
	// the contract honest — clients can now trust the field as a per-hit
	// provenance signal rather than a meta-flag.
	for _, sq := range subQueries {
		for _, coll := range colls {
			var results []pipeline.ScoredResult
			var err error
			rerankedThisCall := false
			if rerankClient != nil && rerankClient.Enabled() {
				results, rerankedThisCall, err = sp.SearchByTextWithRerank(ctx, coll, sq, req.TopK)
			} else {
				results, err = sp.SearchByText(ctx, coll, sq, req.TopK)
			}
			if err != nil {
				continue
			}
			for _, r := range results {
				if seen[r.ID] {
					continue // dedup across sub-queries
				}
				seen[r.ID] = true
				allResults = append(allResults, fiber.Map{
					"id":         r.ID,
					"score":      r.Score,
					"collection": coll,
					"metadata":   json.RawMessage(r.Metadata),
					"reranked":   rerankedThisCall,
				})
			}
		}
	}

	// Graph-aware reranking: boost results that are graph-neighbors of query entities
	if cfg.DB != nil && len(allResults) > 1 {
		queryEntityNames := extractQueryEntities(ctx, cfg.DB, req.QueryText)
		if len(queryEntityNames) > 0 {
			grResults := make([]graphrank.ScoredResult, len(allResults))
			for i, r := range allResults {
				score, _ := r["score"].(float32)
				if score == 0 {
					if fs, ok := r["fused_score"].(float64); ok {
						score = float32(fs)
					}
				}
				meta, _ := r["metadata"].(json.RawMessage)
				grResults[i] = graphrank.ScoredResult{ID: r["id"].(string), Score: score, Metadata: meta}
			}
			reranked := graphrank.RerankWithGraph(ctx, cfg.DB, queryEntityNames, grResults, graphrank.DefaultConfig())
			for i, r := range reranked {
				allResults[i]["id"] = r.ID
				allResults[i]["score"] = r.Score
				allResults[i]["metadata"] = r.Metadata
			}
		}
	}

	// RBAC post-filter by allowed datasets
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	// Tag-based post-filter
	allResults = filterByTags(allResults, req.Tags)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}

	return respondSearchItems(c, req, "CHUNKS", allResults)
}

func bm25Search(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.BM25Indexes == nil {
		return respondSearchItems(c, req, "CHUNKS_LEXICAL", []any{})
	}

	var allResults []fiber.Map
	for collection, idx := range cfg.BM25Indexes {
		if req.Collection != "" && collection != req.Collection {
			continue
		}
		results := idx.Search(req.QueryText, req.TopK)
		for _, r := range results {
			allResults = append(allResults, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": collection,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}
	return respondSearchItems(c, req, "CHUNKS_LEXICAL", allResults)
}

func hybridSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return respondSearchItems(c, req, "HYBRID", []any{})
	}
	ctx, cancel := searchRequestContext(c)
	defer cancel()

	var rerankClient *rerank.Client
	if req.Rerank && cfg.RerankEndpoint != "" {
		rerankClient = rerank.NewClient(cfg.RerankEndpoint, cfg.RerankModel, 0, cfg.RerankTimeoutMs)
	}

	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, rerankClient)

	colls := resolveCollections(cfg, req)
	var allResults []fiber.Map

	for _, coll := range colls {
		// Vector search
		vectorResults, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK*2)
		if err != nil {
			continue
		}
		var vr []bm25.VectorResult
		for _, r := range vectorResults {
			vr = append(vr, bm25.VectorResult{
				ID: r.ID, Score: r.Score, Metadata: string(r.Metadata),
			})
		}

		// BM25 search with optional graph-based query expansion
		bm25Query := req.QueryText
		if cfg.DB != nil {
			if expanded := expandQueryFromGraph(ctx, cfg.DB, req.QueryText); expanded != "" {
				bm25Query = req.QueryText + " " + expanded
			}
		}
		var br []bm25.Result
		if cfg.BM25Indexes != nil {
			if idx, ok := cfg.BM25Indexes[coll]; ok {
				br = idx.Search(bm25Query, req.TopK*2)
			}
		}

		// Fuse with RRF
		hybrid := bm25.HybridSearch(vr, br, req.TopK, 1.0, 1.0)
		for _, h := range hybrid {
			allResults = append(allResults, fiber.Map{
				"id":           h.ID,
				"fused_score":  h.FusedScore,
				"vector_score": h.VectorScore,
				"bm25_score":   h.BM25Score,
				"collection":   coll,
				"metadata":     json.RawMessage(h.Metadata),
			})
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	// Tag-based post-filter
	allResults = filterByTags(allResults, req.Tags)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}
	return respondSearchItems(c, req, "HYBRID", allResults)
}

func temporalSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// Step 1: Extract dates from query text
	events := temporal.ExtractTimestamps(req.QueryText, time.Now())

	var temporalResults []fiber.Map

	if len(events) > 0 {
		from, to, ok := temporal.DateRangeFromEvents(events)
		if ok {
			// Step 2: Try Neo4j temporal query
			if cfg.Neo4jCfg.Neo4jURL != "" {
				temporalResults = temporalSearchNeo4j(ctx, cfg, from, to, req.TopK)
			}

			// Step 3: Fallback to PostgreSQL if Neo4j returned nothing
			if len(temporalResults) == 0 && cfg.DB != nil {
				temporalResults = temporalSearchPostgres(ctx, cfg, from, to, req.TopK)
			}
		}
	}

	// Step 4: Also do vector search for temporal context if we have embed
	var vectorResults []fiber.Map
	if cfg.EmbedEndpoint != "" && cfg.Collections != nil {
		embedClient := cfg.EmbedClient
		sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

		colls := resolveCollections(cfg, req)
		for _, coll := range colls {
			results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
			if err != nil {
				continue
			}
			for _, r := range results {
				vectorResults = append(vectorResults, fiber.Map{
					"id":         r.ID,
					"score":      r.Score,
					"collection": coll,
					"metadata":   json.RawMessage(r.Metadata),
					"source":     "vector",
				})
			}
		}
	}

	// RBAC post-filter
	vectorResults = filterByAllowedDatasets(vectorResults, req.AllowedDatasetIDs)

	// Combine: temporal results first, then vector results
	combined := make([]fiber.Map, 0, len(temporalResults)+len(vectorResults))
	combined = append(combined, temporalResults...)
	combined = append(combined, vectorResults...)

	if len(combined) > req.TopK {
		combined = combined[:req.TopK]
	}

	// Include extracted dates for transparency
	extractedDates := make([]fiber.Map, 0, len(events))
	for _, e := range events {
		extractedDates = append(extractedDates, fiber.Map{
			"date":       e.Date.Format(time.RFC3339),
			"date_str":   e.DateStr,
			"confidence": e.Confidence,
		})
	}

	return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
		"results":         combined,
		"extracted_dates": extractedDates,
		"search_type":     "TEMPORAL",
	}))
}

// temporalSearchNeo4j queries Neo4j for entities linked to TemporalEvent nodes in a date range.
func temporalSearchNeo4j(ctx context.Context, cfg APIConfig, from, to time.Time, limit int) []fiber.Map {
	// Use a timeout context for Neo4j query (5 seconds max)
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	writer, err := graphdb.NewWriter(queryCtx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		return nil
	}
	defer writer.Close(queryCtx)

	cypher := `MATCH (e:` + "`__Node__`" + `)-[:HAPPENED_AT]->(t:` + "`__Node__`" + `)
		WHERE t.type = 'TemporalEvent'
		AND t.date >= $from AND t.date <= $to
		RETURN e.id AS entity_id, e.name AS entity_name, e.type AS entity_type,
		       e.description AS entity_desc, t.date AS date, t.name AS date_str
		LIMIT $limit`

	rows, err := writer.Query(queryCtx, cypher, map[string]any{
		"from":  from.Format("2006-01-02"),
		"to":    to.Format("2006-01-02"),
		"limit": int64(limit),
	})
	if err != nil {
		return nil
	}

	var results []fiber.Map
	for _, row := range rows {
		name, _ := row["entity_name"].(string)
		typ, _ := row["entity_type"].(string)
		desc, _ := row["entity_desc"].(string)
		date, _ := row["date"].(string)
		dateStr, _ := row["date_str"].(string)
		entityID, _ := row["entity_id"].(string)

		results = append(results, fiber.Map{
			"id":          entityID,
			"name":        name,
			"type":        typ,
			"description": desc,
			"date":        date,
			"date_str":    dateStr,
			"source":      "neo4j_temporal",
		})
	}
	return results
}

// temporalSearchPostgres queries PostgreSQL for TemporalEvent nodes in a date range.
func temporalSearchPostgres(ctx context.Context, cfg APIConfig, from, to time.Time, limit int) []fiber.Map {
	if cfg.DB == nil {
		return nil
	}

	// Query temporal nodes and their connected entities via edges
	query := Q(`
		SELECT gn.id, gn.name, gn.type, gn.description,
		       gn.properties::jsonb->>'date' AS date,
		       ge.source_id AS entity_id,
		       en.name AS entity_name, en.type AS entity_type, en.description AS entity_desc
		FROM graph_nodes gn
		LEFT JOIN graph_edges ge ON ge.target_id = gn.id AND ge.relationship_name = 'HAPPENED_AT'
		LEFT JOIN graph_nodes en ON en.id = ge.source_id
		WHERE gn.type = 'TemporalEvent'
		AND gn.properties::jsonb->>'date' >= $1
		AND gn.properties::jsonb->>'date' <= $2
		LIMIT $3`)

	rows, err := cfg.DB.QueryContext(ctx, query, from.Format("2006-01-02"), to.Format("2006-01-02"), limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []fiber.Map
	for rows.Next() {
		var id, name, typ, desc string
		var date, entityID, entityName, entityType, entityDesc sql.NullString
		rows.Scan(&id, &name, &typ, &desc, &date, &entityID, &entityName, &entityType, &entityDesc)

		if entityID.Valid && entityName.Valid {
			results = append(results, fiber.Map{
				"id":          entityID.String,
				"name":        entityName.String,
				"type":        entityType.String,
				"description": entityDesc.String,
				"date":        date.String,
				"date_str":    name,
				"source":      "postgres_temporal",
			})
		} else {
			// No linked entity, return the temporal node itself
			results = append(results, fiber.Map{
				"id":          id,
				"name":        name,
				"type":        typ,
				"description": desc,
				"date":        date.String,
				"source":      "postgres_temporal",
			})
		}
	}
	return results
}

// ragCompletionSearch does vector search + LLM completion over results.
// Returns both raw chunks and an LLM-generated answer.
func ragCompletionSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
			"chunks":         []any{},
			"answer":         "",
			"confidence":     0.0,
			"abstained":      true,
			"abstain_reason": "embedding backend unavailable",
		}))
	}
	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// Step 1: vector search (same as chunksSearch)
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var chunks []fiber.Map

	for _, coll := range colls {
		// Overfetch: entities don't have text, need more results to find chunks
		fetchK := req.TopK * 3
		if fetchK < 20 {
			fetchK = 20
		}
		results, err := sp.SearchByText(ctx, coll, req.QueryText, fetchK)
		if err != nil {
			continue
		}
		for _, r := range results {
			chunks = append(chunks, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}
	// RBAC post-filter
	chunks = filterByAllowedDatasets(chunks, req.AllowedDatasetIDs)
	chunks, verification := verifyScoredResults(chunks, req.MinScore, req.VerifyResults)

	threshold := ragAbstainThresholdFor("RAG_COMPLETION")
	breakdown := buildConfidenceBreakdown(c, chunks, threshold)
	confidence := breakdown.Combined
	evidenceIDs := extractEvidenceChunkIDs(chunks, 10)
	lowConfidence := threshold > 0 && (len(chunks) == 0 || confidence < threshold)
	noEvidence := req.StrictGrounded && len(evidenceIDs) == 0
	abstained := lowConfidence || noEvidence
	abstainReason := ""
	if noEvidence {
		abstainReason = "strict_grounded_no_evidence"
	} else if lowConfidence {
		abstainReason = "low_confidence"
	}
	emitRAGMetrics("RAG_COMPLETION", confidence, abstained, abstainReason, verification)

	// Step 2: LLM completion using retrieved chunks as context
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if abstained {
		answer = defaultAbstainMessage
	} else if llmEndpoint != "" && llmModel != "" && len(chunks) > 0 {
		// Build context from chunk metadata — extract "text" field, skip entities without text
		var contextParts []string
		for _, chunk := range chunks {
			if raw, ok := chunk["metadata"].(json.RawMessage); ok {
				var meta map[string]any
				if json.Unmarshal(raw, &meta) == nil {
					text, _ := meta["text"].(string)
					if text == "" {
						// Entity without text — use name + description as fallback
						name, _ := meta["name"].(string)
						desc, _ := meta["description"].(string)
						if name != "" {
							text = name
							if desc != "" {
								text += ": " + desc
							}
						}
					}
					if len(text) > 20 { // skip tiny stubs
						contextParts = append(contextParts, fmt.Sprintf("[%d] %s", len(contextParts)+1, text))
					}
				}
			}
			if len(contextParts) >= 10 {
				break
			}
		}

		// Load conversation history if session_id provided
		var historySection string
		if req.SessionID != "" && cfg.DB != nil {
			rows, err := cfg.DB.QueryContext(ctx,
				Q(`SELECT query, response FROM interactions
				   WHERE session_id = $1 ORDER BY created_at DESC LIMIT 5`), req.SessionID)
			if err == nil {
				defer rows.Close()
				var turns []string
				for rows.Next() {
					var q, r string
					rows.Scan(&q, &r)
					turns = append(turns, fmt.Sprintf("User: %s\nAssistant: %s", truncate(q, 200), truncate(r, 300)))
				}
				if len(turns) > 0 {
					// Reverse order (oldest first)
					for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
						turns[i], turns[j] = turns[j], turns[i]
					}
					historySection = "\n\nPrevious conversation:\n" + strings.Join(turns, "\n\n")
				}
			}
		}

		prompt := fmt.Sprintf("Based on the following context, answer the question.%s\n\nContext:\n%s\n\nQuestion: %s\n\nAnswer:",
			historySection, strings.Join(contextParts, "\n"), req.QueryText)

		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)

		// Save this interaction for future conversational context
		if req.SessionID != "" && cfg.DB != nil {
			cfg.DB.ExecContext(ctx,
				Q(`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
				   VALUES ($1, $2, $3, $4, $5, $6, NOW())`),
				uuid.New().String(), req.SessionID, "", req.QueryText, truncate(answer, 500), "RAG_COMPLETION")
		}
	}

	return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
		"chunks":               chunks,
		"evidence_ids":         evidenceIDs,
		"answer":               answer,
		"confidence":           confidence,
		"confidence_breakdown": breakdown,
		"abstained":            abstained,
		"abstain_reason":       abstainReason,
		"threshold":            threshold,
		"search_type":          "RAG_COMPLETION",
		"verification":         verification,
	}))
}

// summariesSearch searches only in summary collections (TextSummary nodes from memify).
func summariesSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return respondSearchItems(c, req, "SUMMARIES", []any{})
	}
	ctx, cancel := searchRequestContext(c)
	defer cancel()

	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	// Search only in summary/triplet collections
	colls := resolveCollections(cfg, req)
	var allResults []fiber.Map

	for _, coll := range colls {
		// Filter: only collections that contain summaries or triplets
		lower := strings.ToLower(coll)
		if !strings.Contains(lower, "summary") && !strings.Contains(lower, "triplet") && !strings.Contains(lower, "memify") {
			continue
		}

		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			allResults = append(allResults, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(r.Metadata),
			})
		}
	}

	// Also check PostgreSQL graph_nodes for TextSummary type
	if cfg.DB != nil {
		var sqlQuery string
		var sqlArgs []any
		if req.AllowedDatasetIDs != nil {
			// Build dataset_id filter placeholders starting at $3
			dsPlaceholders := make([]string, len(req.AllowedDatasetIDs))
			pgArgs := []any{req.QueryText, req.TopK}
			for i, id := range req.AllowedDatasetIDs {
				dsPlaceholders[i] = fmt.Sprintf("$%d", i+3)
				pgArgs = append(pgArgs, id)
			}
			sqlQuery, sqlArgs = QArgs(fmt.Sprintf(`SELECT id, name, description FROM graph_nodes
			 WHERE type = 'TextSummary' AND (
				 name ILIKE '%%' || $1 || '%%' OR description ILIKE '%%' || $1 || '%%'
			 ) AND (dataset_id IS NULL OR dataset_id = '' OR dataset_id IN (%s))
			 LIMIT $2`, strings.Join(dsPlaceholders, ",")), pgArgs...)
		} else {
			sqlQuery, sqlArgs = QArgs(`SELECT id, name, description FROM graph_nodes
			 WHERE type = 'TextSummary' AND (
				 name ILIKE '%' || $1 || '%' OR description ILIKE '%' || $1 || '%'
			 ) LIMIT $2`, req.QueryText, req.TopK)
		}
		rows, err := cfg.DB.QueryContext(ctx, sqlQuery, sqlArgs...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, name, desc string
				rows.Scan(&id, &name, &desc)
				allResults = append(allResults, fiber.Map{
					"id":         id,
					"score":      0.5, // SQL match, no vector score
					"collection": "graph_nodes",
					"metadata":   json.RawMessage(fmt.Sprintf(`{"name":%q,"description":%q,"type":"TextSummary"}`, name, desc)),
				})
			}
		}
	}

	// RBAC post-filter
	allResults = filterByAllowedDatasets(allResults, req.AllowedDatasetIDs)

	if len(allResults) > req.TopK {
		allResults = allResults[:req.TopK]
	}

	return respondSearchItems(c, req, "SUMMARIES", allResults)
}

// callLLMFromAPI is a standalone LLM call helper for search handlers.
// If provider is non-nil, uses the provider abstraction (supports Anthropic, etc.).
// Otherwise falls back to raw HTTP POST to OpenAI-compatible endpoint.
func callLLMFromAPI(ctx context.Context, endpoint, model, prompt string, provider ...llm.Provider) string {
	// Provider path: use abstraction if available.
	if len(provider) > 0 && provider[0] != nil {
		resp, err := provider[0].ChatCompletion(ctx, llm.CompletionRequest{
			Model:       model,
			Messages:    []llm.Message{{Role: "user", Content: prompt}},
			Temperature: 0.3,
			MaxTokens:   2000,
		})
		if err != nil {
			return ""
		}
		return strings.TrimSpace(resp.Content)
	}

	// Legacy raw HTTP path.
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  2000,
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(respBody, &result) == nil && len(result.Choices) > 0 {
		return strings.TrimSpace(result.Choices[0].Message.Content)
	}
	return ""
}
