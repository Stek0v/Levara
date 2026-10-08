package http

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stek0v/levara/pkg/workspace"
)

func historyFixture(t *testing.T) (APIConfig, workspaceCommitRecord, map[string][]byte) {
	t.Helper()
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	original := map[string][]byte{"a.bin": {0xff, 0, 1, 2}, "docs/b.md": []byte("Привет.\r\n"), "empty.md": {}}
	for name, data := range original {
		sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), name), data)
	}
	record, err := prepareWorkspaceCommit(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha", Branch: "main", Message: "snapshot"}, "first")
	if err != nil {
		t.Fatal(err)
	}
	return cfg, record, original
}

func TestWorkspaceHistoryPublicationPreservesExactBytes(t *testing.T) {
	cfg, record, original := historyFixture(t)
	if len(record.Files) != len(original) {
		t.Fatalf("files=%+v", record.Files)
	}
	loaded, err := loadWorkspaceCommitRecordConfined(cfg, record.Path)
	if err != nil || loaded.ProjectID != "alpha" || loaded.CommitID != "first" {
		t.Fatalf("record=%+v error=%v", loaded, err)
	}
	for _, file := range record.Files {
		data := original[file.Path]
		if file.Size != int64(len(data)) || file.Digest != digestBytes(data) {
			t.Fatalf("snapshot proof=%+v", file)
		}
		sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), file.Path), []byte("current "+file.Path))
	}
	sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "extra.md"), []byte("must disappear"))
	if err := restoreWorkspaceSnapshot(context.Background(), cfg, "alpha", "main", record.Path, loaded); err != nil {
		t.Fatal(err)
	}
	for name, want := range original {
		got, err := os.ReadFile(filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s restored=%v want=%v error=%v", name, got, want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "extra.md")); !os.IsNotExist(err) {
		t.Fatalf("extra file remains: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(workspaceProjectRoot(cfg, "alpha", "main")))
	if err != nil || len(entries) != 1 || entries[0].Name() != "main" {
		t.Fatalf("restore leftovers=%v error=%v", entries, err)
	}
	if err := os.Mkdir(filepath.Join(workspaceCommitsRoot(cfg, "alpha", "main"), ".commit-unpublished"), 0700); err != nil {
		t.Fatal(err)
	}
	log, err := listWorkspaceCommitRecordsConfined(cfg, "alpha", "main")
	if err != nil || len(log.Commits) != 1 {
		t.Fatalf("log=%+v error=%v", log, err)
	}
	if _, err := prepareWorkspaceCommit(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, "first"); err == nil {
		t.Fatal("existing immutable commit replaced")
	}
	again, err := loadWorkspaceCommitRecordConfined(cfg, record.Path)
	if err != nil || again.CreatedAt != record.CreatedAt {
		t.Fatal("existing commit changed")
	}
}

func TestWorkspaceHistoryValidationPreservesCurrentTree(t *testing.T) {
	for _, kind := range []string{"missing_later", "corrupt_later", "wrong_size", "duplicate", "file_directory_conflict", "noncanonical", "traversal", "wrong_target", "snapshot_symlink", "live_symlink"} {
		t.Run(kind, func(t *testing.T) {
			cfg, record, _ := historyFixture(t)
			live := workspaceProjectRoot(cfg, "alpha", "main")
			currentA := []byte("current a")
			currentB := []byte("current b")
			sidecarFixtureWrite(t, filepath.Join(live, "a.bin"), currentA)
			sidecarFixtureWrite(t, filepath.Join(live, "docs", "b.md"), currentB)
			outside := filepath.Join(t.TempDir(), "private")
			sidecarFixtureWrite(t, outside, []byte("external bytes"))
			later := filepath.Join(record.Path, "files", "docs", "b.md")
			switch kind {
			case "missing_later":
				if err := os.Remove(later); err != nil {
					t.Fatal(err)
				}
			case "corrupt_later":
				sidecarFixtureWrite(t, later, []byte("corrupt snapshot"))
			case "wrong_size":
				record.Files[len(record.Files)-1].Size++
			case "duplicate":
				record.Files = append(record.Files, record.Files[0])
			case "file_directory_conflict":
				record.Files = append(record.Files, workspaceCommitFile{Path: "a.bin/child", Size: 0, Digest: digestBytes(nil)})
			case "noncanonical":
				record.Files = append(record.Files, workspaceCommitFile{Path: "docs/../evil", Size: 0, Digest: digestBytes(nil)})
			case "traversal":
				record.Files = append(record.Files, workspaceCommitFile{Path: "../evil", Size: 0, Digest: digestBytes(nil)})
			case "wrong_target":
				record.ProjectID = "foreign"
			case "snapshot_symlink":
				if err := os.Remove(later); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, later); err != nil {
					t.Fatal(err)
				}
			case "live_symlink":
				if err := os.Remove(filepath.Join(live, "a.bin")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(live, "a.bin")); err != nil {
					t.Fatal(err)
				}
				currentA = []byte("external bytes")
			}
			if err := restoreWorkspaceSnapshot(context.Background(), cfg, "alpha", "main", record.Path, record); err == nil {
				t.Fatal("invalid restore accepted")
			}
			for name, want := range map[string][]byte{"a.bin": currentA, "docs/b.md": currentB} {
				got, err := os.ReadFile(filepath.Join(live, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("current %s changed: %q %v", name, got, err)
				}
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "external bytes" {
				t.Fatal("external target changed")
			}
			entries, err := os.ReadDir(filepath.Dir(live))
			if err != nil || len(entries) != 1 {
				t.Fatalf("failed restore staging remains: %v %v", entries, err)
			}
		})
	}
}

func TestWorkspaceHistoryPublicationRollbackSecondRename(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	parent, err := openWorkspaceDirectory(cfg, filepath.Join(workspaceRoot(cfg), "projects", "alpha"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	if err := parent.Mkdir("main", 0700); err != nil {
		t.Fatal(err)
	}
	live, err := workspace.OpenDir(parent, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.WriteFile(context.Background(), live, "sentinel.bin", []byte{0, 0xff}, 0600); err != nil {
		t.Fatal(err)
	}
	live.Close()
	if err := publishWorkspaceRestore(parent, "missing-stage", "main", ".backup-test"); err == nil {
		t.Fatal("missing stage succeeded")
	}
	live, err = workspace.OpenDir(parent, "main", false)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	got, err := workspace.ReadFile(live, "sentinel.bin")
	if err != nil || !bytes.Equal(got, []byte{0, 0xff}) {
		t.Fatalf("rollback bytes=%v error=%v", got, err)
	}
	if _, err := parent.Lstat(".backup-test"); !os.IsNotExist(err) {
		t.Fatalf("rollback backup remains: %v", err)
	}
}

func TestWorkspaceHistoryEmptyCancellationAndCorruptRecord(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	empty, err := prepareWorkspaceCommit(context.Background(), cfg, workspaceCommitRequest{ProjectID: "alpha"}, "empty")
	if err != nil || len(empty.Files) != 0 {
		t.Fatalf("empty snapshot=%+v error=%v", empty, err)
	}
	sidecarFixtureWrite(t, filepath.Join(workspaceProjectRoot(cfg, "alpha", "main"), "new"), []byte("current"))
	if err := restoreWorkspaceSnapshot(context.Background(), cfg, "alpha", "main", empty.Path, empty); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(workspaceProjectRoot(cfg, "alpha", "main"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty restore=%v %v", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareWorkspaceCommit(ctx, cfg, workspaceCommitRequest{ProjectID: "alpha"}, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	if err := restoreWorkspaceSnapshot(ctx, cfg, "alpha", "main", empty.Path, empty); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel restore=%v", err)
	}
	sidecarFixtureWrite(t, filepath.Join(empty.Path, "commit.json"), []byte("{invalid"))
	if _, err := listWorkspaceCommitRecordsConfined(cfg, "alpha", "main"); err == nil {
		t.Fatal("corrupt history silently skipped")
	}
}

func TestWorkspaceHistoryFilesystemCaseAliases(t *testing.T) {
	cfg := APIConfig{WorkspacePath: t.TempDir()}
	history, err := workspaceCommitDir(cfg, "alpha", "main", "aliases")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("same bytes")
	sidecarFixtureWrite(t, filepath.Join(history, "files", "A.md"), data)
	_, aliasErr := os.Stat(filepath.Join(history, "files", "a.md"))
	insensitive := aliasErr == nil
	if aliasErr != nil && !os.IsNotExist(aliasErr) {
		t.Fatal(aliasErr)
	}
	sidecarFixtureWrite(t, filepath.Join(history, "files", "a.md"), data)
	live := workspaceProjectRoot(cfg, "alpha", "main")
	sidecarFixtureWrite(t, filepath.Join(live, "current.md"), []byte("preserve current"))
	record := workspaceCommitRecord{ProjectID: "alpha", Branch: "main", CommitID: "aliases", Files: []workspaceCommitFile{{Path: "A.md", Size: int64(len(data)), Digest: digestBytes(data)}, {Path: "a.md", Size: int64(len(data)), Digest: digestBytes(data)}}}
	err = restoreWorkspaceSnapshot(context.Background(), cfg, "alpha", "main", history, record)
	if insensitive {
		if err == nil {
			t.Fatal("case aliases silently collapsed")
		}
		got, readErr := os.ReadFile(filepath.Join(live, "current.md"))
		if readErr != nil || string(got) != "preserve current" {
			t.Fatal("alias rejection destroyed current tree")
		}
	} else {
		if err != nil {
			t.Fatalf("case-distinct filesystem rejected valid paths: %v", err)
		}
		for _, name := range []string{"A.md", "a.md"} {
			got, err := os.ReadFile(filepath.Join(live, name))
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s=%q %v", name, got, err)
			}
		}
	}
}
