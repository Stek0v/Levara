package http

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

func executorTestDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	var db *sql.DB
	if dialect == "postgres" {
		db = syncTruthPostgresDB(t)
	} else {
		SetDBProvider(DBSQLite)
		var err error
		db, err = sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "executor.db")+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close(); SetDBProvider(DBPostgres) })
		if err := MigrateSchema(db); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`INSERT INTO principals(id,type) VALUES ('executor-owner','user')`, `INSERT INTO users(id,email,hashed_password,is_active) VALUES ('executor-owner','executor@test.invalid','unused',true)`, `INSERT INTO datasets(id,name,owner_id) VALUES ('p','executor project','executor-owner')`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func executorPayload(t *testing.T, r mcp.ToolResult) map[string]any {
	t.Helper()
	if r.IsError || len(r.Content) != 1 {
		t.Fatalf("MCP error: %+v", r)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func executorAction(tool, rel, text string) map[string]any {
	args := map[string]any{"project_id": "p", "path": rel}
	pointer := "/text"
	var expected any = text
	if tool == "workspace_write" {
		args["text"] = text
		args["index"] = false
		pointer = "/bytes"
		expected = len([]byte(text))
	}
	return map[string]any{"kind": "mcp_tool", "name": tool, "arguments": args, "assertions": []any{map[string]any{"pointer": pointer, "equals": expected}}}
}
func executorFixture(t *testing.T, db *sql.DB, actions []map[string]any) (APIConfig, context.Context, string) {
	t.Helper()
	root := t.TempDir()
	project := actions[0]["arguments"].(map[string]any)["project_id"].(string)
	projectDir := filepath.Join("projects", workspace.SafeID(project), "main/docs")
	if err := os.MkdirAll(filepath.Join(root, projectDir), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(fmt.Sprintf("allowed_tools: [workspace_read, workspace_write]\nallowed_paths: [%s]\n", filepath.ToSlash(projectDir)))
	if err := os.WriteFile(filepath.Join(root, "authority.yaml"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	cfg := APIConfig{DB: db, WorkspacePath: root}
	h := NewMCPDeps(cfg)
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "executor-owner")
	opened := executorPayload(t, mcp.ToolTaskOpen(ctx, h, map[string]any{"collection": "tests", "room": "task-runtime", "objective": "verify workspace effects", "idempotency_key": fmt.Sprint(time.Now().UnixNano()), "risk_level": "low", "authority": map[string]any{"auto_run": true, "allowed_tools": []any{"workspace_read", "workspace_write"}, "manifest": "authority.yaml", "manifest_sha256": hex.EncodeToString(sum[:]), "max_step_attempts": 1}, "definition_of_done": []any{map[string]any{"criterion_id": "verified", "description": "actual result matches"}}}))
	steps := []any{}
	for i, action := range actions {
		step := map[string]any{"step_id": fmt.Sprint(i), "description": "perform declared action", "criterion_ids": []any{"verified"}, "action": action}
		if i > 0 {
			step["dependencies"] = []any{fmt.Sprint(i - 1)}
		}
		steps = append(steps, step)
	}
	executorPayload(t, mcp.ToolTaskPlan(ctx, h, map[string]any{"task_id": opened["task_id"], "base_version": opened["version"], "steps": steps}))
	return cfg, ctx, opened["task_id"].(string)
}
func executorStart(t *testing.T, cfg APIConfig, executor mcp.TaskStepExecutor, ctx context.Context) *mcp.TaskWorker {
	t.Helper()
	worker := mcp.NewTaskWorker(NewMCPDeps(cfg), executor, mcp.TaskWorkerConfig{PollInterval: 5 * time.Millisecond, LeaseSeconds: 30, MaxStalledRounds: 10000})
	worker.Start(ctx)
	t.Cleanup(worker.Stop)
	return worker
}
func executorWait(t *testing.T, db *sql.DB, id, state string, want int) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		var n int
		err := db.QueryRow(Q(`SELECT COUNT(*) FROM task_steps WHERE task_id=$1 AND status=$2`), id, state).Scan(&n)
		if err == nil && n == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	rows, err := db.Query(Q(`SELECT event_type,payload_json FROM task_events WHERE task_id=$1`), id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var k, v string
			rows.Scan(&k, &v)
			t.Log(k, v)
		}
	}
	t.Fatalf("task %s did not reach %s count %d", id, state, want)
}

func TestTaskExecutorThreeObservableWorkspaceSteps(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			cfg, ctx, id := executorFixture(t, db, []map[string]any{executorAction("workspace_write", "docs/first.md", "Привет\n"), executorAction("workspace_read", "docs/first.md", "Привет\n"), executorAction("workspace_write", "docs/second.md", "verified\n")})
			worker := executorStart(t, cfg, NewTaskExecutor(cfg), context.Background())
			executorWait(t, db, id, "passed", 3)
			worker.Stop()
			for name, want := range map[string]string{"first.md": "Привет\n", "second.md": "verified\n"} {
				data, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "projects/p/main/docs", name))
				if err != nil || string(data) != want {
					t.Fatalf("artifact=%q err=%v", data, err)
				}
			}
			var n int
			if err := db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass' AND receipt_type='observation'`), id).Scan(&n); err != nil || n != 3 {
				t.Fatalf("receipts=%d %v", n, err)
			}
			bootstrap := executorPayload(t, mcp.ToolTaskBootstrap(ctx, NewMCPDeps(cfg), map[string]any{"task_id": id, "max_tokens": 4000}))
			steps := bootstrap["steps"].([]any)
			if steps[0].(map[string]any)["action"] == nil {
				t.Fatal("bootstrap lost executable action")
			}
			validation := executorPayload(t, mcp.ToolTaskValidate(ctx, NewMCPDeps(cfg), map[string]any{"task_id": id}))
			if validation["valid"] != true {
				t.Fatalf("validation=%v", validation)
			}
		})
	}
}

func TestTaskExecutorDeniesBeforeWorkspaceEffect(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			for _, kind := range []string{"path", "symlink", "hardlink", "tool", "index", "unknown-argument", "manifest", "inactive", "profile", "lease-replaced", "cancelled"} {
				t.Run(kind, func(t *testing.T) {
					action := executorAction("workspace_write", "docs/result.md", "forbidden")
					switch kind {
					case "path":
						action["arguments"].(map[string]any)["path"] = "../outside.md"
					case "tool":
						action["name"] = "add"
					case "index":
						action["arguments"].(map[string]any)["index"] = true
					case "unknown-argument":
						action["arguments"].(map[string]any)["generation"] = "would-index"
					}
					cfg, ownerCtx, id := executorFixture(t, db, []map[string]any{action})
					outside := filepath.Join(t.TempDir(), "outside.md")
					if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
						t.Fatal(err)
					}
					target := filepath.Join(cfg.WorkspacePath, "projects/p/main/docs/result.md")
					switch kind {
					case "symlink":
						if err := os.Symlink(outside, target); err != nil {
							t.Fatal(err)
						}
					case "hardlink":
						if err := os.Link(outside, target); err != nil {
							t.Fatal(err)
						}
					case "manifest":
						os.WriteFile(filepath.Join(cfg.WorkspacePath, "authority.yaml"), []byte("allowed_tools: [workspace_write]\nallowed_paths: [. ]"), 0600)
					case "inactive":
						db.Exec(`UPDATE users SET is_active=false WHERE id='executor-owner'`)
						defer db.Exec(`UPDATE users SET is_active=true WHERE id='executor-owner'`)
					case "profile":
						t.Setenv("LEVARA_MCP_TOOLSET", "memory")
					}
					h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
					executor := NewTaskExecutor(cfg)
					if kind == "lease-replaced" || kind == "cancelled" {
						executor = mcp.NewTaskExecutor(h, func(ctx context.Context, e *mcp.TaskExecution) (mcp.ToolResult, error) {
							if kind == "cancelled" {
								cancelCtx, cancel := context.WithCancel(ctx)
								cancel()
								return h.executeTool(cancelCtx, nil, e.Action.Name, e.Action.Arguments), nil
							}
							var version int
							db.QueryRow(Q(`SELECT version FROM tasks WHERE id=$1`), id).Scan(&version)
							executorPayload(t, mcp.ToolTaskStep(ownerCtx, h, map[string]any{"task_id": id, "step_id": "0", "actor_id": e.ActorID, "action": "release", "base_version": version}))
							executorPayload(t, mcp.ToolTaskStep(ownerCtx, h, map[string]any{"task_id": id, "step_id": "0", "actor_id": "replacement", "action": "claim", "base_version": version + 1}))
							return h.executeTool(ctx, nil, e.Action.Name, e.Action.Arguments), nil
						})
					}
					worker := executorStart(t, cfg, executor, context.Background())
					if kind == "lease-replaced" {
						until := time.Now().Add(5 * time.Second)
						for time.Now().Before(until) {
							var actor string
							db.QueryRow(Q(`SELECT actor_id FROM task_leases WHERE task_id=$1`), id).Scan(&actor)
							if actor == "replacement" {
								break
							}
							time.Sleep(5 * time.Millisecond)
						}
						worker.Stop()
					} else {
						executorWait(t, db, id, "failed", 1)
						worker.Stop()
					}
					if data, err := os.ReadFile(outside); err != nil || string(data) != "unchanged" {
						t.Fatalf("outside changed: %q %v", data, err)
					}
					if kind != "symlink" && kind != "hardlink" {
						if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("forbidden artifact exists: %v", err)
						}
					}
					var n int
					db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'`), id).Scan(&n)
					if n != 0 {
						t.Fatal("denied action has passing receipt")
					}
				})
			}
		})
	}
}

func TestTaskExecutorRetryAndRestartAfterUnreceiptedWrite(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			for _, kind := range []string{"retry", "restart", "restart-create", "restart-update", "restart-conflict"} {
				t.Run(kind, func(t *testing.T) {
					action := executorAction("workspace_write", "docs/retry.md", "once")
					if kind == "restart-create" || kind == "restart-conflict" {
						action["arguments"].(map[string]any)["expected_file_digest"] = ""
					} else if kind == "restart-update" {
						action["arguments"].(map[string]any)["expected_file_digest"] = digestBytes([]byte("before"))
					}
					cfg, _, id := executorFixture(t, db, []map[string]any{action})
					artifact := filepath.Join(cfg.WorkspacePath, "projects/p/main/docs/retry.md")
					if kind == "restart-update" {
						if err := os.WriteFile(artifact, []byte("before"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					var authority string
					db.QueryRow(Q(`SELECT authority_json FROM tasks WHERE id=$1`), id).Scan(&authority)
					authority = strings.Replace(authority, `"max_step_attempts":1`, `"max_step_attempts":2`, 1)
					if _, err := db.Exec(Q(`UPDATE tasks SET authority_json=$1 WHERE id=$2`), authority, id); err != nil {
						t.Fatal(err)
					}
					h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					var calls atomic.Int32
					wrote := make(chan struct{})
					executor := mcp.NewTaskExecutor(h, func(callCtx context.Context, e *mcp.TaskExecution) (mcp.ToolResult, error) {
						if calls.Add(1) == 1 {
							if kind == "retry" {
								return mcp.ToolResult{}, errors.New("transient unavailable")
							}
							result := h.executeTool(callCtx, nil, e.Action.Name, e.Action.Arguments)
							cancel()
							close(wrote)
							return result, nil
						}
						return h.executeTool(callCtx, nil, e.Action.Name, e.Action.Arguments), nil
					})
					worker := executorStart(t, cfg, executor, ctx)
					if strings.HasPrefix(kind, "restart") {
						select {
						case <-wrote:
						case <-time.After(5 * time.Second):
							t.Fatal("first write did not occur")
						}
						worker.Stop()
						var n int
						db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1`), id).Scan(&n)
						if n != 0 {
							t.Fatal("cancelled result became receipt")
						}
						if _, err := db.Exec(Q(`UPDATE task_leases SET expires_at=$1 WHERE task_id=$2`), time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), id); err != nil {
							t.Fatal(err)
						}
						if kind == "restart-conflict" {
							if err := os.WriteFile(artifact, []byte("changed externally"), 0600); err != nil {
								t.Fatal(err)
							}
						}
						worker = executorStart(t, cfg, NewTaskExecutor(cfg), context.Background())
					}
					if kind == "restart-conflict" {
						executorWait(t, db, id, "failed", 1)
						worker.Stop()
						data, err := os.ReadFile(artifact)
						if err != nil || string(data) != "changed externally" {
							t.Fatalf("conflict overwritten: %q %v", data, err)
						}
						var passed int
						db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'`), id).Scan(&passed)
						if passed != 0 {
							t.Fatal("conflict produced passing receipt")
						}
						return
					}
					executorWait(t, db, id, "passed", 1)
					worker.Stop()
					data, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "projects/p/main/docs/retry.md"))
					if err != nil || string(data) != "once" {
						t.Fatalf("artifact %q %v", data, err)
					}
					var n, attempts int
					db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1`), id).Scan(&n)
					db.QueryRow(Q(`SELECT attempts FROM task_steps WHERE task_id=$1`), id).Scan(&attempts)
					if n != 1 || attempts != 2 {
						t.Fatalf("receipts=%d attempts=%d", n, attempts)
					}
				})
			}
		})
	}
}

func TestTaskExecutorActionMigrationPreservesManualPlan(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			deps := NewMCPDeps(APIConfig{DB: db})
			ctx := context.WithValue(context.Background(), mcp.UserIDKey, "executor-owner")
			opened := executorPayload(t, mcp.ToolTaskOpen(ctx, deps, map[string]any{"collection": "tests", "room": "task-runtime", "objective": "manual compatibility", "idempotency_key": "legacy", "definition_of_done": []any{map[string]any{"criterion_id": "verified", "description": "manual evidence"}}}))
			executorPayload(t, mcp.ToolTaskPlan(ctx, deps, map[string]any{"task_id": opened["task_id"], "base_version": opened["version"], "steps": []any{map[string]any{"step_id": "manual", "description": "human executes this"}}}))
			if _, err := db.Exec(`ALTER TABLE task_steps DROP COLUMN action_json`); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := MigrateSchema(db); err != nil {
					t.Fatal(err)
				}
			}
			out := executorPayload(t, mcp.ToolTaskBootstrap(ctx, deps, map[string]any{"task_id": opened["task_id"], "max_tokens": 4000}))
			step := out["steps"].([]any)[0].(map[string]any)
			if step["description"] != "human executes this" || len(step["action"].(map[string]any)) != 0 {
				t.Fatalf("manual step changed: %v", step)
			}
		})
	}
}

func TestTaskExecutorWorkspaceProjectMapping(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			project := "12345678-abcd-4abc-a123-123456789012"
			if _, err := db.Exec(Q(`INSERT INTO datasets(id,name,owner_id) VALUES ($1,'uuid workspace','executor-owner')`), project); err != nil {
				t.Fatal(err)
			}
			for _, collision := range []bool{false, true} {
				t.Run(fmt.Sprint(collision), func(t *testing.T) {
					if collision {
						if _, err := db.Exec(Q(`INSERT INTO datasets(id,name,owner_id) VALUES ($1,'alias workspace','another-owner')`), workspace.SafeID(project)); err != nil {
							t.Fatal(err)
						}
					}
					action := executorAction("workspace_write", "docs/result.md", "safe")
					action["arguments"].(map[string]any)["project_id"] = project
					cfg, _, id := executorFixture(t, db, []map[string]any{action})
					worker := executorStart(t, cfg, NewTaskExecutor(cfg), context.Background())
					status := "passed"
					if collision {
						status = "failed"
					}
					executorWait(t, db, id, status, 1)
					worker.Stop()
					data, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "projects", workspace.SafeID(project), "main/docs/result.md"))
					if collision {
						if !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("ambiguous path effect: %q %v", data, err)
						}
					} else if err != nil || string(data) != "safe" {
						t.Fatalf("UUID artifact: %q %v", data, err)
					}
				})
			}
		})
	}
}
