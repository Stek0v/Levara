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
	"github.com/stek0v/cognevra/pkg/bm25"
	"github.com/stek0v/cognevra/pkg/classify"
	"github.com/stek0v/cognevra/pkg/community"
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
	// Room: sub-topic label propagated to chunk metadata for filtered search.
	Room string
	// Tags: semantic tags propagated to chunk metadata for filtered search.
	Tags []string
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
	// BM25Indexes (optional): shared BM25 indexes for lexical search.
	// If set, pipeline updates BM25 index when inserting vectors.
	BM25Indexes map[string]*bm25.Index
	// SkipGraph when true skips LLM entity extraction (Stage 2),
	// deduplication (Stage 3), temporal extraction (Stage 3b),
	// Neo4j write (Stage 4a), and PostgreSQL graph upsert (Stage 4c).
	// Only chunking (Stage 1) and vector embedding (Stage 4b-chunks) execute.
	// This is the "RAG mode" — fastest ingestion, no LLM calls needed.
	SkipGraph bool
	// OverlapChars for sliding window chunking. Default: MaxChunkChars/5.
	OverlapChars int
	// SnapToSentence for sliding window: snap boundaries to sentence/word ends. Default: true.
	SnapToSentence *bool
	// ParentChild enables dual-level chunking: large parents + small children.
	ParentChild bool
	// ParentMaxChars for parent chunks in parent-child mode. Default: 2000.
	ParentMaxChars int
	// ChildMaxChars for child chunks in parent-child mode. Default: 256.
	ChildMaxChars int
	// DocumentTitle for contextual chunk headers (prepended before embedding).
	DocumentTitle string
	// DocumentID stable document identifier for metadata.
	DocumentID string
	// CommunityResolution (γ) for Louvain. >1=finer, <1=coarser. Default 1.0.
	CommunityResolution float64
	// DedupThreshold for semantic entity dedup. Default 0.95.
	DedupThreshold float64
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
		id       string
		text     string // original text (stored in metadata)
		embedTxt string // text for embedding (with contextual headers)
		idx      int    // source document index
		parentID string // for parent-child mode: parent chunk ID
		section  string // detected section header
	}

	snapSentence := cfg.SnapToSentence == nil || *cfg.SnapToSentence // default true

	var allChunks []indexedChunk
	var allParentChunks []indexedChunk // parent chunks for parent-child mode
	for i, text := range texts {
		docID := cfg.DocumentID
		if docID == "" {
			docID = fmt.Sprintf("doc-%d", i)
		}

		// Detect sections in source text
		sections := chunker.DetectSections(text)

		var chunks []chunker.Chunk

		if cfg.ParentChild {
			// Parent-child mode: parents + children
			parentMax := cfg.ParentMaxChars
			if parentMax <= 0 {
				parentMax = 2000
			}
			childMax := cfg.ChildMaxChars
			if childMax <= 0 {
				childMax = 256
			}
			parents, children := chunker.ChunkParentChild(text, parentMax, childMax, docID)

			// Assign sections and document metadata to parents
			for pi := range parents {
				parents[pi].DocumentID = docID
				parents[pi].DocumentTitle = cfg.DocumentTitle
				parents[pi].Section = chunker.FindSectionForOffset(sections, parents[pi].ChunkIndex*cfg.MaxChunkChars)
			}

			// Store parents separately for embedding into main collection
			for _, p := range parents {
				allParentChunks = append(allParentChunks, indexedChunk{
					id: p.ID, text: p.Text,
					embedTxt: buildEmbedText(p.Text, cfg.DocumentTitle, p.Section),
					idx: i, section: p.Section,
				})
			}

			// Children → allChunks (will be embedded into _child collection)
			for _, c := range children {
				parentSection := ""
				for _, p := range parents {
					if p.ID == c.ParentID {
						parentSection = p.Section
						break
					}
				}
				allChunks = append(allChunks, indexedChunk{
					id: c.ID, text: c.Text,
					embedTxt: buildEmbedText(c.Text, cfg.DocumentTitle, parentSection),
					idx: i, parentID: c.ParentID, section: parentSection,
				})
			}
		} else {
			// Regular chunking
			switch cfg.ChunkStrategy {
			case "auto":
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
			case "sliding":
				overlap := cfg.OverlapChars
				if overlap <= 0 {
					overlap = cfg.MaxChunkChars / 5
				}
				chunks = chunker.ChunkBySlidingOpts(text, cfg.MaxChunkChars, overlap, snapSentence, docID)
			case "code":
				chunks = chunker.ChunkByFunction(text, cfg.MaxChunkChars, docID)
			case "sentence":
				chunks = chunker.ChunkBySentence(text, cfg.MinChunkChars, cfg.MaxChunkChars, docID)
			case "row":
				chunks = chunker.ChunkByRow(text, 20, docID)
			default:
				chunks = chunker.ChunkByParagraphMerged(text, cfg.MinChunkChars, cfg.MaxChunkChars, docID)
			}

			// Assign sections and build embed text
			for _, c := range chunks {
				sec := chunker.FindSectionForOffset(sections, c.ChunkIndex*cfg.MaxChunkChars)
				allChunks = append(allChunks, indexedChunk{
					id: c.ID, text: c.Text,
					embedTxt: buildEmbedText(c.Text, cfg.DocumentTitle, sec),
					idx: i, section: sec,
				})
			}
		}
	}

	totalChunks := len(allChunks) + len(allParentChunks)
	progressCh <- Progress{
		Stage: "chunking", ItemsTotal: len(texts), ItemsProcessed: len(texts),
		ChunksCreated: totalChunks, Message: fmt.Sprintf("%d chunks from %d texts", totalChunks, len(texts)),
		ElapsedMs: ms(start),
	}

	// --- Stages 2-3: Graph extraction (skipped in RAG mode) ---
	var dedupResult graph.DeduplicateResult

	if cfg.SkipGraph {
		progressCh <- Progress{
			Stage: "embedding", ChunksCreated: len(allChunks),
			Message: "RAG mode: skipping entity extraction, proceeding to embedding",
			ElapsedMs: ms(start),
		}
	} else {
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
			// Set provenance on extracted entities
			docID := fmt.Sprintf("doc-%d", chunk.idx)
			for i := range nodes {
				nodes[i].SourceChunkID = chunk.id
				nodes[i].SourceDocID = docID
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

	dedupResult = graph.Deduplicate(allNodes, allEdges)

	// --- Stage 3a: Semantic dedup (merge similar entities by name embedding) ---
	if cfg.EmbedEndpoint != "" && len(dedupResult.Nodes) > 1 {
		embedClient := embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3)
		names := make([]string, len(dedupResult.Nodes))
		for i, n := range dedupResult.Nodes {
			names[i] = n.Name
			if n.Description != "" {
				names[i] += ": " + n.Description
			}
		}
		nameVecs, err := embedClient.EmbedTexts(ctx, names)
		if err == nil && len(nameVecs) == len(dedupResult.Nodes) {
			dedupThresh := cfg.DedupThreshold
			if dedupThresh <= 0 || dedupThresh > 1 {
				dedupThresh = 0.95
			}
			semResult := graph.SemanticDedup(nameVecs, float32(dedupThresh))
			if len(semResult.Removed) > 0 {
				// Build ID remap: removed node ID → kept node ID
				idRemap := make(map[string]string)
				for _, pair := range semResult.Pairs {
					removedID := dedupResult.Nodes[pair.RemovedIdx].ID
					keptID := dedupResult.Nodes[pair.KeptIdx].ID
					idRemap[removedID] = keptID
				}
				// Filter nodes to kept only
				var keptNodes []graph.DedupNode
				for _, idx := range semResult.Kept {
					keptNodes = append(keptNodes, dedupResult.Nodes[idx])
				}
				// Remap edges
				for i := range dedupResult.Edges {
					if newID, ok := idRemap[dedupResult.Edges[i].SourceID]; ok {
						dedupResult.Edges[i].SourceID = newID
					}
					if newID, ok := idRemap[dedupResult.Edges[i].TargetID]; ok {
						dedupResult.Edges[i].TargetID = newID
					}
				}
				dedupResult.Nodes = keptNodes
				log.Printf("[pipeline] semantic dedup: merged %d entities (threshold 0.95, kept %d)",
					len(semResult.Removed), len(semResult.Kept))
				// Metric tracked via log; Prometheus in calling code
			}
		}
	}

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
	} // end if !cfg.SkipGraph

	// Force-disable triplets in RAG mode (no entities to form triplets from)
	if cfg.SkipGraph {
		cfg.GenerateTriplets = false
	}

	// --- Stage 4: Write to DBs (parallel: Neo4j + vector) ---
	progressCh <- Progress{Stage: "writing", EntitiesExtracted: len(dedupResult.Nodes), EdgesExtracted: len(dedupResult.Edges), ElapsedMs: ms(start)}

	var writeWg sync.WaitGroup
	var nodesWritten, edgesWritten atomic.Int32

	// Neo4j write (goroutine) — skipped in RAG mode
	if cfg.Neo4jURL != "" && !cfg.SkipGraph {
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
				props := map[string]any{
					"name": n.Name, "description": n.Description, "type": n.Type,
					"dataset_id": cfg.DatasetID, "confidence": n.Confidence,
					"source_chunk": n.SourceChunkID, "source_doc": n.SourceDocID,
					"extracted_at": n.ExtractedAt,
				}
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
			if len(res.Errors) > 0 {
				log.Printf("[pipeline] neo4j write errors: %v", res.Errors)
			}
			log.Printf("[pipeline] neo4j write: %d nodes, %d edges written", res.NodesWritten, res.EdgesWritten)
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

			// --- Embed raw text chunks (for full-text search) ---
			tagsJSON := "[]"
			if len(cfg.Tags) > 0 {
				if b, err := json.Marshal(cfg.Tags); err == nil {
					tagsJSON = string(b)
				}
			}

			// Helper to build chunk metadata JSON
			buildChunkMeta := func(ch indexedChunk) string {
				meta := fmt.Sprintf(`{"text":%s,"dataset_id":"%s","room":%s,"tags":%s`,
					mustJSON(ch.text), cfg.DatasetID,
					mustJSON(cfg.Room), tagsJSON)
				if cfg.DocumentID != "" {
					meta += `,"document_id":` + mustJSON(cfg.DocumentID)
				}
				if cfg.DocumentTitle != "" {
					meta += `,"document_title":` + mustJSON(cfg.DocumentTitle)
				}
				if ch.section != "" {
					meta += `,"section":` + mustJSON(ch.section)
				}
				if ch.parentID != "" {
					meta += `,"parent_id":` + mustJSON(ch.parentID)
				}
				meta += "}"
				return meta
			}

			// Determine target collection for chunks
			chunkColl := coll
			if cfg.ParentChild {
				// In parent-child mode: children go to _child collection
				chunkColl = coll + "_child"
				if !cfg.Collections.Has(chunkColl) {
					if err := cfg.Collections.Create(chunkColl); err != nil {
						log.Printf("[pipeline] collection create %q: %v", chunkColl, err)
					}
				}
			}

			chunkInserted := 0
			// Collect embed texts (with contextual headers) and original texts
			embedTexts := make([]string, 0, len(allChunks))
			chunkIDs := make([]string, 0, len(allChunks))
			chunkMetas := make([]indexedChunk, 0, len(allChunks))
			for _, ch := range allChunks {
				if len(ch.text) > 30 { // skip tiny fragments
					et := ch.embedTxt
					if et == "" {
						et = ch.text
					}
					embedTexts = append(embedTexts, et)
					chunkIDs = append(chunkIDs, ch.id)
					chunkMetas = append(chunkMetas, ch)
				}
			}
			if len(embedTexts) > 0 {
				chunkVecs, err := embedClient.EmbedTexts(ctx, embedTexts)
				if err != nil {
					log.Printf("[pipeline] chunk embed FAILED (%d chunks): %v", len(embedTexts), err)
				} else {
					for i, vec := range chunkVecs {
						if i < len(chunkIDs) {
							meta := buildChunkMeta(chunkMetas[i])
							if err := cfg.Collections.Insert(chunkColl, chunkIDs[i], vec, meta); err != nil {
								log.Printf("[pipeline] chunk insert error: %v", err)
							} else {
								chunkInserted++
								if cfg.BM25Indexes != nil {
									if idx, ok := cfg.BM25Indexes[chunkColl]; ok {
										idx.Add(chunkIDs[i], chunkMetas[i].text, meta)
									}
								}
							}
						}
					}
					log.Printf("[pipeline] chunk write: %d/%d raw chunks embedded into %q", chunkInserted, len(embedTexts), chunkColl)
				}
			}

			// Embed parent chunks for parent-child mode (into main collection)
			if cfg.ParentChild && len(allParentChunks) > 0 {
				parentEmbedTexts := make([]string, 0, len(allParentChunks))
				parentIDs := make([]string, 0, len(allParentChunks))
				parentMetas := make([]indexedChunk, 0, len(allParentChunks))
				for _, ch := range allParentChunks {
					if len(ch.text) > 30 {
						et := ch.embedTxt
						if et == "" {
							et = ch.text
						}
						parentEmbedTexts = append(parentEmbedTexts, et)
						parentIDs = append(parentIDs, ch.id)
						parentMetas = append(parentMetas, ch)
					}
				}
				if len(parentEmbedTexts) > 0 {
					pVecs, err := embedClient.EmbedTexts(ctx, parentEmbedTexts)
					if err != nil {
						log.Printf("[pipeline] parent embed FAILED: %v", err)
					} else {
						parentInserted := 0
						for i, vec := range pVecs {
							if i < len(parentIDs) {
								meta := buildChunkMeta(parentMetas[i])
								if err := cfg.Collections.Insert(coll, parentIDs[i], vec, meta); err != nil {
									log.Printf("[pipeline] parent insert error: %v", err)
								} else {
									parentInserted++
								}
							}
						}
						log.Printf("[pipeline] parent write: %d/%d parent chunks embedded into %q", parentInserted, len(parentEmbedTexts), coll)
					}
				}
			}

			// Embed node names/descriptions (skipped in RAG mode — no entities)
			if !cfg.SkipGraph {
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
								// Update BM25 index for lexical search
								if cfg.BM25Indexes != nil {
									if idx, ok := cfg.BM25Indexes[coll]; ok {
										idx.Add(n.ID, texts[i], meta)
									}
								}
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
			} // end if !cfg.SkipGraph (entity + triplet embedding)
		}()
	}

	// PostgreSQL graph upsert (goroutine, parallel with Neo4j + vector) — skipped in RAG mode
	if cfg.DB != nil && !cfg.SkipGraph {
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

	// --- Stage 5: Community Detection + Hierarchical Summarization ---
	// Runs on FULL graph (not per-dataset) to capture cross-document relationships.
	// Skipped in RAG mode (no graph) or when DB is nil.
	communitiesDetected := 0
	if !cfg.SkipGraph && cfg.DB != nil && len(dedupResult.Nodes) >= 3 {
		progressCh <- Progress{Stage: "communities", Message: "detecting communities", ElapsedMs: ms(start)}

		g, gErr := community.BuildGraphFromSQL(ctx, cfg.DB)
		if gErr != nil {
			log.Printf("[pipeline] community graph build: %v", gErr)
		} else if g.NodeCount() >= 3 {
			commCfg := community.DefaultConfig()
			if cfg.CommunityResolution > 0 {
				commCfg.Resolution = cfg.CommunityResolution
			}
			var dendro *community.Dendrogram

			// Try incremental first, falls back to full compute internally
			d, iErr := community.IncrementalUpdate(ctx, cfg.DB, g, commCfg)
			if iErr != nil {
				log.Printf("[pipeline] community detect: %v", iErr)
			} else if d != nil {
				dendro = d
			}

			if dendro != nil {
				if len(dendro.Levels) > 0 {
					log.Printf("[pipeline] communities: %d levels, %d leaf communities (Q=%.4f, %d iterations)",
						dendro.MaxLevel+1, len(dendro.Levels[0]), dendro.Modularity[0], dendro.Iterations)
				}

				if err := community.ReplaceCommunities(ctx, cfg.DB, *dendro); err != nil {
					log.Printf("[pipeline] community write: %v", err)
				} else {
					for _, level := range dendro.Levels {
						communitiesDetected += len(level)
					}
				}

				// Hierarchical summarization (optional, needs LLM + embed)
				if cfg.LLMProvider != nil && cfg.EmbedEndpoint != "" {
					progressCh <- Progress{
						Stage:   "communities",
						Message: fmt.Sprintf("summarizing %d communities", communitiesDetected),
						ElapsedMs: ms(start),
					}
					sumCfg := community.SummarizeConfig{
						LLMProvider: cfg.LLMProvider,
						EmbedClient: embed.NewClient(cfg.EmbedEndpoint, cfg.EmbedModel, 16, 3),
						Collections: cfg.Collections,
						DB:          cfg.DB,
						LLMCache:    cfg.LLMCache,
						Concurrency: 3,
						MinMembers:  3,
						MaxContext:   50,
					}
					if err := community.SummarizeHierarchy(ctx, *dendro, g, sumCfg); err != nil {
						log.Printf("[pipeline] community summarize: %v", err)
					}
				}
			}
		}
	}

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
		nodes[i] = graph.DedupNode{
			ID: id, Name: n.Name, Type: n.Type, Description: n.Description,
			Confidence: 1.0, // default; could parse from LLM if model supports it
			ExtractedAt: time.Now().UTC().Format(time.RFC3339),
		}
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

// mustJSON returns s as a JSON-safe quoted string.
// buildEmbedText prepends contextual headers to chunk text for better embedding.
// The embedding will capture document/section context, improving retrieval relevance.
// Original text is stored separately in metadata for display.
func buildEmbedText(text, documentTitle, section string) string {
	var prefix string
	if documentTitle != "" {
		prefix += "[Document: " + documentTitle + "]\n"
	}
	if section != "" {
		prefix += "[Section: " + section + "]\n"
	}
	return prefix + text
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

const defaultExtractionPrompt = `Extract entities and relationships from the following text.
Return a JSON object with this exact structure:
{
  "nodes": [{"id": "unique_id", "name": "Entity Name", "type": "EntityType", "description": "Brief description"}],
  "edges": [{"source": "source_id", "target": "target_id", "relationship": "RELATIONSHIP_TYPE", "edge_text": "description of relationship"}]
}
Return ONLY the JSON object, no other text.`
