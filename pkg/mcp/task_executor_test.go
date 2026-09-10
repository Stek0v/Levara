package mcp

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func taskExecutorAction() map[string]any {
	return map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": map[string]any{"project_id": "p", "path": "doc.md"}, "assertions": []any{map[string]any{"pointer": "/text", "equals": "hello"}}}
}

func TestTaskPlanStrictExecutableAction(t *testing.T) {
	cases := []struct {
		name   string
		action any
	}{
		{"missing", nil}, {"kind", map[string]any{"kind": "shell", "name": "workspace_read", "arguments": map[string]any{}, "assertions": []any{map[string]any{"pointer": "/text", "equals": "hello"}}}},
		{"unknown field", map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": map[string]any{}, "assertions": []any{map[string]any{"pointer": "/text", "equals": "hello"}}, "shell": "touch unexpected"}},
		{"null arguments", map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": nil, "assertions": []any{map[string]any{"pointer": "/text", "equals": "hello"}}}},
		{"no assertions", map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": map[string]any{}, "assertions": []any{}}},
		{"missing equals", map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": map[string]any{}, "assertions": []any{map[string]any{"pointer": "/text"}}}},
		{"invalid pointer", map[string]any{"kind": "mcp_tool", "name": "workspace_read", "arguments": map[string]any{}, "assertions": []any{map[string]any{"pointer": "text", "equals": "hello"}}}},
	}
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			for i, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					owner := fmt.Sprintf("strict-action-%d", i)
					ctx := context.WithValue(context.Background(), UserIDKey, owner)
					opened := taskPayload(t, ToolTaskOpen(ctx, deps, map[string]any{"collection": "tests", "room": "task-runtime", "objective": "validate action", "idempotency_key": owner, "authority": map[string]any{"auto_run": true}, "definition_of_done": []any{map[string]any{"criterion_id": "verified", "description": "assert file"}}}))
					step := map[string]any{"step_id": "step", "description": "assert file", "criterion_ids": []any{"verified"}}
					if tc.action != nil {
						step["action"] = tc.action
					}
					result := ToolTaskPlan(ctx, deps, map[string]any{"task_id": opened["task_id"], "base_version": opened["version"], "steps": []any{step}})
					if !result.IsError {
						t.Errorf("invalid executable action accepted: %v", tc.action)
					}
				})
			}
		})
	}
}

func TestTaskLoggingExecutorDoesNotFabricateSuccess(t *testing.T) {
	passed, _, err := NewLoggingStepExecutor().ExecuteStep(context.Background(), "task", "step", "perform work", time.Now().Add(time.Second))
	if passed || err == nil {
		t.Errorf("logging executor fabricated success: passed=%v err=%v", passed, err)
	}
}

func TestTaskExecutorAssertionsAreExact(t *testing.T) {
	cases := []struct {
		pointer, expected, output string
		ok                        bool
	}{
		{"/a~1b/~0", `9007199254740993`, `{"a/b":{"~":9007199254740993}}`, true},
		{"/n", `9007199254740993`, `{"n":9007199254740992}`, false},
		{"/missing", `null`, `{}`, false}, {"/present", `null`, `{"present":null}`, true},
		{"/0", `"x"`, `["x"]`, true}, {"/00", `"x"`, `["x"]`, false},
	}
	for _, c := range cases {
		t.Run(c.pointer+c.expected, func(t *testing.T) {
			a := TaskAction{Assertions: []TaskAssertion{{Pointer: c.pointer, Equals: []byte(c.expected)}}}
			if got := checkTaskAssertions(a, []byte(c.output)); (got == nil) != c.ok {
				t.Fatalf("error=%v want pass=%v", got, c.ok)
			}
		})
	}
}

func TestTaskExecutorReceiptRejectsReplacedLease(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			ctx, id := openWorkerTestTask(t, deps, "receipt-lease", map[string]any{"auto_run": true, "allowed_tools": []any{"workspace_read"}})
			taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "old", "base_version": workerTaskVersion(t, deps, id)}))
			ctx = context.WithValue(ctx, taskLeaseContextKey{}, taskLeaseIdentity{id, "same-step", "old"})
			executor := NewTaskExecutor(deps, func(ctx context.Context, e *TaskExecution) (ToolResult, error) {
				taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "release", "actor_id": "old", "base_version": workerTaskVersion(t, deps, id)}))
				taskPayload(t, ToolTaskStep(ctx, deps, map[string]any{"task_id": id, "step_id": "same-step", "action": "claim", "actor_id": "replacement", "base_version": workerTaskVersion(t, deps, id)}))
				return jsonResult(map[string]any{"text": "hello"}), nil
			})
			passed, _, err := executor.ExecuteStep(ctx, id, "same-step", "", time.Now().Add(time.Second))
			if passed || err == nil {
				t.Fatalf("stale executor success: %v %v", passed, err)
			}
			var n int
			if err := deps.DB().QueryRow(deps.Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1`), id).Scan(&n); err != nil || n != 0 {
				t.Fatalf("receipts=%d err=%v", n, err)
			}
		})
	}
}

func TestTaskListMemoriesDoesNotTurnSQLFailureIntoEvidence(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			deps := workerTestDeps(t, dialect)
			if _, err := deps.DB().Exec(`DROP TABLE memories`); err != nil {
				t.Fatal(err)
			}
			if result := ToolListMemories(context.Background(), deps, map[string]any{}); !result.IsError {
				t.Fatal("failed query returned successful empty evidence")
			}
		})
	}
}

func TestTaskExecutorContractAndVisibility(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	found := false
	for _, tool := range ToolDescriptors() {
		if tool.Name == "task_plan" {
			props := tool.InputSchema["properties"].(map[string]any)
			step := props["steps"].(map[string]any)["items"].(map[string]any)
			action := step["properties"].(map[string]any)["action"].(map[string]any)
			if action["additionalProperties"] != false {
				t.Fatal("action contract accepts unknown fields")
			}
			required := action["required"].([]string)
			if len(required) != 4 {
				t.Fatalf("action required=%v", required)
			}
			found = true
		}
		if tool.Name == "task_receipt" {
			if tool.InputSchema["properties"].(map[string]any)["step_id"] == nil {
				t.Fatal("missing execution receipt binding")
			}
		}
	}
	if !found || !ToolAllowedForMode("full", "task_plan") || ToolAllowedForMode("memory", "task_plan") {
		t.Fatal("task profile visibility mismatch")
	}
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "0")
	if ToolAllowedForMode("full", "task_plan") {
		t.Fatal("task action exposed while runtime disabled")
	}
}
