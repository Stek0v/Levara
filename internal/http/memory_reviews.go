package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/levara/internal/trajectory"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/llm"
)

const memoryReviewDDL = `
CREATE TABLE IF NOT EXISTS memory_review_runs (
	id TEXT PRIMARY KEY,
	status TEXT NOT NULL,
	scope_json TEXT NOT NULL DEFAULT '{}',
	summary TEXT NOT NULL DEFAULT '',
	error TEXT NOT NULL DEFAULT '',
	prompt_preview TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	completed_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS memory_review_findings (
	id TEXT PRIMARY KEY,
	run_id TEXT NOT NULL,
	category TEXT NOT NULL,
	severity TEXT NOT NULL DEFAULT 'medium',
	trajectory_id TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL,
	evidence TEXT NOT NULL DEFAULT '',
	recommendation TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL
);`

type memoryReviewRunRequest struct {
	Hours      int    `json:"hours"`
	Collection string `json:"collection"`
	Client     string `json:"client"`
	Limit      int    `json:"limit"`
	DryRun     bool   `json:"dry_run"`
}

type memoryReviewRun struct {
	ID            string                `json:"id"`
	Status        string                `json:"status"`
	Scope         memoryReviewScope     `json:"scope"`
	Summary       string                `json:"summary,omitempty"`
	Error         string                `json:"error,omitempty"`
	PromptPreview string                `json:"prompt_preview,omitempty"`
	CreatedAt     string                `json:"created_at"`
	CompletedAt   string                `json:"completed_at,omitempty"`
	Findings      []memoryReviewFinding `json:"findings,omitempty"`
}

type memoryReviewScope struct {
	Hours      int    `json:"hours"`
	Collection string `json:"collection,omitempty"`
	Client     string `json:"client,omitempty"`
	Limit      int    `json:"limit"`
}

type memoryReviewFinding struct {
	ID             string `json:"id"`
	RunID          string `json:"run_id"`
	Category       string `json:"category"`
	Severity       string `json:"severity"`
	TrajectoryID   string `json:"trajectory_id,omitempty"`
	Summary        string `json:"summary"`
	Evidence       string `json:"evidence,omitempty"`
	Recommendation string `json:"recommendation,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type memoryReviewLLMResponse struct {
	Summary  string `json:"summary"`
	Findings []struct {
		Category       string `json:"category"`
		Severity       string `json:"severity"`
		TrajectoryID   string `json:"trajectory_id"`
		Summary        string `json:"summary"`
		Evidence       string `json:"evidence"`
		Recommendation string `json:"recommendation"`
	} `json:"findings"`
}

func memoryReviewRunHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		if err := ensureMemoryReviewSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review schema unavailable"})
		}
		var req memoryReviewRunRequest
		if len(c.Body()) > 0 {
			if err := c.BodyParser(&req); err != nil {
				return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
			}
		}
		scope := normalizeMemoryReviewScope(req)
		traces, err := loadReviewTrajectories(c.UserContext(), cfg, scope)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "agent trajectory query failed"})
		}
		prompt := buildMemoryReviewPrompt(traces)
		if req.DryRun {
			return c.JSON(fiber.Map{
				"dry_run":            true,
				"scope":              scope,
				"trajectory_count":   len(traces),
				"prompt_preview":     prompt,
				"findings_persisted": 0,
			})
		}
		run := memoryReviewRun{
			ID:            uuid.New().String(),
			Status:        "running",
			Scope:         scope,
			PromptPreview: prompt,
			CreatedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := insertMemoryReviewRun(c.UserContext(), cfg.DB, run); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review run create failed"})
		}
		run = executeMemoryReview(c.UserContext(), cfg, run, traces, prompt)
		if err := updateMemoryReviewRun(c.UserContext(), cfg.DB, run); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review run update failed"})
		}
		if run.Status == "completed" {
			if err := insertMemoryReviewFindings(c.UserContext(), cfg.DB, run.Findings); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "memory review findings create failed"})
			}
			if _, err := createMemoryScaffoldProposalsFromReview(c.UserContext(), cfg.DB, run); err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "memory scaffold proposal create failed"})
			}
		}
		status := fiber.StatusOK
		if run.Status == "failed" {
			status = fiber.StatusServiceUnavailable
		}
		return c.Status(status).JSON(run)
	}
}

func memoryReviewListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if err := ensureMemoryReviewSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review schema unavailable"})
		}
		limit := c.QueryInt("limit", 50)
		offset := c.QueryInt("offset", 0)
		runs, err := listMemoryReviewRuns(c.UserContext(), cfg.DB, limit, offset)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review list failed"})
		}
		return c.JSON(fiber.Map{"limit": normalizePageLimit(limit), "offset": maxInt(offset, 0), "runs": runs})
	}
}

func memoryReviewDetailHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "database unavailable"})
		}
		if err := ensureMemoryReviewSchema(c.UserContext(), cfg.DB); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review schema unavailable"})
		}
		run, err := getMemoryReviewRun(c.UserContext(), cfg.DB, c.Params("id"))
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "memory review run not found"})
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review detail failed"})
		}
		findings, err := getMemoryReviewFindings(c.UserContext(), cfg.DB, run.ID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory review findings failed"})
		}
		run.Findings = findings
		return c.JSON(run)
	}
}

func normalizeMemoryReviewScope(req memoryReviewRunRequest) memoryReviewScope {
	hours := req.Hours
	if hours != 1 && hours != 24 && hours != 168 && hours != 720 {
		hours = 24
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	return memoryReviewScope{Hours: hours, Collection: strings.TrimSpace(req.Collection), Client: strings.TrimSpace(req.Client), Limit: limit}
}

func loadReviewTrajectories(ctx context.Context, cfg APIConfig, scope memoryReviewScope) ([]trajectory.Trajectory, error) {
	rows, err := cfg.MCPAuditReadModel.EventsForTrajectories(ctx, audit.EventFilter{
		Since:      time.Now().Add(-time.Duration(scope.Hours) * time.Hour),
		Client:     scope.Client,
		Collection: scope.Collection,
		Limit:      20000,
	})
	if err != nil {
		return nil, err
	}
	traces := trajectory.Build(rows, true)
	if len(traces) > scope.Limit {
		traces = traces[:scope.Limit]
	}
	return traces, nil
}

func buildMemoryReviewPrompt(traces []trajectory.Trajectory) string {
	type eventView struct {
		TS           string `json:"ts"`
		Tool         string `json:"tool"`
		Outcome      string `json:"outcome"`
		ResultCount  int    `json:"result_count"`
		ZeroResult   bool   `json:"zero_result"`
		LatencyMS    int64  `json:"latency_ms"`
		ErrorMessage string `json:"error_message,omitempty"`
	}
	type traceView struct {
		ID         string      `json:"id"`
		Collection string      `json:"collection,omitempty"`
		ClientName string      `json:"client_name,omitempty"`
		EventCount int         `json:"event_count"`
		Counters   any         `json:"counters"`
		Events     []eventView `json:"events"`
	}
	views := make([]traceView, 0, len(traces))
	for _, tr := range traces {
		view := traceView{ID: tr.ID, Collection: tr.Collection, ClientName: tr.ClientName, EventCount: tr.EventCount, Counters: tr.Counters}
		for _, event := range tr.Events {
			view.Events = append(view.Events, eventView{
				TS:           event.TS,
				Tool:         event.Tool,
				Outcome:      event.Outcome,
				ResultCount:  event.ResultCount,
				ZeroResult:   event.ZeroResult,
				LatencyMS:    event.LatencyMS,
				ErrorMessage: truncateReviewText(event.ErrorMessage, 180),
			})
		}
		views = append(views, view)
	}
	payload, _ := json.MarshalIndent(views, "", "  ")
	return `You are reviewing AI-agent memory behavior from sanitized Levara MCP audit trajectories.

Classify concrete findings using only these categories:
- missed_recall
- blind_save
- duplicate_save
- wrong_room_hall
- noisy_memory
- useful_memory_pattern
- scaffold_recommendation

Return strict JSON:
{"summary":"short operator summary","findings":[{"category":"blind_save","severity":"low|medium|high","trajectory_id":"trace:...","summary":"...","evidence":"...","recommendation":"..."}]}

Rules:
- Do not recommend automatic edits.
- Prefer specific trajectory evidence.
- Raw tool args, query text and memory text are intentionally omitted; do not invent them.
- Mention privacy/scaffold gaps only when supported by counters/events.

Sanitized trajectories:
` + string(payload)
}

func executeMemoryReview(ctx context.Context, cfg APIConfig, run memoryReviewRun, traces []trajectory.Trajectory, prompt string) memoryReviewRun {
	run.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	model := strings.TrimSpace(os.Getenv("LLM_MODEL"))
	if cfg.LLMProvider == nil || model == "" {
		run.Status = "failed"
		run.Error = "LLM provider/model unavailable"
		return run
	}
	resp, err := cfg.LLMProvider.ChatCompletion(ctx, llm.CompletionRequest{
		Model:       model,
		Temperature: 0,
		MaxTokens:   1500,
		Messages: []llm.Message{
			{Role: "system", Content: "You produce privacy-safe JSON memory behavior reviews for Levara operators."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		return run
	}
	parsed, err := parseMemoryReviewFindings(resp.Content)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		return run
	}
	run.Status = "completed"
	run.Summary = truncateReviewText(parsed.Summary, 1000)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	knownTrajectories := map[string]bool{}
	for _, tr := range traces {
		knownTrajectories[tr.ID] = true
	}
	for _, f := range parsed.Findings {
		trajectoryID := strings.TrimSpace(f.TrajectoryID)
		if trajectoryID != "" && !knownTrajectories[trajectoryID] {
			trajectoryID = ""
		}
		run.Findings = append(run.Findings, memoryReviewFinding{
			ID:             uuid.New().String(),
			RunID:          run.ID,
			Category:       normalizeReviewCategory(f.Category),
			Severity:       normalizeReviewSeverity(f.Severity),
			TrajectoryID:   trajectoryID,
			Summary:        truncateReviewText(f.Summary, 1000),
			Evidence:       truncateReviewText(f.Evidence, 1200),
			Recommendation: truncateReviewText(f.Recommendation, 1200),
			CreatedAt:      now,
		})
	}
	return run
}

func parseMemoryReviewFindings(raw string) (memoryReviewLLMResponse, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	var parsed memoryReviewLLMResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return parsed, fmt.Errorf("memory review LLM response is not valid JSON: %w", err)
	}
	for _, finding := range parsed.Findings {
		if strings.TrimSpace(finding.Summary) == "" {
			return parsed, errors.New("memory review finding summary is required")
		}
	}
	return parsed, nil
}

func normalizeReviewCategory(category string) string {
	switch strings.TrimSpace(category) {
	case "missed_recall", "blind_save", "duplicate_save", "wrong_room_hall", "noisy_memory", "useful_memory_pattern", "scaffold_recommendation":
		return category
	default:
		return "scaffold_recommendation"
	}
}

func normalizeReviewSeverity(severity string) string {
	switch strings.TrimSpace(strings.ToLower(severity)) {
	case "low", "medium", "high":
		return strings.ToLower(severity)
	default:
		return "medium"
	}
}

func ensureMemoryReviewSchema(ctx context.Context, db *sql.DB) error {
	for _, stmt := range strings.Split(memoryReviewDDL, ";") {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, Q(stmt)); err != nil {
			return err
		}
	}
	return nil
}

func insertMemoryReviewRun(ctx context.Context, db *sql.DB, run memoryReviewRun) error {
	scope, _ := json.Marshal(run.Scope)
	_, err := db.ExecContext(ctx, Q(`INSERT INTO memory_review_runs (id,status,scope_json,summary,error,prompt_preview,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?)`),
		run.ID, run.Status, string(scope), run.Summary, run.Error, run.PromptPreview, run.CreatedAt, run.CompletedAt)
	return err
}

func updateMemoryReviewRun(ctx context.Context, db *sql.DB, run memoryReviewRun) error {
	_, err := db.ExecContext(ctx, Q(`UPDATE memory_review_runs SET status=?, summary=?, error=?, completed_at=? WHERE id=?`),
		run.Status, run.Summary, run.Error, run.CompletedAt, run.ID)
	return err
}

func insertMemoryReviewFindings(ctx context.Context, db *sql.DB, findings []memoryReviewFinding) error {
	for _, f := range findings {
		if _, err := db.ExecContext(ctx, Q(`INSERT INTO memory_review_findings (id,run_id,category,severity,trajectory_id,summary,evidence,recommendation,created_at) VALUES (?,?,?,?,?,?,?,?,?)`),
			f.ID, f.RunID, f.Category, f.Severity, f.TrajectoryID, f.Summary, f.Evidence, f.Recommendation, f.CreatedAt); err != nil {
			return err
		}
	}
	return nil
}

func listMemoryReviewRuns(ctx context.Context, db *sql.DB, limit, offset int) ([]memoryReviewRun, error) {
	limit = normalizePageLimit(limit)
	offset = maxInt(offset, 0)
	rows, err := db.QueryContext(ctx, Q(`SELECT id,status,scope_json,summary,error,prompt_preview,created_at,completed_at FROM memory_review_runs ORDER BY created_at DESC LIMIT ? OFFSET ?`), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memoryReviewRun{}
	for rows.Next() {
		run, err := scanMemoryReviewRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func getMemoryReviewRun(ctx context.Context, db *sql.DB, id string) (memoryReviewRun, error) {
	row := db.QueryRowContext(ctx, Q(`SELECT id,status,scope_json,summary,error,prompt_preview,created_at,completed_at FROM memory_review_runs WHERE id=?`), id)
	return scanMemoryReviewRun(row)
}

type memoryReviewRunScanner interface {
	Scan(dest ...any) error
}

func scanMemoryReviewRun(scanner memoryReviewRunScanner) (memoryReviewRun, error) {
	var run memoryReviewRun
	var scopeJSON string
	if err := scanner.Scan(&run.ID, &run.Status, &scopeJSON, &run.Summary, &run.Error, &run.PromptPreview, &run.CreatedAt, &run.CompletedAt); err != nil {
		return run, err
	}
	_ = json.Unmarshal([]byte(scopeJSON), &run.Scope)
	return run, nil
}

func getMemoryReviewFindings(ctx context.Context, db *sql.DB, runID string) ([]memoryReviewFinding, error) {
	rows, err := db.QueryContext(ctx, Q(`SELECT id,run_id,category,severity,trajectory_id,summary,evidence,recommendation,created_at FROM memory_review_findings WHERE run_id=? ORDER BY created_at ASC, id ASC`), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []memoryReviewFinding{}
	for rows.Next() {
		var f memoryReviewFinding
		if err := rows.Scan(&f.ID, &f.RunID, &f.Category, &f.Severity, &f.TrajectoryID, &f.Summary, &f.Evidence, &f.Recommendation, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func normalizePageLimit(limit int) int {
	if limit <= 0 || limit > 100 {
		return 50
	}
	return limit
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func truncateReviewText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}
