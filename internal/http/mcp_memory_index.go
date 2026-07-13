package http

import "context"

func mcpErrorResult(message string) mcpToolResult {
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: message}}, IsError: true}
}

func (h *mcpHandler) toolMemoryIndexStatus(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.MemoryIndexOutbox == nil {
		return mcpJSONResult(map[string]any{"jobs": []any{}, "counts": map[string]int{}})
	}
	limit := 20
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	owner, _ := ctx.Value(mcpUserIDKey).(string)
	jobs, err := h.cfg.MemoryIndexOutbox.List(ctx, owner, limit)
	if err != nil {
		return mcpErrorResult("memory index status failed")
	}
	counts, err := h.cfg.MemoryIndexOutbox.Counts(ctx)
	if err != nil {
		return mcpErrorResult("memory index counts failed")
	}
	return mcpJSONResult(map[string]any{"jobs": jobs, "counts": counts})
}

func (h *mcpHandler) toolMemoryIndexRetry(ctx context.Context, args map[string]any) mcpToolResult {
	id, _ := args["job_id"].(string)
	if id == "" {
		return mcpErrorResult("job_id required")
	}
	if h.cfg.MemoryIndexOutbox == nil {
		return mcpErrorResult("memory index outbox unavailable")
	}
	owner, _ := ctx.Value(mcpUserIDKey).(string)
	ok, err := h.cfg.MemoryIndexOutbox.Retry(ctx, id, owner)
	if err != nil {
		return mcpErrorResult("memory index retry failed")
	}
	if !ok {
		return mcpErrorResult("job not found or not retryable")
	}
	return mcpJSONResult(map[string]any{"ok": true, "job_id": id, "status": "pending"})
}
