// Package orchestrator implements a streaming cognify pipeline using Go goroutines.
//
// Instead of Python's sequential batch processing:
//   batch 20 items → task1 ALL → wait → task2 ALL → wait → task3 ALL → wait
//
// Go pipeline streams items through stages concurrently:
//   item1 → [chunk] → [LLM extract] → [dedup+write] → done
//   item2 →    [chunk] → [LLM extract] →    [dedup+write] → done
//   item3 →       [chunk] →    [LLM extract] →    [dedup+write] → done
//
// This enables pipeline parallelism: while one item is in LLM (5-30s),
// others are being chunked or having their results written to DBs.
package orchestrator

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stek0v/cognevra/pkg/chunker"
	"github.com/stek0v/cognevra/pkg/classify"
	"github.com/stek0v/cognevra/pkg/graph"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/temporal"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/embed"
)

// Config for the pipeline.
type Config struct {
	// Chunking
	ChunkStrategy string // "merged", "paragraph", "sentence", "row", "auto"
	MinChunkChars int
	MaxChunkChars int
	// LLM
	LLMEndpoint    string
	LLMModel       string
	SystemPrompt   string
	Temperature    float32
	LLMConcurrency int
	// Embedding
	EmbedEndpoint string
	EmbedModel    string
	// Neo4j
	Neo4jURL      string
	Neo4jUser     string
	Neo4jPassword string
	Neo4jDatabase string
	// Vector
	Collection       string
	Collections      *store.CollectionManager
	GenerateTriplets bool
	// Dataset tracking
	DatasetID string
	// PostgreSQL (for graph node/edge upsert)
	DB *sql.DB
	// LLM response cache (optional, nil = no caching)
	LLMCache llmcache.LLMCacher
	// UseStructuredOutput enables JSON Schema mode for LLM extraction (default: true)
	UseStructuredOutput *bool
	// LLMProvider (optional): multi-provider abstraction (OpenAI, Anthropic, etc.)
	// If set, extractEntities uses Provider.ChatCompletion instead of raw HTTP.
	// If nil, falls back to existing raw HTTP logic (backward compatible).
	LLMProvider llm.Provider
}

// Progress reports pipeline status.
type Progress struct {
	Stage             string
	ItemsTotal        int
	ItemsProcessed    int
	ChunksCreated     int
	EntitiesExtracted int
	EdgesExtracted    int
	NodesWritten      int
	EdgesWritten      int
	Message           string
	ElapsedMs         int64
}

// ExtractedGraph is the LLM extraction result for one chunk.
type ExtractedGraph struct {
	Nodes []graph.DedupNode
	Edges []graph.DedupEdge
}

// TextItem carries text content along with its source filename for classification.
type TextItem struct {
	Text     string
	Filename string // optional: used by "auto" chunk strategy for extension-based classification
}

// RunWithItems executes the pipeline with filename metadata for auto-classification.
// When ChunkStrategy is "auto", each item is classified by filename extension and
// content heuristics, then chunked with the appropriate strategy.
// For non-auto strategies, delegates directly to Run.
func RunWithItems(ctx context.Context, items []TextItem, cfg Config, progressCh chan<- Progress) error {
	if cfg.ChunkStrategy != "auto" {
		texts := make([]string, len(items))
		for i, it := range items {
			texts[i] = it.Text
		}
		return Run(ctx, texts, cfg, progressCh)
	}

	// Auto mode with filenames: pre-chunk each item using classification,
	// then pass pre-chunked texts to Run with permissive limits so they pass through as-is.
	var preChunked []string
	for i, item := range items {
		docID := fmt.Sprintf("doc-%d", i)
		cr := classify.Classify(item.Filename, []byte(item.Text))
		var chunks []chunker.Chunk
		switch cr.RecommendedChunker {
		case "code":
			chunks = chunker.ChunkByFunction(item.Text, cr.MaxChunkChars, docID)
		case "row":
			chunks = chunker.ChunkByRow(item.Text, 20, docID)
		case "sentence":
			chunks = chunker.ChunkBySentence(item.Text, cr.MinChunkChars, cr.MaxChunkChars, docID)
		default:
			chunks = chunker.ChunkByParagraphMerged(item.Text, cr.MinChunkChars, cr.MaxChunkChars, docID)
		}
		for _, c := range chunks {
			preChunked = append(preChunked, c.Text)
		}
	}

	// Run with merged strategy + permissive limits so pre-chunked texts pass through.
	cfg.ChunkStrategy = "merged"
	cfg.MinChunkChars = 1
	cfg.MaxChunkChars = 999999
	return Run(ctx, preChunked, cfg, progressCh)
}

// Run executes the full cognify pipeline and sends progress updates.
func Run(ctx context.Context, texts []string, cfg Config, progressCh chan<- Progress) error {
	start := time.Now()
	defer close(progressCh)

	if cfg.LLMConcurrency <= 0 {
		cfg.LLMConcurrency = 5
	}
	if cfg.MinChunkChars <= 0 {
		cfg.MinChunkChars = 80
	}
	if cfg.MaxChunkChars <= 0 {
		cfg.MaxChunkChars = 600
	}
	if cfg.ChunkStrategy == "" {
		cfg.ChunkStrategy = "merged"
	}

	// --- Stage 1: Chunk all texts (Go, fast) ---
	progressCh <- Progress{Stage: "chunking", ItemsTotal: len(texts), ElapsedMs: ms(start)}

	type indexedChunk struct {
		id   string
		text string
		idx  int
	}

	var allChunks []indexedChunk
	for i, text := range texts {
		docID := fmt.Sprintf("doc-%d", i)
		var chunks []chunker.Chunk

		switch cfg.ChunkStrategy {
		case "auto":
			// Content-based classification (no filename available, use heuristics only)
			cr := classify.Classify("", []byte(text))
			switch cr.RecommendedChunker {
			case "code":
				chunks = chunker.ChunkByFunction(text, cr.MaxChunkChars, docID)
			case "row":
				chunks = chunker.ChunkByRow(text, 20, docID)
			case "sentence":
				chunks = chunker.ChunkBySentence(text, cr.MinChunkChars, cr.MaxChunkChars, docID)
			default:
				chunks = chunker.ChunkByParagraphMerged(text, cr.MinChunkChars, cr.MaxChunkChars, docID)
			}
		case "code":
			chunks = chunker.ChunkByFunction(text, cfg.MaxChunkChars, docID)
		case "sentence":
			chunks = chunker.ChunkBySentence(text, cfg.MinChunkChars, cfg.MaxChunkChars, docID)
		case "row":
			chunks = chunker.ChunkByRow(text, 20, docID)
		default: // "merged", "paragraph"
			chunks = chunker.ChunkByParagraphMerged(text, cfg.MinChunkChars, cfg.MaxChunkChars, docID)
		}

		for _, c := range chunks {
			allChunks = append(allChunks, indexedChunk{id: c.ID, text: c.Text, idx: i})
		}
	}

	progressCh <- Progress{
		Stage: "chunking", ItemsTotal: len(texts), ItemsProcessed: len(texts),
		ChunksCreated: len(allChunks), Message: fmt.Sprintf("%d chunks from %d texts", len(allChunks), len(texts)),
		ElapsedMs: ms(start),
	}

	// --- Stage 2: LLM extraction (concurrent, through channels) ---
	progressCh <- Progress{Stage: "extracting", ChunksCreated: len(allChunks), ElapsedMs: ms(start)}

	var (
		allNodes  []graph.DedupNode
		allEdges  []graph.DedupEdge
		nodesMu   sync.Mutex
		extracted atomic.Int32
		entCount  atomic.Int32
		edgeCount atomic.Int32
	)

	httpClient := &http.Client{Timeout: 600 * time.Second}

	sem := make(chan struct{}, cfg.LLMConcurrency)
	var wg sync.WaitGroup

	for _, chunk := range allChunks {
		wg.Add(1)
		chunk := chunk
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			nodes, edges, err := extractEntities(ctx, httpClient, cfg, chunk.text)
			if err != nil {
				log.Printf("[pipeline] LLM extract chunk %s: %v", chunk.id, err)
				extracted.Add(1)
				return
			}

			nodesMu.Lock()
			allNodes = append(allNodes, nodes...)
			allEdges = append(allEdges, edges...)
			nodesMu.Unlock()

			entCount.Add(int32(len(nodes)))
			edgeCount.Add(int32(len(edges)))
			cur := extracted.Add(1)

			// Send progress every 5 chunks
			if cur%5 == 0 || int(cur) == len(allChunks) {
				progressCh <- Progress{
					Stage: "extracting", ChunksCreated: len(allChunks),
					ItemsProcessed: int(cur), EntitiesExtracted: int(entCount.Load()),
					EdgesExtracted: int(edgeCount.Load()), ElapsedMs: ms(start),
				}
			}
		}()
	}

	wg.Wait()

	progressCh <- Progress{
		Stage: "extracting", ChunksCreated: len(allChunks),
		ItemsProcessed: len(allChunks), EntitiesExtracted: int(entCount.Load()),
		EdgesExtracted: int(edgeCount.Load()),
		Message: fmt.Sprintf("extracted %d entities, %d edges", entCount.Load(), edgeCount.Load()),
		ElapsedMs: ms(start),
	}

	// --- Stage 3: Dedup ---
	progressCh <- Progress{Stage: "deduplicating", ElapsedMs: ms(start)}

	dedupResult := graph.Deduplicate(allNodes, allEdges)

	progressCh <- Progress{
		Stage: "deduplicating",
		EntitiesExtracted: len(dedupResult.Nodes),
		EdgesExtracted:    len(dedupResult.Edges),
		Message: fmt.Sprintf("deduped: %d nodes, %d edges, %d triplets",
			len(dedupResult.Nodes), len(dedupResult.Edges), len(dedupResult.Triplets)),
		ElapsedMs: ms(start),
	}

	// --- Stage 3b: Temporal extraction (optional, fast, no LLM) ---
	// For each chunk, extract timestamps and link them to entities found in that chunk.
	temporalNodesAdded := 0
	temporalEdgesAdded := 0
	{
		// Build a map: chunk index → entity IDs extracted from that chunk.
		// Since extraction is concurrent and we don't track per-chunk results,
		// we do temporal extraction on the full text of each original document
		// and link to ALL deduped entities (conservative approach).
		now := time.Now()
		existingNodeIDs := make([]string, len(dedupResult.Nodes))
		for i, n := range dedupResult.Nodes {
			existingNodeIDs[i] = n.ID
		}

		temporalNodesSeen := make(map[string]bool)
		for _, chunk := range allChunks {
			events := temporal.ExtractTimestamps(chunk.text, now)
			if len(events) == 0 {
				continue
			}

			// Find entity IDs that were extracted from this chunk's parent document.
			// Since we can't track per-chunk entity mapping (concurrent extraction),
			// use all entities for now. This creates more edges but ensures coverage.
			tNodes, tEdges := temporal.LinkEventsToEntities(events, existingNodeIDs)

			for _, tn := range tNodes {
				if temporalNodesSeen[tn.ID] {
					continue
				}
				temporalNodesSeen[tn.ID] = true
				dedupResult.Nodes = append(dedupResult.Nodes, graph.DedupNode{
					ID:          tn.ID,
					Name:        tn.Name,
					Type:        tn.Type,
					Description: tn.Description,
					Properties:  map[string]string{"date": tn.DateISO},
				})
				temporalNodesAdded++
			}
			for _, te := range tEdges {
				dedupResult.Edges = append(dedupResult.Edges, graph.DedupEdge{
					SourceID:         te.SourceID,
					TargetID:         te.TargetID,
					RelationshipName: te.RelationshipName,
					EdgeText:         te.EdgeText,
				})
				temporalEdgesAdded++
			}
		}

		if temporalNodesAdded > 0 {
			log.Printf("[pipeline] temporal: %d temporal nodes, %d HAPPENED_AT edges", temporalNodesAdded, temporalEdgesAdded)
		}
	}

	// --- Stage 4: Write to DBs (parallel: Neo4j + vector) ---
	progressCh <- Progress{Stage: "writing", ElapsedMs: ms(start)}

	var writeWg sync.WaitGroup
	var nodesWritten, edgesWritten atomic.Int32

	// Neo4j write (goroutine)
	if cfg.Neo4jURL != "" {
		writeWg.Add(1)
		go func() {
			defer writeWg.Done()
			writer, err := graphdb.NewWriter(ctx, cfg.Neo4jURL, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
			if err != nil {
				log.Printf("[pipeline] neo4j connect: %v", err)
				return
			}
			defer writer.Close(ctx)

			neoNodes := make([]graphdb.NodeRecord, len(dedupResult.Nodes))
			for i, n := range dedupResult.Nodes {
				props := map[string]any{"name": n.Name, "description": n.Description, "type": n.Type, "dataset_id": cfg.DatasetID}
				// Add date property for TemporalEvent nodes
				if n.Type == "TemporalEvent" && n.Properties != nil {
					if dateStr, ok := n.Properties["date"]; ok && dateStr != "" {
						props["date"] = dateStr
					}
				}
				neoNodes[i] = graphdb.NodeRecord{
					ID: n.ID, Label: n.Type,
					Properties: props,
				}
			}
			neoEdges := make([]graphdb.EdgeRecord, len(dedupResult.Edges))
			for i, e := range dedupResult.Edges {
				neoEdges[i] = graphdb.EdgeRecord{
					SourceID: e.SourceID, TargetID: e.TargetID,
					RelationshipName: e.RelationshipName,
					Properties:       map[string]any{"edge_text": e.EdgeText, "dataset_id": cfg.DatasetID},
				}
			}

			res := writer.BatchWrite(ctx, neoNodes, neoEdges)
			nodesWritten.Add(int32(res.NodesWritten))
			edgesWritten.Add(int32(res.EdgesWritten))
		}()
	}

	// Vector embed + index (goroutine)
	if cfg.EmbedEndpoint == "" {
		log.Printf("[pipeline] WARNING: EmbedEndpoint not configured — vector indexing SKIPPED")
	}
	if cfg.Collections == nil {
		log.Printf("[pipeline] WARNING: Collections not configured — vector indexing SKIPPED")
	}
	if cfg.EmbedEndpoint != "" && cfg.Collections != nil {
		writeWg.Add(1)
		go func() {
			defer writeWg.Done()
			embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3)

			coll := cfg.Collection
			if coll == "" {
				coll = "PipelineEntity_name"
			}
			if !cfg.Collections.Has(coll) {
				if err := cfg.Collections.Create(coll); err != nil {
					log.Printf("[pipeline] collection create %q: %v", coll, err)
				}
			}

			// Embed node names/descriptions
			texts := make([]string, len(dedupResult.Nodes))
			for i, n := range dedupResult.Nodes {
				t := n.Name
				if n.Description != "" {
					t += ": " + n.Description
				}
				texts[i] = t
			}

			inserted := 0
			if len(texts) > 0 {
				vecs, err := embedClient.EmbedTexts(ctx, texts)
				if err != nil {
					log.Printf("[pipeline] embed FAILED (%d texts): %v", len(texts), err)
					// Don't return — continue to triplets which may use different texts
				} else {
					for i, n := range dedupResult.Nodes {
						if i < len(vecs) {
							meta := fmt.Sprintf(`{"name":"%s","type":"%s","dataset_id":"%s"}`, n.Name, n.Type, cfg.DatasetID)
							if err := cfg.Collections.Insert(coll, n.ID, vecs[i], meta); err != nil {
								log.Printf("[pipeline] vector insert %q error: %v", n.Name, err)
							} else {
								inserted++
							}
						}
					}
					log.Printf("[pipeline] vector write: %d/%d entities embedded into %q", inserted, len(texts), coll)
				}
			}

			// Embed triplets if requested
			if cfg.GenerateTriplets && len(dedupResult.Triplets) > 0 {
				tripletColl := "Triplet_text"
				if !cfg.Collections.Has(tripletColl) {
					if err := cfg.Collections.Create(tripletColl); err != nil {
						log.Printf("[pipeline] collection create %q: %v", tripletColl, err)
					}
				}
				tripletTexts := make([]string, len(dedupResult.Triplets))
				for i, t := range dedupResult.Triplets {
					tripletTexts[i] = t.Text
				}
				if tvecs, err := embedClient.EmbedTexts(ctx, tripletTexts); err != nil {
					log.Printf("[pipeline] triplet embed FAILED: %v", err)
				} else {
					tripletInserted := 0
					for i, t := range dedupResult.Triplets {
						if i < len(tvecs) {
							meta := fmt.Sprintf(`{"from":"%s","to":"%s","dataset_id":"%s"}`, t.FromNodeID, t.ToNodeID, cfg.DatasetID)
							if err := cfg.Collections.Insert(tripletColl, t.ID, tvecs[i], meta); err != nil {
								log.Printf("[pipeline] triplet insert error: %v", err)
							} else {
								tripletInserted++
							}
						}
					}
					log.Printf("[pipeline] triplet write: %d/%d triplets into %q", tripletInserted, len(tripletTexts), tripletColl)
				}
			}
		}()
	}

	// PostgreSQL graph upsert (goroutine, parallel with Neo4j + vector)
	if cfg.DB != nil {
		writeWg.Add(1)
		go func() {
			defer writeWg.Done()
			nw, ew, err := UpsertGraphToPostgres(ctx, cfg.DB, dedupResult.Nodes, dedupResult.Edges)
			if err != nil {
				log.Printf("[pipeline] pg upsert: %v", err)
			} else {
				log.Printf("[pipeline] pg upsert: %d nodes, %d edges", nw, ew)
			}
		}()
	}

	writeWg.Wait()

	// --- Done ---
	progressCh <- Progress{
		Stage: "complete", ItemsTotal: len(texts), ItemsProcessed: len(texts),
		ChunksCreated: len(allChunks),
		EntitiesExtracted: len(dedupResult.Nodes), EdgesExtracted: len(dedupResult.Edges),
		NodesWritten: int(nodesWritten.Load()), EdgesWritten: int(edgesWritten.Load()),
		Message: "pipeline complete",
		ElapsedMs: ms(start),
	}

	return nil
}

// useStructuredOutput returns whether structured output mode is enabled.
// Defaults to true if UseStructuredOutput is nil.
func useStructuredOutput(cfg Config) bool {
	if cfg.UseStructuredOutput == nil {
		return true
	}
	return *cfg.UseStructuredOutput
}

// extractEntities calls LLM to extract entities and relationships from text.
// Uses LLMCache if configured — cache hit avoids HTTP call entirely.
// When structured output is enabled (default), tries JSON Schema mode first,
// then falls back to regex parsing on failure.
func extractEntities(ctx context.Context, client *http.Client, cfg Config, text string) ([]graph.DedupNode, []graph.DedupEdge, error) {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = defaultExtractionPrompt
	}

	// --- LLM Cache: check before HTTP call ---
	var cacheKey string
	if cfg.LLMCache != nil {
		cacheKey = llmcache.Key(cfg.LLMModel, text, sysPrompt, cfg.Temperature)
		if cached, ok := cfg.LLMCache.Get(cacheKey); ok {
			return parseEntities(cached)
		}
	}

	// --- Structured output: try JSON Schema mode first ---
	if useStructuredOutput(cfg) && cfg.LLMEndpoint != "" && cfg.LLMModel != "" {
		result, err := llm.StructuredCall(ctx, llm.StructuredRequest{
			Endpoint:       cfg.LLMEndpoint,
			Model:          cfg.LLMModel,
			SystemPrompt:   sysPrompt,
			UserPrompt:     text,
			Temperature:    cfg.Temperature,
			ResponseSchema: llm.KnowledgeGraphSchema,
			MaxRetries:     3,
			Client:         client,
			Provider:       cfg.LLMProvider, // nil = legacy HTTP, non-nil = use provider
		})
		if err == nil {
			// Cache the structured result
			if cfg.LLMCache != nil && cacheKey != "" {
				cfg.LLMCache.Put(cacheKey, result, cfg.LLMModel)
			}
			return parseEntities(result)
		}
		log.Printf("[pipeline] structured output failed, fallback to regex: %v", err)
		// Fall through to regular call
	}

	// --- Fallback: regular LLM call without response_format ---

	// If provider is available, use it for the fallback path too.
	if cfg.LLMProvider != nil {
		provResp, provErr := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{
			Model: cfg.LLMModel,
			Messages: []llm.Message{
				{Role: "system", Content: sysPrompt},
				{Role: "user", Content: text},
			},
			Temperature: cfg.Temperature,
		})
		if provErr != nil {
			return nil, nil, fmt.Errorf("LLM provider call: %w", provErr)
		}
		content := provResp.Content
		if cfg.LLMCache != nil && cacheKey != "" {
			cfg.LLMCache.Put(cacheKey, content, cfg.LLMModel)
		}
		return parseEntities(content)
	}

	// Legacy raw HTTP path (no provider set).
	reqBody, _ := json.Marshal(map[string]any{
		"model": cfg.LLMModel,
		"messages": []map[string]string{
			{"role": "system", "content": sysPrompt},
			{"role": "user", "content": text},
		},
		"temperature": cfg.Temperature,
		"stream":      false,
	})

	// Endpoint may already contain /v1 path
	endpoint := cfg.LLMEndpoint
	if !strings.HasSuffix(endpoint, "/chat/completions") {
		endpoint = strings.TrimSuffix(endpoint, "/") + "/chat/completions"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("LLM call: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, nil, fmt.Errorf("LLM status %d: %s", resp.StatusCode, string(body[:min(200, len(body))]))
	}

	// Parse OpenAI-compatible response
	var llmResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &llmResp); err != nil {
		return nil, nil, fmt.Errorf("parse LLM response: %w", err)
	}
	if len(llmResp.Choices) == 0 {
		return nil, nil, fmt.Errorf("LLM returned no choices")
	}

	content := llmResp.Choices[0].Message.Content

	// --- LLM Cache: store successful response ---
	if cfg.LLMCache != nil && cacheKey != "" {
		cfg.LLMCache.Put(cacheKey, content, cfg.LLMModel)
	}

	return parseEntities(content)
}

// parseEntities extracts structured entities from LLM text response.
func parseEntities(content string) ([]graph.DedupNode, []graph.DedupEdge, error) {
	// Try to parse as JSON first
	var kg struct {
		Nodes []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Type        string `json:"type"`
			Description string `json:"description"`
		} `json:"nodes"`
		Edges []struct {
			Source           string `json:"source"`
			Target           string `json:"target"`
			Relationship     string `json:"relationship"`
			RelationshipName string `json:"relationship_name"`
			EdgeText         string `json:"edge_text"`
		} `json:"edges"`
	}

	// Find JSON in content (may be wrapped in markdown code blocks)
	jsonStr := extractJSON(content)
	if jsonStr == "" {
		return nil, nil, fmt.Errorf("no JSON found in LLM response")
	}

	if err := json.Unmarshal([]byte(jsonStr), &kg); err != nil {
		return nil, nil, fmt.Errorf("parse entities JSON: %w", err)
	}

	nodes := make([]graph.DedupNode, len(kg.Nodes))
	for i, n := range kg.Nodes {
		id := n.ID
		if id == "" {
			id = graph.GenerateNodeID(n.Name)
		}
		nodes[i] = graph.DedupNode{ID: id, Name: n.Name, Type: n.Type, Description: n.Description}
	}

	edges := make([]graph.DedupEdge, len(kg.Edges))
	for i, e := range kg.Edges {
		relName := e.Relationship
		if relName == "" {
			relName = e.RelationshipName
		}
		edges[i] = graph.DedupEdge{
			SourceID: e.Source, TargetID: e.Target,
			RelationshipName: relName, EdgeText: e.EdgeText,
		}
	}

	return nodes, edges, nil
}

// extractJSON finds a JSON object in text (handles ```json ... ``` blocks).
func extractJSON(s string) string {
	// Try raw JSON first
	start := -1
	for i, c := range s {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}

	// Find matching closing brace
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return ""
}

func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

const defaultExtractionPrompt = `Extract entities and relationships from the following text.
Return a JSON object with this exact structure:
{
  "nodes": [{"id": "unique_id", "name": "Entity Name", "type": "EntityType", "description": "Brief description"}],
  "edges": [{"source": "source_id", "target": "target_id", "relationship": "RELATIONSHIP_TYPE", "edge_text": "description of relationship"}]
}
Return ONLY the JSON object, no other text.`
