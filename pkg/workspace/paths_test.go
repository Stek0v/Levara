package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePathsPolicy(t *testing.T) {
	root := t.TempDir()
	if got := DefaultBranch(""); got != "main" {
		t.Fatalf("DefaultBranch empty = %q, want main", got)
	}
	if got := SafeID("../bad id!"); got != "bad_id" {
		t.Fatalf("SafeID = %q, want bad_id", got)
	}
	if got := ManifestPath(root, "proj/1", "feature/x"); got != filepath.Join(root, ".kb", "manifests", "proj_1__feature_x.json") {
		t.Fatalf("ManifestPath = %q", got)
	}
}

func TestListLocalProjectsAndBranches(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		filepath.Join(root, "projects", "proj_b", "main"),
		filepath.Join(root, "projects", "proj_a", "dev"),
		filepath.Join(root, "projects", "proj_a", "main"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if got := ListLocalProjects(root); len(got) != 2 || got[0] != "proj_a" || got[1] != "proj_b" {
		t.Fatalf("ListLocalProjects = %#v", got)
	}
	if got := ListLocalBranches(root, "proj_a"); len(got) != 2 || got[0] != "dev" || got[1] != "main" {
		t.Fatalf("ListLocalBranches = %#v", got)
	}
}
