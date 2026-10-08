package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func taskReplayArgs(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	var copied map[string]any
	if err := json.Unmarshal(body, &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

// Compare authoritative rows, including events and checkpoint side effects,
// rather than accepting a tool response as proof of an unchanged ledger.
func taskReplayLedger(t *testing.T, deps Deps, taskID string) string {
	t.Helper()
	state := map[string][][]any{}
	for _, table := range []string{"tasks", "task_receipts", "task_checkpoints", "task_blockers", "task_memory_candidates", "task_events", "task_steps", "task_leases"} {
		selector, order := "task_id", "id"
		if table == "tasks" {
			selector = "id"
		}
		if table == "task_leases" {
			order = "step_id"
		}
		rows, err := deps.DB().QueryContext(context.Background(), deps.Q(fmt.Sprintf("SELECT * FROM %s WHERE %s=$1 ORDER BY %s", table, selector, order)), taskID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for i, value := range values {
				if body, ok := value.([]byte); ok {
					values[i] = string(body)
				}
			}
			state[table] = append(state[table], values)
		}
		readErr := rows.Err()
		closeErr := rows.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func taskReplayConflict(t *testing.T, result ToolResult) {
	t.Helper()
	text := strings.ToLower(toolResultText(result))
	if !result.IsError || !strings.Contains(text, "idempot") || (!strings.Contains(text, "conflict") && !strings.Contains(text, "mismatch")) {
		t.Errorf("different payload must return explicit idempotency conflict: IsError=%v result=%s", result.IsError, text)
	}
}

func taskReplayOpen(t *testing.T, deps Deps) (string, any) {
	t.Helper()
	opened := taskPayload(t, ToolTaskOpen(context.Background(), deps, map[string]any{
		"collection": "replay-fixture", "room": "task-runtime", "objective": "immutable native replay control", "idempotency_key": "open", "risk_level": "low",
		"definition_of_done": []any{map[string]any{"criterion_id": "tests", "description": "tests"}, map[string]any{"criterion_id": "docs", "description": "docs"}},
	}))
	return opened["task_id"].(string), opened["version"]
}

func TestTaskReceiptReplayPayloadConflictNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			deps.DB().SetMaxOpenConns(1)
			taskID, version := taskReplayOpen(t, deps)
			args := map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "receipt", "receipt_type": "command", "status": "pass", "exit_code": 0,
				"criterion_ids": []any{"tests"}, "observation": "observed command", "workspace_revision": "rev-one", "evidence_uri": "file:///fixture/original", "artifact_digest": "sha256:" + strings.Repeat("a", 64), "metadata": map[string]any{"run": "first"}}
			first := taskPayload(t, ToolTaskReceipt(context.Background(), deps, args))
			var persistedID, status, observation string
			if err := deps.DB().QueryRow(deps.Q(`SELECT id,status,observation FROM task_receipts WHERE task_id=$1 AND idempotency_key=$2`), taskID, "receipt").Scan(&persistedID, &status, &observation); err != nil {
				t.Fatal(err)
			}
			if persistedID != first["receipt_id"] || status != "pass" || observation != "observed command" {
				t.Fatal("initial receipt not persisted as acknowledged")
			}
			before := taskReplayLedger(t, deps, taskID)
			replay := taskPayload(t, ToolTaskReceipt(context.Background(), deps, taskReplayArgs(t, args)))
			if replay["receipt_id"] != first["receipt_id"] || replay["version"] != first["version"] || replay["idempotent_replay"] != true {
				t.Fatalf("exact stale-version replay: %v", replay)
			}
			if after := taskReplayLedger(t, deps, taskID); after != before {
				t.Fatal("exact replay changed SQL ledger")
			}
			for _, change := range []struct {
				name, field string
				value       any
			}{
				{"status", "status", "fail"}, {"revision", "workspace_revision", "rev-two"}, {"evidence", "evidence_uri", "file:///fixture/different"}, {"digest", "artifact_digest", "sha256:" + strings.Repeat("b", 64)},
				{"criteria", "criterion_ids", []any{"docs"}}, {"metadata", "metadata", map[string]any{"run": "second"}}, {"observation", "observation", "different observation"}, {"exit", "exit_code", 7},
			} {
				t.Run(change.name, func(t *testing.T) {
					changed := taskReplayArgs(t, args)
					changed["base_version"] = first["version"]
					changed[change.field] = change.value
					taskReplayConflict(t, ToolTaskReceipt(context.Background(), deps, changed))
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Error("conflicting receipt changed SQL ledger")
					}
				})
			}
		})
	}
}

func TestTaskCheckpointReplayPayloadConflictNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			deps.DB().SetMaxOpenConns(1)
			taskID, version := taskReplayOpen(t, deps)
			plan := taskPayload(t, ToolTaskPlan(context.Background(), deps, map[string]any{"task_id": taskID, "base_version": version, "steps": []any{map[string]any{"step_id": "one", "description": "one"}, map[string]any{"step_id": "two", "description": "two"}}}))
			args := map[string]any{"task_id": taskID, "base_version": plan["version"], "idempotency_key": "checkpoint", "step_id": "one", "summary": "original summary", "workspace_revision": "rev-one", "verified": []any{"tests"}, "failed": []any{}, "next_action": "review",
				"blocker": map[string]any{"reason": "decision pending", "required_decision": "approve"}, "memory_candidates": []any{map[string]any{"key": "candidate", "value": "original value", "room": "task-runtime", "hall": "discovery", "evidence_receipt_ids": []any{}}}}
			first := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, args))
			var persistedID, summary, blockerID string
			if err := deps.DB().QueryRow(deps.Q(`SELECT id,summary FROM task_checkpoints WHERE task_id=$1 AND idempotency_key=$2`), taskID, "checkpoint").Scan(&persistedID, &summary); err != nil {
				t.Fatal(err)
			}
			if persistedID != first["checkpoint_id"] || summary != "original summary" {
				t.Fatal("initial checkpoint not persisted as acknowledged")
			}
			if err := deps.DB().QueryRow(deps.Q(`SELECT id FROM task_blockers WHERE task_id=$1 AND status='active'`), taskID).Scan(&blockerID); err != nil {
				t.Fatal(err)
			}
			var candidateValue string
			if err := deps.DB().QueryRow(deps.Q(`SELECT value FROM task_memory_candidates WHERE task_id=$1 AND memory_key=$2`), taskID, "candidate").Scan(&candidateValue); err != nil || candidateValue != "original value" {
				t.Fatalf("initial candidate: %q err=%v", candidateValue, err)
			}
			before := taskReplayLedger(t, deps, taskID)
			replay := taskPayload(t, ToolTaskCheckpoint(context.Background(), deps, taskReplayArgs(t, args)))
			if replay["checkpoint_id"] != first["checkpoint_id"] || replay["version"] != first["version"] || replay["idempotent_replay"] != true {
				t.Fatalf("exact stale-version replay: %v", replay)
			}
			if after := taskReplayLedger(t, deps, taskID); after != before {
				t.Fatal("exact checkpoint replay changed SQL ledger")
			}
			for _, change := range []struct {
				name, field string
				value       any
			}{
				{"summary", "summary", "different summary"}, {"revision", "workspace_revision", "rev-two"}, {"verified", "verified", []any{"docs"}}, {"failed", "failed", []any{"docs"}}, {"next", "next_action", "different action"}, {"step", "step_id", "two"},
				{"blocker", "blocker", map[string]any{"reason": "different decision", "required_decision": "reject"}}, {"resolve", "resolved_blocker_ids", []any{blockerID}},
				{"candidate", "memory_candidates", []any{map[string]any{"key": "candidate", "value": "replacement value", "room": "task-runtime", "hall": "discovery", "evidence_receipt_ids": []any{}}}},
			} {
				t.Run(change.name, func(t *testing.T) {
					changed := taskReplayArgs(t, args)
					changed["base_version"] = first["version"]
					changed[change.field] = change.value
					taskReplayConflict(t, ToolTaskCheckpoint(context.Background(), deps, changed))
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Error("conflicting checkpoint changed SQL ledger")
					}
				})
			}
		})
	}
}

func TestTaskReplayNormalizationAndLegacyNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			deps.DB().SetMaxOpenConns(1)
			for _, checkpoint := range []bool{false, true} {
				t.Run(fmt.Sprintf("checkpoint=%v", checkpoint), func(t *testing.T) {
					taskID, version := taskReplayOpenWithKey(t, deps, fmt.Sprintf("normalization-%v", checkpoint))
					args := map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "same", "workspace_revision": "rev"}
					call := ToolTaskReceipt
					table, idKey := "task_receipts", "receipt_id"
					if checkpoint {
						call = ToolTaskCheckpoint
						table, idKey = "task_checkpoints", "checkpoint_id"
						args["summary"] = " original "
						args["memory_candidates"] = []any{map[string]any{"key": "k", "value": "v", "room": "task-runtime", "hall": "discovery", "ignored": "first"}}
					} else {
						args["receipt_type"] = "command"
						args["status"] = "pass"
						args["criterion_ids"] = []any{" tests "}
						args["metadata"] = json.RawMessage("{\"b\":2,\"a\":{\"y\":2,\"x\":1}}")
					}
					first := taskPayload(t, call(context.Background(), deps, args))
					replay := taskReplayArgs(t, args)
					replay["actor_id"] = " anonymous "
					replay["ignored"] = "does not affect persistence"
					if checkpoint {
						replay["verified"] = []any{}
						replay["failed"] = []any{}
						replay["resolved_blocker_ids"] = []any{}
						replay["memory_candidates"] = []any{map[string]any{"key": " k ", "value": " v ", "room": " task-runtime ", "hall": " discovery ", "ignored": "changed", "evidence_receipt_ids": []any{}}}
						replay["blocker"] = map[string]any{"reason": "", "required_decision": "ignored empty reason"}
					} else {
						replay["criterion_ids"] = []any{"tests"}
						replay["metadata"] = json.RawMessage("{ \"a\": { \"x\":1, \"y\":2 }, \"b\":2 }")
					}
					before := taskReplayLedger(t, deps, taskID)
					exact := taskPayload(t, call(context.Background(), deps, replay))
					if exact[idKey] != first[idKey] || exact["idempotent_replay"] != true || exact["version"] != first["version"] {
						t.Fatalf("normalized replay differs: %+v", exact)
					}
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Fatal("normalized replay changed ledger")
					}
					changed := taskReplayArgs(t, replay)
					changed["actor_id"] = "different-actor"
					taskReplayConflict(t, call(context.Background(), deps, changed))
					if !checkpoint {
						changed = taskReplayArgs(t, replay)
						changed["exit_code"] = 0
						taskReplayConflict(t, call(context.Background(), deps, changed))
					}
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Fatal("identity/nullable-exit conflict changed ledger")
					}
					if _, err := deps.DB().Exec(deps.Q("UPDATE "+table+" SET request_digest='' WHERE task_id=$1"), taskID); err != nil {
						t.Fatal(err)
					}
					before = taskReplayLedger(t, deps, taskID)
					legacy := call(context.Background(), deps, replay)
					if !legacy.IsError || !strings.Contains(toolResultText(legacy), "unverifiable") {
						t.Fatalf("legacy replay claimed equivalence: %+v", legacy)
					}
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Fatal("legacy rejection changed ledger")
					}
				})
			}
		})
	}
}

func taskReplayOpenWithKey(t *testing.T, deps Deps, key string) (string, any) {
	t.Helper()
	opened := taskPayload(t, ToolTaskOpen(context.Background(), deps, map[string]any{
		"collection": "replay-fixture", "room": "task-runtime", "objective": "normalized replay control", "idempotency_key": key, "risk_level": "low",
		"definition_of_done": []any{map[string]any{"criterion_id": "tests", "description": "tests"}},
	}))
	return opened["task_id"].(string), opened["version"]
}

func TestTaskValidationUnavailableTablesNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, table := range []string{"task_receipts", "task_criteria", "task_steps", "task_blockers", "task_leases"} {
				t.Run(table, func(t *testing.T) {
					deps := workerTestDeps(t, dialect)
					deps.DB().SetMaxOpenConns(1)
					taskID, version := taskReplayOpen(t, deps)
					recorded := taskPayload(t, ToolTaskReceipt(context.Background(), deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "pass", "receipt_type": "observation", "status": "pass", "criterion_ids": []any{"tests", "docs"}}))
					version = recorded["version"]
					positive := taskPayload(t, ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID, "mode": "completion"}))
					if positive["valid"] != true {
						t.Fatalf("invalid positive control: %+v", positive)
					}
					before := taskReplayLedger(t, deps, taskID)
					if _, err := deps.DB().Exec("ALTER TABLE " + table + " RENAME TO unavailable_" + table); err != nil {
						t.Fatal(err)
					}
					validation := ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID, "mode": "completion"})
					completion := ToolTaskComplete(context.Background(), deps, map[string]any{"task_id": taskID, "expected_version": version})
					if _, err := deps.DB().Exec("ALTER TABLE unavailable_" + table + " RENAME TO " + table); err != nil {
						t.Fatal(err)
					}
					if !validation.IsError {
						t.Errorf("unavailable authoritative table admitted validation: %+v", validation)
					}
					if !completion.IsError {
						t.Errorf("unavailable authoritative table admitted completion: %+v", completion)
					}
					if after := taskReplayLedger(t, deps, taskID); after != before {
						t.Error("failed validation/completion mutated ledger")
					}
					if validation.IsError && completion.IsError {
						restored := taskPayload(t, ToolTaskComplete(context.Background(), deps, map[string]any{"task_id": taskID, "expected_version": version}))
						if restored["ok"] != true {
							t.Fatalf("restored positive completion failed: %+v", restored)
						}
					}
				})
			}
		})
	}
}

func TestTaskPolicyMatrixNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			deps.DB().SetMaxOpenConns(1)
			ctx := context.Background()
			opened := taskPayload(t, ToolTaskOpen(ctx, deps, map[string]any{"collection": "policy-fixture", "room": "task-runtime", "objective": "native policy matrix", "idempotency_key": "policy", "risk_level": "high",
				"definition_of_done": []any{map[string]any{"criterion_id": "tests", "description": "tests"}}}))
			taskID, version := opened["task_id"].(string), opened["version"]
			before := taskReplayLedger(t, deps, taskID)
			cycle := ToolTaskPlan(ctx, deps, map[string]any{"task_id": taskID, "base_version": version, "steps": []any{
				map[string]any{"step_id": "a", "description": "a", "dependencies": []any{"b"}}, map[string]any{"step_id": "b", "description": "b", "dependencies": []any{"a"}},
			}})
			if !cycle.IsError {
				t.Fatal("native dependency cycle accepted")
			}
			if after := taskReplayLedger(t, deps, taskID); after != before {
				t.Fatal("cycle rejection changed ledger")
			}
			receipt := func(key, typ, status, rev string) {
				r := taskPayload(t, ToolTaskReceipt(ctx, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": key, "receipt_type": typ, "status": status, "criterion_ids": []any{"tests"}, "workspace_revision": rev, "exit_code": 0}))
				version = r["version"]
			}
			check := func(want bool) map[string]any {
				t.Helper()
				v := taskPayload(t, ToolTaskValidate(ctx, deps, map[string]any{"task_id": taskID, "mode": "completion"}))
				if v["valid"] != want {
					t.Fatalf("policy valid=%v want%v: %+v", v["valid"], want, v)
				}
				return v
			}
			reject := func() {
				t.Helper()
				state := taskReplayLedger(t, deps, taskID)
				r := taskPayload(t, ToolTaskComplete(ctx, deps, map[string]any{"task_id": taskID, "expected_version": version}))
				if r["ok"] != false {
					t.Fatalf("policy completion accepted: %+v", r)
				}
				if after := taskReplayLedger(t, deps, taskID); after != state {
					t.Fatal("denied policy completion changed ledger")
				}
			}
			receipt("command", "command", "pass", "current")
			v := check(false)
			if v["audit_required"] != true || v["reviewer_satisfied"] != false {
				t.Fatal("high risk reviewer not required")
			}
			reject()
			receipt("old-review", "reviewer", "pass", "old")
			receipt("current-command", "command", "pass", "current")
			v = check(false)
			if v["reviewer_satisfied"] != false {
				t.Fatal("stale reviewer admitted")
			}
			reject()
			receipt("failed-review", "reviewer", "fail", "current")
			v = check(false)
			if v["reviewer_satisfied"] != false {
				t.Fatal("failed reviewer admitted")
			}
			reject()
			receipt("current-review", "reviewer", "pass", "current")
			check(true)
			blocked := taskPayload(t, ToolTaskCheckpoint(ctx, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "blocked", "summary": "policy decision pending", "workspace_revision": "current", "blocker": map[string]any{"reason": "requires decision"}}))
			version = blocked["version"]
			v = check(false)
			if len(v["active_blockers"].([]any)) != 1 {
				t.Fatal("active blocker missing")
			}
			reject()
			blockerID := v["active_blockers"].([]any)[0].(string)
			cleared := taskPayload(t, ToolTaskCheckpoint(ctx, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "resolved", "summary": "decision resolved", "resolved_blocker_ids": []any{blockerID}}))
			version = cleared["version"]
			check(true)
			done := taskPayload(t, ToolTaskComplete(ctx, deps, map[string]any{"task_id": taskID, "expected_version": version}))
			if done["ok"] != true {
				t.Fatalf("current positive policy completion failed: %+v", done)
			}
		})
	}
}

func TestTaskValidationScanFailureNative(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			deps.DB().SetMaxOpenConns(1)
			taskID, version := taskReplayOpen(t, deps)
			taskPayload(t, ToolTaskReceipt(context.Background(), deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "scan", "receipt_type": "observation", "status": "pass", "criterion_ids": []any{"tests", "docs"}}))
			if _, err := deps.DB().Exec(deps.Q("UPDATE task_receipts SET receipt_type=NULL WHERE task_id=$1"), taskID); err != nil {
				t.Fatal(err)
			}
			before := taskReplayLedger(t, deps, taskID)
			result := ToolTaskValidate(context.Background(), deps, map[string]any{"task_id": taskID})
			if !result.IsError {
				t.Fatalf("partial receipt scan was reported as complete validation: %+v", result)
			}
			if after := taskReplayLedger(t, deps, taskID); after != before {
				t.Fatal("failed scan changed ledger")
			}
		})
	}
}
