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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// RegisterTaskReadAPI mounts the read-only task endpoints. No-op without a
// live DB (the tables do not exist in embedded mode).
func RegisterTaskReadAPI(app fiber.Router, cfg APIConfig) {
	if cfg.DB == nil {
		return
	}
	app.Get("/tasks", taskReadScope(cfg), taskListHandler(cfg))
	app.Get("/tasks/:taskId", taskReadScope(cfg), taskDetailHandler(cfg))
}

// Task ownership is user-global, matching MCP: own rows and explicit empty-owner
// shared rows. Tasks have no tenant column; selected tenants are admission facts,
// not a per-task origin namespace. Instance administrators gain no foreign-owner read.
type taskReadQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Fiber locals must not hold *sql.DB directly: request cleanup closes io.Closer values.
type taskReadState struct {
	db    taskReadQuerier
	owner string
	local bool
}

func taskReadScope(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		actor := uploadMetadataActor(c, cfg, ctx)
		if actor.Credential.ExpiresAt > 0 {
			expiresAt := time.Unix(actor.Credential.ExpiresAt, 0)
			if deadline, ok := ctx.Deadline(); !ok || expiresAt.Before(deadline) {
				var expiryCancel context.CancelFunc
				ctx, expiryCancel = context.WithDeadline(ctx, expiresAt)
				defer expiryCancel()
			}
		}
		local := actor.TrustedLocal && actor.UserID == "" && actor.TenantID == ""
		state := &taskReadState{owner: actor.UserID, local: local}
		c.Locals("task_read_state", state)
		c.SetUserContext(ctx)
		if local {
			state.db = cfg.DB
			return c.Next()
		}
		if actor.UserID == "" || actor.Credential.Kind == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "authentication required"})
		}
		if !accesspkg.APIKeyAllows(actor.APIKeyPermissions, accesspkg.ActionRead) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "task read denied"})
		}
		policy := accesspkg.SQLPolicy{DB: cfg.DB, Q: Q, QA: QArgs}
		tx, locked, release, err := policy.BeginTransferFenceTx(ctx, GetDBProvider() == DBSQLite)
		if err != nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "task authorization unavailable"})
		}
		if err := locked.RecheckCredential(ctx, actor.UserID, actor.Credential.Kind, actor.Credential.KeyID, actor.APIKeyPermissions, actor.Credential.SessionID, actor.Credential.Epoch, actor.Credential.IssuedAt, actor.Credential.ExpiresAt); err != nil {
			release()
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "credential revoked"})
		}
		if actor.TenantID != "" {
			member, err := locked.IsTenantMember(ctx, actor.UserID, actor.TenantID)
			if err != nil || !member {
				release()
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "tenant access denied"})
			}
		}
		state.db = tx
		if err := c.Next(); err != nil {
			release()
			return err
		}
		if c.Response().StatusCode() >= 400 {
			release()
			return nil
		}
		deadline, _ := ctx.Deadline()
		streamCtx, streamCancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		return sendFencedResponse(c, streamCtx, func() { release(); streamCancel() })
	}
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
		state := c.Locals("task_read_state").(*taskReadState)
		db, owner, local := state.db, state.owner, state.local
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
					COUNT(*) FILTER (WHERE status IN ('active', 'claimed')) AS in_progress,
					COUNT(*) FILTER (WHERE status='passed') AS passed,
					COUNT(*) FILTER (WHERE status='failed') AS failed
				FROM task_steps GROUP BY task_id
			) sc ON sc.task_id = t.id
			LEFT JOIN (
				SELECT task_id, COUNT(*) AS blockers FROM task_blockers WHERE status='active' GROUP BY task_id
			) b ON b.task_id = t.id
			WHERE ($1 = '' OR t.status = $1) AND ($2 = '' OR t.collection_name = $2)
				AND ($4 OR t.owner_id = $5 OR t.owner_id = '')
			ORDER BY t.updated_at DESC
			LIMIT $3`, status, collection, limit, local, owner)
		rows, err := db.QueryContext(c.UserContext(), q, args...)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task list failed"})
		}
		defer rows.Close()

		tasks := []taskSummary{}
		for rows.Next() {
			var t taskSummary
			var created, updated, completed any
			var pending, inProg, passed, failed, blockers int
			if err := rows.Scan(&t.ID, &t.OwnerID, &t.Collection, &t.Room, &t.Objective, &t.Status,
				&t.RiskLevel, &created, &updated, &completed,
				&pending, &inProg, &passed, &failed, &blockers); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task scan failed"})
			}
			if err := setTaskReadTimes(&t, created, updated, completed); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task timestamp failed"})
			}
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
		ctx := c.UserContext()
		state := c.Locals("task_read_state").(*taskReadState)
		db, owner, local := state.db, state.owner, state.local

		var v taskDetailView
		var created, updated, completed any
		err := db.QueryRowContext(ctx,
			Q(`SELECT id, owner_id, collection_name, room, objective, status, risk_level,
				created_at, updated_at, completed_at
			FROM tasks WHERE id = $1 AND ($2 OR owner_id = $3 OR owner_id = '')`), taskID, local, owner).Scan(
			&v.ID, &v.OwnerID, &v.Collection, &v.Room, &v.Objective, &v.Status,
			&v.RiskLevel, &created, &updated, &completed)
		if errors.Is(err, sql.ErrNoRows) {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "task not found"})
		}
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task detail failed"})
		}
		if err := setTaskReadTimes(&v.taskSummary, created, updated, completed); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task timestamp failed"})
		}

		v.Criteria = []map[string]any{}
		rows, err := db.QueryContext(ctx,
			Q(`SELECT id, description, required, verification_json FROM task_criteria WHERE task_id = $1 ORDER BY id`), taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task criteria failed"})
		}
		defer rows.Close()
		for rows.Next() {
			var id, desc, verJSON string
			var required bool
			if err := rows.Scan(&id, &desc, &required, &verJSON); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task criteria scan failed"})
			}
			v.Criteria = append(v.Criteria, map[string]any{
				"id": id, "description": desc, "required": required, "verification": jsonRaw(verJSON),
			})
		}
		if err := rows.Err(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task criteria iterate failed"})
		}
		rows.Close()

		v.Steps = []taskStepView{}
		v.StepCounts = map[string]int{"pending": 0, "claimed": 0, "passed": 0, "failed": 0}
		rows, err = db.QueryContext(ctx,
			Q(`SELECT s.id, s.description, s.status, s.required, s.attempts, s.position,
				l.actor_id, l.expires_at
			FROM task_steps s
			LEFT JOIN task_leases l ON l.task_id = s.task_id AND l.step_id = s.id
			WHERE s.task_id = $1 ORDER BY s.position, s.id`), taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task steps failed"})
		}
		defer rows.Close()
		now := time.Now().UTC()
		for rows.Next() {
			var step taskStepView
			var leasedBy *string
			var leaseRaw any
			if err := rows.Scan(&step.ID, &step.Description, &step.Status, &step.Required, &step.Attempts, &step.Position,
				&leasedBy, &leaseRaw); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task steps scan failed"})
			}
			leaseExpiry, err := taskReadTime(leaseRaw, true)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task lease timestamp failed"})
			}
			if leasedBy != nil && leaseExpiry != nil && leaseExpiry.After(now) {
				step.LeasedBy, step.LeaseExpiry = *leasedBy, leaseExpiry
			}
			status := step.Status
			if status == "active" {
				status = "claimed"
			}
			if _, ok := v.StepCounts[status]; ok {
				v.StepCounts[status]++
			}
			v.Steps = append(v.Steps, step)
		}
		if err := rows.Err(); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task steps iterate failed"})
		}
		rows.Close()

		v.Receipts, err = queryMaps(db, ctx, `
			SELECT id, receipt_type, status, observation, evidence_uri, exit_code, created_at
			FROM task_receipts WHERE task_id = $1 ORDER BY created_at DESC LIMIT 20`, taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task receipts failed"})
		}
		v.Checkpoints, err = queryMaps(db, ctx, `
			SELECT id, summary, next_action, workspace_revision, created_at
			FROM task_checkpoints WHERE task_id = $1 ORDER BY created_at DESC LIMIT 10`, taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task checkpoints failed"})
		}
		v.Blockers, err = queryMaps(db, ctx, `
			SELECT id, reason, required_decision, status, created_at, resolved_at
			FROM task_blockers WHERE task_id = $1 ORDER BY created_at DESC`, taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task blockers failed"})
		}
		for _, blocker := range v.Blockers {
			if blocker["status"] == "active" {
				v.BlockerCount++
			}
		}
		v.RecentEvents, err = queryMaps(db, ctx, `
			SELECT actor_id, event_type, payload_json, created_at
			FROM task_events WHERE task_id = $1 ORDER BY created_at DESC LIMIT 30`, taskID)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "task events failed"})
		}
		return c.JSON(v)
	}
}

// SQLite Task timestamps are TEXT, while PostgreSQL and legacy fixtures return time.Time.
func taskReadTime(value any, optional bool) (*time.Time, error) {
	if value == nil && optional {
		return nil, nil
	}
	if instant, ok := value.(time.Time); ok {
		instant = instant.UTC()
		return &instant, nil
	}
	text := timestampString(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07", "2006-01-02 15:04:05.999999999"} {
		if instant, err := time.Parse(layout, text); err == nil {
			instant = instant.UTC()
			return &instant, nil
		}
	}
	return nil, fmt.Errorf("invalid task timestamp: %T", value)
}

func setTaskReadTimes(task *taskSummary, created, updated, completed any) error {
	createdAt, err := taskReadTime(created, false)
	if err != nil {
		return err
	}
	updatedAt, err := taskReadTime(updated, false)
	if err != nil {
		return err
	}
	completedAt, err := taskReadTime(completed, true)
	if err != nil {
		return err
	}
	task.CreatedAt, task.UpdatedAt, task.CompletedAt = *createdAt, *updatedAt, completedAt
	return nil
}

// queryMaps runs a query and renders rows as generic maps for JSON.
func queryMaps(db taskReadQuerier, ctx context.Context, q string, args ...any) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, Q(q), args...)
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
			return nil, err
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
