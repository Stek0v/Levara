//go:build darwin || linux

package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

// First-attempt limitation: use the existing exported production MCP executor
// dispatcher seam to pause after the real validated HTTP dispatch, before its
// receipt. A wrapper around HTTP NewTaskExecutor.ExecuteStep would be too late.
// Recovery uses the unchanged HTTP NewTaskExecutor constructor. This is native
// direct-core persisted-owner/ACL proof, not public transport authentication.
func taskExecutorProcessRecoveryCommand(ctx context.Context, config taskProcessAuthorityConfig, cfg APIConfig, command taskProcessAuthorityCommand, writer *json.Encoder) taskProcessAuthorityReply {
	if cfg.RequireAuth == false {
		return taskProcessAuthorityReply{Error: "process recovery fixture requires persisted authenticated owner ACL"}
	}
	h := &mcpHandler{cfg: cfg, sessions: mcppkg.NewSessionStore()}
	executor := NewTaskExecutor(cfg)
	if command.Op == "executor_write_before_receipt" {
		executor = mcppkg.NewTaskExecutor(h, func(callCtx context.Context, e *mcppkg.TaskExecution) (mcppkg.ToolResult, error) {
			if err := validateTaskWorkspaceAction(e); err != nil {
				return mcppkg.ToolResult{}, err
			}
			if e.Action.Name != "workspace_write" {
				return mcppkg.ToolResult{}, errors.New("checkpoint requires a real workspace_write")
			}
			session := &mcppkg.Session{}
			session.SetUserID(e.OwnerID)
			result := h.executeTool(callCtx, session, e.Action.Name, e.Action.Arguments)
			if result.IsError {
				return result, nil
			}
			actual, err := os.ReadFile(filepath.Join(config.Workspace, "projects", "alpha", "main", "docs", "proof.md"))
			if err != nil {
				return mcppkg.ToolResult{}, err
			}
			if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "write_before_receipt", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(actual))}); err != nil {
				return mcppkg.ToolResult{}, err
			}
			// Parent SIGKILLs this actual process while the production executor
			// has not yet regained control to record its observation receipt.
			<-callCtx.Done()
			return result, callCtx.Err()
		})
	}
	worker := mcppkg.NewTaskWorker(h, executor, mcppkg.TaskWorkerConfig{PollInterval: 5 * time.Millisecond, LeaseSeconds: 30, MaxInFlight: 1, MaxStalledRounds: 10000})
	worker.Start(ctx)
	defer worker.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	taskID, _ := command.Args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return taskProcessAuthorityReply{Error: "fixture task_id required"}
	}
	for {
		if command.Op == "executor_recover" {
			var status string
			if err := h.DB().QueryRowContext(ctx, Q(`SELECT status FROM task_steps WHERE task_id=$1 AND id='only'`), taskID).Scan(&status); err != nil {
				return taskProcessAuthorityReply{Error: err.Error()}
			}
			if status == "passed" {
				return taskProcessAuthorityReply{Checkpoint: "recovered"}
			}
			if status == "failed" {
				return taskProcessAuthorityReply{Error: "recovery executor failed actual action"}
			}
		}
		select {
		case <-ctx.Done():
			return taskProcessAuthorityReply{Error: ctx.Err().Error()}
		case <-ticker.C:
		}
	}
}

func taskExecutorProcessProductTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := workspaceIntegrityTree(t, root)
	// Actual dispatcher auditing is an intended side effect, checked separately.
	for path := range tree {
		if strings.HasPrefix(path, ".kb/audit/alpha/") {
			delete(tree, path)
		}
	}
	return tree
}

func TestTaskExecutorIndependentProcessCrashBeforeReceiptNaturalRecovery(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		config := taskAuthorityConfig(t, f)
		config.TrustedLocal = false
		cfg := APIConfig{DB: f.db, RequireAuth: true, WorkspacePath: config.Workspace}
		verified := taskProcessAuthorityContext(ctx, cfg)
		deps := NewMCPDeps(cfg)
		path := filepath.Join(config.Workspace, "projects", "alpha", "main", "docs", "proof.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		manifest := []byte("allowed_tools: [workspace_write]\nallowed_paths: [projects/alpha/main/docs]\n")
		if err := os.WriteFile(filepath.Join(config.Workspace, "authority.yaml"), manifest, 0600); err != nil {
			t.Fatal(err)
		}
		authorityHash := sha256.Sum256(manifest)
		publicationPath := workspaceManifestPath(cfg, "alpha", "main")
		if err := saveWorkspaceManifest(ctx, cfg, publicationPath, workspace.NewManifest("alpha", "main")); err != nil {
			t.Fatal(err)
		}
		publicationBefore, err := os.ReadFile(publicationPath)
		if err != nil {
			t.Fatal(err)
		}
		release, err := workspace.LockProject(ctx, config.Workspace, "alpha")
		if err != nil {
			t.Fatal(err)
		}
		release()
		before := taskExecutorProcessProductTree(t, config.Workspace)
		text := "actual process CAS source survives SIGKILL\n"
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text)))
		opened := mcppkg.ToolTaskOpen(verified, deps, map[string]any{
			"collection": "task-process-fixture", "room": "task-runtime", "objective": "recover actual write before receipt", "idempotency_key": "executor-process-crash", "risk_level": "low",
			"authority":          map[string]any{"auto_run": true, "allowed_tools": []any{"workspace_write"}, "manifest": "authority.yaml", "manifest_sha256": hex.EncodeToString(authorityHash[:]), "max_step_attempts": 2},
			"definition_of_done": []any{map[string]any{"criterion_id": "artifact", "description": "real reconciled source"}},
		})
		payload := taskAuthorityPayload(t, &opened)
		taskID := payload["task_id"].(string)
		planned := mcppkg.ToolTaskPlan(verified, deps, map[string]any{
			"task_id": taskID, "base_version": payload["version"],
			"steps": []any{map[string]any{"step_id": "only", "description": "create then reconcile identical source", "criterion_ids": []any{"artifact"},
				"action": map[string]any{"kind": "mcp_tool", "name": "workspace_write", "arguments": map[string]any{"project_id": "alpha", "path": "docs/proof.md", "text": text, "index": false, "expected_file_digest": ""},
					"assertions": []any{map[string]any{"pointer": "/bytes", "equals": len([]byte(text))}}}}},
		})
		taskAuthorityPayload(t, &planned)
		a := taskAuthorityStart(t, ctx, config)
		a.send(t, taskProcessAuthorityCommand{Op: "executor_write_before_receipt", Args: map[string]any{"task_id": taskID}})
		written := a.receive(t, ctx)
		if written.Checkpoint != "write_before_receipt" || written.Digest != digest {
			t.Fatalf("actual native postwrite checkpoint missing: %+v", written)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != text {
			t.Fatalf("real child source=%q err=%v", actual, err)
		}
		firstInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		var receipts, attempts int
		var actor, status string
		var expiryValue, createdValue any
		if err := f.db.QueryRow(Q(`SELECT actor_id,expires_at,created_at FROM task_leases WHERE task_id=$1 AND step_id='only'`), taskID).Scan(&actor, &expiryValue, &createdValue); err != nil {
			t.Fatal(err)
		}
		expiry, created := taskAuthorityDate(t, expiryValue), taskAuthorityDate(t, createdValue)
		if actor == "" || expiry.Sub(created) < 30*time.Second-time.Microsecond || !time.Now().Before(expiry) {
			t.Fatal("actual minimum live lease not established")
		}
		if err := f.db.QueryRow(Q(`SELECT status,attempts FROM task_steps WHERE task_id=$1 AND id='only'`), taskID).Scan(&status, &attempts); err != nil || status != "active" || attempts != 1 {
			t.Fatalf("prekill step=%s attempts=%d err=%v", status, attempts, err)
		}
		if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1`), taskID).Scan(&receipts); err != nil || receipts != 0 {
			t.Fatalf("receipt existed before SIGKILL=%d err=%v", receipts, err)
		}
		death := a.kill(t)
		var exit *exec.ExitError
		if !errors.As(death, &exit) {
			t.Fatalf("actual child was not SIGKILLed: %v", death)
		}
		bits, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !bits.Signaled() || bits.Signal() != syscall.SIGKILL {
			t.Fatalf("unexpected process death: %v", exit.Sys())
		}
		timer := time.NewTimer(time.Until(expiry) + time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		var persistedActor string
		if err := f.db.QueryRow(Q(`SELECT actor_id,expires_at FROM task_leases WHERE task_id=$1 AND step_id='only'`), taskID).Scan(&persistedActor, &expiryValue); err != nil {
			t.Fatal(err)
		}
		if persistedActor != actor || !taskAuthorityDate(t, expiryValue).Equal(expiry) || time.Now().Before(expiry) {
			t.Fatal("abandoned lease expiry was changed instead of naturally elapsed")
		}
		b := taskAuthorityStart(t, ctx, config)
		if reply := b.call(t, ctx, taskProcessAuthorityCommand{Op: "executor_recover", Args: map[string]any{"task_id": taskID}}); reply.Checkpoint != "recovered" {
			t.Fatalf("actual restarted worker did not reconcile: %+v", reply)
		}
		expected := make(map[string]string, len(before)+1)
		for path, data := range before {
			expected[path] = data
		}
		expected["projects/alpha/main/docs/proof.md"] = text
		if after := taskExecutorProcessProductTree(t, config.Workspace); !reflect.DeepEqual(after, expected) {
			t.Fatalf("unexpected source/index/tree effect: %#v want %#v", after, expected)
		}
		auditEvents := 0
		for path, content := range workspaceIntegrityTree(t, config.Workspace) {
			if !strings.HasPrefix(path, ".kb/audit/alpha/") {
				continue
			}
			if !strings.HasSuffix(path, ".jsonl") {
				t.Fatalf("unexpected audit artifact %s", path)
			}
			for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
				var event workspaceAuditEvent
				if err := json.Unmarshal([]byte(line), &event); err != nil {
					t.Fatal(err)
				}
				if event.Source != "mcp" || event.Operation != "write" || event.ProjectID != "alpha" || event.UserID != "owner" || event.Result != "success" {
					t.Fatalf("unexpected real dispatcher audit: %+v", event)
				}
				auditEvents++
			}
		}
		if auditEvents != 2 {
			t.Fatalf("native write/reconcile audit events=%d want2", auditEvents)
		}
		afterInfo, err := os.Stat(path)
		if err != nil || !os.SameFile(firstInfo, afterInfo) || !firstInfo.ModTime().Equal(afterInfo.ModTime()) {
			t.Fatalf("identical CAS retry republished source: %v", err)
		}
		publicationAfter, err := os.ReadFile(publicationPath)
		if err != nil || !bytes.Equal(publicationBefore, publicationAfter) {
			t.Fatalf("nonindex action changed publication manifest: %v", err)
		}
		if err := f.db.QueryRow(Q(`SELECT status,attempts FROM task_steps WHERE task_id=$1 AND id='only'`), taskID).Scan(&status, &attempts); err != nil || status != "passed" || attempts != 2 {
			t.Fatalf("recovered step=%s attempts=%d err=%v", status, attempts, err)
		}
		if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1`), taskID).Scan(&receipts); err != nil || receipts != 1 {
			t.Fatalf("duplicate receipt count=%d err=%v", receipts, err)
		}
		var owner, receiptStatus, receiptType, metadata string
		if err := f.db.QueryRow(Q(`SELECT owner_id,status,receipt_type,metadata_json FROM task_receipts WHERE task_id=$1`), taskID).Scan(&owner, &receiptStatus, &receiptType, &metadata); err != nil {
			t.Fatal(err)
		}
		var observed struct {
			Attempt int            `json:"attempt"`
			Tool    string         `json:"tool"`
			Output  map[string]any `json:"output"`
		}
		if err := json.Unmarshal([]byte(metadata), &observed); err != nil {
			t.Fatal(err)
		}
		if owner != "owner" || receiptStatus != "pass" || receiptType != "observation" || observed.Attempt != 2 || observed.Tool != "workspace_write" || observed.Output["bytes"] != float64(len([]byte(text))) {
			t.Fatalf("receipt not actual recovery observation: owner=%s status=%s type=%s metadata=%s", owner, receiptStatus, receiptType, metadata)
		}
		var claims, liveLeases int
		if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM task_events WHERE task_id=$1 AND event_type='step_claim'`), taskID).Scan(&claims); err != nil || claims != 2 {
			t.Fatalf("claim events=%d err=%v", claims, err)
		}
		if err := f.db.QueryRow(Q(`SELECT COUNT(*) FROM task_leases WHERE task_id=$1`), taskID).Scan(&liveLeases); err != nil || liveLeases != 0 {
			t.Fatalf("remaining leases=%d err=%v", liveLeases, err)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("process recovery leaked parent SQL pool")
		}
	})
}
