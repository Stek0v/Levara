package http

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stek0v/levara/pkg/workspace"
)

func TestWorkspaceManifestLegacyContext(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	m := workspace.NewManifest("uuid-with-hyphens", "feature/topic")
	m.ActiveGeneration = "active"
	m.Chunks["chunk"] = workspace.ChunkRecord{ProjectID: m.ProjectID, Branch: m.Branch, Generation: "active", Path: "note.md", Collection: "historical"}
	if err := m.Save(workspace.LegacyManifestPath(workspaceRoot(cfg), m.ProjectID, m.Branch)); err != nil {
		t.Fatal(err)
	}
	out := workspaceContextBranch(context.Background(), cfg, "uuid_with_hyphens", "feature_topic", WorkspaceWatchStatus{})
	if out.Error != "" || !out.ManifestExists || out.ActiveChunkCount != 1 || out.ActivePathCount != 1 || out.ActiveCollection != "historical" {
		t.Fatalf("legacy context lost stored identity: %+v", out)
	}
}

func TestWorkspaceManifestTargetFallback(t *testing.T) {
	for _, kind := range []string{"foreign legacy", "malformed canonical", "wrong target canonical"} {
		t.Run(kind, func(t *testing.T) {
			cfg := APIConfig{WorkspacePath: t.TempDir()}
			p, b := "team", "a__main"
			m := workspace.NewManifest(p, b)
			m.ActiveGeneration = "legacy"
			if kind == "foreign legacy" {
				m.ProjectID, m.Branch = "team__a", "main"
			}
			if err := m.Save(workspace.LegacyManifestPath(workspaceRoot(cfg), p, b)); err != nil {
				t.Fatal(err)
			}
			canonical := workspace.ManifestPath(workspaceRoot(cfg), p, b)
			switch kind {
			case "malformed canonical":
				if err := os.MkdirAll(filepath.Dir(canonical), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(canonical, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong target canonical":
				if err := workspace.NewManifest("other", b).Save(canonical); err != nil {
					t.Fatal(err)
				}
			}
			loaded, exists, err := readWorkspaceManifest(cfg, p, b)
			if kind == "foreign legacy" {
				if err != nil || exists || loaded.ActiveGeneration != "" {
					t.Fatalf("foreign legacy exposed: exists=%v error=%v", exists, err)
				}
			} else if err == nil {
				t.Fatal("invalid canonical fell back to legacy")
			}
		})
	}
}
