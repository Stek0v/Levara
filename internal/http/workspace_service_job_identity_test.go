package http

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceServiceJobSavedIdentity(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		for _, id := range []string{"uuid-with-hyphens", "team-a", "team_a"} {
			f.exec("INSERT INTO datasets(id,name,owner_id) VALUES($1,$1,'owner')", id)
		}
		for _, target := range []string{"uuid_with_hyphens", "team-a", "orphan"} {
			t.Run(target, func(t *testing.T) {
				cfg, closeCfg := newWorkspaceTestConfig(t)
				defer closeCfg()
				cfg.DB, cfg.RequireAuth = f.db, true
				cfg.WorkspaceWatcher = NewWorkspaceWatchState()
				cfg.WorkspaceWatcher.markStarted(WorkspaceWatchOptions{})
				path, _, err := workspaceFilePath(cfg, target, "main", "note.md")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.Repeat("Existing document must remain unindexed. ", 20)), 0600); err != nil {
					t.Fatal(err)
				}
				job, err := enqueueWorkspaceIndexJobFromPayload(cfg, workspaceIndexJobPayload{Operation: "reconcile", ProjectID: target, Branch: "main", Generation: "saved", Paths: []string{"note.md"}, ActivateGeneration: true}, &workspaceIndexJobAuthority{Service: true})
				if err != nil {
					t.Fatal(err)
				}
				jobPath := workspaceIndexJobPath(cfg, target, "main", job.ID)
				before, err := os.ReadFile(jobPath)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, _, err := runWorkspaceIndexJob(ctx, cfg, job, WorkspaceIndexWorkerOptions{}); err == nil {
					t.Fatal("invalid saved service target executed")
				}
				after, err := os.ReadFile(jobPath)
				if err != nil || string(before) != string(after) || f.db.Stats().InUse != 0 {
					t.Fatalf("denied job changed state or leaked SQL: %v", err)
				}
				if _, err := os.Stat(workspaceManifestPath(cfg, target, "main")); !os.IsNotExist(err) {
					t.Fatalf("denied job published manifest: %v", err)
				}
			})
		}
	})
}
