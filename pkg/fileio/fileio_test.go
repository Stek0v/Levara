package fileio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFiles(t *testing.T) {
	// Create temp files
	dir := t.TempDir()
	f1 := filepath.Join(dir, "test1.txt")
	f2 := filepath.Join(dir, "test2.txt")
	os.WriteFile(f1, []byte("hello world"), 0644)
	os.WriteFile(f2, []byte("goodbye world"), 0644)

	results := HashFiles([]string{f1, f2, filepath.Join(dir, "nonexistent.txt")}, 4)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// File 1: valid hash
	if results[0].SHA256 == "" || results[0].Error != "" {
		t.Errorf("file1: expected hash, got error=%s", results[0].Error)
	}
	if results[0].FileSize != 11 {
		t.Errorf("file1: expected size 11, got %d", results[0].FileSize)
	}
	// File 2: valid hash
	if results[1].SHA256 == "" {
		t.Error("file2: expected hash")
	}
	// File 3: error
	if results[2].Error == "" {
		t.Error("file3: expected error for nonexistent file")
	}
	// Different files have different hashes
	if results[0].SHA256 == results[1].SHA256 {
		t.Error("different files should have different hashes")
	}
}

func TestHashFilesDeterministic(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	os.WriteFile(f, []byte("deterministic content"), 0644)

	r1 := HashFiles([]string{f}, 1)
	r2 := HashFiles([]string{f}, 1)

	if r1[0].SHA256 != r2[0].SHA256 {
		t.Error("same file should produce same hash")
	}
}

func TestListDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "b.pdf"), []byte("b"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("c"), 0644)

	// Flat, no filter
	files := ListDirectory(dir, false, nil)
	if len(files) != 2 { // a.txt, b.pdf (no sub/)
		t.Errorf("flat: expected 2, got %d", len(files))
	}

	// Recursive, no filter
	files = ListDirectory(dir, true, nil)
	if len(files) != 3 { // a.txt, b.pdf, sub/c.txt
		t.Errorf("recursive: expected 3, got %d", len(files))
	}

	// Recursive, filter .txt
	files = ListDirectory(dir, true, []string{".txt"})
	if len(files) != 2 { // a.txt, sub/c.txt
		t.Errorf("filtered: expected 2, got %d", len(files))
	}
}

func TestListDirectoryNonexistent(t *testing.T) {
	files := ListDirectory("/nonexistent/path", true, nil)
	if len(files) != 0 {
		t.Errorf("expected empty, got %d", len(files))
	}
}
