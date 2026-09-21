// status.go — A1: unified operational status.
//
// One data source (REST /api/v1/status), three consumers: the CLI
// (`levara status`), MCP agents (`levara_status`), and the WebUI.
// Aggregates server health, memory pressure, corpus stats, background
// job progress, and active warnings — the user-facing answer to
// "what is Levara doing to my machine right now".
package http

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/governor"
)

// StatusResponse is the complete operational snapshot.
type StatusResponse struct {
	Server   ServerStatus     `json:"server"`
	Memory   MemoryStatus     `json:"memory"`
	Corpus   []CollectionStat `json:"corpus"`
	Jobs     JobsStatus       `json:"jobs"`
	Warnings []string         `json:"warnings"`
}

type ServerStatus struct {
	Version    string `json:"version"`
	Profile    string `json:"profile,omitempty"`
	Standalone bool   `json:"standalone"`
}

type MemoryStatus struct {
	RSSBytes    uint64 `json:"rss_bytes"`
	HeapAlloc   uint64 `json:"heap_alloc"`
	HeapObjects uint64 `json:"heap_objects"`
	NumGC       uint32 `json:"num_gc"`
	BudgetBytes uint64 `json:"budget_bytes,omitempty"`
	Pressure    string `json:"pressure"` // low|normal|elevated|critical
}

type CollectionStat struct {
	Name         string `json:"name"`
	Records      int64  `json:"records"`
	Dim          int    `json:"dim"`
	EmbedModel   string `json:"embed_model,omitempty"`
	IndexingTier string `json:"indexing_tier"` // full|vectors-only|minimal
}

type JobsStatus struct {
	SourcesDaemon  []SourceJobState `json:"sources_daemon,omitempty"`
	CognifyPending int              `json:"cognify_pending"`
	RagJanitor     RagProgress      `json:"rag_janitor"`
	Distill        DistillProgress  `json:"distill"`
}

type SourceJobState struct {
	Platform    string `json:"platform"`
	LastScanAgo string `json:"last_scan_ago"`
	Scans       int    `json:"scans"`
	LastError   string `json:"last_error,omitempty"`
}

type RagProgress struct {
	CurrentVersion int `json:"current_version"`
	AtCurrent      int `json:"at_current"`
	TotalSessions  int `json:"total_sessions"`
}

type DistillProgress struct {
	Done    int `json:"done"`
	Failed  int `json:"failed"`
	Pending int `json:"pending"`
}

func RegisterStatusAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/status", statusHandler(cfg))
}

func statusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		status := buildStatus(c.UserContext(), cfg)
		return c.JSON(status)
	}
}

func buildStatus(ctx context.Context, cfg APIConfig) StatusResponse {
	resp := StatusResponse{
		Server: ServerStatus{
			Version:    cfg.Version,
			Standalone: true,
		},
		Warnings: []string{},
	}

	// Memory: real OS-reported resident set (MemStats.Sys over-states by
	// reserved-but-untouched arenas — see governor.ProcessRSS).
	rss := governor.ProcessRSS()
	budget := governor.EnvBudgetBytes()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	resp.Memory = MemoryStatus{
		RSSBytes:    rss,
		HeapAlloc:   ms.HeapAlloc,
		HeapObjects: ms.HeapObjects,
		NumGC:       ms.NumGC,
		BudgetBytes: budget,
		Pressure:    memoryPressure(rss, budget),
	}

	// Corpus
	if cfg.Collections != nil {
		for _, name := range cfg.Collections.List() {
			m := cfg.Collections.GetMeta(name)
			if m == nil {
				continue
			}
			resp.Corpus = append(resp.Corpus, CollectionStat{
				Name:         m.Name,
				Records:      int64(m.RecordCount),
				Dim:          m.EmbeddingDim,
				EmbedModel:   m.EmbeddingModel,
				IndexingTier: indexingTier(int64(m.RecordCount)),
			})
		}
	}

	// Jobs from DB
	if cfg.DB != nil {
		resp.Jobs = buildJobsStatus(ctx, cfg.DB)
	}

	// Warnings
	resp.Warnings = buildWarnings(resp)
	return resp
}

// memoryPressure classifies RSS. With a budget (GOMEMLIMIT) the bands are
// relative to it, mirroring the governor's 0.5/0.8/0.9 thresholds; without
// one, absolute bands for a 16 GB host.
func memoryPressure(rss, budget uint64) string {
	if budget > 0 {
		switch {
		case rss > budget*90/100:
			return "critical"
		case rss > budget*80/100:
			return "elevated"
		case rss > budget*50/100:
			return "normal"
		default:
			return "low"
		}
	}
	const MiB = 1024 * 1024
	switch {
	case rss > 12*1024*MiB:
		return "critical"
	case rss > 8*1024*MiB:
		return "elevated"
	case rss > 4*1024*MiB:
		return "normal"
	default:
		return "low"
	}
}

func indexingTier(records int64) string {
	switch {
	case records > 100_000:
		return "vectors-only"
	case records > 10_000:
		return "vectors+bm25"
	default:
		return "full"
	}
}

func buildJobsStatus(ctx context.Context, db *sql.DB) JobsStatus {
	jobs := JobsStatus{}

	// Cognify pending
	_ = db.QueryRowContext(ctx, Q(`
		SELECT COUNT(*) FROM document_pipeline_statuses
		WHERE pipeline_state IN ('RUNNING','FAILED')
	`)).Scan(&jobs.CognifyPending)

	// RAG janitor progress
	_ = db.QueryRowContext(ctx, Q(`
		SELECT COUNT(*) FROM chat_import_rag WHERE render_version >= 2
	`)).Scan(&jobs.RagJanitor.AtCurrent)
	_ = db.QueryRowContext(ctx, Q(`
		SELECT COUNT(DISTINCT session_id) FROM chat_import_messages
	`)).Scan(&jobs.RagJanitor.TotalSessions)
	jobs.RagJanitor.CurrentVersion = 2

	// Distill progress
	_ = db.QueryRowContext(ctx, Q(`
		SELECT COALESCE(SUM(CASE WHEN status='ok' THEN 1 ELSE 0 END),0),
		       COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0)
		FROM chat_import_distill
	`)).Scan(&jobs.Distill.Done, &jobs.Distill.Failed)
	_ = db.QueryRowContext(ctx, Q(`
		SELECT COUNT(DISTINCT m.session_id) FROM chat_import_messages m
		WHERE NOT EXISTS (SELECT 1 FROM chat_import_distill d
			WHERE d.platform=m.platform AND d.session_id=m.session_id AND d.hall='decision' AND d.status='ok')
		GROUP BY m.session_id HAVING COUNT(*) >= 6
		   AND SUM(CASE WHEN m.role='user' THEN 1 ELSE 0 END) >= 2
	`)).Scan(&jobs.Distill.Pending)

	// Sources daemon
	rows, err := db.QueryContext(ctx, Q(`
		SELECT platform, last_scan_at, scans, last_error FROM chat_import_sources
	`))
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var s SourceJobState
			var lastScan string
			var scans int
			if err := rows.Scan(&s.Platform, &lastScan, &scans, &s.LastError); err == nil {
				s.Scans = scans
				s.LastScanAgo = timeAgo(lastScan)
				jobs.SourcesDaemon = append(jobs.SourcesDaemon, s)
			}
		}
	}
	return jobs
}

func timeAgo(iso string) string {
	if iso == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}

func buildWarnings(resp StatusResponse) []string {
	warnings := []string{}
	if resp.Memory.Pressure == "critical" {
		warnings = append(warnings, fmt.Sprintf("memory critical: RSS %.1f GB", float64(resp.Memory.RSSBytes)/1e9))
	}
	if resp.Jobs.CognifyPending > 500 {
		warnings = append(warnings, fmt.Sprintf("cognify backlog: %d pending (possible stuck large docs)", resp.Jobs.CognifyPending))
	}
	for _, col := range resp.Corpus {
		if col.IndexingTier == "vectors-only" && col.Records > 0 {
			warnings = append(warnings, fmt.Sprintf("collection %s: BM25/graph disabled (>100k threshold, %d records)", col.Name, col.Records))
		}
	}
	if len(warnings) == 0 {
		warnings = []string{}
	}
	return warnings
}
