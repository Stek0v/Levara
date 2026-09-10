// graph_search.go — Graph-based search handlers for Levara search API.
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
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/community"
	"github.com/stek0v/levara/pkg/graphdb"
)

// graphCompletionSearch performs vector search → extract entities → graph context → LLM answer.
func graphCompletionSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
			"answer":         "",
			"context":        []any{},
			"search_type":    "GRAPH_COMPLETION",
			"confidence":     0.0,
			"abstained":      true,
			"abstain_reason": "embedding backend unavailable",
		}))
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// Step 1: Vector search across entity collections
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var entityNames []string
	var vectorChunks []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		results, err = filterScoredSearchDocuments(c, cfg, results)
		if err != nil {
			return err
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
	if filtered, err := filterSearchDocuments(c, cfg, vectorChunks); err != nil {
		return err
	} else {
		vectorChunks = filtered
	}
	vectorChunks, verification := verifyScoredResults(vectorChunks, req.MinScore, req.VerifyResults)

	if len(vectorChunks) > req.TopK {
		vectorChunks = vectorChunks[:req.TopK]
	}

	// Deduplicate entity names
	entityNames = dedup(entityNames)

	// Step 2: Graph context
	graphPolicy := defaultGraphContextPolicy()
	graphPolicy.QueryText = req.QueryText
	graphPolicy.RouteCandidates = dcdRouteCandidatesFromCtx(c)
	graphAssembly := assembleGraphContext(ctx, cfg, entityNames, req.AllowedDatasetIDs, graphPolicy)
	graphContext := graphAssembly.Context

	threshold := ragAbstainThresholdFor("GRAPH_COMPLETION")
	breakdown := buildConfidenceBreakdown(c, vectorChunks, threshold)
	confidence := breakdown.Combined
	evidenceIDs := extractEvidenceChunkIDs(vectorChunks, 10)
	lowConfidence := threshold > 0 && ((len(graphContext) == 0 && len(vectorChunks) == 0) || confidence < threshold)
	noEvidence := req.StrictGrounded && len(evidenceIDs) == 0
	abstained := lowConfidence || noEvidence
	abstainReason := ""
	if noEvidence {
		abstainReason = "strict_grounded_no_evidence"
	} else if lowConfidence {
		abstainReason = "low_confidence"
	}
	emitRAGMetrics("GRAPH_COMPLETION", confidence, abstained, abstainReason, verification)

	// Step 3: LLM completion
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if abstained {
		answer = defaultAbstainMessage
	} else if llmEndpoint != "" && llmModel != "" && (len(graphContext) > 0 || len(vectorChunks) > 0) {
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
		prompt = prependSessionContext(ctx, cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION")

	resp := fiber.Map{
		"answer":               answer,
		"context":              graphContext,
		"context_vsa":          graphAssembly.VSAContext,
		"context_sql":          graphAssembly.SQLContext,
		"context_neo4j":        graphAssembly.Neo4jContext,
		"chunks":               vectorChunks,
		"evidence_ids":         evidenceIDs,
		"search_type":          "GRAPH_COMPLETION",
		"confidence":           confidence,
		"confidence_breakdown": breakdown,
		"abstained":            abstained,
		"abstain_reason":       abstainReason,
		"threshold":            threshold,
		"verification":         verification,
	}
	for k, v := range graphContextDebugMetadata(graphAssembly) {
		resp[k] = v
	}
	return c.JSON(attachSearchDebugMetadata(c, resp))
}

// contextExtensionSearch performs 2-hop graph traversal for richer context.
// Unlike graphCompletionSearch (1-hop: entity→neighbours), this extends to
// entity→neighbours→THEIR neighbours, gathering a wider knowledge context.
func contextExtensionSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
			"answer":         "",
			"context":        []any{},
			"search_type":    "GRAPH_COMPLETION_CONTEXT_EXTENSION",
			"confidence":     0.0,
			"abstained":      true,
			"abstain_reason": "embedding backend unavailable",
		}))
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// Step 1: Vector search — same as graphCompletionSearch
	embedClient := cfg.EmbedClient
	sp := pipeline.NewSearchPipeline(embedClient, cfg.Collections, nil)

	colls := resolveCollections(cfg, req)
	var entityNames []string
	var vectorChunks []fiber.Map

	for _, coll := range colls {
		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		results, err = filterScoredSearchDocuments(c, cfg, results)
		if err != nil {
			return err
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
	if filtered, err := filterSearchDocuments(c, cfg, vectorChunks); err != nil {
		return err
	} else {
		vectorChunks = filtered
	}
	vectorChunks, verification := verifyScoredResults(vectorChunks, req.MinScore, req.VerifyResults)
	if len(vectorChunks) > req.TopK {
		vectorChunks = vectorChunks[:req.TopK]
	}
	entityNames = dedup(entityNames)

	// Step 2: VSA-first direct context, with SQL/Neo4j as filler.
	graphPolicy := defaultGraphContextPolicy()
	graphPolicy.QueryText = req.QueryText
	graphPolicy.RouteCandidates = dcdRouteCandidatesFromCtx(c)
	graphAssembly := assembleGraphContext(ctx, cfg, entityNames, req.AllowedDatasetIDs, graphPolicy)
	vsaContext := graphAssembly.VSAContext
	hop1Context := append([]string{}, graphAssembly.SQLContext...)
	hop1Context = append(hop1Context, graphAssembly.Neo4jContext...)
	hop1TargetNames := graphAssembly.TargetNames

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
		remaining := graphAssembly.TotalLimit - len(graphAssembly.Context)
		if remaining > 0 {
			if cfg.Neo4jCfg.Neo4jURL != "" {
				hop2Context, _ = graphContextWithTargetsNeo4j(ctx, cfg, extendedNames, req.AllowedDatasetIDs)
			} else if cfg.DB != nil {
				hop2Context = graphContextFromPostgres(ctx, cfg, extendedNames, req.AllowedDatasetIDs)
			}
			if len(hop2Context) > remaining {
				hop2Context = hop2Context[:remaining]
			}
		}
	}

	// Merge all context
	allContext := append([]string{}, vsaContext...)
	allContext = append(allContext, hop1Context...)
	allContext = append(allContext, hop2Context...)

	threshold := ragAbstainThresholdFor("GRAPH_COMPLETION_CONTEXT_EXTENSION")
	breakdown := buildConfidenceBreakdown(c, vectorChunks, threshold)
	confidence := breakdown.Combined
	evidenceIDs := extractEvidenceChunkIDs(vectorChunks, 10)
	lowConfidence := threshold > 0 && ((len(allContext) == 0 && len(vectorChunks) == 0) || confidence < threshold)
	noEvidence := req.StrictGrounded && len(evidenceIDs) == 0
	abstained := lowConfidence || noEvidence
	abstainReason := ""
	if noEvidence {
		abstainReason = "strict_grounded_no_evidence"
	} else if lowConfidence {
		abstainReason = "low_confidence"
	}
	emitRAGMetrics("GRAPH_COMPLETION_CONTEXT_EXTENSION", confidence, abstained, abstainReason, verification)

	// Step 4: LLM completion with extended context
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	answer := ""

	if abstained {
		answer = defaultAbstainMessage
	} else if llmEndpoint != "" && llmModel != "" && (len(allContext) > 0 || len(vectorChunks) > 0) {
		var contextStr string
		if len(vsaContext) > 0 {
			contextStr = "VSA recall relationships:\n" + strings.Join(vsaContext, "\n")
		}
		if len(hop1Context) > 0 {
			if contextStr != "" {
				contextStr += "\n\n"
			}
			contextStr += "Direct relationships (1-hop):\n" + strings.Join(hop1Context, "\n")
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
		prompt = prependSessionContext(ctx, cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION_CONTEXT_EXTENSION")

	resp := fiber.Map{
		"answer":               answer,
		"context":              allContext,
		"context_vsa":          vsaContext,
		"context_hop1":         hop1Context,
		"context_hop2":         hop2Context,
		"chunks":               vectorChunks,
		"evidence_ids":         evidenceIDs,
		"hops":                 2,
		"search_type":          "GRAPH_COMPLETION_CONTEXT_EXTENSION",
		"confidence":           confidence,
		"confidence_breakdown": breakdown,
		"abstained":            abstained,
		"abstain_reason":       abstainReason,
		"threshold":            threshold,
		"verification":         verification,
	}
	for k, v := range graphContextDebugMetadata(graphAssembly) {
		resp[k] = v
	}
	return c.JSON(attachSearchDebugMetadata(c, resp))
}

// graphContextWithTargetsNeo4j returns context strings AND target entity names (for 2nd hop).
func graphContextWithTargetsNeo4j(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) ([]string, []string) {
	items := graphContextItemsFromNeo4j(ctx, cfg, names, allowedDatasetIDs)
	var lines, targets []string
	for _, item := range items {
		lines = append(lines, item.format())
		targets = append(targets, item.TargetName)
	}
	return lines, targets
}

// cotSearch performs multi-step Chain-of-Thought search:
// Step 1: LLM decomposes query into sub-questions.
// Step 2: Each sub-question runs graph search (vector + graph traversal).
// Step 3: LLM synthesizes a final answer from all gathered context.
func cotSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	// If no LLM configured, fall back to single-step graph completion.
	if llmEndpoint == "" || llmModel == "" {
		return graphCompletionSearch(c, cfg, req)
	}
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "reasoning_steps": []any{}, "search_type": "GRAPH_COMPLETION_COT"})
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()

	// ── Step 1: Decompose query into sub-questions via LLM ──
	decomposePrompt := fmt.Sprintf(
		"Break the following question into 2-3 independent sub-questions that each need a knowledge-graph lookup. "+
			"Return ONLY a JSON array of strings, no explanation.\n\nQuestion: %s\n\nSub-questions:", req.QueryText)
	rawSubs := callLLMFromAPI(ctx, llmEndpoint, llmModel, decomposePrompt, cfg.LLMProvider)

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
			results, err := sp.SearchByText(ctx, coll, sub, req.TopK)
			if err != nil {
				continue
			}
			results, err = filterScoredSearchDocuments(c, cfg, results)
			if err != nil {
				return err
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
		synthesizePrompt = prependSessionContext(ctx, cfg, req.SessionID, synthesizePrompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, synthesizePrompt, cfg.LLMProvider)
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

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "GRAPH_COMPLETION_COT")

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
func codingRulesSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"rules": []any{}, "entities": []any{}, "search_type": "CODING_RULES"})
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()

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
		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK*2)
		if err != nil {
			continue
		}
		results, err = filterScoredSearchDocuments(c, cfg, results)
		if err != nil {
			return err
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
	if filtered, err := filterSearchDocuments(c, cfg, codeEntities); err != nil {
		return err
	} else {
		codeEntities = filtered
	}
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
		// Filter both endpoints (gn = source, gn2 = target). A cross-dataset
		// edge can otherwise leak the target's name into another tenant.
		dsFilter = fmt.Sprintf(
			" AND (gn.dataset_id IS NULL OR gn.dataset_id = '' OR gn.dataset_id IN (%s))"+
				" AND (gn2.dataset_id IS NULL OR gn2.dataset_id = '' OR gn2.dataset_id IN (%s))",
			strings.Join(dsPlaceholders, ","), strings.Join(dsPlaceholders, ","))
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
		"CALLS":      "calls",
		"IMPORTS":    "imports",
		"INHERITS":   "inherits from",
		"EXTENDS":    "extends",
		"IMPLEMENTS": "implements",
		"CONTAINS":   "contains",
		"HAS_PART":   "contains",
		"DEPENDS_ON": "depends on",
		"RELATES_TO": "is related to",
		"USES":       "uses",
		"RETURNS":    "returns",
		"ACCEPTS":    "accepts",
		"DEFINES":    "defines",
		"OVERRIDES":  "overrides",
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
func tripletCompletionSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "triplets": []any{}, "search_type": "TRIPLET_COMPLETION"})
	}
	ctx, cancel := searchRequestContext(c)
	defer cancel()

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
		results, err := sp.SearchByText(ctx, coll, req.QueryText, req.TopK)
		if err != nil {
			continue
		}
		results, err = filterScoredSearchDocuments(c, cfg, results)
		if err != nil {
			return err
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
	if filtered, err := filterSearchDocuments(c, cfg, triplets); err != nil {
		return err
	} else {
		triplets = filtered
	}

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
		prompt = prependSessionContext(ctx, cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "TRIPLET_COMPLETION")

	return c.JSON(fiber.Map{
		"answer":      answer,
		"triplets":    triplets,
		"search_type": "TRIPLET_COMPLETION",
	})
}

// cypherSearch executes a raw Cypher query against Neo4j.
func cypherSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
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

	if !isCypherAllowed(cypherQuery, os.Getenv("ALLOW_CYPHER_WRITE") == "true") {
		return c.Status(403).JSON(fiber.Map{
			"detail": "Cypher policy violation: only read-only MATCH/RETURN/CALL/WITH/OPTIONAL MATCH/UNWIND queries are allowed unless ALLOW_CYPHER_WRITE=true",
		})
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()
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

// isCypherAllowed validates whether a Cypher query is allowed by policy.
//
// Default policy (allowWrite=false):
//   - Query must start with a read clause: MATCH / OPTIONAL MATCH / WITH / CALL / UNWIND
//   - Query must not include write/admin keywords.
//   - This is intentionally conservative: uncertain queries are denied.
//
// Write policy (allowWrite=true):
//   - Still blocks administrative/destructive schema/database operations.
func isCypherAllowed(query string, allowWrite bool) bool {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return false
	}
	upper := strings.ToUpper(trimmed)
	// Pad with spaces on both ends so word-boundary checks below work for
	// keywords at the very start or end of the query.
	padded := " " + upper + " "

	if containsAnyCypherKeyword(padded, []string{
		" DROP ",
		" DATABASE ",
		" CONSTRAINT ",
		" INDEX ",
		" DBMS ", // `CALL DBMS …`
		" DBMS.", // `dbms.<func>(…)` namespace syntax
		" TERMINATE ",
		" LOAD CSV ",
	}) {
		return false
	}

	if !allowWrite {
		if !hasAllowedReadPrefix(upper) {
			return false
		}
		if containsAnyCypherKeyword(upper, []string{
			"CREATE ",
			"MERGE ",
			"DELETE ",
			"DETACH ",
			"SET ",
			"REMOVE ",
			"FOREACH ",
		}) {
			return false
		}
	}
	return true
}

func hasAllowedReadPrefix(upperQuery string) bool {
	for _, prefix := range []string{"MATCH ", "OPTIONAL MATCH ", "WITH ", "CALL ", "UNWIND "} {
		if strings.HasPrefix(upperQuery, prefix) {
			return true
		}
	}
	return false
}

func containsAnyCypherKeyword(upperQuery string, keywords []string) bool {
	for _, kw := range keywords {
		if strings.Contains(upperQuery, kw) {
			return true
		}
	}
	return false
}

// naturalLanguageSearch converts a natural language question to Cypher via LLM, then executes it.
func naturalLanguageSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
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

	ctx, cancel := searchRequestContext(c)
	defer cancel()

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

	cypherRaw := callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	if cypherRaw == "" {
		return graphCompletionSearch(c, cfg, req)
	}

	// Parse: extract Cypher from LLM response (may include markdown code blocks)
	cypher := extractCypher(cypherRaw)
	if cypher == "" {
		return graphCompletionSearch(c, cfg, req)
	}

	// Safety check: force read-only policy for NL-generated Cypher.
	if !isCypherAllowed(cypher, false) {
		log.Printf("[nl-search] LLM generated disallowed query, falling back: %s", cypher)
		return graphCompletionSearch(c, cfg, req)
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
	items := graphContextItemsFromNeo4j(ctx, cfg, names, allowedDatasetIDs)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.format())
	}
	return out
}

func graphContextItemsFromNeo4j(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []graphContextItem {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
		cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
	if err != nil {
		log.Printf("[graph-search] neo4j connect: %v", err)
		return nil
	}
	defer writer.Close(ctx)

	cypher := "MATCH (n:`__Node__`)-[r]-(m:`__Node__`) WHERE n.name IN $names " +
		"AND TYPE(r) <> 'HAPPENED_AT' AND (m.type IS NULL OR m.type <> 'TemporalEvent') " +
		"AND (elementId(r) > $after_edge OR (elementId(r) = $after_edge AND elementId(n) > $after_source)) " +
		"RETURN elementId(r) AS edge_cursor, elementId(n) AS source_cursor, n.name AS source, TYPE(r) AS rel, m.name AS target, properties(n) AS source_properties, " +
		"properties(r) AS edge_properties, properties(m) AS target_properties ORDER BY edge_cursor, source_cursor LIMIT 128"
	var items []graphContextItem
	afterEdge, afterSource := "", ""
	for ctx.Err() == nil {
		rows, err := writer.Query(ctx, cypher, map[string]any{"names": names, "after_edge": afterEdge, "after_source": afterSource})
		if err != nil {
			return nil
		}
		for _, row := range rows {
			afterEdge, _ = row["edge_cursor"].(string)
			afterSource, _ = row["source_cursor"].(string)
			var sources []searchDocumentSource
			for _, key := range []string{"source_properties", "edge_properties", "target_properties"} {
				source, err := decodeSearchDocumentSource(row[key])
				if err != nil {
					sources = nil
					break
				}
				sources = append(sources, source)
			}
			if len(sources) != 3 {
				continue
			}
			if sources[1].DatasetID == "" && sources[0].DatasetID == sources[2].DatasetID {
				sources[1].DatasetID = sources[0].DatasetID
			}
			if !graphSourcesAllowed(ctx, cfg, sources, allowedDatasetIDs) {
				continue
			}
			src, _ := row["source"].(string)
			rel, _ := row["rel"].(string)
			tgt, _ := row["target"].(string)
			if src != "" && tgt != "" {
				items = append(items, graphContextItem{SourceName: src, Predicate: rel, TargetName: tgt, DatasetID: sources[1].DatasetID, DocumentID: sources[1].DocumentID, Provider: graphContextProviderNeo4j})
			}
			if len(items) == 50 {
				return items
			}
		}
		if len(rows) < 128 {
			break
		}
	}
	return items
}

// graphContextFromPostgres uses PostgreSQL graph_nodes/graph_edges as fallback.
// If allowedDatasetIDs is non-nil, only nodes with matching dataset_id are returned.
func graphContextFromPostgres(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []string {
	items := graphContextItemsFromPostgres(ctx, cfg, names, allowedDatasetIDs)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.format())
	}
	return out
}

func graphContextItemsFromPostgres(ctx context.Context, cfg APIConfig, names []string, allowedDatasetIDs []string) []graphContextItem {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cfg.DB == nil {
		return nil
	}

	if len(names) == 0 {
		return nil
	}
	args := make([]any, 0, len(names)+2)
	for _, name := range names {
		args = append(args, name)
	}
	args = append(args, "", 128)
	query := Q(fmt.Sprintf(`SELECT ge.id, gn.name, ge.relationship_name, gn2.name,
	 COALESCE(gn.dataset_id,''), COALESCE(gn.properties,'{}'),
	 COALESCE(ge.dataset_id,''), COALESCE(ge.properties,'{}'),
	 COALESCE(gn2.dataset_id,''), COALESCE(gn2.properties,'{}')
	 FROM graph_edges ge JOIN graph_nodes gn ON ge.source_id=gn.id
	 JOIN graph_nodes gn2 ON ge.target_id=gn2.id
	 WHERE gn.name %s AND ge.relationship_name <> 'HAPPENED_AT'
	 AND (gn2.type IS NULL OR gn2.type <> 'TemporalEvent')
	 AND ge.id > $%d ORDER BY ge.id LIMIT $%d`, InPlaceholders(len(names), 1), len(args)-1, len(args)))
	var items []graphContextItem
	for ctx.Err() == nil {
		rows, err := cfg.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return nil
		}
		type candidate struct {
			item    graphContextItem
			sources []searchDocumentSource
		}
		var candidates []candidate
		fetched := 0
		afterID := ""
		for rows.Next() {
			fetched++
			var c candidate
			var datasets [3]string
			var properties [3][]byte
			if err := rows.Scan(&afterID, &c.item.SourceName, &c.item.Predicate, &c.item.TargetName,
				&datasets[0], &properties[0], &datasets[1], &properties[1], &datasets[2], &properties[2]); err != nil {
				_ = rows.Close()
				return nil
			}
			// Old edges omit their dataset. Both endpoints must agree before that
			// legacy association can be inferred; registered documents still require
			// explicit document/version metadata on every assertion.
			if datasets[1] == "" && datasets[0] == datasets[2] {
				datasets[1] = datasets[0]
			}
			valid := true
			for i := range datasets {
				source, err := decodeSearchDocumentSource(properties[i])
				if err != nil {
					valid = false
					break
				}
				source.DatasetID = datasets[i]
				c.sources = append(c.sources, source)
			}
			if valid {
				c.item.DatasetID = datasets[1]
				c.item.DocumentID = c.sources[1].DocumentID
				c.item.Provider = graphContextProviderSQL
				candidates = append(candidates, c)
			}
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil || closeErr != nil {
			return nil
		}
		// Release rows before policy queries; single-connection pools must work.
		for _, c := range candidates {
			if graphSourcesAllowed(ctx, cfg, c.sources, allowedDatasetIDs) {
				items = append(items, c.item)
				if len(items) == 50 {
					return items
				}
			}
		}
		if fetched < 128 {
			break
		}
		args[len(args)-2] = afterID
	}
	return items
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
func communityLocalSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "search_type": "COMMUNITY_LOCAL"})
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()
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
		results, err = filterScoredSearchDocuments(c, cfg, results)
		if err != nil {
			return err
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
		prompt = prependSessionContext(ctx, cfg, req.SessionID, prompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)
	}

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "COMMUNITY_LOCAL")

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
func communityGlobalSearch(c *fiber.Ctx, cfg APIConfig, req UnifiedSearchRequest) error {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")

	if cfg.EmbedEndpoint == "" || cfg.Collections == nil {
		return c.JSON(fiber.Map{"answer": "", "search_type": "COMMUNITY_GLOBAL"})
	}
	if llmEndpoint == "" && cfg.LLMProvider == nil {
		// No LLM → fallback to CHUNKS
		return chunksSearch(c, cfg, req)
	}

	ctx, cancel := searchRequestContext(c)
	defer cancel()
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
			partial := callLLMFromAPI(ctx, llmEndpoint, llmModel, prompt, cfg.LLMProvider)

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
		synthesizePrompt = prependSessionContext(ctx, cfg, req.SessionID, synthesizePrompt)
		answer = callLLMFromAPI(ctx, llmEndpoint, llmModel, synthesizePrompt, cfg.LLMProvider)
	}

	recordInteraction(ctx, cfg, req.SessionID, "", req.QueryText, answer, "COMMUNITY_GLOBAL")

	return c.JSON(fiber.Map{
		"answer":                     answer,
		"communities_used":           partials,
		"total_communities_searched": len(hits),
		"search_type":                "COMMUNITY_GLOBAL",
	})
}
