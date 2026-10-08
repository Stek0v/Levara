package http

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceAuxiliaryFilesystemConfined(t *testing.T) {
	for _, mode := range []string{"namespace", "audit_leaf", "watch_leaf"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			outside := t.TempDir()
			cfg := APIConfig{WorkspacePath: base}
			marker := filepath.Join(outside, "marker")
			if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			kb := filepath.Join(base, ".kb")
			if mode == "namespace" {
				if err := os.Symlink(outside, kb); err != nil {
					t.Skip(err)
				}
			} else {
				if err := os.MkdirAll(kb, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "namespace" || mode == "audit_leaf" {
				auditPath := workspaceAuditPath(cfg, "alpha", time.Now().UTC())
				if mode == "audit_leaf" {
					if err := os.MkdirAll(filepath.Dir(auditPath), 0755); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(marker, auditPath); err != nil {
						t.Skip(err)
					}
				}
				if err := recordWorkspaceAuditEvent(cfg, workspaceAuditEvent{ProjectID: "alpha", Operation: "read", Source: "rest", Result: "denied"}); err == nil {
					t.Fatal("redirected audit write accepted")
				}
			}
			if mode == "namespace" || mode == "watch_leaf" {
				path := workspaceWatchStatusPath(cfg)
				if mode == "watch_leaf" {
					if err := os.Symlink(marker, path); err != nil {
						t.Skip(err)
					}
				}
				state := &WorkspaceWatchState{persistPath: path}
				if err := state.loadPersisted(path); err == nil {
					t.Fatal("redirected watcher read accepted")
				}
				state.mu.Lock()
				state.persistLocked()
				state.mu.Unlock()
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "unchanged" {
				t.Fatalf("outside target changed: %q %v", data, err)
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 1 {
				t.Fatalf("outside metadata changed: %v %v", entries, err)
			}
		})
	}
}
