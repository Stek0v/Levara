package http

import (
	"os"
	"path/filepath"
	"testing"

	workspacepkg "github.com/stek0v/levara/pkg/workspace"
)

func TestWorkspaceManifestCompositeNamespace(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	first := workspacepkg.NewManifest("team__a", "main")
	first.ActiveGeneration = "first"
	path := workspaceManifestPath(cfg, "team__a", "main")
	if err := first.Save(path); err != nil {
		t.Fatal(err)
	}
	second, otherPath, err := loadWorkspaceManifest(cfg, "team", "a__main")
	if err != nil {
		t.Fatal(err)
	}
	if path == otherPath || second.ActiveGeneration != "" || len(second.Chunks) != 0 {
		t.Fatal("distinct project/branch pairs share a manifest")
	}
	second.ActiveGeneration = "second"
	if err := second.Save(otherPath); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := loadWorkspaceManifest(cfg, "team__a", "main")
	if err != nil || loaded.ActiveGeneration != "first" {
		t.Fatalf("second manifest overwrote first: error=%v", err)
	}
}

func TestWorkspaceManifestLegacyTargetCompatibility(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	legacy := filepath.Join(workspaceRoot(cfg), ".kb", "manifests", "uuid_with_hyphens__feature_topic.json")
	manifest := workspacepkg.NewManifest("uuid-with-hyphens", "feature/topic")
	manifest.ActiveGeneration = "legacy"
	if err := manifest.Save(legacy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	loaded, canonical, err := loadWorkspaceManifest(cfg, "uuid_with_hyphens", "feature_topic")
	if err != nil || loaded.ActiveGeneration != "legacy" {
		t.Fatalf("watcher normalized identity lost legacy: error=%v", err)
	}
	if canonical == legacy {
		t.Fatal("legacy target returned as a writable canonical path")
	}
	loaded.ActiveGeneration = "current"
	if err := loaded.Save(canonical); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("canonical write mutated legacy sidecar")
	}
	loaded, _, err = loadWorkspaceManifest(cfg, "uuid-with-hyphens", "feature/topic")
	if err != nil || loaded.ActiveGeneration != "current" {
		t.Fatalf("canonical manifest did not win: error=%v", err)
	}
}
