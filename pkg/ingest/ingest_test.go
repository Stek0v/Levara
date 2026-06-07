package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestSingleText(t *testing.T) {
	dir := t.TempDir()
	items := []Item{{Text: "Hello world", DatasetName: "test"}}

	results, err := Ingest(items, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.ContentHash == "" {
		t.Error("hash should not be empty")
	}
	if r.ID == "" {
		t.Error("ID should not be empty")
	}
	if !strings.HasPrefix(r.FilePath, "file://") {
		t.Errorf("file path should be URI, got %s", r.FilePath)
	}
	if r.FileSize != 11 { // "Hello world" = 11 bytes
		t.Errorf("expected 11 bytes, got %d", r.FileSize)
	}
	if r.MimeType != "text/plain" {
		t.Errorf("expected text/plain, got %s", r.MimeType)
	}

	// File should exist
	path := strings.TrimPrefix(r.FilePath, "file://")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestIngestDeterministicID(t *testing.T) {
	dir := t.TempDir()
	items := []Item{{Text: "same content", DatasetName: "ds1"}}

	r1, _ := Ingest(items, dir)
	r2, _ := Ingest(items, filepath.Join(dir, "sub"))

	if r1[0].ID != r2[0].ID {
		t.Errorf("same content+dataset should produce same ID: %s != %s", r1[0].ID, r2[0].ID)
	}
	if r1[0].ContentHash != r2[0].ContentHash {
		t.Error("same content should produce same hash")
	}
}

func TestIngestDedup(t *testing.T) {
	dir := t.TempDir()
	items := []Item{
		{Text: "duplicate text", DatasetName: "ds"},
		{Text: "duplicate text", DatasetName: "ds"}, // same content
		{Text: "unique text", DatasetName: "ds"},
	}

	results, err := Ingest(items, dir)
	if err != nil {
		t.Fatal(err)
	}

	if !results[1].AlreadyExists {
		t.Error("second item should be marked as duplicate")
	}
	if results[0].AlreadyExists {
		t.Error("first item should NOT be marked as duplicate")
	}
	if results[2].AlreadyExists {
		t.Error("unique item should NOT be marked as duplicate")
	}
}

func TestIngestBinaryFile(t *testing.T) {
	dir := t.TempDir()
	items := []Item{{
		FileData: []byte("binary content here"),
		Filename: "test.bin",
		DatasetName: "ds",
	}}

	results, err := Ingest(items, dir)
	if err != nil {
		t.Fatal(err)
	}

	r := results[0]
	if r.Extension != ".bin" {
		t.Errorf("expected .bin, got %s", r.Extension)
	}
	if r.FileSize != 19 {
		t.Errorf("expected 19 bytes, got %d", r.FileSize)
	}
}

func TestIngestParallel(t *testing.T) {
	dir := t.TempDir()
	items := make([]Item, 100)
	for i := range items {
		items[i] = Item{Text: strings.Repeat("x", i+10), DatasetName: "ds"}
	}

	results, err := Ingest(items, dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 100 {
		t.Fatalf("expected 100 results, got %d", len(results))
	}

	// All should have unique hashes (different text lengths)
	hashes := map[string]bool{}
	for _, r := range results {
		if r.ContentHash == "" {
			t.Error("empty hash")
		}
		hashes[r.ContentHash] = true
	}
	if len(hashes) != 100 {
		t.Errorf("expected 100 unique hashes, got %d", len(hashes))
	}
}

func TestIngestCustomID(t *testing.T) {
	dir := t.TempDir()
	items := []Item{{ID: "custom-id-123", Text: "content", DatasetName: "ds"}}

	results, _ := Ingest(items, dir)
	if results[0].ID != "custom-id-123" {
		t.Errorf("expected custom ID, got %s", results[0].ID)
	}
}

func TestSanitizeFilename(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		"foo.txt":                    "foo.txt",
		"../../etc/passwd":           "passwd",
		"/etc/passwd":                "passwd",
		"..":                         "",
		".":                          "",
		"/":                          "",
		"\\..\\..\\windows\\sys.dll": "sys.dll",
		"a/b/c.md":                   "c.md",
		"with\x00null":               "",
	}
	for in, want := range cases {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIngestRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	// Create the target the attack would overwrite, outside storagePath.
	outside := filepath.Join(dir, "..", "pwned.txt")
	items := []Item{{
		FileData: []byte("malicious"),
		Filename: "../pwned.txt",
	}}
	results, err := Ingest(items, filepath.Join(dir, "store"))
	// Either Ingest returns an error or the file is written safely inside store/.
	if err == nil {
		if _, statErr := os.Stat(outside); statErr == nil {
			t.Fatalf("traversal succeeded: %s exists", outside)
		}
		if len(results) == 1 && !strings.HasPrefix(results[0].FilePath, "file://"+filepath.Join(dir, "store")) {
			t.Fatalf("file landed outside store/: %s", results[0].FilePath)
		}
	}
}

func BenchmarkIngest100(b *testing.B) {
	dir := b.TempDir()
	items := make([]Item, 100)
	for i := range items {
		items[i] = Item{Text: strings.Repeat("benchmark text content ", 50), DatasetName: "bench"}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Ingest(items, dir)
	}
}
