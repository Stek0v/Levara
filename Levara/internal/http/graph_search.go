// graph_search.go — Graph-based search handlers for Cognee-compatible search API.
// Implements: GRAPH_COMPLETION, GRAPH_COMPLETION_COT, TRIPLET_COMPLETION, CYPHER, NATURAL_LANGUAGE, CODING_RULES.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/community"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pipeline"
)

// graphCompletionSearch performs vector search → extract entities → graph context → LLM answer.
func graphCompletionSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "context": []any{}, "search_type": "GRAPH_COMPLETION"})
	}

	ctx := context.Background()

	// Step 1: Vector search across entity collections
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var entityNames []string
	var vectorChunks []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			meta := string(r.Metadata)
			vectorChunks = append(vectorChunks, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(meta),
			})
			// Extract entity name from metadata
			var metaMap map[string]any
			if json.Unmarshal([]byte(meta), &metaMap) == nil {
				if name, ok := metaMap["name"].(string); ok && name != "" {
					entityNames = append(entityNames, name)
				}
			}
		}
	}
	// RBAC post-filter
	vectorChunks = filterByAllowedDatasets(vectorChunks, req.AllowedDatasetIDs)

	if len(vectorChunks) > req.TopK {
		vectorChunks = vectorChunks[:req.TopK]
	}

	// Deduplicate entity names
	entityNames = dedup(entityNames)

	// Step 2: Graph context
	var graphContext []string

	if cfg.Neo4jCfg.Neo4jURL != "" && len(entityNames) > 0 {
		// Neo4j path
		graphContext = graphContextFromNeo4j(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	} else if cfg.DB != nil && len(entityNames) > 0 {
		// PostgreSQL fallback
		graphContext = graphContextFromPostgres(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	}

	// Step 3: LLM completion
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if llmEndpoint != "" && llmModel != "" && (len(graphContext) > 0 || len(vectorChunks) > 0) {
		var contextStr string
		if len(graphContext) > 0 {
			contextStr = "Knowledge graph context:\n" + strings.Join(graphContext, "\n")
		}

		// Also include top vector results as supplementary context
		if len(vectorChunks) > 0 {
			var chunkTexts []string
			for i, chunk := range vectorChunks {
				if raw, ok := chunk["metadata"].(json.RawMessage); ok {
					chunkTexts = append(chunkTexts, fmt.Sprintf("[%d] %s", i+1, string(raw)))
				}
				if i >= 4 {
					break
				}
			}
			if contextStr != "" {
				contextStr += "\n\nVector search results:\n" + strings.Join(chunkTexts, "\n")
			} else {
				contextStr = "Vector search results:\n" + strings.Join(chunkTexts, "\n")
			}
		}

		prompt := fmt.Sprintf("Answer the question based on the following knowledge graph and search context.\n\n%s\n\nQuestion: %s\n\nAnswer:", contextStr, req.QueryText)
		prompt = prependSessionContext(cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION")

	return c.JSON(fiber.Map{
		"answer":      answer,
		"context":     graphContext,
		"chunks":      vectorChunks,
		"search_type": "GRAPH_COMPLETION",
	})
}

// contextExtensionSearch performs 2-hop graph traversal for richer context.
// Unlike graphCompletionSearch (1-hop: entity→neighbours), this extends to
// entity→neighbours→THEIR neighbours, gathering a wider knowledge context.
func contextExtensionSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "context": []any{}, "search_type": "GRAPH_COMPLETION_CONTEXT_EXTENSION"})
	}

	ctx := context.Background()

	// Step 1: Vector search — same as graphCompletionSearch
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var entityNames []string
	var vectorChunks []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			meta := string(r.Metadata)
			vectorChunks = append(vectorChunks, fiber.Map{
				"id": r.ID, "score": r.Score, "collection": coll,
				"metadata": json.RawMessage(meta),
			})
			var metaMap map[string]any
			if json.Unmarshal([]byte(meta), &metaMap) == nil {
				if name, ok := metaMap["name"].(string); ok && name != "" {
					entityNames = append(entityNames, name)
				}
			}
		}
	}
	vectorChunks = filterByAllowedDatasets(vectorChunks, req.AllowedDatasetIDs)
	if len(vectorChunks) > req.TopK {
		vectorChunks = vectorChunks[:req.TopK]
	}
	entityNames = dedup(entityNames)

	// Step 2: 1st hop — direct neighbours (same as GRAPH_COMPLETION)
	var hop1Context []string
	var hop1TargetNames []string

	if cfg.Neo4jCfg.Neo4jURL != "" && len(entityNames) > 0 {
		hop1Context, hop1TargetNames = graphContextWithTargetsNeo4j(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	} else if cfg.DB != nil && len(entityNames) > 0 {
		hop1Context = graphContextFromPostgres(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	}

	// Step 3: 2nd hop — neighbours of neighbours (EXTENSION)
	var hop2Context []string
	newTargets := dedup(hop1TargetNames)
	// Remove entities we already know about
	seen := make(map[string]bool)
	for _, n := range entityNames {
		seen[n] = true
	}
	var extendedNames []string
	for _, t := range newTargets {
		if !seen[t] {
			extendedNames = append(extendedNames, t)
			seen[t] = true
		}
	}

	if len(extendedNames) > 0 {
		// Cap at 10 to avoid explosion
		if len(extendedNames) > 10 {
			extendedNames = extendedNames[:10]
		}
		if cfg.Neo4jCfg.Neo4jURL != "" {
			hop2Context, _ = graphContextWithTargetsNeo4j(ctx, cfg, extendedNames, req.AllowedDatasetIDs)
		} else if cfg.DB != nil {
			hop2Context = graphContextFromPostgres(ctx, cfg, extendedNames, req.AllowedDatasetIDs)
		}
	}

	// Merge all context
	allContext := append(hop1Context, hop2Context...)

	// Step 4: LLM completion with extended context
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if llmEndpoint != "" && llmModel != "" && (len(allContext) > 0 || len(vectorChunks) > 0) {
		var contextStr string
		if len(hop1Context) > 0 {
			contextStr = "Direct relationships (1-hop):\n" + strings.Join(hop1Context, "\n")
		}
		if len(hop2Context) > 0 {
			contextStr += "\n\nExtended relationships (2-hop):\n" + strings.Join(hop2Context, "\n")
		}

		if len(vectorChunks) > 0 {
			var chunkTexts []string
			for i, chunk := range vectorChunks {
				if raw, ok := chunk["metadata"].(json.RawMessage); ok {
					chunkTexts = append(chunkTexts, fmt.Sprintf("[%d] %s", i+1, string(raw)))
				}
				if i >= 4 {
					break
				}
			}
			if contextStr != "" {
				contextStr += "\n\nVector search results:\n" + strings.Join(chunkTexts, "\n")
			} else {
				contextStr = "Vector search results:\n" + strings.Join(chunkTexts, "\n")
			}
		}

		prompt := fmt.Sprintf(
			"Answer the question using the extended knowledge graph context below. "+
				"The context includes both direct (1-hop) and extended (2-hop) relationships.\n\n"+
				"%s\n\nQuestion: %s\n\nAnswer:", contextStr, req.QueryText)
		prompt = prependSessionContext(cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION_CONTEXT_EXTENSION")

	return c.JSON(fiber.Map{
		"answer":       answer,
		"context_hop1": hop1Context,
		"context_hop2": hop2Context,
		"chunks":       vectorChunks,
		"hops":         2,
		"search_type":  "GRAPH_COMPLETION_CONTEXT_EXTENSION",
	})
}

// graphContextWithTargetsNeo4j returns context strings AND target entity names (for 2nd hop).
func graphContextWithTargetsNeo4j(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) ([]string, []string) {
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		log.Printf("[context-extension] neo4j connect: %v", err)
		return nil, nil
	}
	defer writer.Close(ctx)

	var cypher string
	params := map[string]any{"names": names}
	if allowedDatasetIDs != nil {
		cypher = "MATCH (n:`__Node__`)-[r]-(m:`__Node__`) WHERE n.name IN $names AND TYPE(r) <> 'HAPPENED_AT' AND (m.type IS NULL OR m.type <> 'TemporalEvent') AND (n.dataset_id IS NULL OR n.dataset_id IN $allowedIDs) RETURN n.name AS source, TYPE(r) AS rel, m.name AS target LIMIT 50"
		params["allowedIDs"] = allowedDatasetIDs
	} else {
		cypher = "MATCH (n:`__Node__`)-[r]-(m:`__Node__`) WHERE n.name IN $names AND TYPE(r) <> 'HAPPENED_AT' AND (m.type IS NULL OR m.type <> 'TemporalEvent') RETURN n.name AS source, TYPE(r) AS rel, m.name AS target LIMIT 50"
	}

	rows, err := writer.Query(ctx, cypher, params)
	if err != nil {
		log.Printf("[context-extension] neo4j query: %v", err)
		return nil, nil
	}

	var contextLines []string
	var targetNames []string
	for _, row := range rows {
		src, _ := row["source"].(string)
		rel, _ := row["rel"].(string)
		tgt, _ := row["target"].(string)
		if src != "" && tgt != "" {
			contextLines = append(contextLines, fmt.Sprintf("%s is related to %s via %s", src, tgt, rel))
			targetNames = append(targetNames, tgt)
		}
	}
	return contextLines, targetNames
}

// cotSearch performs multi-step Chain-of-Thought search:
// Step 1: LLM decomposes query into sub-questions.
// Step 2: Each sub-question runs graph search (vector + graph traversal).
// Step 3: LLM synthesizes a final answer from all gathered context.
func cotSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	// If no LLM configured, fall back to single-step graph completion.
	if llmEndpoint == "" || llmModel == "" {
		return graphCompletionSearch(c, cfg, req)
	}
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "reasoning_steps": []any{}, "search_type": "GRAPH_COMPLETION_COT"})
	}

	ctx := context.Background()

	// ── Step 1: Decompose query into sub-questions via LLM ──
	decomposePrompt := fmt.Sprintf(
		"Break the following question into 2-3 independent sub-questions that each need a knowledge-graph lookup. "+
			"Return ONLY a JSON array of strings, no explanation.\n\nQuestion: %s\n\nSub-questions:", req.QueryText)
	rawSubs := callLLMFromAPI(llmEndpoint, llmModel, decomposePrompt, cfg.LLMProvider)

	subQuestions := parseJSONStringArray(rawSubs)
	if len(subQuestions) == 0 {
		// Fallback: use the original query as the sole sub-question.
		subQuestions = []string{req.QueryText}
	}
	// Cap at 5 to avoid runaway costs.
	if len(subQuestions) > 5 {
		subQuestions = subQuestions[:5]
	}

	// ── Step 2: For each sub-question, run vector search + graph traversal ──
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)
	colls := resolveCollections(cfg, req)

	type reasoningStep struct {
		Step         int    `json:"step"`
		SubQuestion  string `json:"sub_question"`
		ContextFound string `json:"context_found"`
	}

	var steps []reasoningStep
	var allContext []string

	for i, sub := range subQuestions {
		var entityNames []string

		for _, coll := range colls {
			results, err := sp.SearchByText(context.Background(), coll, sub, req.TopK)
			if err != nil {
				continue
			}
			for _, r := range results {
				var metaMap map[string]any
				if json.Unmarshal(r.Metadata, &metaMap) == nil {
					if name, ok := metaMap["name"].(string); ok && name != "" {
						entityNames = append(entityNames, name)
					}
				}
			}
		}
		entityNames = dedup(entityNames)

		// Graph traversal for discovered entities.
		var graphCtx []string
		if cfg.Neo4jCfg.Neo4jURL != "" && len(entityNames) > 0 {
			graphCtx = graphContextFromNeo4j(ctx, cfg, entityNames, req.AllowedDatasetIDs)
		} else if cfg.DB != nil && len(entityNames) > 0 {
			graphCtx = graphContextFromPostgres(ctx, cfg, entityNames, req.AllowedDatasetIDs)
		}

		stepContext := strings.Join(graphCtx, "; ")
		if stepContext == "" && len(entityNames) > 0 {
			stepContext = "Entities found: " + strings.Join(entityNames, ", ")
		}
		if stepContext == "" {
			stepContext = "(no relevant context found)"
		}

		steps = append(steps, reasoningStep{
			Step:         i + 1,
			SubQuestion:  sub,
			ContextFound: stepContext,
		})
		allContext = append(allContext, graphCtx...)
	}

	// ── Step 3: Synthesize final answer ──
	answer := ""
	if len(allContext) > 0 {
		var stepSummary string
		for _, s := range steps {
			stepSummary += fmt.Sprintf("Step %d — %s\nContext: %s\n\n", s.Step, s.SubQuestion, s.ContextFound)
		}

		synthesizePrompt := fmt.Sprintf(
			"Given this multi-step research:\n\n%s\nAnswer the original question: %s", stepSummary, req.QueryText)
		synthesizePrompt = prependSessionContext(cfg, req.SessionID, synthesizePrompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, synthesizePrompt, cfg.LLMProvider)
	}

	// Build JSON-serialisable steps slice.
	stepsJSON := make([]fiber.Map, len(steps))
	for i, s := range steps {
		stepsJSON[i] = fiber.Map{
			"step":          s.Step,
			"sub_question":  s.SubQuestion,
			"context_found": s.ContextFound,
		}
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION_COT")

	return c.JSON(fiber.Map{
		"answer":          answer,
		"reasoning_steps": stepsJSON,
		"search_type":     "GRAPH_COMPLETION_COT",
	})
}

// parseJSONStringArray tries to extract a []string from an LLM response that should be a JSON array.
func parseJSONStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	// Strip markdown code fences if present.
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + 3
		if nl := strings.Index(raw[start:], "\n"); nl >= 0 {
			start += nl + 1
		}
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			raw = strings.TrimSpace(raw[start : start+end])
		}
	}
	// Find the first '[' and last ']' to be lenient with surrounding text.
	lbracket := strings.Index(raw, "[")
	rbracket := strings.LastIndex(raw, "]")
	if lbracket >= 0 && rbracket > lbracket {
		raw = raw[lbracket : rbracket+1]
	}
	var arr []string
	if json.Unmarshal([]byte(raw), &arr) == nil {
		return arr
	}
	return nil
}

// codingRulesSearch searches for code-related entities (Function, Class, Module, Method, Import)
// and returns their relationships formatted as coding rules.
func codingRulesSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"rules": []any{}, "entities": []any{}, "search_type": "CODING_RULES"})
	}

	ctx := context.Background()

	// Code-related entity types to filter on.
	codeTypes := map[string]bool{
		"function": true, "class": true, "module": true,
		"method": true, "import": true,
	}

	// Step 1: Vector search across collections, filter to code entities.
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)
	colls := resolveCollections(cfg, req)

	var codeEntities []fiber.Map
	var entityNames []string

	for _, coll := range colls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK*2)
		if err != nil {
			continue
		}
		for _, r := range results {
			meta := string(r.Metadata)
			var metaMap map[string]any
			if json.Unmarshal([]byte(meta), &metaMap) != nil {
				continue
			}
			nodeType, _ := metaMap["type"].(string)
			if nodeType != "" && !codeTypes[strings.ToLower(nodeType)] {
				continue
			}
			// Accept entities without type too — they may still be code-related based on collection name.
			if nodeType == "" {
				lower := strings.ToLower(coll)
				if !strings.Contains(lower, "code") && !strings.Contains(lower, "function") && !strings.Contains(lower, "class") {
					continue
				}
			}

			codeEntities = append(codeEntities, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(meta),
			})
			if name, ok := metaMap["name"].(string); ok && name != "" {
				entityNames = append(entityNames, name)
			}
		}
	}

	// RBAC post-filter.
	codeEntities = filterByAllowedDatasets(codeEntities, req.AllowedDatasetIDs)
	if len(codeEntities) > req.TopK {
		codeEntities = codeEntities[:req.TopK]
	}
	entityNames = dedup(entityNames)

	// Step 2: Graph traversal to find relationships between code entities.
	var rules []string

	if cfg.Neo4jCfg.Neo4jURL != "" && len(entityNames) > 0 {
		rules = codeGraphContextFromNeo4j(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	} else if cfg.DB != nil && len(entityNames) > 0 {
		rules = codeGraphContextFromPostgres(ctx, cfg, entityNames, req.AllowedDatasetIDs)
	}

	// Fallback: if no graph rules found, generate rules from the entities themselves.
	if len(rules) == 0 {
		for _, ent := range codeEntities {
			if raw, ok := ent["metadata"].(json.RawMessage); ok {
				var m map[string]any
				if json.Unmarshal(raw, &m) == nil {
					name, _ := m["name"].(string)
					typ, _ := m["type"].(string)
					desc, _ := m["description"].(string)
					if name != "" {
						rule := name
						if typ != "" {
							rule = fmt.Sprintf("[%s] %s", typ, name)
						}
						if desc != "" {
							rule += ": " + desc
						}
						rules = append(rules, rule)
					}
				}
			}
		}
	}

	return c.JSON(fiber.Map{
		"rules":       rules,
		"entities":    codeEntities,
		"search_type": "CODING_RULES",
	})
}

// codeGraphContextFromNeo4j queries Neo4j for code-entity relationships, formatted as rules.
func codeGraphContextFromNeo4j(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []string {
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		log.Printf("[coding-rules] neo4j connect: %v", err)
		return nil
	}
	defer writer.Close(ctx)

	params := map[string]any{"names": names}
	var cypher string
	if allowedDatasetIDs != nil {
		cypher = `MATCH (n:` + "`__Node__`" + `)-[r]-(m:` + "`__Node__`" + `)
		 WHERE n.name IN $names AND (n.dataset_id IS NULL OR n.dataset_id IN $allowedIDs)
		 RETURN n.name AS source, n.type AS source_type, TYPE(r) AS rel, m.name AS target, m.type AS target_type
		 LIMIT 100`
		params["allowedIDs"] = allowedDatasetIDs
	} else {
		cypher = `MATCH (n:` + "`__Node__`" + `)-[r]-(m:` + "`__Node__`" + `)
		 WHERE n.name IN $names
		 RETURN n.name AS source, n.type AS source_type, TYPE(r) AS rel, m.name AS target, m.type AS target_type
		 LIMIT 100`
	}

	rows, err := writer.Query(ctx, cypher, params)
	if err != nil {
		log.Printf("[coding-rules] neo4j query: %v", err)
		return nil
	}

	return formatCodeRules(rows)
}

// codeGraphContextFromPostgres queries PostgreSQL for code-entity relationships, formatted as rules.
func codeGraphContextFromPostgres(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []string {
	if cfg.DB == nil {
		return nil
	}

	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}

	var dsFilter string
	if allowedDatasetIDs != nil {
		dsPlaceholders := make([]string, len(allowedDatasetIDs))
		for i, id := range allowedDatasetIDs {
			idx := len(names) + i + 1
			dsPlaceholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, id)
		}
		dsFilter = fmt.Sprintf(" AND (gn.dataset_id IS NULL OR gn.dataset_id = '' OR gn.dataset_id IN (%s))", strings.Join(dsPlaceholders, ","))
	}

	limitIdx := len(args) + 1
	args = append(args, 100)

	nameFilter := InPlaceholders(len(names), 1)
	query := Q(fmt.Sprintf(`
		SELECT gn.name AS source, gn.type AS source_type,
		       ge.relationship_name AS rel,
		       gn2.name AS target, gn2.type AS target_type
		FROM graph_edges ge
		JOIN graph_nodes gn ON ge.source_id = gn.id
		JOIN graph_nodes gn2 ON ge.target_id = gn2.id
		WHERE gn.name %s%s
		LIMIT $%d`, nameFilter, dsFilter, limitIdx))

	rows, err := cfg.DB.QueryContext(ctx, query, args...)
	if err != nil {
		log.Printf("[coding-rules] postgres query: %v", err)
		return nil
	}
	defer rows.Close()

	var rowMaps []map[string]any
	for rows.Next() {
		var src, srcType, rel, tgt, tgtType string
		rows.Scan(&src, &srcType, &rel, &tgt, &tgtType)
		rowMaps = append(rowMaps, map[string]any{
			"source": src, "source_type": srcType,
			"rel": rel, "target": tgt, "target_type": tgtType,
		})
	}
	return formatCodeRules(rowMaps)
}

// formatCodeRules converts raw relationship rows into human-readable coding rules.
func formatCodeRules(rows []map[string]any) []string {
	// Map relationship types to human-readable verbs.
	verbMap := map[string]string{
		"CALLS":        "calls",
		"IMPORTS":      "imports",
		"INHERITS":     "inherits from",
		"EXTENDS":      "extends",
		"IMPLEMENTS":   "implements",
		"CONTAINS":     "contains",
		"HAS_PART":     "contains",
		"DEPENDS_ON":   "depends on",
		"RELATES_TO":   "is related to",
		"USES":         "uses",
		"RETURNS":      "returns",
		"ACCEPTS":      "accepts",
		"DEFINES":      "defines",
		"OVERRIDES":    "overrides",
	}

	var rules []string
	seen := make(map[string]bool)

	for _, row := range rows {
		src, _ := row["source"].(string)
		rel, _ := row["rel"].(string)
		tgt, _ := row["target"].(string)
		if src == "" || tgt == "" {
			continue
		}

		key := src + "|" + rel + "|" + tgt
		if seen[key] {
			continue
		}
		seen[key] = true

		verb := verbMap[strings.ToUpper(rel)]
		if verb == "" {
			verb = strings.ToLower(strings.ReplaceAll(rel, "_", " "))
		}

		// Include type annotations when available.
		srcType, _ := row["source_type"].(string)
		tgtType, _ := row["target_type"].(string)
		srcLabel := src
		tgtLabel := tgt
		if srcType != "" {
			srcLabel = fmt.Sprintf("%s (%s)", src, srcType)
		}
		if tgtType != "" {
			tgtLabel = fmt.Sprintf("%s (%s)", tgt, tgtType)
		}

		rules = append(rules, fmt.Sprintf("%s %s %s", srcLabel, verb, tgtLabel))
	}
	return rules
}

// tripletCompletionSearch searches triplet collections and uses triplet context for LLM.
func tripletCompletionSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "triplets": []any{}, "search_type": "TRIPLET_COMPLETION"})
	}

	// Search only triplet collections
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var tripletColls []string
	for _, coll := range colls {
		lower := strings.ToLower(coll)
		if strings.Contains(lower, "triplet") {
			tripletColls = append(tripletColls, coll)
		}
	}

	// Fallback: if no triplet collections, delegate to graphCompletionSearch
	if len(tripletColls) == 0 {
		return graphCompletionSearch(c, cfg, req)
	}

	var triplets []fiber.Map
	var tripletTexts []string

	for _, coll := range tripletColls {
		results, err := sp.SearchByText(context.Background(), coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			meta := string(r.Metadata)
			triplets = append(triplets, fiber.Map{
				"id":         r.ID,
				"score":      r.Score,
				"collection": coll,
				"metadata":   json.RawMessage(meta),
			})

			// Parse triplet metadata for context
			var metaMap map[string]any
			if json.Unmarshal([]byte(meta), &metaMap) == nil {
				src, _ := metaMap["source"].(string)
				tgt, _ := metaMap["target"].(string)
				rel, _ := metaMap["rel"].(string)
				if src != "" && tgt != "" && rel != "" {
					tripletTexts = append(tripletTexts, fmt.Sprintf("%s -> %s -> %s", src, rel, tgt))
				}
			}
		}
	}
	// RBAC post-filter
	triplets = filterByAllowedDatasets(triplets, req.AllowedDatasetIDs)

	if len(triplets) > req.TopK {
		triplets = triplets[:req.TopK]
	}

	// LLM completion with triplet context
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if llmEndpoint != "" && llmModel != "" && len(tripletTexts) > 0 {
		contextStr := "Knowledge graph triplets (Subject -> Predicate -> Object):\n" + strings.Join(tripletTexts, "\n")
		prompt := fmt.Sprintf("Answer the question based on the following knowledge graph triplets.\n\n%s\n\nQuestion: %s\n\nAnswer:", contextStr, req.QueryText)
		prompt = prependSessionContext(cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "TRIPLET_COMPLETION")

	return c.JSON(fiber.Map{
		"answer":      answer,
		"triplets":    triplets,
		"search_type": "TRIPLET_COMPLETION",
	})
}

// cypherSearch executes a raw Cypher query against Neo4j.
func cypherSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	// Security gate
	if os.Getenv("ALLOW_CYPHER_QUERY") != "true" {
		return c.Status(403).JSON(fiber.Map{"detail": "Cypher queries disabled. Set ALLOW_CYPHER_QUERY=true to enable."})
	}

	if cfg.Neo4jCfg.Neo4jURL == "" {
		return c.Status(503).JSON(fiber.Map{"detail": "Neo4j not configured"})
	}

	cypherQuery := req.CypherQuery
	if cypherQuery == "" {
		return c.Status(400).JSON(fiber.Map{"detail": "cypher_query required for CYPHER search type"})
	}

	// Block write operations
	upper := strings.ToUpper(cypherQuery)
	for _, keyword := range []string{"CREATE", "MERGE", "DELETE", "DETACH", "SET ", "REMOVE"} {
		if strings.Contains(upper, keyword) {
			return c.Status(403).JSON(fiber.Map{"detail": "Write operations not allowed in Cypher search"})
		}
	}

	ctx := context.Background()
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"detail": fmt.Sprintf("neo4j connect: %v", err)})
	}
	defer writer.Close(ctx)

	rows, err := writer.Query(ctx, cypherQuery, nil)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"detail": fmt.Sprintf("cypher error: %v", err)})
	}

	return c.JSON(fiber.Map{
		"results":     rows,
		"query":       cypherQuery,
		"search_type": "CYPHER",
	})
}

// naturalLanguageSearch converts a natural language question to Cypher via LLM, then executes it.
func naturalLanguageSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	if cfg.Neo4jCfg.Neo4jURL == "" {
		// No Neo4j — fallback to graph completion
		return graphCompletionSearch(c, cfg, req)
	}

	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	if llmEndpoint == "" || llmModel == "" {
		// No LLM for NL→Cypher translation — fallback
		return graphCompletionSearch(c, cfg, req)
	}

	ctx := context.Background()

	// Step 1: Get schema info from Neo4j (labels + relationship types)
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		log.Printf("[nl-search] neo4j connect error: %v, falling back to graph completion", err)
		return graphCompletionSearch(c, cfg, req)
	}
	defer writer.Close(ctx)

	labels := getNeo4jLabels(ctx, writer)
	relTypes := getNeo4jRelTypes(ctx, writer)

	// Step 2: LLM → Cypher
	prompt := fmt.Sprintf(`Convert this natural language question into a Cypher query for Neo4j.

Available node labels: %s
Available relationship types: %s
All nodes have a base label __Node__ with an 'id' property. Common properties: name, description, type.

IMPORTANT: Return ONLY the Cypher query, no explanation. Use READ-ONLY operations (MATCH/RETURN only, no CREATE/MERGE/DELETE).
Add LIMIT 50 at the end.

Question: %s

Cypher query:`, strings.Join(labels, ", "), strings.Join(relTypes, ", "), req.QueryText)

	cypherRaw := callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	if cypherRaw == "" {
		return graphCompletionSearch(c, cfg, req)
	}

	// Parse: extract Cypher from LLM response (may include markdown code blocks)
	cypher := extractCypher(cypherRaw)
	if cypher == "" {
		return graphCompletionSearch(c, cfg, req)
	}

	// Safety check: block write operations
	upper := strings.ToUpper(cypher)
	for _, keyword := range []string{"CREATE", "MERGE", "DELETE", "DETACH", "SET ", "REMOVE"} {
		if strings.Contains(upper, keyword) {
			log.Printf("[nl-search] LLM generated write query, falling back: %s", cypher)
			return graphCompletionSearch(c, cfg, req)
		}
	}

	// Step 3: Execute
	rows, err := writer.Query(ctx, cypher, nil)
	if err != nil {
		log.Printf("[nl-search] cypher execution error: %v, falling back to graph completion", err)
		return graphCompletionSearch(c, cfg, req)
	}

	return c.JSON(fiber.Map{
		"results":         rows,
		"generated_query": cypher,
		"search_type":     "NATURAL_LANGUAGE",
	})
}

// ── Helpers ──

// graphContextFromNeo4j queries Neo4j for relationships involving the given entity names.
// If allowedDatasetIDs is non-nil, only nodes with matching dataset_id are returned.
func graphContextFromNeo4j(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []string {
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		log.Printf("[graph-search] neo4j connect: %v", err)
		return nil
	}
	defer writer.Close(ctx)

	var cypher string
	params := map[string]any{"names": names}
	if allowedDatasetIDs != nil {
		cypher = `MATCH (n:` + "`__Node__`" + `)-[r]-(m:` + "`__Node__`" + `)
		 WHERE n.name IN $names AND (n.dataset_id IS NULL OR n.dataset_id IN $allowedIDs)
		 RETURN n.name AS source, TYPE(r) AS rel, m.name AS target
		 LIMIT 50`
		params["allowedIDs"] = allowedDatasetIDs
	} else {
		cypher = `MATCH (n:` + "`__Node__`" + `)-[r]-(m:` + "`__Node__`" + `)
		 WHERE n.name IN $names
		 RETURN n.name AS source, TYPE(r) AS rel, m.name AS target
		 LIMIT 50`
	}

	rows, err := writer.Query(ctx, cypher, params)
	if err != nil {
		log.Printf("[graph-search] neo4j query: %v", err)
		return nil
	}

	var context []string
	for _, row := range rows {
		src, _ := row["source"].(string)
		rel, _ := row["rel"].(string)
		tgt, _ := row["target"].(string)
		if src != "" && tgt != "" {
			context = append(context, fmt.Sprintf("%s is related to %s via %s", src, tgt, rel))
		}
	}
	return context
}

// graphContextFromPostgres uses PostgreSQL graph_nodes/graph_edges as fallback.
// If allowedDatasetIDs is non-nil, only nodes with matching dataset_id are returned.
func graphContextFromPostgres(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []string {
	if cfg.DB == nil {
		return nil
	}

	// Build placeholders for names
	placeholders := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = name
	}

	// Build query with optional dataset_id filter
	var dsFilter string
	if allowedDatasetIDs != nil {
		dsPlaceholders := make([]string, len(allowedDatasetIDs))
		for i, id := range allowedDatasetIDs {
			idx := len(names) + i + 1
			dsPlaceholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, id)
		}
		dsFilter = fmt.Sprintf(" AND (gn.dataset_id IS NULL OR gn.dataset_id = '' OR gn.dataset_id IN (%s))", strings.Join(dsPlaceholders, ","))
	}

	limitIdx := len(args) + 1
	args = append(args, 50)

	nameFilter := InPlaceholders(len(names), 1)
	query := Q(fmt.Sprintf(`
		SELECT gn.name AS source, ge.relationship_name AS rel, gn2.name AS target
		FROM graph_edges ge
		JOIN graph_nodes gn ON ge.source_id = gn.id
		JOIN graph_nodes gn2 ON ge.target_id = gn2.id
		WHERE gn.name %s AND ge.relationship_name <> 'HAPPENED_AT' AND (gn2.type IS NULL OR gn2.type <> 'TemporalEvent')%s
		LIMIT $%d`, nameFilter, dsFilter, limitIdx))

	rows, err := cfg.DB.QueryContext(ctx, query, args...)
	if err != nil {
		log.Printf("[graph-search] postgres query: %v", err)
		return nil
	}
	defer rows.Close()

	var context []string
	for rows.Next() {
		var src, rel, tgt string
		rows.Scan(&src, &rel, &tgt)
		if src != "" && tgt != "" {
			context = append(context, fmt.Sprintf("%s is related to %s via %s", src, tgt, rel))
		}
	}
	return context
}

// getNeo4jLabels returns all node labels from Neo4j.
func getNeo4jLabels(ctx context.Context, writer *graphdb.Writer) []string {
	rows, err := writer.Query(ctx, "CALL db.labels() YIELD label RETURN label", nil)
	if err != nil {
		return []string{"__Node__", "Entity", "TextSummary"}
	}
	var labels []string
	for _, row := range rows {
		if l, ok := row["label"].(string); ok {
			labels = append(labels, l)
		}
	}
	if len(labels) == 0 {
		return []string{"__Node__", "Entity", "TextSummary"}
	}
	return labels
}

// getNeo4jRelTypes returns all relationship types from Neo4j.
func getNeo4jRelTypes(ctx context.Context, writer *graphdb.Writer) []string {
	rows, err := writer.Query(ctx, "CALL db.relationshipTypes() YIELD relationshipType RETURN relationshipType", nil)
	if err != nil {
		return []string{"RELATES_TO", "HAS_PART", "MENTIONS"}
	}
	var types []string
	for _, row := range rows {
		if t, ok := row["relationshipType"].(string); ok {
			types = append(types, t)
		}
	}
	if len(types) == 0 {
		return []string{"RELATES_TO", "HAS_PART", "MENTIONS"}
	}
	return types
}

// extractCypher extracts a Cypher query from LLM output (handles markdown code blocks).
func extractCypher(raw string) string {
	raw = strings.TrimSpace(raw)

	// Try extracting from ```cypher ... ``` or ``` ... ```
	if idx := strings.Index(raw, "```"); idx >= 0 {
		start := idx + 3
		// Skip language identifier (e.g., "cypher")
		if nl := strings.Index(raw[start:], "\n"); nl >= 0 {
			start += nl + 1
		}
		if end := strings.Index(raw[start:], "```"); end >= 0 {
			return strings.TrimSpace(raw[start : start+end])
		}
	}

	// Take the whole thing if it looks like Cypher
	upper := strings.ToUpper(raw)
	if strings.Contains(upper, "MATCH") || strings.Contains(upper, "RETURN") {
		// Remove any leading text before MATCH
		if idx := strings.Index(upper, "MATCH"); idx > 0 {
			return strings.TrimSpace(raw[idx:])
		}
		return raw
	}

	return ""
}

// dedup removes duplicate strings preserving order.
func dedup(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// --- Community-based search ---

// communityLocalSearch: find entity's community → enrich context from community members → LLM answer.
func communityLocalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "search_type": "COMMUNITY_LOCAL"})
	}

	ctx := context.Background()
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)
	colls := resolveCollections(cfg, req)

	// Step 1: Vector search → entity names
	var entityNames []string
	for _, coll := range colls {
		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		for _, r := range results {
			var meta map[string]any
			if json.Unmarshal(r.Metadata, &meta) == nil {
				if name, ok := meta["name"].(string); ok && name != "" {
					entityNames = append(entityNames, name)
				}
			}
		}
	}
	entityNames = dedup(entityNames)

	if len(entityNames) == 0 || cfg.DB == nil {
		// Fallback to regular graph completion
		return graphCompletionSearch(c, cfg, req)
	}

	// Step 2: Find communities for these entities
	// Need to convert entity names to IDs first
	var nodeIDs []string
	for _, name := range entityNames {
		var id string
		err := cfg.DB.QueryRowContext(ctx, "SELECT id FROM graph_nodes WHERE name = ? LIMIT 1", name).Scan(&id)
		if err == nil {
			nodeIDs = append(nodeIDs, id)
		}
	}

	commIDs, err := community.LookupCommunities(ctx, cfg.DB, nodeIDs, 0)
	if err != nil || len(commIDs) == 0 {
		return graphCompletionSearch(c, cfg, req)
	}

	// Step 3: Load community context
	var communityContexts []string
	for _, commID := range commIDs {
		var summary string
		cfg.DB.QueryRowContext(ctx, "SELECT summary FROM graph_communities WHERE id = ?", commID).Scan(&summary)

		// Load community members and edges for richer context
		graphCtx := graphContextFromPostgres(ctx, cfg, entityNames, req.AllowedDatasetIDs)
		context := ""
		if summary != "" {
			context = "Community summary: " + summary + "\n"
		}
		if len(graphCtx) > 0 {
			context += "Relationships: " + strings.Join(graphCtx, "; ")
		}
		if context != "" {
			communityContexts = append(communityContexts, context)
		}
	}

	// Step 4: LLM answer
	answer := ""
	if len(communityContexts) > 0 && (llmEndpoint != "" || cfg.LLMProvider != nil) {
		prompt := fmt.Sprintf(
			"Answer based on these knowledge graph communities:\n\n%s\n\nQuestion: %s\n\nAnswer:",
			strings.Join(communityContexts, "\n---\n"), req.QueryText)
		prompt = prependSessionContext(cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "COMMUNITY_LOCAL")

	return c.JSON(fiber.Map{
		"answer":           answer,
		"communities_used": commIDs,
		"entity_names":     entityNames,
		"search_type":      "COMMUNITY_LOCAL",
	})
}

// communityGlobalSearch: map-reduce across community summaries.
// Step 1: vector search _community_summaries → top-K communities.
// Step 2: per-community partial answers via LLM.
// Step 3: synthesize final answer.
func communityGlobalSearch(c *fiber.Ctx, cfg APIConfig, req CogneeSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "search_type": "COMMUNITY_GLOBAL"})
	}
	if (llmEndpoint == "" && cfg.LLMProvider == nil) {
		// No LLM → fallback to CHUNKS
		return chunksSearch(c, cfg, req)
	}

	ctx := context.Background()
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	// Step 1: Search community summaries
	summaryCollName := "_community_summaries"
	if !cfg.Collections.Has(summaryCollName) {
		return graphCompletionSearch(c, cfg, req)
	}

	summaryResults, err := sp.SearchByText(ctx, summaryCollName, req.QueryText, 5)
	if err != nil || len(summaryResults) == 0 {
		return graphCompletionSearch(c, cfg, req)
	}

	// Extract community info
	type communityHit struct {
		ID          string
		Summary     string
		MemberCount int
		Level       int
	}
	var hits []communityHit
	for _, r := range summaryResults {
		var meta map[string]any
		if json.Unmarshal(r.Metadata, &meta) != nil {
			continue
		}
		hit := communityHit{
			ID: fmt.Sprintf("%v", meta["community_id"]),
		}
		if text, ok := meta["text"].(string); ok {
			hit.Summary = text
		}
		if mc, ok := meta["member_count"].(float64); ok {
			hit.MemberCount = int(mc)
		}
		if lv, ok := meta["level"].(float64); ok {
			hit.Level = int(lv)
		}
		if hit.Summary != "" {
			hits = append(hits, hit)
		}
	}

	if len(hits) == 0 {
		return graphCompletionSearch(c, cfg, req)
	}

	// Step 2: Map — partial answers per community (concurrent)
	type mapResult struct {
		CommunityID   string `json:"community_id"`
		Summary       string `json:"summary"`
		MemberCount   int    `json:"member_count"`
		Level         int    `json:"level"`
		PartialAnswer string `json:"partial_answer"`
	}
	var partials []mapResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)

	for _, hit := range hits {
		wg.Add(1)
		hit := hit
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			prompt := fmt.Sprintf(
				"A knowledge graph community contains these related topics:\n\n%s\n\n"+
					"Based on this community's knowledge, provide relevant information for: %s\n"+
					"If this community has no relevant information, respond with 'NOT_RELEVANT'.",
				hit.Summary, req.QueryText)
			partial := callLLMFromAPI(llmEndpoint, llmModel, prompt, cfg.LLMProvider)

			if !strings.Contains(partial, "NOT_RELEVANT") && partial != "" {
				mu.Lock()
				partials = append(partials, mapResult{
					CommunityID:   hit.ID,
					Summary:       hit.Summary,
					MemberCount:   hit.MemberCount,
					Level:         hit.Level,
					PartialAnswer: partial,
				})
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Step 3: Reduce — synthesize
	answer := ""
	if len(partials) == 0 {
		answer = "No relevant communities found for this query."
	} else if len(partials) == 1 {
		answer = partials[0].PartialAnswer
	} else {
		var partialTexts []string
		for i, p := range partials {
			partialTexts = append(partialTexts, fmt.Sprintf(
				"Community %d (%d entities, level %d):\n%s",
				i+1, p.MemberCount, p.Level, p.PartialAnswer))
		}
		synthesizePrompt := fmt.Sprintf(
			"Multiple knowledge communities provided these perspectives on '%s':\n\n%s\n\n"+
				"Synthesize a comprehensive answer combining all relevant perspectives. "+
				"Resolve any contradictions. Be thorough but concise.",
			req.QueryText, strings.Join(partialTexts, "\n\n---\n\n"))
		synthesizePrompt = prependSessionContext(cfg, req.SessionID, synthesizePrompt)
		answer = callLLMFromAPI(llmEndpoint, llmModel, synthesizePrompt, cfg.LLMProvider)
	}

	recordInteraction(cfg, req.SessionID, "", req.QueryText, answer, "COMMUNITY_GLOBAL")

	return c.JSON(fiber.Map{
		"answer":                   answer,
		"communities_used":         partials,
		"total_communities_searched": len(hits),
		"search_type":              "COMMUNITY_GLOBAL",
	})
}
