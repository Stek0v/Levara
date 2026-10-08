package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

// Draft destination: internal/http/task_executor_lock_deadline_test.go.
// This proves real worker ACTION deadline expiry, not persisted lease expiry:
// the production worker reserves 5s before lease expiry for its final transition.
type taskLockDeadlineEntry struct {
	TaskID      string
	Context     context.Context
	LeaseExpiry time.Time
	Error       error
}
type taskLockDeadlineReturn struct {
	ContextError error
	ToolError    bool
	ErrorText    string
}

func taskLockDeadlineTime(value any) (time.Time, error) {
	if instant, ok := value.(time.Time); ok {
		return instant.UTC(), nil
	}
	text := timestampString(value)
	return time.Parse(time.RFC3339Nano, text)
}
func taskLockDeadlineOpen(t *testing.T, cfg APIConfig, owner context.Context, key, text string) string {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "authority.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(manifest)
	h := NewMCPDeps(cfg)
	opened := executorPayload(t, mcp.ToolTaskOpen(owner, h, map[string]any{
		"collection": "task-lock-deadline", "room": "task-runtime", "objective": "actual project-lock wait honors action deadline",
		"risk_level": "low", "idempotency_key": key,
		"authority": map[string]any{"auto_run": true, "allowed_tools": []any{"workspace_write"},
			"manifest": "authority.yaml", "manifest_sha256": hex.EncodeToString(digest[:]),
			"step_deadline_seconds": 2, "max_step_attempts": 1},
		"definition_of_done": []any{map[string]any{"criterion_id": "verified", "description": "exact workspace write matches"}},
	}))
	action := executorAction("workspace_write", "docs/result.md", text)
	action["arguments"].(map[string]any)["expected_file_digest"] = digestBytes([]byte("before"))
	executorPayload(t, mcp.ToolTaskPlan(owner, h, map[string]any{"task_id": opened["task_id"], "base_version": opened["version"],
		"steps": []any{map[string]any{"step_id": "0", "description": "native bounded write", "criterion_ids": []any{"verified"}, "action": action}},
	}))
	return opened["task_id"].(string)
}
func taskLockDeadlineWorker(t *testing.T, cfg APIConfig, ctx context.Context, taskID string) (*mcp.TaskWorker, <-chan taskLockDeadlineEntry, <-chan taskLockDeadlineReturn) {
	t.Helper()
	entered := make(chan taskLockDeadlineEntry, 1)
	returned := make(chan taskLockDeadlineReturn, 1)
	h := &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
	// Existing dispatcher seam only observes the live executor context/lease and
	// delegates the entire effect to the real production adapter. No fake Fence,
	// filesystem writer, clock, result or receipt replaces the actual path.
	executor := mcp.NewTaskExecutor(h, func(callCtx context.Context, e *mcp.TaskExecution) (mcp.ToolResult, error) {
		observation := taskLockDeadlineEntry{TaskID: e.TaskID, Context: callCtx}
		if e.TaskID != taskID {
			observation.Error = fmt.Errorf("unexpected executable task %s", e.TaskID)
		} else {
			var expiry any
			observation.Error = cfg.DB.QueryRowContext(callCtx, Q("SELECT expires_at FROM task_leases WHERE task_id=$1 AND step_id=$2 AND actor_id=$3"), e.TaskID, e.StepID, e.ActorID).Scan(&expiry)
			if observation.Error == nil {
				observation.LeaseExpiry, observation.Error = taskLockDeadlineTime(expiry)
			}
		}
		entered <- observation
		if observation.Error != nil {
			returned <- taskLockDeadlineReturn{ContextError: observation.Error}
			return mcp.ToolResult{}, observation.Error
		}
		result := h.executeTool(callCtx, nil, e.Action.Name, e.Action.Arguments)
		text := ""
		if len(result.Content) > 0 {
			text = result.Content[0].Text
		}
		returned <- taskLockDeadlineReturn{ContextError: callCtx.Err(), ToolError: result.IsError, ErrorText: text}
		return result, nil
	})
	// One scheduler round finishes after launching the sole eligible task;
	// the next poll comes after its 2s deadline. Pool1 observation therefore
	// cannot be a competing periodic worker query during the asserted wait.
	worker := mcp.NewTaskWorker(h, executor, mcp.TaskWorkerConfig{PollInterval: 3 * time.Second, LeaseSeconds: 30, MaxStalledRounds: 10000})
	worker.Start(ctx)
	t.Cleanup(worker.Stop)
	return worker, entered, returned
}
func taskLockDeadlineObserve(t *testing.T, ctx context.Context, entered <-chan taskLockDeadlineEntry) taskLockDeadlineEntry {
	t.Helper()
	select {
	case observation := <-entered:
		if observation.Error != nil {
			t.Fatal(observation.Error)
		}
		deadline, ok := observation.Context.Deadline()
		if !ok || !deadline.Before(observation.LeaseExpiry) || time.Until(deadline) < 250*time.Millisecond {
			t.Fatalf("missing live shorter action deadline: deadline=%s lease=%s", deadline, observation.LeaseExpiry)
		}
		return observation
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return taskLockDeadlineEntry{}
}
func taskLockDeadlineSQLWait(t *testing.T, cfg APIConfig, ctx context.Context, returned <-chan taskLockDeadlineReturn) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case result := <-returned:
			t.Fatalf("adapter returned while real project lock held: %+v", result)
		case <-ctx.Done():
			t.Fatalf("deadline elapsed before observing SQL-held wait: %v", ctx.Err())
		case <-deadline.C:
			t.Fatal("no retained SQL admission while native project lock held")
		default:
		}
		// Native Fence occupies the sole SQL connection before LockProject. The
		// parent already owns the actual flock/LockFileEx; the valid fresh control
		// below proves this same authority/action reaches the effect after release.
		if cfg.DB.Stats().InUse == 1 {
			return
		}
		runtime.Gosched()
	}
}
func taskLockDeadlineManifestUnchanged(t *testing.T, cfg APIConfig, authority []byte) {
	t.Helper()
	current, err := os.ReadFile(filepath.Join(cfg.WorkspacePath, "authority.yaml"))
	if err != nil || !bytes.Equal(current, authority) {
		t.Fatalf("authority manifest changed: %q error=%v", current, err)
	}
	if _, err := os.Stat(workspaceManifestPath(cfg, "p", "main")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-indexing action created generation manifest: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(cfg.WorkspacePath, "projects", "p", "main", "docs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".task-") {
			t.Fatalf("task temp leaked: %s", entry.Name())
		}
	}
}

// Snapshot every product file and directory except the exact project audit
// directory. Audit is an expected append from executeTool, including failures;
// unrelated .kb namespaces and all authority/index manifests remain compared.
func taskLockDeadlineTree(t *testing.T, cfg APIConfig) map[string]string {
	t.Helper()
	tree := map[string]string{}
	auditDir := workspaceAuditDir(cfg, "p")
	err := filepath.WalkDir(cfg.WorkspacePath, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == auditDir {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(cfg.WorkspacePath, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			tree[rel] = fmt.Sprintf("dir:%o", info.Mode().Perm())
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unexpected special product file %s", rel)
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		tree[rel] = fmt.Sprintf("file:%o:", info.Mode().Perm()) + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
func taskLockDeadlineTreeEquals(t *testing.T, cfg APIConfig, want map[string]string) {
	t.Helper()
	got := taskLockDeadlineTree(t, cfg)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("product tree changed outside expected file and exact project audit directory: got=%q want=%q", got, want)
	}
}
func taskLockDeadlineTreeFile(tree map[string]string, name string, data []byte) map[string]string {
	copyTree := make(map[string]string, len(tree))
	for key, value := range tree {
		copyTree[key] = value
	}
	// All initial fixture files and production task writes use 0600.
	copyTree[name] = "file:600:" + string(data)
	return copyTree
}
func taskLockDeadlineAudit(t *testing.T, cfg APIConfig, failures, successes int) {
	t.Helper()
	response, err := listWorkspaceAuditEvents(cfg, workspaceAuditListRequest{ProjectID: "p", Branch: "main", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	gotFailures, gotSuccesses := 0, 0
	for _, event := range response.Events {
		if event.Source != "mcp" || event.Operation != "write" || event.ProjectID != "p" || event.Branch != "main" || event.UserID != "executor-owner" || event.Access != "write" {
			t.Fatalf("unexpected native audit event: %+v", event)
		}
		switch event.Result {
		case "failure":
			if event.Error != "mcp_error" {
				t.Fatalf("unexpected native failure audit code: %+v", event)
			}
			gotFailures++
		case "success":
			if event.Error != "" {
				t.Fatalf("successful audit contains error: %+v", event)
			}
			gotSuccesses++
		default:
			t.Fatalf("unexpected native audit outcome: %+v", event)
		}
	}
	if gotFailures != failures || gotSuccesses != successes || len(response.Events) != failures+successes {
		t.Fatalf("native audit failures=%d successes=%d total=%d want=%d/%d", gotFailures, gotSuccesses, len(response.Events), failures, successes)
	}
}
func taskLockDeadlineReplaceAuthority(t *testing.T, cfg APIConfig, data []byte) {
	t.Helper()
	file, err := os.CreateTemp(cfg.WorkspacePath, ".authority-")
	if err != nil {
		t.Fatal(err)
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Sync(); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(name, filepath.Join(cfg.WorkspacePath, "authority.yaml")); err != nil {
		t.Fatal(err)
	}
}
func TestTaskExecutorNativeProjectLockActionDeadline(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cfg := APIConfig{DB: db, WorkspacePath: t.TempDir()}
			dir := filepath.Join(cfg.WorkspacePath, "projects", "p", "main", "docs")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			authority := []byte("allowed_tools: [workspace_write]\nallowed_paths: [projects/p/main/docs]\n")
			if err := os.WriteFile(filepath.Join(cfg.WorkspacePath, "authority.yaml"), authority, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(workspaceAuditDir(cfg, "p"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "result.md")
			if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			owner := context.WithValue(ctx, mcp.UserIDKey, "executor-owner")
			expiredID := taskLockDeadlineOpen(t, cfg, owner, "expired-wait", "forbidden")
			release, err := workspace.LockProject(ctx, cfg.WorkspacePath, "p")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			baseline := taskLockDeadlineTree(t, cfg)
			worker, entered, returned := taskLockDeadlineWorker(t, cfg, ctx, expiredID)
			observation := taskLockDeadlineObserve(t, ctx, entered)
			taskLockDeadlineSQLWait(t, cfg, observation.Context, returned)
			// The native project lock remains owned across the real context deadline;
			// no sleep/start-only event is accepted as a successful expiry oracle.
			select {
			case <-observation.Context.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if !errors.Is(observation.Context.Err(), context.DeadlineExceeded) {
				t.Fatalf("wrong clock expiry: %v", observation.Context.Err())
			}
			select {
			case result := <-returned:
				if !errors.Is(result.ContextError, context.DeadlineExceeded) || !result.ToolError {
					t.Fatalf("expired native lock wait result: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			executorWait(t, db, expiredID, "failed", 1)
			worker.Stop()
			if !time.Now().Before(observation.LeaseExpiry) {
				t.Fatal("fixture no longer distinguishes action deadline from lease expiry")
			}
			if db.Stats().InUse != 0 {
				t.Fatalf("deadline retained SQL pool1: %+v", db.Stats())
			}
			if err := db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "before" {
				t.Fatalf("expired action wrote bytes %q error=%v", data, err)
			}
			var receipts, passing int
			if err := db.QueryRowContext(ctx, Q("SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='pass' THEN 1 ELSE 0 END),0) FROM task_receipts WHERE task_id=$1"), expiredID).Scan(&receipts, &passing); err != nil || receipts != 0 || passing != 0 {
				t.Fatalf("expired receipts=%d pass=%d error=%v", receipts, passing, err)
			}
			taskLockDeadlineManifestUnchanged(t, cfg, authority)
			taskLockDeadlineTreeEquals(t, cfg, baseline)
			taskLockDeadlineAudit(t, cfg, 1, 0)
			release()

			// Fresh native grant/action uses the same untouched file and expected digest.
			// Re-establish contention, observe SQL admission, then release before its
			// actual action deadline to prove the positive path is otherwise identical.
			freshID := taskLockDeadlineOpen(t, cfg, owner, "fresh-wait", "fresh")
			releaseFresh, err := workspace.LockProject(ctx, cfg.WorkspacePath, "p")
			if err != nil {
				t.Fatal(err)
			}
			defer releaseFresh()
			freshWorker, freshEntered, freshReturned := taskLockDeadlineWorker(t, cfg, ctx, freshID)
			freshObservation := taskLockDeadlineObserve(t, ctx, freshEntered)
			taskLockDeadlineSQLWait(t, cfg, freshObservation.Context, freshReturned)
			releaseFresh()
			select {
			case result := <-freshReturned:
				if result.ContextError != nil || result.ToolError {
					t.Fatalf("fresh native adapter failed: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			executorWait(t, db, freshID, "passed", 1)
			freshWorker.Stop()
			data, err = os.ReadFile(target)
			if err != nil || string(data) != "fresh" {
				t.Fatalf("fresh native write %q error=%v", data, err)
			}
			if err := db.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'"), freshID).Scan(&passing); err != nil || passing != 1 {
				t.Fatalf("fresh passing receipts=%d error=%v", passing, err)
			}
			if db.Stats().InUse != 0 {
				t.Fatalf("fresh completion leaked SQL pool1: %+v", db.Stats())
			}
			taskLockDeadlineManifestUnchanged(t, cfg, authority)
			taskLockDeadlineTreeEquals(t, cfg, taskLockDeadlineTreeFile(baseline, filepath.Join("projects", "p", "main", "docs", "result.md"), []byte("fresh")))
			taskLockDeadlineAudit(t, cfg, 1, 1)
		})
	}
}

func TestTaskExecutorManifestChangedAfterSQLAdmissionBeforeFirstRead(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			db.SetMaxOpenConns(1)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cfg := APIConfig{DB: db, WorkspacePath: t.TempDir()}
			dir := filepath.Join(cfg.WorkspacePath, "projects", "p", "main", "docs")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			authority := []byte("allowed_tools: [workspace_write]\nallowed_paths: [projects/p/main/docs]\n")
			if err := os.WriteFile(filepath.Join(cfg.WorkspacePath, "authority.yaml"), authority, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(workspaceAuditDir(cfg, "p"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "result.md")
			if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			owner := context.WithValue(ctx, mcp.UserIDKey, "executor-owner")
			changedID := taskLockDeadlineOpen(t, cfg, owner, "manifest-changed-wait", "forbidden")
			release, err := workspace.LockProject(ctx, cfg.WorkspacePath, "p")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			baseline := taskLockDeadlineTree(t, cfg)
			worker, entered, returned := taskLockDeadlineWorker(t, cfg, ctx, changedID)
			observation := taskLockDeadlineObserve(t, ctx, entered)
			taskLockDeadlineSQLWait(t, cfg, observation.Context, returned)
			// Dispatch and retained SQL grant admission are observed while the real
			// project lock blocks filesystem entry. The FIRST manifest byte check has
			// not yet happened: it is inside taskWorkspaceFile, after LockProject.
			// This proves changed-manifest denial after initial dispatch, not a final
			// post-digest recheck or an already-admitted manifest digest race.
			changedAuthority := []byte("allowed_tools: [workspace_write, workspace_read]\nallowed_paths: [projects/p/main/docs]\n")
			if _, err := mcp.ParseAuthorityManifest(changedAuthority); err != nil {
				t.Fatal(err)
			}
			taskLockDeadlineReplaceAuthority(t, cfg, changedAuthority)
			if observation.Context.Err() != nil {
				t.Fatal("fixture consumed deadline before releasing changed-manifest wait")
			}
			release()
			select {
			case result := <-returned:
				if result.ContextError != nil || !result.ToolError || !strings.Contains(result.ErrorText, "task authority manifest digest changed") {
					t.Fatalf("first manifest check did not deny changed authority before deadline: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			executorWait(t, db, changedID, "failed", 1)
			worker.Stop()
			if db.Stats().InUse != 0 {
				t.Fatalf("manifest denial retained SQL pool1: %+v", db.Stats())
			}
			if err := db.PingContext(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(target)
			if err != nil || string(data) != "before" {
				t.Fatalf("manifest-denied action wrote bytes %q error=%v", data, err)
			}
			var receipts, passing int
			if err := db.QueryRowContext(ctx, Q("SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='pass' THEN 1 ELSE 0 END),0) FROM task_receipts WHERE task_id=$1"), changedID).Scan(&receipts, &passing); err != nil || receipts != 0 || passing != 0 {
				t.Fatalf("manifest-denied receipts=%d pass=%d error=%v", receipts, passing, err)
			}
			taskLockDeadlineManifestUnchanged(t, cfg, changedAuthority)
			taskLockDeadlineTreeEquals(t, cfg, taskLockDeadlineTreeFile(baseline, "authority.yaml", changedAuthority))
			taskLockDeadlineAudit(t, cfg, 1, 0)
			taskLockDeadlineReplaceAuthority(t, cfg, authority)
			taskLockDeadlineTreeEquals(t, cfg, baseline)

			// Fresh native grant/action uses the same untouched file and expected digest.
			// Re-establish contention, observe SQL admission, then release before its
			// actual action deadline to prove the positive path is otherwise identical.
			freshID := taskLockDeadlineOpen(t, cfg, owner, "manifest-restored-wait", "fresh")
			releaseFresh, err := workspace.LockProject(ctx, cfg.WorkspacePath, "p")
			if err != nil {
				t.Fatal(err)
			}
			defer releaseFresh()
			freshWorker, freshEntered, freshReturned := taskLockDeadlineWorker(t, cfg, ctx, freshID)
			freshObservation := taskLockDeadlineObserve(t, ctx, freshEntered)
			taskLockDeadlineSQLWait(t, cfg, freshObservation.Context, freshReturned)
			releaseFresh()
			select {
			case result := <-freshReturned:
				if result.ContextError != nil || result.ToolError {
					t.Fatalf("fresh native adapter failed: %+v", result)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			executorWait(t, db, freshID, "passed", 1)
			freshWorker.Stop()
			data, err = os.ReadFile(target)
			if err != nil || string(data) != "fresh" {
				t.Fatalf("fresh native write %q error=%v", data, err)
			}
			if err := db.QueryRowContext(ctx, Q("SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'"), freshID).Scan(&passing); err != nil || passing != 1 {
				t.Fatalf("fresh passing receipts=%d error=%v", passing, err)
			}
			if db.Stats().InUse != 0 {
				t.Fatalf("fresh completion leaked SQL pool1: %+v", db.Stats())
			}
			taskLockDeadlineManifestUnchanged(t, cfg, authority)
			taskLockDeadlineTreeEquals(t, cfg, taskLockDeadlineTreeFile(baseline, filepath.Join("projects", "p", "main", "docs", "result.md"), []byte("fresh")))
			taskLockDeadlineAudit(t, cfg, 1, 1)
		})
	}
}
