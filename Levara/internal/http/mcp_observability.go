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


// toolRecentErrors aggregates recent error signals from two sources:
// FAILED background runs in the registry, and doctor heartbeats whose
// payload reported any check with status=fail. Designed to answer
// "what's been going wrong lately?" without grepping logs.
func (h *mcpHandler) toolRecentErrors(ctx context.Context, args map[string]any) mcpToolResult {
	limit := 20
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 100 {
		limit = 100
	}

	type errorEntry struct {
		Source    string `json:"source"` // "pipeline_run" | "doctor"
		Stage     string `json:"stage,omitempty"`
		Message   string `json:"message"`
		Reference string `json:"reference,omitempty"` // run_id or heartbeat_id
		At        string `json:"at"`
	}

	var entries []errorEntry

	if h.cfg.Runs != nil {
		for _, s := range h.cfg.Runs.Snapshot() {
			if s.Status != "FAILED" {
				continue
			}
			entries = append(entries, errorEntry{
				Source:    "pipeline_run",
				Stage:     s.Stage,
				Message:   s.Message,
				Reference: s.RunID,
				At:        s.StartedAt.UTC().Format(time.RFC3339),
			})
		}
	}

	if h.cfg.DB != nil {
		rows, err := h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, payload, created_at FROM heartbeats
			   WHERE event_type = 'doctor' ORDER BY created_at DESC LIMIT $1`), 50)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, payload, createdAt string
				if err := rows.Scan(&id, &payload, &createdAt); err != nil {
					continue
				}
				var report struct {
					Status string `json:"status"`
					Checks []struct {
						Name    string `json:"name"`
						Status  string `json:"status"`
						Message string `json:"message"`
					} `json:"checks"`
				}
				if json.Unmarshal([]byte(payload), &report) != nil {
					continue
				}
				for _, ch := range report.Checks {
					if ch.Status != "fail" {
						continue
					}
					entries = append(entries, errorEntry{
						Source:    "doctor",
						Stage:     ch.Name,
						Message:   ch.Message,
						Reference: id,
						At:        createdAt,
					})
				}
			}
		}
	}

	// Already mostly-sorted (registry first, then doctor desc); cap.
	if len(entries) > limit {
		entries = entries[:limit]
	}

	data, _ := json.MarshalIndent(map[string]any{
		"count":  len(entries),
		"errors": entries,
	}, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

// toolSyncStatus summarizes recent sync events per direction (push|pull)
// from the heartbeats table. Returns last-seen-at and count for each
// direction plus the most recent N events. Sync only emits a heartbeat
// on success today, so this view answers "did sync run lately?" rather
// than "did sync fail?".
func (h *mcpHandler) toolSyncStatus(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"error":"no database configured"}`}}, IsError: true}
	}

	limit := 10
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	if limit > 50 {
		limit = 50
	}

	rows, err := h.cfg.DB.QueryContext(ctx,
		Q(`SELECT id, payload, created_at FROM heartbeats
		   WHERE event_type = 'sync' ORDER BY created_at DESC LIMIT $1`), limit)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"error":"sync heartbeats query failed"}`}}, IsError: true}
	}
	defer rows.Close()

	type perDirection struct {
		Count      int    `json:"count"`
		LastAt     string `json:"last_at"`
		LastRemote string `json:"last_remote"`
	}
	byDir := map[string]*perDirection{}
	type evt struct {
		ID        string          `json:"id"`
		Direction string          `json:"direction"`
		Remote    string          `json:"remote"`
		Types     json.RawMessage `json:"types,omitempty"`
		At        string          `json:"at"`
	}
	var events []evt

	for rows.Next() {
		var id, payload, createdAt string
		if err := rows.Scan(&id, &payload, &createdAt); err != nil {
			continue
		}
		var p struct {
			Direction string          `json:"direction"`
			Remote    string          `json:"remote"`
			Types     json.RawMessage `json:"types"`
		}
		if json.Unmarshal([]byte(payload), &p) != nil {
			continue
		}
		dir := p.Direction
		if dir == "" {
			dir = "unknown"
		}
		entry := byDir[dir]
		if entry == nil {
			entry = &perDirection{LastAt: createdAt, LastRemote: p.Remote}
			byDir[dir] = entry
		}
		entry.Count++
		events = append(events, evt{
			ID:        id,
			Direction: dir,
			Remote:    p.Remote,
			Types:     p.Types,
			At:        createdAt,
		})
	}

	data, _ := json.MarshalIndent(map[string]any{
		"by_direction": byDir,
		"events":       events,
	}, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
}

