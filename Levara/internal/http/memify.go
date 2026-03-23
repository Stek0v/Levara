// memify.go — Post-cognify graph enrichment endpoint.
// Implements Cognee's memify pipeline: extract subgraph → LLM enrich → persist.
//
// Enrichment tasks:
//   - entity_consolidation: merge duplicate/fragmented entities via LLM
//   - triplet_embeddings: embed graph relationships as searchable vectors
//   - rule_associations: derive rules/patterns from document chunks via LLM
//   - summary_generation: generate summaries for node clusters
package http

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/graph"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/orchestrator"
)

type memifyRequest struct {
	Dataset          string   `json:"dataset"`
	NodeNames        []string `json:"node_name"`
	NodeType         string   `json:"node_type"`
	RunInBackground  bool     `json:"run_in_background"`
	EnrichmentTasks  []string `json:"enrichment_tasks"` // entity_consolidation, triplet_embeddings, rule_associations, summary_generation
}

type memifyRunStatus struct {
	RunID     string    `json:"run_id"`
	Status    string    `json:"status"`
	Stage     string    `json:"stage"`
	Message   string    `json:"message"`
	Enriched  int       `json:"nodes_enriched"`
	ElapsedMs int64     `json:"elapsed_ms"`
	StartedAt time.Time `json:"started_at"`
}

var memifyRuns sync.Map

func memifyStatusHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if val, ok := memifyRuns.Load(runID); ok {
			return c.JSON(val)
		}
		return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
	}
}

// memifyStreamHandler streams memify progress via SSE.
func memifyStreamHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		runID := c.Params("runId")
		if _, ok := memifyRuns.Load(runID); !ok {
			return c.Status(404).JSON(fiber.Map{"detail": "run not found"})
		}

		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			lastStage := ""
			for {
				val, ok := memifyRuns.Load(runID)
				if !ok {
					fmt.Fprintf(w, "event: error\ndata: {\"error\":\"run not found\"}\n\n")
					w.Flush()
					return
				}
				status := val.(*memifyRunStatus)

				if status.Stage != lastStage || status.Status != "RUNNING" {
					data, _ := json.Marshal(status)
					fmt.Fprintf(w, "event: progress\ndata: %s\n\n", data)
					w.Flush()
					lastStage = status.Stage
				}

				if status.Status != "RUNNING" {
					data, _ := json.Marshal(status)
					fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
					w.Flush()
					return
				}

				time.Sleep(500 * time.Millisecond)
			}
		})
		return nil
	}
}

func memifyHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req memifyRequest
		c.BodyParser(&req)

		if len(req.EnrichmentTasks) == 0 {
			req.EnrichmentTasks = []string{"entity_consolidation", "triplet_embeddings"}
		}

		// Verify Neo4j is configured (memify works on existing graph)
		if cfg.Neo4jCfg.Neo4jURL == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "Neo4j not configured — memify requires an existing knowledge graph"})
		}

		runID := uuid.New().String()
		status := &memifyRunStatus{
			RunID:     runID,
			Status:    "RUNNING",
			Stage:     "starting",
			StartedAt: time.Now(),
		}
		memifyRuns.Store(runID, status)

		runner := func() {
			start := time.Now()
			ctx := context.Background()

			// Stage 1: Extract subgraph from Neo4j
			status.Stage = "extracting"
			writer, err := graphdb.NewWriter(ctx, cfg.Neo4jCfg.Neo4jURL, cfg.Neo4jCfg.Neo4jUser,
				cfg.Neo4jCfg.Neo4jPassword, cfg.Neo4jCfg.Neo4jDatabase)
			if err != nil {
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("neo4j connect: %v", err)
				return
			}
			defer writer.Close(ctx)

			graphResult, err := writer.ReadFullGraph(ctx)
			if err != nil {
				status.Status = "FAILED"
				status.Message = fmt.Sprintf("graph read: %v", err)
				return
			}

			// Filter nodes if requested
			nodes := graphResult.Nodes
			if req.NodeType != "" {
				var filtered []graphdb.ReadNode
				for _, n := range nodes {
					if n.Label == req.NodeType {
						filtered = append(filtered, n)
					}
				}
				nodes = filtered
			}
			if len(req.NodeNames) > 0 {
				nameSet := make(map[string]bool)
				for _, name := range req.NodeNames {
					nameSet[strings.ToLower(name)] = true
				}
				var filtered []graphdb.ReadNode
				for _, n := range nodes {
					if name, ok := n.Properties["name"].(string); ok && nameSet[strings.ToLower(name)] {
						filtered = append(filtered, n)
					}
				}
				nodes = filtered
			}

			status.Message = fmt.Sprintf("extracted %d nodes, %d edges", len(nodes), len(graphResult.Edges))
			log.Printf("[memify] extracted %d nodes, %d edges", len(nodes), len(graphResult.Edges))

			// Stage 2: Run enrichment tasks
			for _, task := range req.EnrichmentTasks {
				status.Stage = "enriching:" + task

				switch task {
				case "entity_consolidation":
					enriched := entityConsolidation(ctx, cfg, writer, nodes)
					status.Enriched += enriched

				case "triplet_embeddings":
					enriched := tripletEmbeddings(ctx, cfg, graphResult.Edges)
					status.Enriched += enriched

				case "rule_associations":
					enriched := ruleAssociations(ctx, cfg, nodes)
					status.Enriched += enriched

				case "summary_generation":
					enriched := summaryGeneration(ctx, cfg, writer, nodes, graphResult.Edges)
					status.Enriched += enriched
				}
			}

			// Stage 3: PostgreSQL upsert (if configured)
			if cfg.DB != nil {
				status.Stage = "persisting"
				dedupNodes := make([]graph.DedupNode, len(nodes))
				for i, n := range nodes {
					name, _ := n.Properties["name"].(string)
					desc, _ := n.Properties["description"].(string)
					dedupNodes[i] = graph.DedupNode{ID: n.ID, Name: name, Type: n.Label, Description: desc}
				}
				dedupEdges := make([]graph.DedupEdge, len(graphResult.Edges))
				for i, e := range graphResult.Edges {
					dedupEdges[i] = graph.DedupEdge{
						SourceID: e.SourceID, TargetID: e.TargetID,
						RelationshipName: e.RelationshipType,
					}
				}
				orchestrator.UpsertGraphToPostgres(ctx, cfg.DB, dedupNodes, dedupEdges)
			}

			status.Status = "COMPLETED"
			status.Stage = "complete"
			status.ElapsedMs = time.Since(start).Milliseconds()
			status.Message = fmt.Sprintf("enriched %d items in %dms", status.Enriched, status.ElapsedMs)
			log.Printf("[memify] complete: %s", status.Message)
		}

		if req.RunInBackground {
			go runner()
			return c.JSON(fiber.Map{
				"status": "MemifyRunStarted",
				"run_id": runID,
			})
		}

		runner()
		return c.JSON(status)
	}
}

// entityConsolidation merges fragmented entity descriptions via LLM.
// Finds nodes with similar names and asks LLM to merge descriptions.
func entityConsolidation(ctx context.Context, cfg APIConfig, writer *graphdb.Writer, nodes []graphdb.ReadNode) int {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	if llmEndpoint == "" || llmModel == "" {
		log.Printf("[memify] entity_consolidation skipped: LLM_ENDPOINT or LLM_MODEL not set")
		return 0
	}

	// Group nodes by normalized name
	groups := make(map[string][]graphdb.ReadNode)
	for _, n := range nodes {
		name, _ := n.Properties["name"].(string)
		if name == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(name))
		groups[key] = append(groups[key], n)
	}

	enriched := 0
	httpClient := &http.Client{Timeout: 60 * time.Second}

	for _, group := range groups {
		if len(group) < 2 {
			continue
		}

		// Build prompt for LLM consolidation
		var descriptions []string
		for _, n := range group {
			desc, _ := n.Properties["description"].(string)
			if desc != "" {
				descriptions = append(descriptions, desc)
			}
		}
		if len(descriptions) < 2 {
			continue
		}

		prompt := fmt.Sprintf("Merge these descriptions of the same entity into one concise description:\n\n%s\n\nMerged description:", strings.Join(descriptions, "\n---\n"))

		merged := callLLM(ctx, httpClient, llmEndpoint, llmModel, prompt)
		if merged == "" {
			continue
		}

		// Update the first node with merged description, Neo4j MERGE
		primary := group[0]
		props := map[string]any{"description": merged}
		writer.BatchWrite(ctx, []graphdb.NodeRecord{
			{ID: primary.ID, Label: primary.Label, Properties: props},
		}, nil)
		enriched++
	}

	log.Printf("[memify] entity_consolidation: %d entities merged", enriched)
	return enriched
}

// tripletEmbeddings embeds graph relationships as searchable vectors.
func tripletEmbeddings(ctx context.Context, cfg APIConfig, edges []graphdb.ReadEdge) int {
	if cfg.EmbedEndpoint == "" || cfg.Collections == nil || len(edges) == 0 {
		return 0
	}

	embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3)
	coll := "Memify_triplet"
	if !cfg.Collections.Has(coll) {
		cfg.Collections.Create(coll)
	}

	// Build triplet texts
	texts := make([]string, len(edges))
	for i, e := range edges {
		texts[i] = fmt.Sprintf("%s %s %s", e.SourceID, e.RelationshipType, e.TargetID)
	}

	vecs, err := embedClient.EmbedTexts(ctx, texts)
	if err != nil {
		log.Printf("[memify] triplet_embeddings: embed error: %v", err)
		return 0
	}

	inserted := 0
	for i, e := range edges {
		if i < len(vecs) {
			id := fmt.Sprintf("%s_%s_%s", e.SourceID, e.RelationshipType, e.TargetID)
			meta := fmt.Sprintf(`{"source":"%s","target":"%s","rel":"%s"}`, e.SourceID, e.TargetID, e.RelationshipType)
			cfg.Collections.Insert(coll, id, vecs[i], meta)
			inserted++
		}
	}

	log.Printf("[memify] triplet_embeddings: %d triplets embedded", inserted)
	return inserted
}

// ruleAssociations derives rules from node descriptions via LLM.
func ruleAssociations(ctx context.Context, cfg APIConfig, nodes []graphdb.ReadNode) int {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	if llmEndpoint == "" || llmModel == "" {
		return 0
	}

	httpClient := &http.Client{Timeout: 120 * time.Second}
	enriched := 0

	// Collect node descriptions for rule extraction
	var texts []string
	for _, n := range nodes {
		desc, _ := n.Properties["description"].(string)
		if desc != "" && len(desc) > 50 {
			texts = append(texts, desc)
		}
	}

	if len(texts) == 0 {
		return 0
	}

	// Batch: take up to 20 descriptions
	if len(texts) > 20 {
		texts = texts[:20]
	}

	prompt := fmt.Sprintf("Extract key rules, patterns, or principles from these text excerpts. Return a JSON array of {\"rule\": \"...\", \"confidence\": 0.0-1.0} objects:\n\n%s", strings.Join(texts, "\n---\n"))

	result := callLLM(ctx, httpClient, llmEndpoint, llmModel, prompt)
	if result != "" {
		// Try to parse and count rules
		var rules []struct {
			Rule       string  `json:"rule"`
			Confidence float64 `json:"confidence"`
		}
		if json.Unmarshal([]byte(extractJSON(result)), &rules) == nil {
			enriched = len(rules)
		}
	}

	log.Printf("[memify] rule_associations: %d rules extracted", enriched)
	return enriched
}

// summaryGeneration generates summaries for node clusters.
func summaryGeneration(ctx context.Context, cfg APIConfig, writer *graphdb.Writer, nodes []graphdb.ReadNode, edges []graphdb.ReadEdge) int {
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmModel := os.Getenv("LLM_MODEL")
	if llmEndpoint == "" || llmModel == "" {
		return 0
	}

	// Group nodes by type
	typeGroups := make(map[string][]graphdb.ReadNode)
	for _, n := range nodes {
		if n.Label != "" {
			typeGroups[n.Label] = append(typeGroups[n.Label], n)
		}
	}

	httpClient := &http.Client{Timeout: 120 * time.Second}
	enriched := 0

	for nodeType, group := range typeGroups {
		if len(group) < 3 {
			continue
		}

		// Build context for summary
		var descriptions []string
		for _, n := range group {
			name, _ := n.Properties["name"].(string)
			desc, _ := n.Properties["description"].(string)
			if name != "" {
				entry := name
				if desc != "" {
					entry += ": " + desc
				}
				descriptions = append(descriptions, entry)
			}
		}

		if len(descriptions) == 0 {
			continue
		}
		if len(descriptions) > 30 {
			descriptions = descriptions[:30]
		}

		prompt := fmt.Sprintf("Summarize this cluster of %d '%s' entities in 2-3 sentences:\n\n%s",
			len(group), nodeType, strings.Join(descriptions, "\n"))

		summary := callLLM(ctx, httpClient, llmEndpoint, llmModel, prompt)
		if summary != "" {
			// Write summary as a new node
			summaryID := fmt.Sprintf("summary_%s_%s", nodeType, uuid.New().String()[:8])
			writer.BatchWrite(ctx, []graphdb.NodeRecord{
				{
					ID: summaryID, Label: "TextSummary",
					Properties: map[string]any{
						"name":        fmt.Sprintf("Summary: %s", nodeType),
						"description": summary,
						"type":        "TextSummary",
						"source_type": nodeType,
						"node_count":  len(group),
					},
				},
			}, nil)
			enriched++
		}
	}

	log.Printf("[memify] summary_generation: %d summaries created", enriched)
	return enriched
}

// callLLM makes a chat completion request to an OpenAI-compatible endpoint.
func callLLM(ctx context.Context, client *http.Client, endpoint, model, prompt string) string {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.3,
		"max_tokens":  2000,
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return ""
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
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

// extractJSON extracts the first JSON array or object from a string.
func extractJSON(s string) string {
	// Find first [ or {
	start := -1
	var closer byte
	for i, c := range s {
		if c == '[' {
			start = i
			closer = ']'
			break
		}
		if c == '{' {
			start = i
			closer = '}'
			break
		}
	}
	if start < 0 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		if s[i] == closer-2 || s[i] == closer-1 { // [ or {
			depth++
		}
		if s[i] == closer {
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}
