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
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// TaskStepExecutor executes a claimed step and returns observed verification.
// Production uses NewTaskExecutor; tests may inject a bounded fake.
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
	deps      Deps
	exec      TaskStepExecutor
	cfg       TaskWorkerConfig
	mu        sync.Mutex
	inFlight  map[string]int // taskID -> in-flight step count
	stalled   map[string]int // taskID -> consecutive stalled rounds
	stop      chan struct{}
	stopOnce  sync.Once
	startOnce sync.Once
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewTaskWorker builds a worker over the shared MCP Deps (same DB handle as
// the tool surface — one connection pool, one write path).
func NewTaskWorker(deps Deps, exec TaskStepExecutor, cfg TaskWorkerConfig) *TaskWorker {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.LeaseSeconds <= 0 {
		cfg.LeaseSeconds = 900
	} else if cfg.LeaseSeconds < 30 {
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
	w.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		go func() {
			defer close(w.done)
			defer w.wg.Wait()
			defer cancel()
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
	})
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
		SELECT t.id, t.owner_id, t.authority_json, s.id, s.description, s.attempts, s.dependencies_json, s.status
		FROM tasks t
		JOIN task_steps s ON s.task_id = t.id
		  AND (s.status IN ('pending','failed')
		       OR (s.status = 'active' AND EXISTS (
		             SELECT 1 FROM task_leases l
		             WHERE l.task_id = s.task_id AND l.step_id = s.id
		               AND l.expires_at <= $1)))
		WHERE t.status NOT IN ('completed','cancelled')
		ORDER BY s.task_id, s.position`), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		log.Printf("task worker: candidate query: %v", err)
		return
	}
	type candidate struct {
		taskID, ownerID, taskAuth, stepID, description, status string
		attempts                                               int
		dependencies                                           []string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		var depsJSON string
		if err := rows.Scan(&c.taskID, &c.ownerID, &c.taskAuth, &c.stepID, &c.description, &c.attempts, &depsJSON, &c.status); err == nil {
			if !parseWorkerPolicy(c.taskAuth).autoRunAllowed() || json.Unmarshal([]byte(depsJSON), &c.dependencies) != nil {
				continue
			}
			candidates = append(candidates, c)
		} else {
			_ = rows.Close()
			return
		}
	}
	readErr := errors.Join(rows.Err(), rows.Close())
	if readErr != nil {
		return
	}
	// Release candidate rows before querying dependencies: a pool with one
	// connection must work too. JSON policy parsing stays dialect-independent.
	depStatus := map[string]string{}
	drows, err := db.QueryContext(ctx, w.deps.Q(`SELECT task_id, id, status FROM task_steps`))
	if err != nil {
		return
	}
	for drows.Next() {
		var taskID, stepID, status string
		if err := drows.Scan(&taskID, &stepID, &status); err != nil {
			_ = drows.Close()
			return
		}
		depStatus[taskID+"\x00"+stepID] = status
	}
	if err := errors.Join(drows.Err(), drows.Close()); err != nil {
		return
	}
	seenTasks := map[string]bool{}
	ready := candidates[:0]
	for _, c := range candidates {
		policy := parseWorkerPolicy(c.taskAuth)
		if c.attempts >= policy.maxAttempts() {
			// A process may die after its last lease expires or after failing
			// the step. Recover the blocker without reclaiming an extra attempt.
			if w.currentInFlight(c.taskID) == 0 {
				w.recordStepExhausted(ownerCtx(ctx, c.ownerID), c.taskID, c.stepID, c.attempts)
			}
			seenTasks[c.taskID] = true
			continue
		}
		if c.status == "failed" {
			continue
		}
		blocked := false
		for _, dep := range c.dependencies {
			if depStatus[c.taskID+"\x00"+dep] != "passed" {
				blocked = true
				break
			}
		}
		if !blocked {
			ready = append(ready, c)
			seenTasks[c.taskID] = true
		}
	}

	// Deadlock accounting for auto_run tasks with no claimable candidates.
	w.markDeadlocks(ctx, seenTasks)
	// Group per task, respect per-task concurrency and global in-flight cap.
	for _, c := range ready {
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
		w.tryClaimAndRun(ctx, c.taskID, c.ownerID, c.stepID, c.description, policy)
	}
}

// markDeadlocks flags auto_run tasks that had zero claimable steps this round
// yet are neither completed nor fully blocked: every pending step is behind
// unmet dependencies. N consecutive stalled rounds → blocker row (deduped).
func (w *TaskWorker) markDeadlocks(ctx context.Context, claimableTasks map[string]bool) {
	db := w.deps.DB()
	rows, err := db.QueryContext(ctx, w.deps.Q(`
		SELECT DISTINCT t.id, t.authority_json FROM tasks t
		JOIN task_steps s ON s.task_id = t.id
		WHERE t.status NOT IN ('completed','cancelled')
		  AND s.status IN ('pending','active')
		  AND NOT EXISTS (SELECT 1 FROM task_leases l WHERE l.task_id=t.id AND l.expires_at>$1)`), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return
	}
	var autoRunTasks []string
	for rows.Next() {
		var id, authority string
		if rows.Scan(&id, &authority) == nil && parseWorkerPolicy(authority).autoRunAllowed() {
			autoRunTasks = append(autoRunTasks, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range autoRunTasks {
		if claimableTasks[id] || w.inFlight[id] > 0 {
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
	if w.exec == nil || ctx.Err() != nil || !w.reserve(taskID, policy.maxConcurrent(w.cfg)) {
		return false
	}
	claimed := false
	defer func() {
		if !claimed {
			w.releaseReservation(taskID)
		}
	}()
	// A fresh actor identifies this lease attempt, never authorization. This
	// fences a late executor from a replacement claim by the same worker.
	actor := w.cfg.ActorID + ":" + newWorkerID()
	deadline := time.Now().Add(time.Duration(policy.deadlineSeconds(w.cfg)) * time.Second)
	// Finish execution before the lease ends, leaving a bounded window for
	// the version read and pass/release/fail transaction.
	completionLimit := time.Now().Add(time.Duration(w.cfg.LeaseSeconds)*time.Second - 5*time.Second)
	if deadline.After(completionLimit) {
		deadline = completionLimit
	}
	// Snapshot version for the CAS (read via deps DB; the claim itself is
	// atomic — version conflict means someone else moved the task first).
	var version int
	if err := w.deps.DB().QueryRowContext(ctx, w.deps.Q(
		`SELECT version FROM tasks WHERE id=$1`), taskID).Scan(&version); err != nil {
		return false
	}
	claimRes := ToolTaskStep(ownerCtx(ctx, ownerID), w.deps, map[string]any{
		"task_id": taskID, "step_id": stepID, "action": "claim",
		"actor_id": actor, "base_version": version,
		"lease_seconds": w.cfg.LeaseSeconds,
	})
	if claimRes.IsError {
		// Already leased / version moved on / not visible — not an error.
		return false
	}

	claimed = true
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer w.releaseReservation(taskID)

		execCtx, cancel := context.WithDeadline(ownerCtx(ctx, ownerID), deadline)
		defer cancel()
		deadline, _ = execCtx.Deadline()
		if execCtx.Err() != nil {
			return
		}

		execCtx = context.WithValue(execCtx, taskLeaseContextKey{}, taskLeaseIdentity{taskID, stepID, actor})
		passed, observation, execErr := w.exec.ExecuteStep(execCtx, taskID, stepID, description, deadline)
		execErr = errors.Join(execErr, execCtx.Err())
		passed = passed && execErr == nil
		if ctx.Err() != nil {
			// Shutdown abandons the lease; a later worker may reclaim on expiry.
			return
		}

		completionCtx, stopCompletion := context.WithTimeout(ctx, 5*time.Second)
		defer stopCompletion()
		var v int
		if err := w.deps.DB().QueryRowContext(completionCtx, w.deps.Q(
			`SELECT version FROM tasks WHERE id=$1`), taskID).Scan(&v); err != nil {
			return
		}
		var attempts int
		if err := w.deps.DB().QueryRowContext(completionCtx, w.deps.Q(
			`SELECT attempts FROM task_steps WHERE id=$1 AND task_id=$2`), stepID, taskID).Scan(&attempts); err != nil {
			return
		}

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
		transition := ToolTaskStep(ownerCtx(completionCtx, ownerID), w.deps, map[string]any{
			"task_id": taskID, "step_id": stepID, "action": action,
			"actor_id": actor, "base_version": v,
		})
		if transition.IsError {
			_ = taskEvent(completionCtx, w.deps.DB(), w.deps.Q, taskID, actor, "step_transition_rejected",
				map[string]any{"step_id": stepID, "action": action})
			return
		}
		if action == "release" {
			_ = taskEvent(completionCtx, w.deps.DB(), w.deps.Q, taskID,
				actor, "step_retry_scheduled",
				map[string]any{"step_id": stepID, "attempt": attempts,
					"error": truncateWorker(errString(execErr), 300)})
		}
		if action == "fail" {
			w.recordStepExhausted(ownerCtx(completionCtx, ownerID), taskID, stepID, attempts)
		}
		if execErr != nil {
			_ = taskEvent(completionCtx, w.deps.DB(), w.deps.Q, taskID,
				actor, "step_execution_error",
				map[string]any{"step_id": stepID, "error": truncateWorker(execErr.Error(), 300)})
		}
		// Receipts come from the executor via ToolTaskReceipt; the worker
		// records an observation event so the WebUI shows what happened.
		_ = taskEvent(completionCtx, w.deps.DB(), w.deps.Q, taskID,
			actor, "step_executed",
			map[string]any{"step_id": stepID, "passed": passed,
				"observation": truncateWorker(observation, 300)})
	}()
	return true
}

func (w *TaskWorker) reserve(taskID string, taskLimit int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	total := 0
	for _, count := range w.inFlight {
		total += count
	}
	if total >= w.cfg.MaxInFlight || w.inFlight[taskID] >= taskLimit {
		return false
	}
	w.inFlight[taskID]++
	return true
}

func (w *TaskWorker) releaseReservation(taskID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inFlight[taskID]--
	if w.inFlight[taskID] <= 0 {
		delete(w.inFlight, taskID)
	}
}

func (w *TaskWorker) currentInFlight(taskID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlight[taskID]
}

// recordStepExhausted checkpoints the attempts ceiling through the public
// owner-scoped CAS path. It also recovers an expired final attempt after a
// restart without accepting an expired lease or altering the step's result.
func (w *TaskWorker) recordStepExhausted(ctx context.Context, taskID, stepID string, attempts int) {
	var version int
	if err := w.deps.DB().QueryRowContext(ctx, w.deps.Q(`
        SELECT t.version FROM tasks t JOIN task_steps s ON s.task_id=t.id
        WHERE t.id=$1 AND (t.owner_id=$2 OR t.owner_id='') AND s.id=$3
          AND t.status NOT IN ('completed','cancelled')
          AND s.status IN ('pending','active','failed') AND s.attempts=$4
          AND NOT EXISTS (SELECT 1 FROM task_leases l WHERE l.task_id=t.id AND l.step_id=s.id AND l.expires_at>$5)`),
		taskID, taskOwner(ctx), stepID, attempts, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&version); err != nil {
		return
	}
	reason := fmt.Sprintf("step %s exceeded max attempts (%d)", truncateWorker(stepID, 20), attempts)
	result := ToolTaskCheckpoint(ctx, w.deps, map[string]any{
		"task_id": taskID, "step_id": stepID, "base_version": version,
		"actor_id":        w.cfg.ActorID,
		"idempotency_key": fmt.Sprintf("worker-exhausted:%s:%d", stepID, attempts),
		"summary":         reason, "next_action": "retry or resolve manually",
		"blocker": map[string]any{"reason": reason, "required_decision": "retry or resolve manually"},
	})
	if result.IsError {
		log.Printf("task worker: exhaustion checkpoint rejected for task %s step %s", taskID, stepID)
	}
}

// ── policy helpers ──

func parseWorkerPolicy(authorityJSON string) workerTaskPolicy {
	var p workerTaskPolicy
	if err := json.Unmarshal([]byte(authorityJSON), &p); err != nil {
		return workerTaskPolicy{}
	}
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
	if p.DeadlineSeconds <= 0 || p.DeadlineSeconds > cfg.LeaseSeconds {
		return cfg.LeaseSeconds
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
// NewLoggingStepExecutor is retained for compatibility and fails closed.
// Use NewTaskExecutor with the server dispatcher for actual execution.
func NewLoggingStepExecutor() TaskStepExecutor { return LoggingStepExecutor{} }

type LoggingStepExecutor struct{}

func (LoggingStepExecutor) ExecuteStep(ctx context.Context, taskID, stepID, description string, deadline time.Time) (bool, string, error) {
	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	return false, "", errors.New("no real task executor configured")
}

// newWorkerID returns a collision-safe id for worker-created rows.
func newWorkerID() string {
	return "w-" + uuid.NewString()
}
