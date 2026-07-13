package http

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/mcp"
)

func (h *mcpHandler) toolConsolidateAsync(ctx context.Context, args map[string]any) mcpToolResult {
	if wait, _ := args["wait"].(bool); wait {
		return mcp.ToolConsolidate(ctx, h, args)
	}
	if h.cfg.DB == nil {
		return mcpErrorResult("database not configured")
	}
	collection, _ := args["collection"].(string)
	if collection == "" {
		return mcpErrorResult("collection required")
	}
	if _, err := h.cfg.DB.Exec(consolidationJobsDDL); err != nil {
		return mcpErrorResult("consolidation job store unavailable")
	}
	_, _ = h.cfg.DB.Exec(`ALTER TABLE consolidation_jobs ADD COLUMN owner_id TEXT NOT NULL DEFAULT ''`)
	for _, column := range []string{"candidates INTEGER NOT NULL DEFAULT 0", "clusters INTEGER NOT NULL DEFAULT 0", "actions INTEGER NOT NULL DEFAULT 0", "llm_calls INTEGER NOT NULL DEFAULT 0"} {
		_, _ = h.cfg.DB.Exec(`ALTER TABLE consolidation_jobs ADD COLUMN ` + column)
	}
	id := uuid.NewString()
	raw, _ := json.Marshal(args)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	owner, _ := ctx.Value(mcpUserIDKey).(string)
	var existing string
	if h.cfg.DB.QueryRowContext(ctx, Q(`SELECT id FROM consolidation_jobs WHERE owner_id=$1 AND args_json=$2 AND status IN ('pending','running') LIMIT 1`), owner, string(raw)).Scan(&existing) == nil {
		return mcpJSONResult(map[string]any{"job_id": existing, "status": "pending", "submitted_at": now, "poll_after_ms": 500})
	}
	if _, err := h.cfg.DB.ExecContext(ctx, Q(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,created_at,updated_at) VALUES($1,$2,'pending',$3,$4,$5)`), id, owner, string(raw), now, now); err != nil {
		return mcpErrorResult("consolidation enqueue failed")
	}
	go h.runConsolidationJob(id, args)
	return mcpJSONResult(map[string]any{"job_id": id, "status": "pending", "submitted_at": now, "poll_after_ms": 500})
}

func (h *mcpHandler) runConsolidationJob(id string, args map[string]any) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = h.cfg.DB.Exec(Q(`UPDATE consolidation_jobs SET status='running',updated_at=$1 WHERE id=$2`), now, id)
	maxDuration := 5 * time.Minute
	if raw, ok := args["max_duration_ms"].(float64); ok && raw > 0 {
		maxDuration = time.Duration(raw) * time.Millisecond
	}
	jobCtx, cancel := context.WithTimeout(context.Background(), maxDuration)
	defer cancel()
	result := mcp.ToolConsolidate(jobCtx, h, args)
	status := "completed"
	text := ""
	last := ""
	if len(result.Content) > 0 {
		text = result.Content[0].Text
	}
	if result.IsError {
		status = "failed"
		last = text
	}
	candidates, clusters, actions := parseConsolidationProgress(text)
	_, _ = h.cfg.DB.Exec(Q(`UPDATE consolidation_jobs SET status=$1,result_text=$2,last_error=$3,candidates=$4,clusters=$5,actions=$6,updated_at=$7 WHERE id=$8`), status, text, last, candidates, clusters, actions, time.Now().UTC().Format(time.RFC3339Nano), id)
}

var consolidationProgressRE = regexp.MustCompile(`candidates=(\d+) clusters=(\d+) actions=(\d+)`)

func parseConsolidationProgress(text string) (int, int, int) {
	m := consolidationProgressRE.FindStringSubmatch(text)
	if len(m) != 4 {
		return 0, 0, 0
	}
	var a, b, c int
	_, _ = fmt.Sscanf(m[1], "%d", &a)
	_, _ = fmt.Sscanf(m[2], "%d", &b)
	_, _ = fmt.Sscanf(m[3], "%d", &c)
	return a, b, c
}

func (h *mcpHandler) toolConsolidationStatus(ctx context.Context, args map[string]any) mcpToolResult {
	id, _ := args["job_id"].(string)
	if id == "" {
		return mcpErrorResult("job_id required")
	}
	var status, result, last, updated string
	var candidates, clusters, actions, llmCalls int
	owner, _ := ctx.Value(mcpUserIDKey).(string)
	err := h.cfg.DB.QueryRowContext(ctx, Q(`SELECT status,result_text,last_error,updated_at,candidates,clusters,actions,llm_calls FROM consolidation_jobs WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), id, owner).Scan(&status, &result, &last, &updated, &candidates, &clusters, &actions, &llmCalls)
	if err != nil {
		return mcpErrorResult("consolidation job not found")
	}
	return mcpJSONResult(map[string]any{"job_id": id, "status": status, "result": result, "last_error": last, "updated_at": updated, "candidates": candidates, "clusters": clusters, "actions": actions, "llm_calls": llmCalls})
}

// StartConsolidationRecovery resumes durable jobs interrupted by a restart.
func StartConsolidationRecovery(cfg APIConfig) {
	if cfg.DB == nil {
		return
	}
	h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
	if _, err := cfg.DB.Exec(consolidationJobsDDL); err != nil {
		return
	}
	rows, err := cfg.DB.Query(`SELECT id,args_json FROM consolidation_jobs WHERE status IN ('pending','running')`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, raw string
		if rows.Scan(&id, &raw) != nil {
			continue
		}
		var args map[string]any
		if json.Unmarshal([]byte(raw), &args) != nil {
			continue
		}
		_, _ = cfg.DB.Exec(Q(`UPDATE consolidation_jobs SET status='pending',last_error='recovered after restart',updated_at=$1 WHERE id=$2`), time.Now().UTC().Format(time.RFC3339Nano), id)
		go h.runConsolidationJob(id, args)
	}
}

const consolidationJobsDDL = `CREATE TABLE IF NOT EXISTS consolidation_jobs (id TEXT PRIMARY KEY,owner_id TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,args_json TEXT NOT NULL,result_text TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',candidates INTEGER NOT NULL DEFAULT 0,clusters INTEGER NOT NULL DEFAULT 0,actions INTEGER NOT NULL DEFAULT 0,llm_calls INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`
