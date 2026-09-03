package mcp

import (
	"context"
	"strings"
	"testing"
)

// Regression tests for the 2026-09-03 review:
//   - M1: task_open idempotent reopen must work without resending
//     definition_of_done (validation runs only for genuinely new tasks).
//   - H1: every task tool returns a tool error — never panics — when
//     deps.DB() is nil (WAL-only profile).

func TestTaskOpenReopenWithoutDefinitionOfDone(t *testing.T) {
	deps := setupTaskTestDB(t)
	ctx := context.Background()

	openArgs := map[string]any{
		"collection":     "levara",
		"room":           "task-runtime",
		"objective":      "reopen compatibility",
		"idempotency_key": "reopen-m1",
		"definition_of_done": []any{
			map[string]any{"criterion_id": "done", "description": "all good"},
		},
	}
	first := ToolTaskOpen(ctx, deps, openArgs)
	firstPayload := taskPayload(t, first)

	// Reopen WITHOUT definition_of_done: must return the existing task.
	reopenArgs := map[string]any{
		"collection":      "levara",
		"room":            "task-runtime",
		"objective":       "reopen compatibility",
		"idempotency_key": "reopen-m1",
	}
	second := ToolTaskOpen(ctx, deps, reopenArgs)
	secondPayload := taskPayload(t, second)
	if secondPayload["task_id"] != firstPayload["task_id"] {
		t.Fatalf("reopen task_id=%v, want %v", secondPayload["task_id"], firstPayload["task_id"])
	}
	if secondPayload["reopened"] != true {
		t.Fatalf("reopen payload missing reopened flag: %v", secondPayload)
	}

	// A genuinely new task still requires definition_of_done.
	missing := ToolTaskOpen(ctx, deps, map[string]any{
		"collection": "levara", "room": "task-runtime",
		"objective": "new task", "idempotency_key": "fresh-key",
	})
	if !missing.IsError || !strings.Contains(missing.Content[0].Text, "definition_of_done") {
		t.Fatalf("new task without DoD: got %+v, want definition_of_done error", missing)
	}
}

func TestTaskToolsReturnErrorNotPanicOnNilDB(t *testing.T) {
	ctx := context.Background()
	deps := nilDBDeps{}

	cases := []struct {
		name string
		call func() ToolResult
	}{
		{"task_open", func() ToolResult {
			return ToolTaskOpen(ctx, deps, map[string]any{
				"collection": "c", "room": "r", "objective": "o",
				"idempotency_key": "k",
				"definition_of_done": []any{
					map[string]any{"criterion_id": "d", "description": "x"},
				},
			})
		}},
		{"task_plan", func() ToolResult {
			return ToolTaskPlan(ctx, deps, map[string]any{"task_id": "t", "base_version": 1, "steps": []any{map[string]any{"step_id": "s", "description": "d"}}})
		}},
		{"task_step", func() ToolResult {
			return ToolTaskStep(ctx, deps, map[string]any{"task_id": "t", "base_version": 1, "step_id": "s", "action": "claim", "actor_id": "a"})
		}},
		{"task_receipt", func() ToolResult {
			return ToolTaskReceipt(ctx, deps, map[string]any{"task_id": "t", "base_version": 1, "receipt_type": "observation", "status": "passed", "idempotency_key": "k"})
		}},
		{"task_checkpoint", func() ToolResult {
			return ToolTaskCheckpoint(ctx, deps, map[string]any{"task_id": "t", "base_version": 1, "summary": "s", "idempotency_key": "k"})
		}},
		{"task_validate", func() ToolResult {
			return ToolTaskValidate(ctx, deps, map[string]any{"task_id": "t"})
		}},
		{"task_bootstrap", func() ToolResult {
			return ToolTaskBootstrap(ctx, deps, map[string]any{"task_id": "t"})
		}},
		{"task_complete", func() ToolResult {
			return ToolTaskComplete(ctx, deps, map[string]any{"task_id": "t", "expected_version": 1})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A panic here crashes the server process — the test fails by panicking.
			res := tc.call()
			if !res.IsError {
				t.Fatalf("%s on nil DB succeeded, want 'database not configured' error", tc.name)
			}
			if !strings.Contains(res.Content[0].Text, "database not configured") {
				t.Fatalf("%s error text = %q, want database-not-configured", tc.name, res.Content[0].Text)
			}
		})
	}
}
