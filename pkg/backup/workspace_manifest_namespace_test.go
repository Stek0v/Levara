package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stek0v/levara/pkg/workspace"
)

func TestVerifiedWorkspaceManifestPrecedence(t *testing.T) {
	for _, kind := range []string{"legacy only", "stale legacy", "malformed canonical", "wrong target canonical", "other tuple canonical"} {
		t.Run(kind, func(t *testing.T) {
			o := verifiedFixture(t)
			canonical := workspace.ManifestPath(o.WorkspacePath, "sample", "main")
			m, err := workspace.LoadManifest(canonical)
			if err != nil {
				t.Fatal(err)
			}
			legacy := workspace.LegacyManifestPath(o.WorkspacePath, "sample", "main")
			if kind != "legacy only" {
				c := m.Chunks["chunk-1"]
				c.VectorID, c.FileDigest = "removed-vector", "sha256:obsolete"
				m.Chunks["chunk-1"] = c
			}
			if err := m.Save(legacy); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(legacy)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "legacy only":
				if err := os.Remove(canonical); err != nil {
					t.Fatal(err)
				}
			case "malformed canonical":
				writeVerifiedFile(t, canonical, []byte("{"))
			case "wrong target canonical":
				if err := workspace.NewManifest("other", "main").Save(canonical); err != nil {
					t.Fatal(err)
				}
			case "other tuple canonical":
				if err := os.Remove(canonical); err != nil {
					t.Fatal(err)
				}
				if err := workspace.NewManifest("other", "main").Save(workspace.ManifestPath(o.WorkspacePath, "other", "main")); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := CreateVerifiedBackup(context.Background(), o)
			wantError := kind != "legacy only" && kind != "stale legacy"
			if (err != nil) != wantError {
				t.Fatalf("backup error=%v, want error=%v", err, wantError)
			}
			if !wantError {
				f, err := os.Open(receipt.Archive)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				gz, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				tr := tar.NewReader(gz)
				found := false
				for {
					header, err := tr.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					rel, err := filepath.Rel(o.WorkspacePath, legacy)
					if err != nil {
						t.Fatal(err)
					}
					if header.Name == "workspace/"+filepath.ToSlash(rel) {
						body, err := io.ReadAll(tr)
						if err != nil || string(body) != string(before) {
							t.Fatalf("archive legacy changed: %v", err)
						}
						found = true
					}
				}
				if !found {
					t.Fatal("legacy omitted from archive")
				}
				if _, err := VerifyArchive(context.Background(), receipt.Archive, o); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(legacy)
			if err != nil || string(after) != string(before) {
				t.Fatalf("legacy changed: %v", err)
			}
		})
	}
}

func TestVerifiedWorkspaceManifestCompositePrecedence(t *testing.T) {
	stage := t.TempDir()
	legacy := workspace.NewManifest("team__a", "main")
	legacy.ActiveGeneration = "missing"
	legacyPath := workspace.LegacyManifestPath("", legacy.ProjectID, legacy.Branch)
	if err := legacy.Save(filepath.Join(stage, "workspace", legacyPath)); err != nil {
		t.Fatal(err)
	}
	other := workspace.NewManifest("team", "a__main")
	canonicalPath := workspace.ManifestPath("", other.ProjectID, other.Branch)
	if err := other.Save(filepath.Join(stage, "workspace", canonicalPath)); err != nil {
		t.Fatal(err)
	}
	m := VerifiedManifest{Files: []InventoryFile{{Root: "workspace", Path: filepath.ToSlash(legacyPath)}, {Root: "workspace", Path: filepath.ToSlash(canonicalPath)}}}
	if _, err := verifyStructuredArtifacts(context.Background(), stage, m, VerifiedOptions{MaxRows: 100}); err == nil {
		t.Fatal("different composite tuple suppressed legacy validation")
	}
}
