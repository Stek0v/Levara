package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/mcp"
)

func ensureConsolidationJobs(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, consolidationJobsDDL); err != nil {
		return err
	}
	for _, column := range []struct{ name, ddl string }{
		{"owner_id", "TEXT NOT NULL DEFAULT ''"}, {"candidates", "INTEGER NOT NULL DEFAULT 0"},
		{"clusters", "INTEGER NOT NULL DEFAULT 0"}, {"actions", "INTEGER NOT NULL DEFAULT 0"}, {"llm_calls", "INTEGER NOT NULL DEFAULT 0"},
	} {
		// Keep projection stable across additive migrations: pgx caches query shapes.
		probe, err := db.QueryContext(ctx, "SELECT "+column.name+" FROM consolidation_jobs WHERE 1=0")
		if err == nil {
			if err := probe.Close(); err != nil {
				return err
			}
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE consolidation_jobs ADD COLUMN "+column.name+" "+column.ddl); err != nil {
			// A concurrent initializer may have added this exact known column.
			probe, probeErr := db.QueryContext(ctx, "SELECT "+column.name+" FROM consolidation_jobs WHERE 1=0")
			if probeErr != nil {
				return err
			}
			if err := probe.Close(); err != nil {
				return err
			}
		}
	}
	return nil
}

func consolidationJobDuration(args map[string]any) (time.Duration, error) {
	const maximum = 300000
	raw, exists := args["max_duration_ms"]
	if !exists {
		return 5 * time.Minute, nil
	}
	n, ok := raw.(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 1 || n > maximum || math.Trunc(n) != n {
		return 0, errors.New("max_duration_ms must be an integer from 1 to 300000")
	}
	return time.Duration(n) * time.Millisecond, nil
}

func (h *mcpHandler) toolConsolidateAsync(ctx context.Context, args map[string]any) mcpToolResult {
	if raw, exists := args["wait"]; exists {
		wait, ok := raw.(bool)
		if !ok {
			return mcpErrorResult("wait must be a boolean")
		}
		if wait {
			return mcp.ToolConsolidate(ctx, h, args)
		}
	}
	if h.cfg.DB == nil {
		return mcpErrorResult("database not configured")
	}
	collection, ok := args["collection"].(string)
	if !ok || collection == "" {
		return mcpErrorResult("collection required")
	}
	if _, err := consolidationJobDuration(args); err != nil {
		return mcpErrorResult(err.Error())
	}
	actor := h.MetadataActor(ctx)
	if !actor.TrustedLocal && (actor.UserID == "" || actor.Credential.Kind == "") {
		return mcpErrorResult("verified consolidation authority required")
	}
	if err := ensureConsolidationJobs(ctx, h.cfg.DB); err != nil {
		return mcpErrorResult("consolidation job store unavailable: " + err.Error())
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return mcpErrorResult("invalid consolidation arguments")
	}
	var captured map[string]any
	if err := json.Unmarshal(raw, &captured); err != nil {
		return mcpErrorResult("invalid consolidation arguments")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var existing, status, submitted string
	err = h.cfg.DB.QueryRowContext(ctx, Q(`SELECT id,status,created_at FROM consolidation_jobs WHERE owner_id=$1 AND args_json=$2 AND status IN ('pending','running') LIMIT 1`), actor.UserID, string(raw)).Scan(&existing, &status, &submitted)
	if err == nil {
		return mcpJSONResult(map[string]any{"job_id": existing, "status": status, "submitted_at": submitted, "poll_after_ms": 500})
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return mcpErrorResult("consolidation job lookup failed")
	}
	id := uuid.NewString()
	if _, err := h.cfg.DB.ExecContext(ctx, Q(`INSERT INTO consolidation_jobs(id,owner_id,status,args_json,created_at,updated_at) VALUES($1,$2,'pending',$3,$4,$5)`), id, actor.UserID, string(raw), now, now); err != nil {
		return mcpErrorResult("consolidation enqueue failed")
	}
	go h.runConsolidationJob(context.WithoutCancel(ctx), id, captured)
	return mcpJSONResult(map[string]any{"job_id": id, "status": "pending", "submitted_at": now, "poll_after_ms": 500})
}

func (h *mcpHandler) runConsolidationJob(parent context.Context, id string, args map[string]any) {
	duration, err := consolidationJobDuration(args)
	if err != nil {
		log.Printf("consolidation job %s: %v", id, err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, duration)
	defer cancel()
	actor := h.MetadataActor(ctx)
	result, err := h.cfg.DB.ExecContext(ctx, Q(`UPDATE consolidation_jobs SET status='running',updated_at=$1 WHERE id=$2 AND owner_id=$3 AND status='pending'`), time.Now().UTC().Format(time.RFC3339Nano), id, actor.UserID)
	if err != nil {
		log.Printf("consolidation claim %s: %v", id, err)
		finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer finalCancel()
		_, finalErr := h.cfg.DB.ExecContext(finalCtx, Q(`UPDATE consolidation_jobs SET status='failed',last_error=$1,updated_at=$2 WHERE id=$3 AND owner_id=$4 AND status='pending'`), "claim failed; execution did not start; resubmit a verified request", time.Now().UTC().Format(time.RFC3339Nano), id, actor.UserID)
		if finalErr != nil {
			log.Printf("consolidation failed-claim status %s: %v", id, finalErr)
		}
		return
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return
	}
	outcome := mcp.ToolConsolidate(ctx, h, args)
	status, text, last := "completed", "", ""
	if len(outcome.Content) > 0 {
		text = outcome.Content[0].Text
	}
	if outcome.IsError {
		status = "failed"
		last = text
	}
	candidates, clusters, actions, llmCalls := parseConsolidationProgress(text)
	// Final status must persist even when execution timed out; it cannot overwrite
	// a recovery decision because only the current running state may transition.
	finalCtx, finalCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer finalCancel()
	_, err = h.cfg.DB.ExecContext(finalCtx, Q(`UPDATE consolidation_jobs SET status=$1,result_text=$2,last_error=$3,candidates=$4,clusters=$5,actions=$6,llm_calls=$7,updated_at=$8 WHERE id=$9 AND owner_id=$10 AND status='running'`), status, text, last, candidates, clusters, actions, llmCalls, time.Now().UTC().Format(time.RFC3339Nano), id, actor.UserID)
	if err != nil {
		log.Printf("consolidation final status %s: %v", id, err)
	}
}

var consolidationProgressRE = regexp.MustCompile(`candidates=(\d+) clusters=(\d+) actions=(\d+)`)
var consolidationLLMCallsRE = regexp.MustCompile(`llm_calls=(\d+)`)

func parseConsolidationProgress(text string) (int, int, int, int) {
	var a, b, c, d int
	if calls := consolidationLLMCallsRE.FindStringSubmatch(text); len(calls) == 2 {
		_, _ = fmt.Sscanf(calls[1], "%d", &d)
	}
	m := consolidationProgressRE.FindStringSubmatch(text)
	if len(m) != 4 {
		return 0, 0, 0, d
	}
	_, _ = fmt.Sscanf(m[1], "%d", &a)
	_, _ = fmt.Sscanf(m[2], "%d", &b)
	_, _ = fmt.Sscanf(m[3], "%d", &c)
	return a, b, c, d
}

func (h *mcpHandler) toolConsolidationStatus(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpErrorResult("database not configured")
	}
	id, ok := args["job_id"].(string)
	if !ok || id == "" {
		return mcpErrorResult("job_id required")
	}
	var status, result, last, updated string
	var candidates, clusters, actions, llmCalls int
	owner := h.MetadataActor(ctx).UserID
	err := h.cfg.DB.QueryRowContext(ctx, Q(`SELECT status,result_text,last_error,updated_at,candidates,clusters,actions,llm_calls FROM consolidation_jobs WHERE id=$1 AND owner_id=$2`), id, owner).Scan(&status, &result, &last, &updated, &candidates, &clusters, &actions, &llmCalls)
	if err != nil {
		return mcpErrorResult("consolidation job not found")
	}
	return mcpJSONResult(map[string]any{"job_id": id, "status": status, "result": result, "last_error": last, "updated_at": updated, "candidates": candidates, "clusters": clusters, "actions": actions, "llm_calls": llmCalls})
}

// StartConsolidationRecovery never recreates credentials from stored job fields.
func StartConsolidationRecovery(cfg APIConfig) {
	if cfg.DB == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureConsolidationJobs(ctx, cfg.DB); err != nil {
		log.Printf("consolidation recovery schema: %v", err)
		return
	}
	rows, err := cfg.DB.QueryContext(ctx, `SELECT id,owner_id,status,args_json FROM consolidation_jobs WHERE status IN ('pending','running')`)
	if err != nil {
		log.Printf("consolidation recovery read: %v", err)
		return
	}
	type job struct{ id, owner, status, raw string }
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.owner, &j.status, &j.raw); err != nil {
			_ = rows.Close()
			log.Printf("consolidation recovery scan: %v", err)
			return
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		log.Printf("consolidation recovery read/close: %v %v", err, closeErr)
		return
	}
	h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
	for _, j := range jobs {
		var args map[string]any
		decodeErr := json.Unmarshal([]byte(j.raw), &args)
		_, durationErr := consolidationJobDuration(args)
		// Running jobs may already have committed. Only unclaimed local shared
		// jobs can resume without fabricating authority or a commit outcome.
		if cfg.RequireAuth || j.owner != "" || j.status == "running" || decodeErr != nil || durationErr != nil {
			_, err = cfg.DB.ExecContext(ctx, Q(`UPDATE consolidation_jobs SET status='failed',last_error=$1,updated_at=$2 WHERE id=$3 AND status IN ('pending','running')`), "unknown outcome after restart; verified authority unavailable or execution may have committed; inspect state and resubmit", time.Now().UTC().Format(time.RFC3339Nano), j.id)
			if err != nil {
				log.Printf("consolidation recovery fail %s: %v", j.id, err)
			}
			continue
		}
		go h.runConsolidationJob(context.Background(), j.id, args)
	}
}

const consolidationJobsDDL = `CREATE TABLE IF NOT EXISTS consolidation_jobs (id TEXT PRIMARY KEY,owner_id TEXT NOT NULL DEFAULT '',status TEXT NOT NULL,args_json TEXT NOT NULL,result_text TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',candidates INTEGER NOT NULL DEFAULT 0,clusters INTEGER NOT NULL DEFAULT 0,actions INTEGER NOT NULL DEFAULT 0,llm_calls INTEGER NOT NULL DEFAULT 0,created_at TEXT NOT NULL,updated_at TEXT NOT NULL)`
