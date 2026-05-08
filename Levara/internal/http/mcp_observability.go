// mcp_observability.go — Agent-facing runtime observability MCP tools.
//
// `doctor` and `heartbeat` already cover health checks and recent event
// history. These tools fill the gap between them: a single snapshot of
// live runtime state (`runtime_stats`) and a queryable view of in-flight
// + recent ingestion runs (`ingestion_status`).
package http

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"time"
)

// toolRuntimeStats returns a compact snapshot of the running instance:
// per-collection record counts and embedding model, dependency
// configuration (embed/llm/rerank/neo4j endpoints + flags), and basic
// process metrics. Read-only and safe to call frequently.
func (h *mcpHandler) toolRuntimeStats(ctx context.Context, args map[string]any) mcpToolResult {
	type collectionEntry struct {
		Name           string `json:"name"`
		Records        int    `json:"records"`
		Dim            int    `json:"dim"`
		Metric         string `json:"metric"`
		EmbeddingModel string `json:"embedding_model"`
		Domain         string `json:"domain,omitempty"`
	}

	var collections []collectionEntry
	totalRecords := 0
	if h.cfg.Collections != nil {
		for _, m := range h.cfg.Collections.ListWithMeta() {
			collections = append(collections, collectionEntry{
				Name:           m.Name,
				Records:        m.RecordCount,
				Dim:            m.EmbeddingDim,
				Metric:         m.DistanceMetric,
				EmbeddingModel: m.EmbeddingModel,
				Domain:         m.Domain,
			})
			totalRecords += m.RecordCount
		}
	}

	llmModel := ""
	llmProvider := ""
	if h.cfg.LLMProvider != nil {
		llmProvider = "configured"
		llmModel = os.Getenv("LLM_MODEL")
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	out := map[string]any{
		"collections":       collections,
		"collection_count":  len(collections),
		"total_records":     totalRecords,
		"embed_endpoint":    h.cfg.EmbedEndpoint,
		"embed_model":       h.cfg.EmbedModel,
		"llm_provider":      llmProvider,
		"llm_model":         llmModel,
		"rerank_enabled":    h.cfg.RerankEndpoint != "",
		"rerank_model":      h.cfg.RerankModel,
		"neo4j_enabled":     h.cfg.Neo4jCfg.Neo4jURL != "",
		"goroutines":        runtime.NumGoroutine(),
		"heap_alloc_bytes":  ms.HeapAlloc,
		"heap_sys_bytes":    ms.HeapSys,
		"num_gc":            ms.NumGC,
		"snapshot_taken_at": time.Now().UTC().Format(time.RFC3339),
	}

	data, _ := json.MarshalIndent(out, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

// toolIngestionStatus surfaces in-flight and recently completed
// background pipeline runs (cognify, codify, analyze_commits) from the
// in-memory run registry. Filter by status (RUNNING|COMPLETED|FAILED) or
// limit the result set.
func (h *mcpHandler) toolIngestionStatus(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.Runs == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"error":"run registry unavailable"}`}}, IsError: true}
	}

	statusFilter, _ := args["status"].(string)
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}

	all := h.cfg.Runs.Snapshot()
	running := 0
	completed := 0
	failed := 0
	filtered := make([]any, 0, len(all))
	for _, s := range all {
		switch s.Status {
		case "RUNNING":
			running++
		case "COMPLETED":
			completed++
		case "FAILED":
			failed++
		}
		if statusFilter != "" && s.Status != statusFilter {
			continue
		}
		if len(filtered) >= limit {
			continue
		}
		filtered = append(filtered, map[string]any{
			"pipeline_run_id":    s.RunID,
			"status":             s.Status,
			"stage":              s.Stage,
			"message":            s.Message,
			"chunks_created":     s.Chunks,
			"entities_extracted": s.Entities,
			"edges_extracted":    s.Edges,
			"elapsed_ms":         s.ElapsedMs,
			"started_at":         s.StartedAt.UTC().Format(time.RFC3339),
		})
	}

	out := map[string]any{
		"summary": map[string]any{
			"total":     len(all),
			"running":   running,
			"completed": completed,
			"failed":    failed,
		},
		"runs": filtered,
	}
	data, _ := json.MarshalIndent(out, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

