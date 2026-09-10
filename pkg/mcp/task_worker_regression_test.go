package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type workerExecutorFunc func(context.Context, string, string, string, time.Time) (bool, string, error)

func (f workerExecutorFunc) ExecuteStep(ctx context.Context, task, step, description string, deadline time.Time) (bool, string, error) {
	return f(ctx, task, step, description, deadline)
}

func workerTestDeps(t *testing.T, dialect string) Deps {
	t.Helper()
	if dialect == "postgres" {
		db := openPostgresMemoryTestDB(t)
		createTaskTestSchema(t, db)
		if _, err := db.Exec(`ALTER TABLE task_leases ALTER COLUMN expires_at TYPE TIMESTAMPTZ USING expires_at::timestamptz`); err != nil {
			t.Fatal(err)
		}
		return &postgresMemoryDeps{fakeDeps: &fakeDeps{db: db}}
	}
	deps := setupTaskTestDB(t)
	deps.DB().SetMaxOpenConns(1)
	return deps
}

func openWorkerTestTask(t *testing.T, deps Deps, identity string, authority map[string]any) (context.Context, string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), UserIDKey, identity)
	opened := taskPayload(t, ToolTaskOpen(ctx, deps, map[string]any{
		"collection": "worker-tests", "room": "task-runtime", "objective": "exercise fake executor scheduling",
		"idempotency_key": identity, "risk_level": "low", "authority": authority,
		"definition_of_done": []any{map[string]any{"criterion_id": "verified", "description": "test observed execution"}},
	}))
	id := opened["task_id"].(string)
	taskPayload(t, ToolTaskPlan(ctx, deps, map[string]any{
		"task_id": id, "base_version": opened["version"],
		"steps": []any{map[string]any{"step_id": "same-step", "description": "call injected fake", "action": taskExecutorAction(), "criterion_ids": []any{"verified"}}},
	}))
	return ctx, id
}

func workerTaskVersion(t *testing.T, deps Deps, id string) int {
	t.Helper()
	var version int
	if err := deps.DB().QueryRow(deps.Q(`SELECT version FROM tasks WHERE id=$1`), id).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func workerStepState(t *testing.T, deps Deps, taskID string) (string, int) {
	t.Helper()
	var status string
	var attempts int
	if err := deps.DB().QueryRow(deps.Q(`SELECT status,attempts FROM task_steps WHERE task_id=$1 AND id=$2`), taskID, "same-step").Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	return status, attempts
}

func TestTaskStepSameIDStaysInsideTask(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			for _, action := range []string{"claim", "release", "pass", "fail"} {
				ctx, taskA := openWorkerTestTask(t, deps, "owner-a-"+action, nil)
				_, taskB := openWorkerTestTask(t, deps, "owner-b-"+action, nil)
				claimed := taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{
					"task_id": taskA, "step_id": "same-step", "action": "claim", "actor_id": "same-actor", "base_version": workerTaskVersion(t, deps, taskA),
				}))
				if action != "claim" {
					taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{
						"task_id": taskA, "step_id": "same-step", "action": action, "actor_id": "same-actor", "base_version": claimed["version"],
					}))
				}
				if status, attempts := workerStepState(t, deps, taskB); status != "pending" || attempts != 0 {
					t.Errorf("%s changed another owner's matching step: status=%s attempts=%d", action, status, attempts)
				}
				var leases int
				if err := deps.DB().QueryRow(deps.Q(`SELECT COUNT(*) FROM task_leases WHERE task_id=$1`), taskB).Scan(&leases); err != nil || leases != 0 {
					t.Errorf("peer lease count=%d err=%v", leases, err)
				}
			}
		})
	}
}

func TestTaskWorkerPublicOpenReachesFakeExecutor(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			_, taskID := openWorkerTestTask(t, deps, "public-worker", map[string]any{"auto_run": true})
			called := make(chan string, 1)
			w := NewTaskWorker(deps, workerExecutorFunc(func(ctx context.Context, task, _, _ string, _ time.Time) (bool, string, error) {
				if taskOwner(ctx) != "public-worker" {
					t.Errorf("executor lost owner context: %q", taskOwner(ctx))
				}
				called <- task
				return true, "fake executor only", nil
			}), TaskWorkerConfig{})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			w.round(ctx)
			select {
			case got := <-called:
				if got != taskID {
					t.Errorf("executed task %s instead of %s", got, taskID)
				}
			case <-time.After(200 * time.Millisecond):
				t.Fatal("task_open -> task_plan did not reach injected fake executor")
			}
			waitFor(t, 2*time.Second, func() bool { return w.currentInFlight(taskID) == 0 }, "worker did not finish")
			if status, _ := workerStepState(t, deps, taskID); status != "passed" {
				t.Errorf("fake executor result was not persisted: %s", status)
			}
		})
	}
}

func TestTaskWorkerExecutorErrorNeverPasses(t *testing.T) {
	deps := workerTestDeps(t, "sqlite")
	_, taskID := openWorkerTestTask(t, deps, "error-worker", map[string]any{"auto_run": true})
	w := NewTaskWorker(deps, workerExecutorFunc(func(context.Context, string, string, string, time.Time) (bool, string, error) {
		return true, "contradictory result", errors.New("execution failed")
	}), TaskWorkerConfig{})
	if !w.tryClaimAndRun(context.Background(), taskID, "error-worker", "same-step", "fake", workerTaskPolicy{AutoRun: true, MaxStepAttempts: 1}) {
		t.Fatal("claim failed")
	}
	waitFor(t, 2*time.Second, func() bool { return w.currentInFlight(taskID) == 0 }, "executor did not finish")
	if status, _ := workerStepState(t, deps, taskID); status != "failed" {
		t.Errorf("executor returned an error, but step status=%s", status)
	}
}

func TestTaskWorkerStaleExecutorCannotCompleteReplacementLease(t *testing.T) {
	for _, actor := range []string{"levara:task-worker", "replacement-worker"} {
		t.Run(actor, func(t *testing.T) {
			deps := workerTestDeps(t, "sqlite")
			ctx, taskID := openWorkerTestTask(t, deps, "stale-worker", map[string]any{"auto_run": true})
			release := make(chan struct{})
			w := NewTaskWorker(deps, workerExecutorFunc(func(context.Context, string, string, string, time.Time) (bool, string, error) {
				<-release
				return true, "stale executor", nil
			}), TaskWorkerConfig{})
			if !w.tryClaimAndRun(context.Background(), taskID, "stale-worker", "same-step", "fake", workerTaskPolicy{AutoRun: true}) {
				t.Fatal("claim failed")
			}
			var oldActor string
			if err := deps.DB().QueryRow(deps.Q(`SELECT actor_id FROM task_leases WHERE task_id=$1`), taskID).Scan(&oldActor); err != nil {
				t.Fatal(err)
			}
			transition := taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": taskID, "step_id": "same-step", "action": "release", "actor_id": oldActor, "base_version": workerTaskVersion(t, deps, taskID)}))
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": taskID, "step_id": "same-step", "action": "claim", "actor_id": actor, "base_version": transition["version"]}))
			close(release)
			waitFor(t, 2*time.Second, func() bool { return w.currentInFlight(taskID) == 0 }, "stale executor did not return")
			if status, _ := workerStepState(t, deps, taskID); status != "active" {
				t.Errorf("stale executor changed replacement lease: %s", status)
			}
			var successes int
			if err := deps.DB().QueryRow(deps.Q(`SELECT COUNT(*) FROM task_events WHERE task_id=$1 AND event_type='step_executed' AND payload_json LIKE $2`), taskID, `%"passed":true%`).Scan(&successes); err != nil || successes != 0 {
				t.Errorf("stale completion reported %d successes: %v", successes, err)
			}
		})
	}
}

func TestTaskWorkerGlobalReservationAndLeaseDeadline(t *testing.T) {
	deps := workerTestDeps(t, "sqlite")
	called := make(chan time.Time, 3)
	abort := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := NewTaskWorker(deps, workerExecutorFunc(func(ctx context.Context, _, _, _ string, deadline time.Time) (bool, string, error) {
		called <- deadline
		select {
		case <-ctx.Done():
		case <-abort:
		}
		return true, "cancelled fake", nil
	}), TaskWorkerConfig{LeaseSeconds: 30, MaxInFlight: 2})
	var tasks []string
	defer func() {
		close(abort)
		waitFor(t, 2*time.Second, func() bool {
			for _, id := range tasks {
				if w.currentInFlight(id) != 0 {
					return false
				}
			}
			return true
		}, "test executor did not drain")
	}()
	for i := range 3 {
		owner := fmt.Sprintf("limited-worker-%d", i)
		_, id := openWorkerTestTask(t, deps, owner, map[string]any{"auto_run": true})
		tasks = append(tasks, id)
		claimed := w.tryClaimAndRun(ctx, id, owner, "same-step", "fake", workerTaskPolicy{AutoRun: true, DeadlineSeconds: 3600})
		if claimed != (i < 2) {
			t.Errorf("claim %d accepted=%v, want global cap of two", i, claimed)
		}
		if claimed {
			deadline := <-called
			var expires string
			if err := deps.DB().QueryRow(deps.Q(`SELECT expires_at FROM task_leases WHERE task_id=$1`), id).Scan(&expires); err != nil {
				t.Fatal(err)
			}
			leaseEnd, err := time.Parse(time.RFC3339Nano, expires)
			if err != nil || deadline.After(leaseEnd) {
				t.Errorf("executor deadline %s exceeds lease %s: %v", deadline, leaseEnd, err)
			}
		}
	}
	cancel()
	waitFor(t, 2*time.Second, func() bool {
		for _, id := range tasks {
			if w.currentInFlight(id) != 0 {
				return false
			}
		}
		return true
	}, "parent cancellation did not reach executor")
	for _, id := range tasks {
		if status, _ := workerStepState(t, deps, id); status == "passed" {
			t.Error("cancelled executor was recorded as passed")
		}
	}
}

func TestTaskWorkerPolicyRejectsInvalidJSON(t *testing.T) {
	for _, raw := range []string{`{"auto_run":true,"allowed_tools":123}`, `{"auto_run":true} trailing`} {
		if parseWorkerPolicy(raw).autoRunAllowed() {
			t.Errorf("invalid policy accepted: %s", strings.TrimSpace(raw))
		}
	}
}

func TestTaskStepPreservesOtherExpiredLeaseForReclaim(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			ctx, id := openWorkerTestTask(t, deps, "expired-lease", nil)
			plan := taskPayload(t, ToolTaskPlan(ctx, deps, map[string]any{
				"task_id": id, "base_version": workerTaskVersion(t, deps, id),
				"steps": []any{map[string]any{"step_id": "same-step", "description": "expired"}, map[string]any{"step_id": "other-step", "description": "independent"}},
			}))
			first := taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "first", "base_version": plan["version"]}))
			// Fault injection only: task and plan were created through public APIs.
			if _, err := deps.DB().Exec(deps.Q(`UPDATE task_leases SET expires_at=$1 WHERE task_id=$2 AND step_id=$3`), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), id, "same-step"); err != nil {
				t.Fatal(err)
			}
			other := taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "other-step", "action": "claim", "actor_id": "other", "base_version": first["version"]}))
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "replacement", "base_version": other["version"]}))
			if status, attempts := workerStepState(t, deps, id); status != "active" || attempts != 2 {
				t.Errorf("expired step not reclaimed: %s, attempts %d", status, attempts)
			}
		})
	}
}

func TestTaskWorkerStopCancelsAndDrainsExecutor(t *testing.T) {
	deps := workerTestDeps(t, "sqlite")
	_, id := openWorkerTestTask(t, deps, "stop-worker", map[string]any{"auto_run": true})
	started := make(chan struct{})
	w := NewTaskWorker(deps, workerExecutorFunc(func(ctx context.Context, _, _, _ string, _ time.Time) (bool, string, error) {
		close(started)
		<-ctx.Done()
		return true, "cancelled fake", nil
	}), TaskWorkerConfig{PollInterval: time.Millisecond})
	w.Start(context.Background())
	defer w.Stop()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("executor not started")
	}
	w.Stop()
	if w.currentInFlight(id) != 0 {
		t.Error("Stop returned without draining executor")
	}
	if status, _ := workerStepState(t, deps, id); status != "active" {
		t.Errorf("shutdown should leave lease to expire, got %s", status)
	}
}

func TestTaskWorkerKeepsGlobalCapAcrossRounds(t *testing.T) {
	deps := workerTestDeps(t, "sqlite")
	for i := range 3 {
		openWorkerTestTask(t, deps, fmt.Sprintf("round-owner-%d", i), map[string]any{"auto_run": true})
	}
	called := make(chan struct{}, 3)
	w := NewTaskWorker(deps, workerExecutorFunc(func(ctx context.Context, _, _, _ string, _ time.Time) (bool, string, error) {
		called <- struct{}{}
		<-ctx.Done()
		return false, "cancelled fake", ctx.Err()
	}), TaskWorkerConfig{MaxInFlight: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); w.wg.Wait() }()
	w.round(ctx)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("executor not started")
	}
	w.round(ctx)
	if len(called) != 0 {
		t.Error("second round exceeded the existing global reservation")
	}
}

// The last attempt must stay observable even if the process exits after its
// lease expires and before it can persist the execution result.
func TestTaskWorkerExpiredFinalAttemptCreatesBlockerAfterRestart(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			ctx, id := openWorkerTestTask(t, deps, "exhausted-restart", map[string]any{"auto_run": true, "max_step_attempts": 1})
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "dead-process", "base_version": workerTaskVersion(t, deps, id)}))
			if _, err := deps.DB().Exec(deps.Q(`UPDATE task_leases SET expires_at=$1 WHERE task_id=$2 AND step_id=$3`), time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), id, "same-step"); err != nil {
				t.Fatal(err)
			}
			w := NewTaskWorker(deps, workerExecutorFunc(func(context.Context, string, string, string, time.Time) (bool, string, error) {
				t.Error("exhausted step executed again")
				return true, "unexpected", nil
			}), TaskWorkerConfig{})
			w.round(context.Background())
			w.round(context.Background())
			w.wg.Wait()
			var status string
			var blockers int
			if err := deps.DB().QueryRow(deps.Q(`SELECT status FROM tasks WHERE id=$1`), id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := deps.DB().QueryRow(deps.Q(`SELECT COUNT(*) FROM task_blockers WHERE task_id=$1 AND status='active'`), id).Scan(&blockers); err != nil {
				t.Fatal(err)
			}
			if status != "blocked" || blockers != 1 {
				t.Errorf("expired last attempt lost: task=%s blockers=%d", status, blockers)
			}
			if step, attempts := workerStepState(t, deps, id); step == "passed" || attempts != 1 {
				t.Errorf("last attempt changed: step=%s attempts=%d", step, attempts)
			}
		})
	}
}

func TestTaskWorkerDeadlineReservesCompletionTime(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			_, id := openWorkerTestTask(t, deps, "deadline-budget", map[string]any{"auto_run": true, "max_step_attempts": 1})
			w := NewTaskWorker(deps, workerExecutorFunc(func(_ context.Context, _, _, _ string, deadline time.Time) (bool, string, error) {
				var expires string
				if err := deps.DB().QueryRow(deps.Q(`SELECT expires_at FROM task_leases WHERE task_id=$1`), id).Scan(&expires); err != nil {
					t.Error(err)
					return false, "", err
				}
				leaseEnd, err := time.Parse(time.RFC3339Nano, expires)
				if err != nil || leaseEnd.Sub(deadline) < 4*time.Second {
					t.Errorf("completion has no reserved time: deadline=%s lease=%s err=%v", deadline, leaseEnd, err)
				}
				return false, "fake deadline exceeded", context.DeadlineExceeded
			}), TaskWorkerConfig{LeaseSeconds: 30})
			w.round(context.Background())
			w.wg.Wait()
			if status, _ := workerStepState(t, deps, id); status != "failed" {
				t.Errorf("deadline step status=%s", status)
			}
			var status string
			if err := deps.DB().QueryRow(deps.Q(`SELECT status FROM tasks WHERE id=$1`), id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "blocked" {
				t.Errorf("exhausted task status=%s", status)
			}
		})
	}
}

func TestTaskWorkerActualDeadlineBlocksFinalAttempt(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			_, id := openWorkerTestTask(t, deps, "real-deadline", map[string]any{"auto_run": true, "max_step_attempts": 1, "step_deadline_seconds": 1})
			w := NewTaskWorker(deps, workerExecutorFunc(func(ctx context.Context, _, _, _ string, _ time.Time) (bool, string, error) {
				<-ctx.Done()
				return true, "contradictory success after deadline", nil
			}), TaskWorkerConfig{LeaseSeconds: 30})
			w.round(context.Background())
			w.wg.Wait()
			var status string
			if err := deps.DB().QueryRow(deps.Q(`SELECT status FROM tasks WHERE id=$1`), id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if step, attempts := workerStepState(t, deps, id); step != "failed" || attempts != 1 || status != "blocked" {
				t.Errorf("deadline final state: task=%s step=%s attempts=%d", status, step, attempts)
			}
		})
	}
}

func TestTaskWorkerExhaustionRecoveryPreservesLiveLease(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			ctx, id := openWorkerTestTask(t, deps, "live-final-attempt", map[string]any{"auto_run": true, "max_step_attempts": 1})
			claimed := taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "live-process", "base_version": workerTaskVersion(t, deps, id)}))
			w := NewTaskWorker(deps, nil, TaskWorkerConfig{})
			w.recordStepExhausted(ctx, id, "same-step", 1)
			var n int
			if err := deps.DB().QueryRow(deps.Q(`SELECT COUNT(*) FROM task_blockers WHERE task_id=$1`), id).Scan(&n); err != nil || n != 0 {
				t.Errorf("live final lease was blocked: n=%d err=%v", n, err)
			}
			// Simulate a process exiting after fail but before recording exhaustion.
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "fail", "actor_id": "live-process", "base_version": claimed["version"]}))
			w.round(context.Background())
			var status string
			if err := deps.DB().QueryRow(deps.Q(`SELECT status FROM tasks WHERE id=$1`), id).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if status != "blocked" {
				t.Errorf("failed final attempt was not recovered: %s", status)
			}
		})
	}
}

func TestTaskStepReclaimsOrphanWithoutStealingLiveLease(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			ctx, id := openWorkerTestTask(t, deps, "orphan-recovery", nil)
			taskPayload(t, ToolTaskPlan(ctx, deps, map[string]any{"task_id": id, "base_version": workerTaskVersion(t, deps, id), "steps": []any{map[string]any{"step_id": "same-step", "description": "recover interrupted bookkeeping"}}}))
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "first", "base_version": workerTaskVersion(t, deps, id)}))
			denied := ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "other", "base_version": workerTaskVersion(t, deps, id)})
			if !denied.IsError {
				t.Fatal("stole live lease")
			}
			// Model the old server's global expiry cleanup removing an unrelated lease.
			if _, err := deps.DB().Exec(deps.Q(`DELETE FROM task_leases WHERE task_id=$1 AND step_id='same-step'`), id); err != nil {
				t.Fatal(err)
			}
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "recovery", "base_version": workerTaskVersion(t, deps, id)}))
			if status, attempts := workerStepState(t, deps, id); status != "active" || attempts != 2 {
				t.Fatalf("orphan status=%s attempts=%d", status, attempts)
			}
		})
	}
}
