package backup

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type restoreTestEntry struct {
	name     string
	typeflag byte
	body     string
}

func writeRestoreArchive(t *testing.T, entries ...restoreTestEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "restore.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		hdr := &tar.Header{Name: entry.name, Typeflag: typeflag, Mode: 0o644, Size: int64(len(entry.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if entry.body != "" {
			if _, err := tw.Write([]byte(entry.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestFullRestoreRejectsPathTraversal(t *testing.T) {
	dataDir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dataDir), "escaped.txt")
	_ = os.Remove(outside)
	archivePath := writeRestoreArchive(t, restoreTestEntry{name: "../escaped.txt", body: "owned"})

	err := FullRestore(archivePath, dataDir, "")
	if err == nil || !strings.Contains(err.Error(), "unsafe restore entry") {
		t.Fatalf("FullRestore err=%v, want unsafe-entry rejection", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("archive wrote outside restore root: %v", err)
	}
}

func TestFullRestoreRejectsArchiveLinks(t *testing.T) {
	archivePath := writeRestoreArchive(t, restoreTestEntry{name: "uploads/link", typeflag: tar.TypeSymlink})
	if err := FullRestore(archivePath, t.TempDir(), ""); err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("FullRestore symlink err=%v, want rejection", err)
	}
}

func TestFullRestoreExtractsRegularFilesInsideRoot(t *testing.T) {
	dataDir := t.TempDir()
	archivePath := writeRestoreArchive(t,
		restoreTestEntry{name: "uploads/doc.txt", body: "hello"},
		restoreTestEntry{name: "collections/docs/meta.bin", body: "index"},
	)
	if err := FullRestore(archivePath, dataDir, ""); err != nil {
		t.Fatalf("FullRestore: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "uploads", "doc.txt")); err != nil || string(got) != "hello" {
		t.Fatalf("restored upload=%q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dataDir, "restored", "collections", "docs", "meta.bin")); err != nil || string(got) != "index" {
		t.Fatalf("restored collection=%q err=%v", got, err)
	}
}

func TestFullRestoreReplacesHardlinkWithoutOverwritingSource(t *testing.T) {
	dataDir := t.TempDir()
	uploads := filepath.Join(dataDir, "uploads")
	if err := os.MkdirAll(uploads, 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(uploads, "doc.txt")
	if err := os.Link(source, target); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}

	archivePath := writeRestoreArchive(t, restoreTestEntry{name: "uploads/doc.txt", body: "restored"})
	if err := FullRestore(archivePath, dataDir, ""); err != nil {
		t.Fatalf("FullRestore: %v", err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "original" {
		t.Fatalf("hardlink source=%q err=%v, want original", got, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "restored" {
		t.Fatalf("restored target=%q err=%v, want restored", got, err)
	}
}
