package http

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This exercises the real persisted Task authority, lease, dispatcher, and
// filesystem capability. The child exits after live-to-backup and leaves the
// durable restore journal; no fake Task fence or direct recovery call is used.
func TestWorkspaceGenerationTaskExecutorCrashRecovery(t *testing.T) {
	t.Setenv("LEVARA_LONG_HORIZON_RUNTIME", "1")
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, operation := range []string{"read", "write_cas"} {
				t.Run(operation, func(t *testing.T) {
					db := executorTestDB(t, dialect)
					db.SetMaxOpenConns(1)
					if _, err := db.Exec(Q(`INSERT INTO datasets(id,name,owner_id) VALUES ($1,$2,$3)`), "alpha", "recovery project", "executor-owner"); err != nil {
						t.Fatal(err)
					}
					original := "old granted bytes\n"
					replacement := "task CAS replacement\n"
					action := executorAction("workspace_read", "docs/a.md", original)
					if operation == "write_cas" {
						action = executorAction("workspace_write", "docs/a.md", replacement)
						action["arguments"].(map[string]any)["expected_file_digest"] = digestBytes([]byte(original))
					}
					action["arguments"].(map[string]any)["project_id"] = "alpha"
					cfg, _, id := executorFixture(t, db, []map[string]any{action})
					expected := map[string]string{"docs/a.md": original, "docs/sibling.md": "old granted sibling\n", "nested/b.md": "old outside-grant sibling\n"}
					for path, text := range expected {
						workspaceIntegrityPut(t, cfg, path, []byte(text))
					}
					live := workspaceProjectRoot(cfg, "alpha", "main")
					parent := filepath.Dir(live)
					workspaceGenerationRunCrashChild(t, workspaceGenerationCrashInput{Root: cfg.WorkspacePath, Mode: "history", Phase: "backup"})
					if _, err := os.Stat(live); !os.IsNotExist(err) {
						t.Fatalf("child did not leave post-first-rename crash: %v", err)
					}
					raw, err := os.ReadFile(filepath.Join(parent, ".restore-state-main.json"))
					if err != nil {
						t.Fatal(err)
					}
					var journal workspaceRestoreJournal
					if err := json.Unmarshal(raw, &journal); err != nil {
						t.Fatal(err)
					}
					if journal.ProjectID != "alpha" || journal.Live != filepath.Base(live) || journal.Backup == "" || journal.Stage == "" {
						t.Fatalf("unexpected persisted journal: %+v", journal)
					}
					if tree := workspaceIntegrityTree(t, filepath.Join(parent, journal.Backup)); !reflect.DeepEqual(tree, expected) {
						t.Fatalf("backup before execution=%#v", tree)
					}
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					worker := executorStart(t, cfg, NewTaskExecutor(cfg), ctx)
					executorWait(t, db, id, "passed", 1)
					worker.Stop()
					if operation == "write_cas" {
						expected["docs/a.md"] = replacement
					}
					if tree := workspaceIntegrityTree(t, live); !reflect.DeepEqual(tree, expected) {
						t.Fatalf("recovered Task tree=%#v want=%#v", tree, expected)
					}
					entries, err := os.ReadDir(parent)
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), ".restore-") || strings.HasPrefix(entry.Name(), ".backup-") {
							t.Fatalf("ambiguous recovery state survived Task execution: %s", entry.Name())
						}
					}
					var receipts int
					if err := db.QueryRow(Q(`SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass' AND receipt_type='observation'`), id).Scan(&receipts); err != nil {
						t.Fatal(err)
					}
					if receipts != 1 {
						t.Fatalf("real observation receipts=%d want1", receipts)
					}
					if inUse := db.Stats().InUse; inUse != 0 {
						t.Fatalf("pool-one Task recovery leaked %d SQL connections", inUse)
					}
				})
			}
		})
	}
}
