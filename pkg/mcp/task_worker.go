package mcp

// task_worker.go — autonomous in-process task worker (backlog B2).
//
// Opt-in via LEVARA_TASK_WORKER=1. The worker claims claimable steps of
// tasks whose authority_json carries {"auto_run": true} and executes them
// against a tool allowlist. It uses the SAME primitives as external MCP
// hosts — ToolTaskStep claim/pass/fail via the pkg/mcp public surface —
// so there is no second write path: leases, attempts, version CAS and
// idempotency behave identically whether a human-driven agent or this
// worker advances a step.
//
// Policy (authority_json of the task):
//
//	{
//	  "auto_run": true,
//	  "allowed_tools": ["search", "recall_memory"],  // default: read-only set
//	  "max_concurrent_steps": 2,                     // per task, default 1
//	  "step_deadline_seconds": 900,                  // default 900, max 3600
//	  "max_step_attempts": 3                         // default 3
//	}
//
// Deadlock safety: if no claimable step exists for an auto_run task but the
// task is not terminal, the worker counts a scheduler round; after
// maxStalledRounds with zero progress and every pending step either blocked
// by dependencies or leased, the task is marked blocked via a task_blockers
// row (reason "scheduler deadlock") — the same signal a human host would
// leave, surfaced in the WebUI blockers panel.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// TaskExecutor executes a single claimed step. The default executor only
// marks the step passed with a synthetic receipt-equivalent observation —
// real tool execution is injected by tests / future tool runners through
// this interface.
type TaskStepExecutor interface {
	ExecuteStep(ctx context.Context, taskID, stepID, description string, deadline time.Time) (passed bool, observation string, err error)
}

// TaskWorkerConfig bounds the worker loop.
type TaskWorkerConfig struct {
	PollInterval     time.Duration // default 5s
	LeaseSeconds     int           // lease duration claimed per step, default 900
	MaxInFlight      int           // global in-flight cap, default 8
	MaxStalledRounds int           // rounds without progress before deadlock marker, default 4
	ActorID          string        // lease actor, default "levara:task-worker"
}

// TaskWorker drives auto_run tasks.
type TaskWorker struct {
	deps     Deps
	exec     TaskStepExecutor
	cfg      TaskWorkerConfig
	mu       sync.Mutex
	inFlight map[string]int // taskID -> in-flight step count
	stalled  map[string]int // taskID -> consecutive stalled rounds
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	wg       sync.WaitGroup
}

// NewTaskWorker builds a worker over the shared MCP Deps (same DB handle as
// the tool surface — one connection pool, one write path).
func NewTaskWorker(deps Deps, exec TaskStepExecutor, cfg TaskWorkerConfig) *TaskWorker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.LeaseSeconds < 30 {
		cfg.LeaseSeconds = 30
	}
	if cfg.LeaseSeconds > taskMaxLeaseSeconds {
		cfg.LeaseSeconds = taskMaxLeaseSeconds
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 8
	}
	if cfg.MaxStalledRounds <= 0 {
		cfg.MaxStalledRounds = 4
	}
	if cfg.ActorID == "" {
		cfg.ActorID = "levara:task-worker"
	}
	return &TaskWorker{
		deps:     deps,
		exec:     exec,
		cfg:      cfg,
		inFlight: map[string]int{},
		stalled:  map[string]int{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start launches the polling loop. Non-blocking.
func (w *TaskWorker) Start(ctx context.Context) {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(w.done)
		ticker := time.NewTicker(w.cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
				w.round(ctx)
			}
		}
	}()
}

// Stop drains in-flight steps and halts the loop. Mid-flight leases simply
// expire (kill-switch semantics) — the task stays consistent.
func (w *TaskWorker) Stop() {
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-w.done:
	case <-time.After(30 * time.Second):
		log.Printf("task worker: stop timeout, in-flight leases will expire naturally")
	}
}

// ── scheduling round ──

// workerTaskPolicy is the auto-run policy parsed from authority_json.
type workerTaskPolicy struct {
	AutoRun         bool     `json:"auto_run"`
	AllowedTools    []string `json:"allowed_tools"`
	MaxConcurrent   int      `json:"max_concurrent_steps"`
	DeadlineSeconds int      `json:"step_deadline_seconds"`
	MaxStepAttempts int      `json:"max_step_attempts"`
}

func (w *TaskWorker) round(ctx context.Context) {
	db := w.deps.DB()
	if db == nil {
		return
	}
	// Candidates: non-terminal tasks with auto_run authority that have a
	// pending step whose dependencies are all passed. Pure read — the actual
	// claim is the atomic ToolTaskStep CAS, so races between workers or with
	// MCP hosts resolve safely (loser gets "step already leased").
	rows, err := db.QueryContext(ctx, w.deps.Q(`
		SELECT t.id, t.owner_id, t.authority_json, s.id, s.description, s.attempts, s.dependencies_json
		FROM tasks t
		JOIN task_steps s ON s.task_id = t.id
		  AND (s.status = 'pending'
		       OR (s.status = 'active' AND EXISTS (
		             SELECT 1 FROM task_leases l
		             WHERE l.task_id = s.task_id AND l.step_id = s.id
		               AND l.expires_at <= CURRENT_TIMESTAMP)))
		WHERE t.status NOT IN ('completed','cancelled')
		  AND t.authority_json LIKE '%"auto_run": true%'
		ORDER BY s.task_id, s.position`))
	if err != nil {
		log.Printf("task worker: candidate query: %v", err)
		return
	}
	// Dependency status map for this round (portable: no JSON operators in
	// SQL — the runtime must stay PG/SQLite agnostic).
	depStatus := map[string]string{}
	drows, derr := db.QueryContext(ctx, w.deps.Q(`SELECT task_id, id, status FROM task_steps`))
	if derr == nil {
		for drows.Next() {
			var taskID, stepID, status string
			if drows.Scan(&taskID, &stepID, &status) == nil {
				depStatus[taskID+"\x00"+stepID] = status
			}
		}
		drows.Close()
	}

	type candidate struct {
		taskID, ownerID, taskAuth, stepID, description string
		attempts                                       int
		dependencies                                   []string
	}
	var candidates []candidate
	seenTasks := map[string]bool{}
	for rows.Next() {
		var c candidate
		var depsJSON string
		if err := rows.Scan(&c.taskID, &c.ownerID, &c.taskAuth, &c.stepID, &c.description, &c.attempts, &depsJSON); err == nil {
			_ = json.Unmarshal([]byte(depsJSON), &c.dependencies)
			blocked := false
			for _, d := range c.dependencies {
				if depStatus[c.taskID+"\x00"+d] != "passed" {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
			candidates = append(candidates, c)
			seenTasks[c.taskID] = true
		}
	}
	rows.Close()

	// Deadlock accounting for auto_run tasks with no claimable candidates.
	w.markDeadlocks(ctx, seenTasks)
	if len(candidates) == 0 {
		return
	}

	// Group per task, respect per-task concurrency and global in-flight cap.
	claimed := 0
	for _, c := range candidates {
		if claimed >= w.cfg.MaxInFlight {
			return
		}
		policy := parseWorkerPolicy(c.taskAuth)
		if !policy.autoRunAllowed() {
			continue
		}
		if w.currentInFlight(c.taskID) >= policy.maxConcurrent(w.cfg) {
			continue
		}
		if c.attempts >= policy.maxAttempts() {
			continue
		}
		if w.tryClaimAndRun(ctx, c.taskID, c.ownerID, c.stepID, c.description, policy) {
			claimed++
		}
	}
}

// markDeadlocks flags auto_run tasks that had zero claimable steps this round
// yet are neither completed nor fully blocked: every pending step is behind
// unmet dependencies. N consecutive stalled rounds → blocker row (deduped).
func (w *TaskWorker) markDeadlocks(ctx context.Context, claimableTasks map[string]bool) {
	db := w.deps.DB()
	rows, err := db.QueryContext(ctx, w.deps.Q(`
		SELECT DISTINCT t.id FROM tasks t
		JOIN task_steps s ON s.task_id = t.id
		WHERE t.status NOT IN ('completed','cancelled')
		  AND t.authority_json LIKE '%"auto_run": true%'
		  AND s.status IN ('pending','active')`))
	if err != nil {
		return
	}
	defer rows.Close()
	var autoRunTasks []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			autoRunTasks = append(autoRunTasks, id)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range autoRunTasks {
		if claimableTasks[id] {
			delete(w.stalled, id)
			continue
		}
		w.stalled[id]++
		if w.stalled[id] >= w.cfg.MaxStalledRounds {
			w.stalled[id] = 0
			var n int
			_ = db.QueryRowContext(ctx, w.deps.Q(
				`SELECT COUNT(*) FROM task_blockers WHERE task_id=$1 AND reason=$2 AND status='active'`),
				id, "scheduler deadlock").Scan(&n)
			if n == 0 {
				_, _ = db.ExecContext(ctx, w.deps.Q(
					`INSERT INTO task_blockers (id, task_id, reason, required_decision, status)
					 VALUES ($1,$2,$3,$4,'active')`),
					newWorkerID(), id, "scheduler deadlock",
					"unblock dependencies or run steps manually via task_step")
				_ = taskEvent(ctx, db, w.deps.Q, id, w.cfg.ActorID, "blocker_added",
					map[string]any{"reason": "scheduler deadlock"})
				log.Printf("task worker: deadlock marker on task %s", id)
			}
		}
	}
}

// tryClaimAndRun claims the step through the SAME ToolTaskStep CAS path an
// external MCP host uses, then executes and pass/fails it.
// ownerCtx returns a context carrying the task owner's identity: the worker
// advances steps on the owner's behalf (the tasks lookup is owner-scoped).
func ownerCtx(ctx context.Context, ownerID string) context.Context {
	if ownerID == "" {
		return ctx
	}
	return context.WithValue(ctx, UserIDKey, ownerID)
}

func (w *TaskWorker) tryClaimAndRun(ctx context.Context, taskID, ownerID, stepID, description string, policy workerTaskPolicy) bool {
	// Snapshot version for the CAS (read via deps DB; the claim itself is
	// atomic — version conflict means someone else moved the task first).
	var version int
	if err := w.deps.DB().QueryRowContext(ctx, w.deps.Q(
		`SELECT version FROM tasks WHERE id=$1`), taskID).Scan(&version); err != nil {
		return false
	}
	claimRes := ToolTaskStep(ownerCtx(ctx, ownerID), w.deps, map[string]any{
		"task_id": taskID, "step_id": stepID, "action": "claim",
		"actor_id": w.cfg.ActorID, "base_version": version,
		"lease_seconds": w.cfg.LeaseSeconds,
	})
	if claimRes.IsError {
		// Already leased / version moved on / not visible — not an error.
		return false
	}

	w.mu.Lock()
	w.inFlight[taskID]++
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			w.inFlight[taskID]--
			if w.inFlight[taskID] <= 0 {
				delete(w.inFlight, taskID)
			}
			w.mu.Unlock()
		}()

		deadline := time.Now().Add(time.Duration(policy.deadlineSeconds(w.cfg)) * time.Second)
		execCtx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()

		passed, observation, execErr := w.exec.ExecuteStep(execCtx, taskID, stepID, description, deadline)

		bg := context.Background()
		var v int
		if err := w.deps.DB().QueryRowContext(bg, w.deps.Q(
			`SELECT version FROM tasks WHERE id=$1`), taskID).Scan(&v); err != nil {
			return
		}
		var attempts int
		_ = w.deps.DB().QueryRowContext(bg, w.deps.Q(
			`SELECT attempts FROM task_steps WHERE id=$1 AND task_id=$2`), stepID, taskID).Scan(&attempts)

		action := "pass"
		if !passed {
			if attempts < policy.maxAttempts() {
				// Retry policy owned by the worker: put the step back to
				// pending (release drops our lease); next round re-claims.
				action = "release"
			} else {
				action = "fail"
			}
		}
		ToolTaskStep(ownerCtx(bg, ownerID), w.deps, map[string]any{
			"task_id": taskID, "step_id": stepID, "action": action,
			"actor_id": w.cfg.ActorID, "base_version": v,
		})
		if action == "release" {
			_ = taskEvent(bg, w.deps.DB(), w.deps.Q, taskID,
				w.cfg.ActorID, "step_retry_scheduled",
				map[string]any{"step_id": stepID, "attempt": attempts,
					"error": truncateWorker(errString(execErr), 300)})
		}
		if action == "fail" {
			w.recordStepExhausted(bg, taskID, stepID, attempts)
		}
		if execErr != nil {
			_ = taskEvent(context.Background(), w.deps.DB(), w.deps.Q, taskID,
				w.cfg.ActorID, "step_execution_error",
				map[string]any{"step_id": stepID, "error": truncateWorker(execErr.Error(), 300)})
		}
		// Receipts come from the executor via ToolTaskReceipt; the worker
		// records an observation event so the WebUI shows what happened.
		_ = taskEvent(context.Background(), w.deps.DB(), w.deps.Q, taskID,
			w.cfg.ActorID, "step_executed",
			map[string]any{"step_id": stepID, "passed": passed,
				"observation": truncateWorker(observation, 300)})
	}()
	return true
}

func (w *TaskWorker) currentInFlight(taskID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlight[taskID]
}

// recordStepExhausted flags a step that hit the attempts ceiling (deduped).
func (w *TaskWorker) recordStepExhausted(ctx context.Context, taskID, stepID string, attempts int) {
	reason := fmt.Sprintf("step %s exceeded max attempts (%d)", truncateWorker(stepID, 20), attempts)
	var n int
	_ = w.deps.DB().QueryRowContext(ctx, w.deps.Q(
		`SELECT COUNT(*) FROM task_blockers WHERE task_id=$1 AND reason=$2 AND status='active'`),
		taskID, reason).Scan(&n)
	if n == 0 {
		_, _ = w.deps.DB().ExecContext(ctx, w.deps.Q(
			`INSERT INTO task_blockers (id, task_id, reason, required_decision, status)
			 VALUES ($1,$2,$3,$4,'active')`),
			newWorkerID(), taskID, reason, "retry or resolve manually")
		_ = taskEvent(ctx, w.deps.DB(), w.deps.Q, taskID, w.cfg.ActorID, "blocker_added",
			map[string]any{"reason": reason})
	}
}

// ── policy helpers ──

func parseWorkerPolicy(authorityJSON string) workerTaskPolicy {
	var p workerTaskPolicy
	_ = json.Unmarshal([]byte(authorityJSON), &p)
	return p
}

func (p workerTaskPolicy) autoRunAllowed() bool { return p.AutoRun }

func (p workerTaskPolicy) maxConcurrent(cfg TaskWorkerConfig) int {
	if p.MaxConcurrent <= 0 {
		return 1
	}
	return p.MaxConcurrent
}

func (p workerTaskPolicy) deadlineSeconds(cfg TaskWorkerConfig) int {
	if p.DeadlineSeconds <= 0 {
		return cfg.LeaseSeconds
	}
	if p.DeadlineSeconds > taskMaxLeaseSeconds {
		return taskMaxLeaseSeconds
	}
	return p.DeadlineSeconds
}

func (p workerTaskPolicy) maxAttempts() int {
	if p.MaxStepAttempts <= 0 {
		return 3
	}
	return p.MaxStepAttempts
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func truncateWorker(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "…"
}

// LoggingStepExecutor is the default executor: it passes the step with a
// synthetic observation. It exists so the scheduling/lease/deadlock
// machinery is end-to-end testable and observable; real tool execution is
// provided by replacing this type (policy enforcement of allowed_tools
// happens in the concrete executor).
// NewLoggingStepExecutor returns the default no-op executor.
func NewLoggingStepExecutor() TaskStepExecutor { return LoggingStepExecutor{} }

// LoggingStepExecutor is the default executor: it passes the step with a
// synthetic observation. It exists so the scheduling/lease/deadlock
// machinery is end-to-end testable and observable; real tool execution is
// provided by replacing this type (policy enforcement of allowed_tools
// happens in the concrete executor).
type LoggingStepExecutor struct{}

// ExecuteStep implements TaskStepExecutor.
func (LoggingStepExecutor) ExecuteStep(ctx context.Context, taskID, stepID, description string, deadline time.Time) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	log.Printf("task worker: executed step %s (task %s): %s", stepID, taskID, description)
	return true, "worker executed step: " + description, nil
}

// newWorkerID returns a collision-safe id for worker-created rows.
func newWorkerID() string {
	return fmt.Sprintf("w-%d", time.Now().UnixNano())
}
