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
	"github.com/stek0v/cognevra/pkg/graph"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/embed"
)

// Config for the pipeline.
type Config struct {
	// Chunking
	ChunkStrategy string // "merged", "paragraph", "sentence"
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
	// PostgreSQL (for graph node/edge upsert)
	DB *sql.DB
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
		chunks := chunker.ChunkByParagraphMerged(text, cfg.MinChunkChars, cfg.MaxChunkChars, fmt.Sprintf("doc-%d", i))
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

	httpClient := &http.Client{Timeout: 300 * time.Second}

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
				neoNodes[i] = graphdb.NodeRecord{
					ID: n.ID, Label: n.Type,
					Properties: map[string]any{"name": n.Name, "description": n.Description, "type": n.Type},
				}
			}
			neoEdges := make([]graphdb.EdgeRecord, len(dedupResult.Edges))
			for i, e := range dedupResult.Edges {
				neoEdges[i] = graphdb.EdgeRecord{
					SourceID: e.SourceID, TargetID: e.TargetID,
					RelationshipName: e.RelationshipName,
					Properties:       map[string]any{"edge_text": e.EdgeText},
				}
			}

			res := writer.BatchWrite(ctx, neoNodes, neoEdges)
			nodesWritten.Add(int32(res.NodesWritten))
			edgesWritten.Add(int32(res.EdgesWritten))
		}()
	}

	// Vector embed + index (goroutine)
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
				cfg.Collections.Create(coll)
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

			if len(texts) > 0 {
				vecs, err := embedClient.EmbedTexts(ctx, texts)
				if err != nil {
					log.Printf("[pipeline] embed: %v", err)
					return
				}
				for i, n := range dedupResult.Nodes {
					if i < len(vecs) {
						meta := fmt.Sprintf(`{"name":"%s","type":"%s"}`, n.Name, n.Type)
						cfg.Collections.Insert(coll, n.ID, vecs[i], meta)
					}
				}
			}

			// Embed triplets if requested
			if cfg.GenerateTriplets && len(dedupResult.Triplets) > 0 {
				tripletColl := "Triplet_text"
				if !cfg.Collections.Has(tripletColl) {
					cfg.Collections.Create(tripletColl)
				}
				tripletTexts := make([]string, len(dedupResult.Triplets))
				for i, t := range dedupResult.Triplets {
					tripletTexts[i] = t.Text
				}
				if tvecs, err := embedClient.EmbedTexts(ctx, tripletTexts); err == nil {
					for i, t := range dedupResult.Triplets {
						if i < len(tvecs) {
							meta := fmt.Sprintf(`{"from":"%s","to":"%s"}`, t.FromNodeID, t.ToNodeID)
							cfg.Collections.Insert(tripletColl, t.ID, tvecs[i], meta)
						}
					}
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

// extractEntities calls LLM to extract entities and relationships from text.
func extractEntities(ctx context.Context, client *http.Client, cfg Config, text string) ([]graph.DedupNode, []graph.DedupEdge, error) {
	sysPrompt := cfg.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = defaultExtractionPrompt
	}

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
			Source       string `json:"source"`
			Target       string `json:"target"`
			Relationship string `json:"relationship"`
			EdgeText     string `json:"edge_text"`
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
		edges[i] = graph.DedupEdge{
			SourceID: e.Source, TargetID: e.Target,
			RelationshipName: e.Relationship, EdgeText: e.EdgeText,
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
