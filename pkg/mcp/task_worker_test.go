package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// ── scaffolding ──

type execResult struct {
	passed      bool
	observation string
	err         error
}

type fakeExecutor struct {
	mu        sync.Mutex
	results   map[string]execResult
	calls     map[string]int
	execDelay time.Duration
}

func (f *fakeExecutor) ExecuteStep(ctx context.Context, taskID, stepID, description string, deadline time.Time) (bool, string, error) {
	f.mu.Lock()
	f.calls[stepID]++
	r, ok := f.results[stepID]
	delay := f.execDelay
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if !ok {
		return true, "default ok", nil
	}
	return r.passed, r.observation, r.err
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal(msg)
}

func stepStatus(t *testing.T, db *sql.DB, taskID, stepID string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM task_steps WHERE task_id=? AND id=?`, taskID, stepID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedAutoTask creates a task with the given steps and authority.
func seedAutoTask(t *testing.T, db *sql.DB, id, authority string, steps [][2]string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO tasks (id, idempotency_key, authority_json, status, version, created_at, updated_at)
		VALUES (?, ?, ?, 'running', 1, ?, ?)`, id, id+"-ik", authority, now, now); err != nil {
		t.Fatal(err)
	}
	for i, s := range steps {
		deps := "[]"
		if i > 0 {
			deps = fmt.Sprintf("[%q]", steps[i-1][0])
		}
		if _, err := db.Exec(`INSERT INTO task_steps (id, task_id, description, status, required, dependencies_json, criterion_ids_json, attempts, position, created_at, updated_at)
			VALUES (?, ?, ?, 'pending', 1, ?, '[]', 0, ?, ?, ?)`, s[0], id, s[1], deps, i, now, now); err != nil {
			t.Fatal(err)
		}
	}
}

// ── tests ──

// Worker executes a 3-step chain end-to-end, respecting dependencies and
// recording execution events.
func TestTaskWorkerExecutesThreeStepChain(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-1", `{"auto_run": true}`, [][2]string{
		{"s-1", "first"}, {"s-2", "second"}, {"s-3", "third"},
	})
	// s-2 depends on s-1, s-3 on s-2.
	db.Exec(`UPDATE task_steps SET dependencies_json='["s-1"]' WHERE id='s-2'`)
	db.Exec(`UPDATE task_steps SET dependencies_json='["s-2"]' WHERE id='s-3'`)

	w := NewTaskWorker(deps, &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}}, TaskWorkerConfig{PollInterval: 50 * time.Millisecond, LeaseSeconds: 60})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitFor(t, 5*time.Second, func() bool {
		return stepStatus(t, db, "t-1", "s-3") == "passed"
	}, "chain did not complete")

	for _, s := range []string{"s-1", "s-2", "s-3"} {
		if got := stepStatus(t, db, "t-1", s); got != "passed" {
			t.Fatalf("step %s = %s", s, got)
		}
	}
	var events int
	db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE task_id='t-1' AND event_type='step_executed'`).Scan(&events)
	if events != 3 {
		t.Fatalf("expected 3 execution events, got %d", events)
	}
	w.Stop()
}

// Worker + a simulated external MCP host race on one step: the CAS lease
// guarantees exactly one winner (0 double-claims — S2 reuse).
func TestTaskWorkerNoDoubleClaims(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-2", `{"auto_run": true}`, [][2]string{{"s-1", "contended"}})
	exec := &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}}
	w := NewTaskWorker(deps, exec, TaskWorkerConfig{PollInterval: 30 * time.Millisecond, LeaseSeconds: 120})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	// Simulate an external host claiming the same step via the tool surface
	// repeatedly, in parallel with the worker's polling.
	var externalClaims, rejections int64
	deadline := time.Now().Add(600 * time.Millisecond)
	for time.Now().Before(deadline) {
		var version int
		db.QueryRow(`SELECT version FROM tasks WHERE id='t-2'`).Scan(&version)
		res := ToolTaskStep(context.Background(), deps, map[string]any{
			"task_id": "t-2", "step_id": "s-1", "action": "claim",
			"actor_id": "external:host", "base_version": version,
		})
		if res.IsError {
			atomic.AddInt64(&rejections, 1)
		} else {
			atomic.AddInt64(&externalClaims, 1)
			// Release so the worker can proceed too.
			db.QueryRow(`SELECT version FROM tasks WHERE id='t-2'`).Scan(&version)
			ToolTaskStep(context.Background(), deps, map[string]any{
				"task_id": "t-2", "step_id": "s-1", "action": "release",
				"actor_id": "external:host", "base_version": version,
			})
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.Stop()

	// Invariant: step executed at most once by the worker, and attempts on
	// the step never exceed executions + claims in flight simultaneously.
	exec.mu.Lock()
	sum := 0
	for _, n := range exec.calls {
		sum += n
	}
	exec.mu.Unlock()
	if sum > 1 {
		t.Fatalf("worker executed step %d times (double-claim)", sum)
	}
	var attempts int
	db.QueryRow(`SELECT attempts FROM task_steps WHERE id='s-1'`).Scan(&attempts)
	if int64(attempts) > externalClaims+int64(sum)+1 {
		t.Fatalf("attempts %d exceeds claims — double-claim", attempts)
	}
}

// A step that keeps failing exhausts max_step_attempts and lands a blocker
// with a clear reason — not an infinite retry loop.
func TestTaskWorkerRetryBackoffExhausts(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-3", `{"auto_run": true, "max_step_attempts": 2}`, [][2]string{{"s-1", "doomed"}})
	exec := &fakeExecutor{results: map[string]execResult{
		"s-1": {passed: false, observation: "boom", err: errors.New("boom")},
	}, calls: map[string]int{}}
	w := NewTaskWorker(deps, exec, TaskWorkerConfig{PollInterval: 40 * time.Millisecond, LeaseSeconds: 60})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitFor(t, 8*time.Second, func() bool {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM task_blockers WHERE task_id='t-3' AND reason LIKE '%max attempts%' AND status='active'`).Scan(&n)
		return n == 1
	}, "exhaustion blocker never appeared")

	exec.mu.Lock()
	calls := exec.calls["s-1"]
	exec.mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", calls)
	}
	w.Stop()
}

// Worker killed mid-step: the lease expires and ANOTHER worker picks the
// step up — the task stays consistent, work is not lost.
func TestTaskWorkerKillMidStepLeaseExpires(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-4", `{"auto_run": true}`, [][2]string{{"s-1", "long task"}})

	slow := &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}, execDelay: 5 * time.Second}
	w1 := NewTaskWorker(deps, slow, TaskWorkerConfig{PollInterval: 30 * time.Millisecond, LeaseSeconds: 30})
	ctx1, cancel1 := context.WithCancel(context.Background())
	w1.Start(ctx1)
	waitFor(t, 5*time.Second, func() bool {
		return stepStatus(t, db, "t-4", "s-1") == "active"
	}, "w1 never claimed the step")

	// Kill w1 mid-step (leases intentionally left behind).
	cancel1()

	// Shorten the abandoned lease to simulate expiry.
	db.Exec(`UPDATE task_leases SET expires_at = datetime('now','-1 minute') WHERE task_id='t-4'`)

	exec2 := &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}}
	w2 := NewTaskWorker(deps, exec2, TaskWorkerConfig{PollInterval: 30 * time.Millisecond, LeaseSeconds: 60})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	w2.Start(ctx2)

	waitFor(t, 8*time.Second, func() bool {
		return stepStatus(t, db, "t-4", "s-1") == "passed"
	}, "w2 never picked up the abandoned step")
	w2.Stop()
}

// A cycle (s-1 needs s-2, s-2 needs s-1) stalls the scheduler; the deadlock
// marker lands after MaxStalledRounds — deadline/deadlock breaks the wait.
func TestTaskWorkerDeadlockDetection(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-5", `{"auto_run": true}`, [][2]string{
		{"s-1", "waits on s-2"}, {"s-2", "waits on s-1"},
	})
	db.Exec(`UPDATE task_steps SET dependencies_json='["s-2"]' WHERE id='s-1'`)
	db.Exec(`UPDATE task_steps SET dependencies_json='["s-1"]' WHERE id='s-2'`)

	w := NewTaskWorker(deps, &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}}, TaskWorkerConfig{PollInterval: 40 * time.Millisecond, LeaseSeconds: 60, MaxStalledRounds: 3})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)

	waitFor(t, 10*time.Second, func() bool {
		var n int
		db.QueryRow(`SELECT COUNT(*) FROM task_blockers WHERE task_id='t-5' AND reason='scheduler deadlock' AND status='active'`).Scan(&n)
		return n == 1
	}, "deadlock marker never appeared")

	// Neither step may have been executed (cycle is unrunnable).
	var events int
	db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE task_id='t-5' AND event_type='step_executed'`).Scan(&events)
	if events != 0 {
		t.Fatalf("cycle steps were executed: %d", events)
	}
	w.Stop()
}

// Non-auto_run tasks are never touched.
func TestTaskWorkerSkipsNonAutoRun(t *testing.T) {
	deps := setupTaskTestDB(t)
	db := deps.DB()
	seedAutoTask(t, db, "t-6", `{}`, [][2]string{{"s-1", "manual only"}})
	exec := &fakeExecutor{results: map[string]execResult{}, calls: map[string]int{}}
	w := NewTaskWorker(deps, exec, TaskWorkerConfig{PollInterval: 40 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	time.Sleep(300 * time.Millisecond)
	w.Stop()
	exec.mu.Lock()
	n := len(exec.calls)
	exec.mu.Unlock()
	if n != 0 {
		t.Fatalf("worker executed non-auto_run step %d times", n)
	}
}
