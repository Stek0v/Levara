// tasks_read.go — Read-only REST surface over the Task Runtime tables
// (backlog B1: WebUI workflow, read-only alpha).
//
// Scope guard: this file intentionally contains ONLY GET handlers. The
// task lifecycle stays authoritative in the MCP task_* tools; the WebUI
// observes. Any write endpoint here is a design violation — mutations must
// go through task_receipt/task_step etc. so leases and idempotency keys
// cannot be bypassed.
package http

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
)

// RegisterTaskReadAPI mounts the read-only task endpoints. No-op without a
// live DB (the tables do not exist in embedded mode).
func RegisterTaskReadAPI(app fiber.Router, cfg APIConfig) {
	if cfg.DB == nil {
		return
	}
	app.Get("/tasks", taskListHandler(cfg))
	app.Get("/tasks/:taskId", taskDetailHandler(cfg))
}

// ── shapes ──

type taskSummary struct {
	ID           string         `json:"id"`
	OwnerID      string         `json:"owner_id"`
	Collection   string         `json:"collection_name"`
	Room         string         `json:"room"`
	Objective    string         `json:"objective"`
	Status       string         `json:"status"`
	RiskLevel    string         `json:"risk_level"`
	StepCounts   map[string]int `json:"step_counts"`
	BlockerCount int            `json:"blocker_count"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
}

type taskStepView struct {
	ID          string     `json:"id"`
	Description string     `json:"description"`
	Status      string     `json:"status"`
	Required    bool       `json:"required"`
	Attempts    int        `json:"attempts"`
	Position    int        `json:"position"`
	LeasedBy    string     `json:"leased_by,omitempty"`
	LeaseExpiry *time.Time `json:"lease_expires_at,omitempty"`
}

type taskDetailView struct {
	taskSummary
	Criteria     []map[string]any `json:"criteria"`
	Steps        []taskStepView   `json:"steps"`
	Receipts     []map[string]any `json:"receipts"`
	Checkpoints  []map[string]any `json:"checkpoints"`
	Blockers     []map[string]any `json:"blockers"`
	RecentEvents []map[string]any `json:"recent_events"`
}

// ── handlers ──

func taskListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 50)
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		status := c.Query("status")
		collection := c.Query("collection_name")

		q, args := QArgs(`SELECT t.id, t.owner_id, t.collection_name, t.room, t.objective, t.status, t.risk_level,
				t.created_at, t.updated_at, t.completed_at,
				COALESCE(sc.pending,0), COALESCE(sc.in_progress,0), COALESCE(sc.passed,0), COALESCE(sc.failed,0),
				COALESCE(b.blockers,0)
			FROM tasks t
			LEFT JOIN (
				SELECT task_id,
					COUNT(*) FILTER (WHERE status='pending') AS pending,
					COUNT(*) FILTER (WHERE status='claimed') AS in_progress,
					COUNT(*) FILTER (WHERE status='passed') AS passed,
					COUNT(*) FILTER (WHERE status='failed') AS failed
				FROM task_steps GROUP BY task_id
			) sc ON sc.task_id = t.id
			LEFT JOIN (
				SELECT task_id, COUNT(*) AS blockers FROM task_blockers WHERE status='active' GROUP BY task_id
			) b ON b.task_id = t.id
			WHERE ($1 = '' OR t.status = $1) AND ($2 = '' OR t.collection_name = $2)
			ORDER BY t.updated_at DESC
			LIMIT $3`, status, collection, limit)
		rows, err := cfg.DB.QueryContext(c.Context(), q, args...)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task list failed"})
		}
		defer rows.Close()

		tasks := []taskSummary{}
		for rows.Next() {
			var t taskSummary
			var completed *time.Time
			var pending, inProg, passed, failed, blockers int
			if err := rows.Scan(&t.ID, &t.OwnerID, &t.Collection, &t.Room, &t.Objective, &t.Status,
				&t.RiskLevel, &t.CreatedAt, &t.UpdatedAt, &completed,
				&pending, &inProg, &passed, &failed, &blockers); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task scan failed"})
			}
			t.CompletedAt = completed
			t.StepCounts = map[string]int{
				"pending": pending, "claimed": inProg, "passed": passed, "failed": failed,
			}
			t.BlockerCount = blockers
			tasks = append(tasks, t)
		}
		if err := rows.Err(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task iterate failed"})
		}
		return c.JSON(fiber.Map{"tasks": tasks, "count": len(tasks)})
	}
}

func taskDetailHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		taskID := c.Params("taskId")
		ctx := c.Context()

		var v taskDetailView
		var completed *time.Time
		err := cfg.DB.QueryRowContext(ctx,
			Q(`SELECT id, owner_id, collection_name, room, objective, status, risk_level,
				created_at, updated_at, completed_at
			FROM tasks WHERE id = $1`), taskID).Scan(
			&v.ID, &v.OwnerID, &v.Collection, &v.Room, &v.Objective, &v.Status,
			&v.RiskLevel, &v.CreatedAt, &v.UpdatedAt, &completed)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
		}
		v.CompletedAt = completed

		// Criteria.
		v.Criteria = []map[string]any{}
		if rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, description, required, verification_json FROM task_criteria WHERE task_id = $1 ORDER BY id`),
			taskID); err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, desc, verJSON string
				var required bool
				if rows.Scan(&id, &desc, &required, &verJSON) == nil {
					v.Criteria = append(v.Criteria, map[string]any{
						"id": id, "description": desc, "required": required,
						"verification": jsonRaw(verJSON),
					})
				}
			}
		}

		// Steps with live lease state.
		v.Steps = []taskStepView{}
		if rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT s.id, s.description, s.status, s.required, s.attempts, s.position,
				l.actor_id, l.expires_at
			FROM task_steps s
			LEFT JOIN task_leases l ON l.task_id = s.task_id AND l.step_id = s.id AND l.expires_at > NOW()
			WHERE s.task_id = $1 ORDER BY s.position, s.id`), taskID); err == nil {
			defer rows.Close()
			for rows.Next() {
				var s taskStepView
				var leasedBy *string
				var leaseExp *time.Time
				if rows.Scan(&s.ID, &s.Description, &s.Status, &s.Required, &s.Attempts, &s.Position,
					&leasedBy, &leaseExp) == nil {
					if leasedBy != nil {
						s.LeasedBy = *leasedBy
					}
					s.LeaseExpiry = leaseExp
					v.Steps = append(v.Steps, s)
				}
			}
		}

		// Receipts (latest 20).
		v.Receipts = []map[string]any{}
		v.Receipts, _ = queryMaps(cfg, ctx, `
			SELECT id, receipt_type, status, observation, evidence_uri, exit_code, created_at
			FROM task_receipts WHERE task_id = $1 ORDER BY created_at DESC LIMIT 20`, taskID)

		// Checkpoints (latest 10).
		v.Checkpoints = []map[string]any{}
		v.Checkpoints, _ = queryMaps(cfg, ctx, `
			SELECT id, summary, next_action, workspace_revision, created_at
			FROM task_checkpoints WHERE task_id = $1 ORDER BY created_at DESC LIMIT 10`, taskID)

		// Blockers (all).
		v.Blockers = []map[string]any{}
		v.Blockers, _ = queryMaps(cfg, ctx, `
			SELECT id, reason, required_decision, status, created_at, resolved_at
			FROM task_blockers WHERE task_id = $1 ORDER BY created_at DESC`, taskID)

		// Recent events (latest 30) — the audit tail.
		v.RecentEvents = []map[string]any{}
		v.RecentEvents, _ = queryMaps(cfg, ctx, `
			SELECT actor_id, event_type, payload_json, created_at
			FROM task_events WHERE task_id = $1 ORDER BY created_at DESC LIMIT 30`, taskID)

		return c.JSON(v)
	}
}

// queryMaps runs a query and renders rows as generic maps for JSON.
func queryMaps(cfg APIConfig, ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	rows, err := cfg.DB.QueryContext(ctx, Q(q), args...)
	if err != nil {
		return []map[string]any{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return []map[string]any{}, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		m := make(map[string]any, len(cols))
		for i, col := range cols {
			m[col] = normalizeCell(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func normalizeCell(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	default:
		return v
	}
}

func jsonRaw(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}
