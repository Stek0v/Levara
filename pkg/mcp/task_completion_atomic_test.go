package mcp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/stek0v/levara/pkg/memoryindex"
	"testing"
	"time"
)

func TestTaskCompletionRollsBackFailedMemoryPromotion(t *testing.T) {
	for _, failure := range []string{"memories", "task_events", "memory_index_jobs"} {
		t.Run(failure, func(t *testing.T) {
			deps := setupTaskTestDB(t)
			testTaskCompletionAtomic(t, deps, deps, false, failure)
		})
	}
}

func TestTaskCompletionAtomicPostgres(t *testing.T) {
	for _, failure := range []string{"memories", "task_events", "memory_index_jobs"} {
		t.Run(failure, func(t *testing.T) {
			db := openPostgresMemoryTestDB(t)
			createTaskTestSchema(t, db)
			base := &fakeDeps{db: db}
			testTaskCompletionAtomic(t, &postgresMemoryDeps{fakeDeps: base}, base, true, failure)
		})
	}
}

func testTaskCompletionAtomic(t *testing.T, deps Deps, base *fakeDeps, postgres bool, failure string) {
	t.Helper()
	outbox, err := memoryindex.NewStore(deps.DB())
	if err != nil {
		t.Fatal(err)
	}
	base.memoryIndexOutbox, base.embedAvailable, base.hasColls = outbox, true, true
	// Replacement of a shared row must preserve its history and owner scope.
	if _, err := deps.DB().Exec(`INSERT INTO memories(id,key,value,type,owner_id,collection_name) VALUES ('old-a','candidate-a','old value','project','','levara'),('foreign-a','candidate-a','foreign secret','project','victim','levara')`); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	taskID, version := openTask(t, deps, "low")
	receipt := taskPayload(t, ToolTaskReceipt(ctx, deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "verified",
		"receipt_type": "command", "status": "pass", "criterion_ids": []any{"tests"}, "exit_code": float64(0), "workspace_revision": "rev-1",
	}))
	var candidates []any
	for _, key := range []string{"candidate-a", "candidate-b"} {
		candidates = append(candidates, map[string]any{"key": key, "value": "A verified outcome.", "room": "memory", "hall": "discovery", "evidence_receipt_ids": []any{receipt["receipt_id"]}})
	}
	checkpoint := taskPayload(t, ToolTaskCheckpoint(ctx, deps, map[string]any{
		"task_id": taskID, "base_version": receipt["version"], "idempotency_key": "outcomes",
		"summary": "two verified outcomes", "workspace_revision": "rev-1", "memory_candidates": candidates,
	}))
	condition := "NEW.key='candidate-b'"
	if failure == "task_events" {
		condition = "NEW.event_type='task_completed'"
	}
	if failure == "memory_index_jobs" {
		condition = "NEW.operation='upsert_vector'"
	}
	trigger := fmt.Sprintf("CREATE TRIGGER fail_promotion BEFORE INSERT ON %s WHEN %s BEGIN SELECT RAISE(ABORT, 'injected promotion failure'); END", failure, condition)
	if postgres {
		trigger = fmt.Sprintf("CREATE FUNCTION fail_promotion_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF %s THEN RAISE EXCEPTION 'injected promotion failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_promotion BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION fail_promotion_fn()", condition, failure)
	}
	if _, err := deps.DB().Exec(trigger); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"task_id": taskID, "expected_version": checkpoint["version"]}
	result := ToolTaskComplete(ctx, deps, args)
	var status string
	var persisted, promoted int
	if err := deps.DB().QueryRow(deps.Q(`SELECT status FROM tasks WHERE id=$1`), taskID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := deps.DB().QueryRow(`SELECT COUNT(*) FROM memories`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if err := deps.DB().QueryRow(`SELECT COUNT(*) FROM task_memory_candidates WHERE status='promoted'`).Scan(&promoted); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || status == "completed" || persisted != 2 || promoted != 0 {
		t.Errorf("partial completion after SQL failure: result=%s task=%s memories=%d promoted=%d", toolResultText(result), status, persisted, promoted)
	}
	drop := "DROP TRIGGER fail_promotion"
	if postgres {
		drop += " ON " + failure
	}
	if _, err := deps.DB().Exec(drop); err != nil {
		t.Fatal(err)
	}
	retry := taskPayload(t, ToolTaskComplete(ctx, deps, args))
	if retry["promoted_memories"] != float64(2) {
		t.Errorf("retry did not complete both outcomes: %+v", retry)
	}
	var oldValue, foreignValue, replacementID, owner string
	if err := deps.DB().QueryRow(`SELECT value,superseded_by FROM memories WHERE id='old-a'`).Scan(&oldValue, &replacementID); err != nil {
		t.Fatal(err)
	}
	if err := deps.DB().QueryRow(`SELECT value FROM memories WHERE id='foreign-a'`).Scan(&foreignValue); err != nil {
		t.Fatal(err)
	}
	if err := deps.DB().QueryRow(deps.Q(`SELECT owner_id FROM memories WHERE id=$1`), replacementID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if oldValue != "old value" || foreignValue != "foreign secret" || owner != "" {
		t.Fatalf("supersession changed history or ownership")
	}
	var jobs int
	if err := deps.DB().QueryRow(`SELECT COUNT(*) FROM memory_index_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 3 {
		t.Fatalf("index jobs=%d, want delete + two upserts", jobs)
	}
	var digest string
	if err := deps.DB().QueryRow(deps.Q(`SELECT digest FROM memory_index_jobs WHERE memory_id=$1 AND operation='upsert_vector'`), replacementID).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%x", sha256.Sum256([]byte("candidate-a\x00A verified outcome.")))
	if digest != want {
		t.Fatalf("outbox digest=%s want %s", digest, want)
	}
	replay := taskPayload(t, ToolTaskComplete(ctx, deps, args))
	if replay["already_completed"] != true {
		t.Fatalf("non-idempotent replay: %+v", replay)
	}

}

type taskIndexHeartbeatDeps struct {
	*fakeDeps
	heartbeatErr error
	logged       bool
}

func (d *taskIndexHeartbeatDeps) LogHeartbeat(_ string, _ any) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	d.logged = true
	_, d.heartbeatErr = d.db.ExecContext(ctx, `INSERT INTO task_events(id) VALUES('index-error')`)
}

func TestTaskIndexFallbackReleasesSQLConnectionBeforeLogging(t *testing.T) {
	base := setupTaskTestDB(t)
	base.db.SetMaxOpenConns(1)
	base.embedAvailable = true
	base.embedFn = func(context.Context, string) ([]float32, error) { return nil, errors.New("injected embed failure") }
	for _, q := range []string{`INSERT INTO memories(id,key,value,type,collection_name) VALUES('m','key','value','project','levara')`, `INSERT INTO task_memory_links(task_id,memory_id,relation) VALUES('task','m','produced')`} {
		if _, err := base.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	deps := &taskIndexHeartbeatDeps{fakeDeps: base}
	syncTaskMemoryIndex(context.Background(), deps, "task")
	if !deps.logged || deps.heartbeatErr != nil {
		t.Fatalf("heartbeat could not acquire sole SQL connection: logged=%v err=%v", deps.logged, deps.heartbeatErr)
	}
}
