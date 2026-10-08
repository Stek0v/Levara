package http

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

func taskExecutorT27Observe(t *testing.T, db *sql.DB, tool string, allowed bool) {
	t.Helper()
	const original = "observable granted source\n"
	const replacement = "observable permitted replacement\n"
	text := original
	if tool == "workspace_write" {
		text = replacement
	}
	cfg, _, id := executorFixture(t, db, []map[string]any{executorAction(tool, "docs/proof.md", text)})
	target := filepath.Join(cfg.WorkspacePath, "projects", "p", "main", "docs", "proof.md")
	if err := os.WriteFile(target, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	// A real valid publication manifest is a separate immutable oracle. The
	// executor only reads/writes source files; it must never publish an index.
	manifestPath := workspaceManifestPath(cfg, "p", "main")
	if err := saveWorkspaceManifest(context.Background(), cfg, manifestPath, workspace.NewManifest("p", "main")); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// Initialize the native coordination file before the exact whole-root tree
	// snapshot. Its existence must not masquerade as an authored/index effect.
	release, err := workspace.LockProject(context.Background(), cfg.WorkspacePath, "p")
	if err != nil {
		t.Fatal(err)
	}
	release()
	before := workspaceIntegrityTree(t, cfg.WorkspacePath)
	worker := executorStart(t, cfg, NewTaskExecutor(cfg), context.Background())
	state := "failed"
	if allowed {
		state = "passed"
	}
	executorWait(t, db, id, state, 1)
	worker.Stop()
	expected := make(map[string]string, len(before))
	for path, text := range before {
		expected[path] = text
	}
	if allowed && tool == "workspace_write" {
		expected["projects/p/main/docs/proof.md"] = replacement
	}
	after := workspaceIntegrityTree(t, cfg.WorkspacePath)
	auditCount := 0
	for path, data := range after {
		if !strings.HasPrefix(path, ".kb/audit/p/") {
			continue
		}
		if !strings.HasSuffix(path, ".jsonl") {
			t.Fatalf("unexpected audit artifact: %s", path)
		}
		for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
			var event workspaceAuditEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatal(err)
			}
			if event.Source != "mcp" || event.ProjectID != "p" || event.UserID != "executor-owner" || event.Operation != strings.TrimPrefix(tool, "workspace_") {
				t.Fatalf("unexpected actual audit event: %+v", event)
			}
			if allowed && event.Result != "success" || !allowed && event.Result != "failure" && event.Result != "denied" {
				t.Fatalf("audit result does not match actual action: %+v", event)
			}
			auditCount++
		}
		delete(after, path)
	}
	if auditCount != 1 {
		t.Fatalf("actual dispatcher audit count=%d want1", auditCount)
	}
	if !reflect.DeepEqual(after, expected) {
		t.Fatalf("executor changed unexpected tree bytes: before=%#v after=%#v expected=%#v", before, after, expected)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil || !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatalf("executor changed publication manifest: %v", err)
	}
	var passed int
	if err := db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'`), id).Scan(&passed); err != nil {
		t.Fatal(err)
	}
	want := 0
	if allowed {
		want = 1
	}
	if passed != want {
		t.Fatalf("passing receipts=%d want=%d", passed, want)
	}
	if allowed {
		var metadata string
		if err := db.QueryRow(Q(`SELECT metadata_json FROM task_receipts WHERE task_id=$1 AND status='pass' AND receipt_type='observation'`), id).Scan(&metadata); err != nil {
			t.Fatal(err)
		}
		var recorded struct {
			Tool   string         `json:"tool"`
			Output map[string]any `json:"output"`
		}
		if err := json.Unmarshal([]byte(metadata), &recorded); err != nil {
			t.Fatal(err)
		}
		if recorded.Tool != tool {
			t.Fatalf("wrong actual receipt tool: %+v", recorded)
		}
		if tool == "workspace_read" && recorded.Output["text"] != original {
			t.Fatalf("read receipt lacks actual source bytes: %+v", recorded)
		}
		if tool == "workspace_write" && recorded.Output["bytes"] != float64(len(replacement)) {
			t.Fatalf("write receipt lacks actual written length: %+v", recorded)
		}
	}
	if db.Stats().InUse != 0 {
		t.Fatal("executor leaked native SQL connection")
	}
}

func TestTaskExecutorReadOnlyACLGrantObservedReadDeniedWrite(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			db.SetMaxOpenConns(1)
			for _, query := range []string{
				`INSERT INTO principals(id,type) VALUES ('acl-project-owner','user')`,
				`INSERT INTO users(id,email,hashed_password,is_active) VALUES ('acl-project-owner','acl-owner@test.invalid','unused',true)`,
				`UPDATE datasets SET owner_id='acl-project-owner' WHERE id='p'`,
				`INSERT INTO dataset_shares(id,dataset_id,user_id,role,granted_by) VALUES ('executor-viewer-share','p','executor-owner','viewer','acl-project-owner')`,
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			// The authenticated task owner is a viewer, not the dataset owner.
			taskExecutorT27Observe(t, db, "workspace_read", true)
			taskExecutorT27Observe(t, db, "workspace_write", false)
			// Explicit real ACL positive control proves the write fixture is viable.
			if _, err := db.Exec(`UPDATE dataset_shares SET role='editor' WHERE id='executor-viewer-share'`); err != nil {
				t.Fatal(err)
			}
			taskExecutorT27Observe(t, db, "workspace_write", true)
		})
	}
}

func TestTaskExecutorLongHorizonProfileMissingWorkspaceTools(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := executorTestDB(t, dialect)
			db.SetMaxOpenConns(1)
			for _, profile := range []string{"full", "long-horizon"} {
				t.Run(profile, func(t *testing.T) {
					// The actual profile spelling is long-horizon. Unknown spellings
					// resolve to full, so an underscore would invalidate this oracle.
					t.Setenv("LEVARA_MCP_TOOLSET", profile)
					if !mcp.ToolAllowedForMode(profile, "task_step") {
						t.Fatal("profile must retain real Task execution")
					}
					for _, tool := range []string{"workspace_read", "workspace_write"} {
						allowed := profile == "full"
						if mcp.ToolAllowedForMode(profile, tool) != allowed {
							t.Fatalf("unexpected %s profile visibility for %s", profile, tool)
						}
						taskExecutorT27Observe(t, db, tool, allowed)
					}
				})
			}
		})
	}
}
