package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/workspace"
)

func workspaceInventoryFixture(t *testing.T) (VerifiedOptions, *workspace.Manifest) {
	t.Helper()
	o := verifiedFixture(t)
	m, err := workspace.LoadManifest(workspace.ManifestPath(o.WorkspacePath, "sample", "main"))
	if err != nil {
		t.Fatal(err)
	}
	m.Files[m.ActiveGeneration] = map[string]string{"note.md": m.Chunks["chunk-1"].FileDigest}
	for name, body := range map[string][]byte{"empty.md": {}, "whitespace.md": []byte(" \t\r\n")} {
		writeVerifiedFile(t, filepath.Join(workspace.ProjectRoot(o.WorkspacePath, "sample", "main"), name), body)
		digest := sha256.Sum256(body)
		m.Files[m.ActiveGeneration][name] = "sha256:" + hex.EncodeToString(digest[:])
	}
	return o, m
}

func TestVerifiedWorkspaceFileInventory(t *testing.T) {
	for _, kind := range []string{"zero chunk files", "wrong digest", "missing file", "unsafe path", "known empty contradicts chunks", "legacy absent", "legacy null", "historical inactive", "committed empty", "bare inventory legacy chunk", "legacy inventory bare chunk"} {
		t.Run(kind, func(t *testing.T) {
			o, m := workspaceInventoryFixture(t)
			wantError := false
			switch kind {
			case "bare inventory legacy chunk":
				m.Files[m.ActiveGeneration]["note.md"] = strings.TrimPrefix(m.Files[m.ActiveGeneration]["note.md"], "sha256:")
			case "legacy inventory bare chunk":
				chunk := m.Chunks["chunk-1"]
				chunk.FileDigest = strings.TrimPrefix(chunk.FileDigest, "sha256:")
				m.Chunks["chunk-1"] = chunk
			case "wrong digest":
				m.Files[m.ActiveGeneration]["empty.md"] = "sha256:" + strings.Repeat("0", 64)
				wantError = true
			case "missing file":
				if err := os.Remove(filepath.Join(workspace.ProjectRoot(o.WorkspacePath, "sample", "main"), "empty.md")); err != nil {
					t.Fatal(err)
				}
				wantError = true
			case "unsafe path":
				m.Files[m.ActiveGeneration]["../escape.md"] = m.Files[m.ActiveGeneration]["empty.md"]
				wantError = true
			case "known empty contradicts chunks":
				m.Files[m.ActiveGeneration] = map[string]string{}
				wantError = true
			case "legacy absent":
				delete(m.Files, m.ActiveGeneration)
			case "legacy null":
				m.Files[m.ActiveGeneration] = nil
			case "historical inactive":
				m.Generations["retired"] = workspace.Generation{ID: "retired", Status: workspace.GenerationGCPending}
				m.Files["retired"] = map[string]string{"removed.md": "sha256:" + strings.Repeat("0", 64)}
			case "committed empty":
				m.Files[m.ActiveGeneration] = map[string]string{}
				m.Chunks = map[string]workspace.ChunkRecord{}
			}
			if err := m.Save(workspace.ManifestPath(o.WorkspacePath, "sample", "main")); err != nil {
				t.Fatal(err)
			}
			receipt, err := CreateVerifiedBackup(context.Background(), o)
			if wantError {
				if err == nil {
					t.Fatal("inconsistent workspace inventory certified")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, root := range []string{o.DataDir, o.WorkspacePath, o.UploadsPath} {
				if err := os.Rename(root, root+"-unavailable"); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := VerifyArchive(context.Background(), receipt.Archive, o)
			if err != nil {
				t.Fatal(err)
			}
			if restored.ArchiveSHA256 != receipt.ArchiveSHA256 {
				t.Fatal("fresh verification changed archive")
			}
		})
	}
}

func TestVerifiedWorkspaceRepackedZeroChunkFile(t *testing.T) {
	for _, kind := range []string{"changed bytes", "missing object"} {
		t.Run(kind, func(t *testing.T) {
			o, m := workspaceInventoryFixture(t)
			if err := m.Save(workspace.ManifestPath(o.WorkspacePath, "sample", "main")); err != nil {
				t.Fatal(err)
			}
			receipt, err := CreateVerifiedBackup(context.Background(), o)
			if err != nil {
				t.Fatal(err)
			}
			opts, err := verifiedDefaults(o)
			if err != nil {
				t.Fatal(err)
			}
			stage := t.TempDir()
			manifest, _, _, err := extractVerified(context.Background(), receipt.Archive, stage, opts)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.ToSlash(filepath.Join(workspace.ProjectRoot("", "sample", "main"), "empty.md"))
			found := false
			for i, file := range manifest.Files {
				if file.Root != "workspace" || file.Path != target {
					continue
				}
				found = true
				p := filepath.Join(stage, "workspace", filepath.FromSlash(target))
				if kind == "missing object" {
					if err := os.Remove(p); err != nil {
						t.Fatal(err)
					}
					manifest.Files = append(manifest.Files[:i], manifest.Files[i+1:]...)
				} else {
					writeVerifiedFile(t, p, []byte("changed independently"))
					proof, err := hashInventoryFile(context.Background(), p, "workspace", target)
					if err != nil {
						t.Fatal(err)
					}
					manifest.Files[i] = proof
				}
				break
			}
			if !found {
				t.Fatal("zero-chunk file missing from archive fixture")
			}
			repacked := filepath.Join(t.TempDir(), "repacked.tar.gz")
			f, err := os.Create(repacked)
			if err != nil {
				t.Fatal(err)
			}
			writeErr := writeVerifiedArchive(context.Background(), f, stage, manifest)
			closeErr := f.Close()
			if writeErr != nil || closeErr != nil {
				t.Fatalf("repack: %v %v", writeErr, closeErr)
			}
			// The outer archive inventory is coherent: semantic workspace proof must
			// reject it even though all payload sizes and hashes were updated.
			_, _, _, err = extractVerified(context.Background(), repacked, t.TempDir(), opts)
			if err != nil {
				t.Fatalf("repacked archive structurally invalid: %v", err)
			}
			if _, err := VerifyArchive(context.Background(), repacked, o); err == nil {
				t.Fatal("repacked stale zero-chunk inventory certified")
			}
		})
	}
}
