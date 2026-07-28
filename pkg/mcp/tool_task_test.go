package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func setupTaskTestDB(t *testing.T) *fakeDeps {
	t.Helper()
	f, _ := os.CreateTemp("", "levara-task-*.db")
	path := f.Name()
	_ = f.Close()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(); _ = os.Remove(path) })
	ddl := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE tasks(id TEXT PRIMARY KEY,idempotency_key TEXT,owner_id TEXT DEFAULT '',collection_name TEXT,room TEXT,objective TEXT,authority_json TEXT,risk_level TEXT,status TEXT,version INTEGER,current_workspace_revision TEXT DEFAULT '',created_at TEXT,updated_at TEXT,completed_at TEXT,UNIQUE(owner_id,collection_name,idempotency_key))`,
		`CREATE TABLE task_criteria(id TEXT,task_id TEXT,description TEXT,required INTEGER,verification_json TEXT,created_at TEXT,PRIMARY KEY(task_id,id))`,
		`CREATE TABLE task_steps(id TEXT,task_id TEXT,description TEXT,status TEXT,required INTEGER,dependencies_json TEXT,criterion_ids_json TEXT,attempts INTEGER,position INTEGER,created_at TEXT,updated_at TEXT,PRIMARY KEY(task_id,id))`,
		`CREATE TABLE task_leases(step_id TEXT,task_id TEXT,actor_id TEXT,expires_at TEXT,created_at TEXT,PRIMARY KEY(task_id,step_id))`,
		`CREATE TABLE task_receipts(id TEXT PRIMARY KEY,task_id TEXT,idempotency_key TEXT,owner_id TEXT,receipt_type TEXT,status TEXT,criterion_ids_json TEXT,observation TEXT,exit_code INTEGER,evidence_uri TEXT,artifact_digest TEXT,workspace_revision TEXT,metadata_json TEXT,created_at TEXT,UNIQUE(task_id,idempotency_key))`,
		`CREATE TABLE task_checkpoints(id TEXT PRIMARY KEY,task_id TEXT,idempotency_key TEXT,step_id TEXT,summary TEXT,verified_json TEXT,failed_json TEXT,next_action TEXT,workspace_revision TEXT,created_at TEXT,UNIQUE(task_id,idempotency_key))`,
		`CREATE TABLE task_blockers(id TEXT PRIMARY KEY,task_id TEXT,reason TEXT,required_decision TEXT,status TEXT,created_at TEXT,resolved_at TEXT)`,
		`CREATE TABLE task_events(id TEXT PRIMARY KEY,task_id TEXT,actor_id TEXT,event_type TEXT,payload_json TEXT,created_at TEXT)`,
		`CREATE TABLE task_memory_candidates(id TEXT PRIMARY KEY,task_id TEXT,memory_key TEXT,value TEXT,room TEXT,hall TEXT,evidence_receipt_ids TEXT,status TEXT,created_at TEXT,UNIQUE(task_id,memory_key))`,
		`CREATE TABLE task_memory_links(task_id TEXT,memory_id TEXT,relation TEXT,created_at TEXT,PRIMARY KEY(task_id,memory_id,relation))`,
		`CREATE TABLE memories(id TEXT PRIMARY KEY,key TEXT,value TEXT,type TEXT,owner_id TEXT DEFAULT '',collection_name TEXT,room TEXT,hall TEXT,is_pinned INTEGER DEFAULT 0,pin_priority INTEGER DEFAULT 0,superseded_by TEXT DEFAULT '',valid_until TEXT,source_task_id TEXT DEFAULT '',source_receipt_ids TEXT DEFAULT '[]',verification_status TEXT DEFAULT '',supersedes_memory_id TEXT DEFAULT '',supersession_reason TEXT DEFAULT '',created_at TEXT,updated_at TEXT,UNIQUE(key,owner_id,collection_name))`,
	}
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return &fakeDeps{db: db}
}

func taskPayload(t *testing.T, result ToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool error: %s", result.Content[0].Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func logTaskRuntimeState(t *testing.T, deps *fakeDeps, taskID, label string) {
	t.Helper()
	var status, revision string
	var version int
	if err := deps.db.QueryRow(`SELECT status,version,current_workspace_revision FROM tasks WHERE id=?`, taskID).Scan(&status, &version, &revision); err != nil {
		t.Logf("[%s] task lookup failed: task_id=%s error=%v", label, taskID, err)
		return
	}
	var steps, leases, receipts, checkpoints, blockers, events int
	queries := []struct {
		name string
		dst  *int
		sql  string
	}{
		{"steps", &steps, `SELECT COUNT(*) FROM task_steps WHERE task_id=?`},
		{"leases", &leases, `SELECT COUNT(*) FROM task_leases WHERE task_id=?`},
		{"receipts", &receipts, `SELECT COUNT(*) FROM task_receipts WHERE task_id=?`},
		{"checkpoints", &checkpoints, `SELECT COUNT(*) FROM task_checkpoints WHERE task_id=?`},
		{"blockers", &blockers, `SELECT COUNT(*) FROM task_blockers WHERE task_id=? AND status='active'`},
		{"events", &events, `SELECT COUNT(*) FROM task_events WHERE task_id=?`},
	}
	for _, query := range queries {
		if err := deps.db.QueryRow(query.sql, taskID).Scan(query.dst); err != nil {
			t.Logf("[%s] %s count failed: error=%v", label, query.name, err)
		}
	}
	t.Logf("[%s] task_id=%s status=%s version=%d workspace_revision=%q steps=%d leases=%d receipts=%d checkpoints=%d active_blockers=%d events=%d",
		label, taskID, status, version, revision, steps, leases, receipts, checkpoints, blockers, events)
}

func toolResultText(result ToolResult) string {
	if len(result.Content) == 0 {
		return "<empty content>"
	}
	return result.Content[0].Text
}

type taskArtifactVerifierDeps struct {
	*fakeDeps
	err   error
	calls []string
}

func (d *taskArtifactVerifierDeps) VerifyArtifact(_ context.Context, evidenceURI, expectedDigest string) error {
	d.calls = append(d.calls, evidenceURI+"|"+expectedDigest)
	return d.err
}

func openTask(t *testing.T, deps *fakeDeps, risk string) (string, int) {
	t.Helper()
	out := taskPayload(t, ToolTaskOpen(context.Background(), deps, map[string]any{
		"collection": "levara", "room": "runtime", "objective": "ship verified runtime",
		"idempotency_key": "open-1-" + risk, "risk_level": risk,
		"definition_of_done": []any{map[string]any{"criterion_id": "tests", "description": "tests pass"}},
	}))
	return out["task_id"].(string), int(out["version"].(float64))
}

func TestTaskRuntime_IdempotencyLeaseAndStaleReceipt(t *testing.T) {
	deps := setupTaskTestDB(t)
	taskID, version := openTask(t, deps, "medium")
	plan := taskPayload(t, ToolTaskPlan(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "actor_id": "agent-a",
		"steps": []any{map[string]any{"step_id": "implement", "description": "implement", "criterion_ids": []any{"tests"}}},
	}))
	version = int(plan["version"].(float64))
	claim := taskPayload(t, ToolTaskStep(context.Background(), deps, map[string]any{"task_id": taskID, "step_id": "implement", "action": "claim", "actor_id": "agent-a", "base_version": float64(version)}))
	version = int(claim["version"].(float64))
	conflict := ToolTaskStep(context.Background(), deps, map[string]any{"task_id": taskID, "step_id": "implement", "action": "claim", "actor_id": "agent-b", "base_version": float64(version)})
	if !conflict.IsError {
		t.Fatal("second claim unexpectedly succeeded")
	}
	passed := taskPayload(t, ToolTaskStep(context.Background(), deps, map[string]any{"task_id": taskID, "step_id": "implement", "action": "pass", "actor_id": "agent-a", "base_version": float64(version)}))
	version = int(passed["version"].(float64))
	receipt := taskPayload(t, ToolTaskReceipt(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "tests-r1", "receipt_type": "command", "status": "pass",
		"criterion_ids": []any{"tests"}, "exit_code": float64(0), "workspace_revision": "rev-1",
	}))
	version = int(receipt["version"].(float64))
	checkpoint := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "cp-1", "summary": "changed after test", "workspace_revision": "rev-2",
	}))
	version = int(checkpoint["version"].(float64))
	validation := taskPayload(t, ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID}))
	if validation["valid"].(bool) || len(validation["stale_receipts"].([]any)) != 1 {
		t.Fatalf("expected stale receipt: %+v", validation)
	}
	latest := taskPayload(t, ToolTaskReceipt(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "tests-r2", "receipt_type": "command", "status": "pass",
		"criterion_ids": []any{"tests"}, "exit_code": float64(0), "workspace_revision": "rev-2",
	}))
	version = int(latest["version"].(float64))
	done := taskPayload(t, ToolTaskComplete(context.Background(), deps, map[string]any{"task_id": taskID, "expected_version": float64(version)}))
	if done["status"] != "completed" {
		t.Fatalf("completion=%+v", done)
	}
}

func TestTaskRuntime_HighRiskRequiresReviewer(t *testing.T) {
	deps := setupTaskTestDB(t)
	taskID, version := openTask(t, deps, "high")
	_, _ = deps.db.Exec(`INSERT INTO task_receipts(id,task_id,idempotency_key,owner_id,receipt_type,status,criterion_ids_json,observation,evidence_uri,artifact_digest,workspace_revision,metadata_json,created_at) VALUES('r1',?,?, '', 'command','pass','["tests"]','','','','rev-1','{}','2026-07-14T00:00:00Z')`, taskID, "r1")
	_, _ = deps.db.Exec(`UPDATE tasks SET current_workspace_revision='rev-1' WHERE id=?`, taskID)
	validation := taskPayload(t, ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID}))
	if validation["valid"].(bool) || !validation["audit_required"].(bool) {
		t.Fatalf("high-risk task passed without reviewer: %+v (version=%d)", validation, version)
	}
}

func TestTaskRuntimeIdempotentReplaysIgnoreStaleBaseVersion(t *testing.T) {
	deps := setupTaskTestDB(t)
	openArgs := map[string]any{
		"collection": "levara", "room": "runtime", "objective": "idempotent task",
		"idempotency_key": "same-open", "risk_level": "low",
		"definition_of_done": []any{map[string]any{"criterion_id": "done", "description": "done"}},
	}
	first := taskPayload(t, ToolTaskOpen(context.Background(), deps, openArgs))
	second := taskPayload(t, ToolTaskOpen(context.Background(), deps, openArgs))
	if first["task_id"] != second["task_id"] || first["version"] != second["version"] {
		t.Fatalf("task_open replay diverged: first=%+v second=%+v", first, second)
	}
	taskID := first["task_id"].(string)
	version := int(first["version"].(float64))

	receiptArgs := map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "same-receipt",
		"receipt_type": "observation", "status": "pass", "criterion_ids": []any{"done"},
	}
	receipt := taskPayload(t, ToolTaskReceipt(context.Background(), deps, receiptArgs))
	replay := taskPayload(t, ToolTaskReceipt(context.Background(), deps, receiptArgs))
	if receipt["receipt_id"] != replay["receipt_id"] || replay["idempotent_replay"] != true {
		t.Fatalf("receipt replay diverged: receipt=%+v replay=%+v", receipt, replay)
	}
	version = int(receipt["version"].(float64))

	checkpointArgs := map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "same-checkpoint", "summary": "verified",
	}
	checkpoint := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, checkpointArgs))
	checkpointReplay := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, checkpointArgs))
	if checkpoint["checkpoint_id"] != checkpointReplay["checkpoint_id"] || checkpointReplay["idempotent_replay"] != true {
		t.Fatalf("checkpoint replay diverged: checkpoint=%+v replay=%+v", checkpoint, checkpointReplay)
	}
	var receipts, checkpoints int
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM task_receipts WHERE task_id=?`, taskID).Scan(&receipts)
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM task_checkpoints WHERE task_id=?`, taskID).Scan(&checkpoints)
	if receipts != 1 || checkpoints != 1 {
		t.Fatalf("receipts=%d checkpoints=%d", receipts, checkpoints)
	}
}

func TestTaskCheckpointResolvesActiveBlocker(t *testing.T) {
	deps := setupTaskTestDB(t)
	taskID, version := openTask(t, deps, "medium")
	blocked := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "blocked-checkpoint",
		"summary": "waiting for an explicit decision",
		"blocker": map[string]any{"reason": "publication approval required", "required_decision": "approve publication"},
	}))
	version = int(blocked["version"].(float64))

	var blockerID string
	if err := deps.db.QueryRow(`SELECT id FROM task_blockers WHERE task_id=? AND status='active'`, taskID).Scan(&blockerID); err != nil {
		t.Fatalf("active blocker missing: %v", err)
	}
	resumed := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "resolved-checkpoint",
		"summary": "publication approved", "resolved_blocker_ids": []any{blockerID},
	}))
	if resumed["status"] != "running" || resumed["resolved_blockers"] != float64(1) {
		t.Fatalf("unexpected resumed checkpoint: %+v", resumed)
	}

	var status string
	var resolvedAt sql.NullString
	if err := deps.db.QueryRow(`SELECT status,resolved_at FROM task_blockers WHERE id=?`, blockerID).Scan(&status, &resolvedAt); err != nil {
		t.Fatal(err)
	}
	if status != "resolved" || !resolvedAt.Valid || resolvedAt.String == "" {
		t.Fatalf("blocker status=%q resolved_at=%q", status, resolvedAt.String)
	}
	validation := taskPayload(t, ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID}))
	if len(validation["active_blockers"].([]any)) != 0 {
		t.Fatalf("resolved blocker still prevents completion: %+v", validation)
	}
}

func TestTaskPlanRejectsDependencyCycle(t *testing.T) {
	deps := setupTaskTestDB(t)
	taskID, version := openTask(t, deps, "medium")
	result := ToolTaskPlan(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version),
		"steps": []any{
			map[string]any{"step_id": "a", "description": "a", "dependencies": []any{"b"}},
			map[string]any{"step_id": "b", "description": "b", "dependencies": []any{"a"}},
		},
	})
	if !result.IsError {
		t.Fatal("cyclic plan unexpectedly accepted")
	}
	var count int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM task_steps WHERE task_id=?`, taskID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cyclic plan left rows behind: count=%d err=%v", count, err)
	}
}

func TestTaskStepConcurrentClaimHasSingleWinner(t *testing.T) {
	deps := setupTaskTestDB(t)
	deps.db.SetMaxOpenConns(4)
	taskID, version := openTask(t, deps, "medium")
	plan := taskPayload(t, ToolTaskPlan(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version),
		"steps": []any{map[string]any{"step_id": "only", "description": "only"}},
	}))
	version = int(plan["version"].(float64))

	start := make(chan struct{})
	results := make(chan ToolResult, 2)
	var wg sync.WaitGroup
	for _, actor := range []string{"agent-a", "agent-b"} {
		wg.Add(1)
		go func(actor string) {
			defer wg.Done()
			<-start
			results <- ToolTaskStep(context.Background(), deps, map[string]any{
				"task_id": taskID, "step_id": "only", "action": "claim",
				"actor_id": actor, "base_version": float64(version),
			})
		}(actor)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := 0
	for result := range results {
		if !result.IsError {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent claims produced %d winners", winners)
	}
	var leases int
	if err := deps.db.QueryRow(`SELECT COUNT(*) FROM task_leases WHERE step_id='only'`).Scan(&leases); err != nil || leases != 1 {
		t.Fatalf("leases=%d err=%v", leases, err)
	}
}

func TestTaskExpiredLeaseCanBeReclaimed(t *testing.T) {
	t.Log("CONTRACT: when a lease has expired, another actor must be able to reclaim the step without manual database repair")
	t.Log("TRIGGER: agent-a owns an active step, its lease expires, then agent-b claims the same step using the current task version")
	deps := setupTaskTestDB(t)
	taskID, version := openTask(t, deps, "medium")
	plan := taskPayload(t, ToolTaskPlan(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "actor_id": "planner",
		"steps": []any{map[string]any{"step_id": "expiring-step", "description": "step whose owner disappears"}},
	}))
	version = int(plan["version"].(float64))
	claimed := taskPayload(t, ToolTaskStep(context.Background(), deps, map[string]any{
		"task_id": taskID, "step_id": "expiring-step", "action": "claim",
		"actor_id": "agent-a", "base_version": float64(version), "lease_seconds": float64(30),
	}))
	version = int(claimed["version"].(float64))
	logTaskRuntimeState(t, deps, taskID, "after-agent-a-claim")

	const expiredAt = "2000-01-01T00:00:00Z"
	if _, err := deps.db.Exec(`UPDATE task_leases SET expires_at=? WHERE task_id=? AND step_id='expiring-step'`, expiredAt, taskID); err != nil {
		t.Fatalf("ARRANGE failed: could not expire lease: %v", err)
	}
	var beforeActor, beforeExpiry, beforeStepStatus string
	if err := deps.db.QueryRow(`SELECT actor_id,expires_at FROM task_leases WHERE task_id=? AND step_id='expiring-step'`, taskID).Scan(&beforeActor, &beforeExpiry); err != nil {
		t.Fatalf("ARRANGE failed: lease missing before reclaim: %v", err)
	}
	if err := deps.db.QueryRow(`SELECT status FROM task_steps WHERE task_id=? AND id='expiring-step'`, taskID).Scan(&beforeStepStatus); err != nil {
		t.Fatalf("ARRANGE failed: step missing before reclaim: %v", err)
	}
	t.Logf("[before-reclaim] actor=%s expires_at=%s step_status=%s task_version=%d", beforeActor, beforeExpiry, beforeStepStatus, version)

	reclaim := ToolTaskStep(context.Background(), deps, map[string]any{
		"task_id": taskID, "step_id": "expiring-step", "action": "claim",
		"actor_id": "agent-b", "base_version": float64(version), "lease_seconds": float64(30),
	})
	t.Logf("[reclaim-response] is_error=%v content=%s", reclaim.IsError, toolResultText(reclaim))
	logTaskRuntimeState(t, deps, taskID, "after-agent-b-reclaim")

	var afterActor, afterStepStatus string
	var leaseCount int
	_ = deps.db.QueryRow(`SELECT COUNT(*) FROM task_leases WHERE task_id=? AND step_id='expiring-step'`, taskID).Scan(&leaseCount)
	if leaseCount == 1 {
		_ = deps.db.QueryRow(`SELECT actor_id FROM task_leases WHERE task_id=? AND step_id='expiring-step'`, taskID).Scan(&afterActor)
	}
	_ = deps.db.QueryRow(`SELECT status FROM task_steps WHERE task_id=? AND id='expiring-step'`, taskID).Scan(&afterStepStatus)
	t.Logf("[reclaim-invariants] lease_count=%d lease_actor=%q step_status=%q expected_actor=agent-b expected_status=active", leaseCount, afterActor, afterStepStatus)

	if reclaim.IsError {
		t.Errorf("expired lease was not reclaimable: %s", toolResultText(reclaim))
	}
	if leaseCount != 1 || afterActor != "agent-b" {
		t.Errorf("expected exactly one replacement lease owned by agent-b; lease_count=%d actor=%q", leaseCount, afterActor)
	}
	if afterStepStatus != "active" {
		t.Errorf("expected reclaimed step to remain active; status=%q", afterStepStatus)
	}
}

func TestTaskArtifactDigestMustBeVerified(t *testing.T) {
	t.Log("CONTRACT: completion must reject an artifact receipt when the referenced artifact is missing or its digest cannot match")
	t.Log("TRIGGER: submit a passing artifact receipt with a deliberately nonexistent URI and a syntactically non-empty fake digest")
	deps := setupTaskTestDB(t)
	verifyingDeps := &taskArtifactVerifierDeps{fakeDeps: deps, err: errors.New("artifact does not exist")}
	opened := taskPayload(t, ToolTaskOpen(context.Background(), deps, map[string]any{
		"collection": "levara", "room": "runtime", "objective": "produce a verifiable artifact",
		"idempotency_key": "artifact-verification", "risk_level": "low",
		"definition_of_done": []any{map[string]any{"criterion_id": "artifact", "description": "artifact exists and SHA-256 matches"}},
	}))
	taskID := opened["task_id"].(string)
	version := int(opened["version"].(float64))
	const missingURI = "file:///definitely/not/present/levara-alpha-artifact.bin"
	const fakeDigest = "sha256:deadbeef"
	t.Logf("[arranged-task] task_id=%s version=%d evidence_uri=%s claimed_digest=%s", taskID, version, missingURI, fakeDigest)

	receiptResult := ToolTaskReceipt(context.Background(), verifyingDeps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "idempotency_key": "missing-artifact-receipt",
		"receipt_type": "artifact", "status": "pass", "criterion_ids": []any{"artifact"},
		"evidence_uri": missingURI, "artifact_digest": fakeDigest, "workspace_revision": "artifact-rev-1",
		"observation": "caller claims the nonexistent artifact is valid",
	})
	t.Logf("[receipt-response] is_error=%v content=%s", receiptResult.IsError, toolResultText(receiptResult))
	if receiptResult.IsError {
		t.Fatalf("ARRANGE failed before validator: artifact receipt was rejected at write time: %s", toolResultText(receiptResult))
	}
	receiptPayload := taskPayload(t, receiptResult)
	version = int(receiptPayload["version"].(float64))
	logTaskRuntimeState(t, deps, taskID, "after-unverifiable-artifact-receipt")

	validationResult := ToolTaskValidate(context.Background(), verifyingDeps, map[string]any{"task_id": taskID, "mode": "completion"})
	validation := taskPayload(t, validationResult)
	validationJSON, _ := json.Marshal(validation)
	t.Logf("[validation-response] %s", validationJSON)

	completionResult := ToolTaskComplete(context.Background(), verifyingDeps, map[string]any{
		"task_id": taskID, "expected_version": float64(version), "actor_id": "artifact-test",
	})
	t.Logf("[completion-response] is_error=%v content=%s", completionResult.IsError, toolResultText(completionResult))
	logTaskRuntimeState(t, deps, taskID, "after-completion-attempt")

	var persistedURI, persistedDigest, persistedReceiptStatus, taskStatus string
	_ = deps.db.QueryRow(`SELECT evidence_uri,artifact_digest,status FROM task_receipts WHERE task_id=? AND idempotency_key='missing-artifact-receipt'`, taskID).Scan(&persistedURI, &persistedDigest, &persistedReceiptStatus)
	_ = deps.db.QueryRow(`SELECT status FROM tasks WHERE id=?`, taskID).Scan(&taskStatus)
	t.Logf("[artifact-invariants] persisted_uri=%s persisted_digest=%s receipt_status=%s task_status=%s expected_validation=false expected_task_status_not_completed",
		persistedURI, persistedDigest, persistedReceiptStatus, taskStatus)
	t.Logf("[artifact-verifier] calls=%d evidence=%v configured_error=%v", len(verifyingDeps.calls), verifyingDeps.calls, verifyingDeps.err)

	if valid, _ := validation["valid"].(bool); valid {
		t.Errorf("validator accepted an artifact that cannot exist or match its claimed digest: validation=%s", validationJSON)
	}
	if taskStatus == "completed" {
		t.Errorf("completion succeeded using unverifiable artifact evidence: task_status=%s completion=%s", taskStatus, toolResultText(completionResult))
	}
	if len(verifyingDeps.calls) != 2 {
		t.Errorf("expected verifier to run once for task_validate and once inside task_complete; calls=%d evidence=%v", len(verifyingDeps.calls), verifyingDeps.calls)
	}
}

func TestTaskBootstrapBudgetIsHardLimit(t *testing.T) {
	t.Log("CONTRACT: task_bootstrap must keep the complete serialized snapshot within the requested token budget")
	t.Log("TRIGGER: create many long criteria and steps while the memory list is empty, leaving no memories for the existing trimming path to remove")
	deps := setupTaskTestDB(t)
	criteria := make([]any, 0, 24)
	steps := make([]any, 0, 24)
	longText := strings.Repeat("bounded recovery context must remain compact; ", 8)
	for index := 0; index < 24; index++ {
		criterionID := fmt.Sprintf("criterion-%02d", index)
		criteria = append(criteria, map[string]any{
			"criterion_id": criterionID,
			"description":  fmt.Sprintf("criterion %02d: %s", index, longText),
		})
		steps = append(steps, map[string]any{
			"step_id":       fmt.Sprintf("step-%02d", index),
			"description":   fmt.Sprintf("step %02d: %s", index, longText),
			"criterion_ids": []any{criterionID},
		})
	}
	opened := taskPayload(t, ToolTaskOpen(context.Background(), deps, map[string]any{
		"collection": "levara", "room": "runtime", "objective": "recover inside a strict bootstrap budget",
		"idempotency_key": "bootstrap-hard-budget", "risk_level": "low", "definition_of_done": criteria,
	}))
	taskID := opened["task_id"].(string)
	version := int(opened["version"].(float64))
	planned := taskPayload(t, ToolTaskPlan(context.Background(), deps, map[string]any{
		"task_id": taskID, "base_version": float64(version), "actor_id": "budget-test", "steps": steps,
	}))
	version = int(planned["version"].(float64))
	logTaskRuntimeState(t, deps, taskID, "before-bootstrap")

	const budget = 100
	bootstrapResult := ToolTaskBootstrap(context.Background(), deps, map[string]any{
		"task_id": taskID, "max_tokens": float64(budget),
	})
	t.Logf("[bootstrap-response] is_error=%v content_bytes=%d", bootstrapResult.IsError, len(toolResultText(bootstrapResult)))
	if bootstrapResult.IsError {
		t.Fatalf("bootstrap unexpectedly failed instead of returning a bounded snapshot: %s", toolResultText(bootstrapResult))
	}
	bootstrap := taskPayload(t, bootstrapResult)
	encoded, _ := json.Marshal(bootstrap)
	tokensUsed := int(bootstrap["tokens_used"].(float64))
	criteriaReturned := len(bootstrap["criteria"].([]any))
	stepsReturned := len(bootstrap["steps"].([]any))
	memoriesReturned := len(bootstrap["memories"].([]any))
	t.Logf("[bootstrap-invariants] task_id=%s task_version=%d requested_budget=%d reported_tokens=%d serialized_bytes=%d approximate_tokens_from_bytes=%d criteria=%d steps=%d memories=%d",
		taskID, version, budget, tokensUsed, len(encoded), (len(encoded)+3)/4, criteriaReturned, stepsReturned, memoriesReturned)
	if len(encoded) > 800 {
		t.Logf("[bootstrap-sample] first_800_bytes=%s", string(encoded[:800]))
	} else {
		t.Logf("[bootstrap-sample] full_payload=%s", string(encoded))
	}

	if tokensUsed > budget {
		t.Errorf("bootstrap reported %d tokens for a hard budget of %d", tokensUsed, budget)
	}
	if approximate := (len(encoded) + 3) / 4; approximate > budget {
		t.Errorf("serialized bootstrap requires approximately %d tokens, exceeding budget %d", approximate, budget)
	}
}

func TestTaskToolProfileFeatureFlag(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "")
	if ToolAllowedForMode("long-horizon", "task_open") {
		t.Fatal("task_open exposed while feature disabled")
	}
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "true")
	if !ToolAllowedForMode("long-horizon", "task_open") || !ToolAllowedForMode("long-horizon", "supersede_memory") {
		t.Fatal("long-horizon profile missing enabled tools")
	}
}
