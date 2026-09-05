package ingest

import (
	"os"
	"strings"
	"testing"
)

func TestIngestFilenameDoesNotAliasAnotherDocument(t *testing.T) {
	for _, owner := range []string{"alice", "bob"} {
		t.Run(owner, func(t *testing.T) {
			dir := t.TempDir()
			first, err := Ingest([]Item{{Text: "Alice confidential report", Filename: "report.txt", OwnerID: "alice"}}, dir)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Ingest([]Item{{Text: "Different report", Filename: "report.txt", OwnerID: owner}}, dir)
			if err != nil {
				t.Fatal(err)
			}
			if first[0].FilePath == second[0].FilePath {
				t.Fatal("different documents share one storage path")
			}
			for i, result := range []Result{first[0], second[0]} {
				data, err := os.ReadFile(strings.TrimPrefix(result.FilePath, "file://"))
				want := []string{"Alice confidential report", "Different report"}[i]
				if err != nil || string(data) != want {
					t.Fatalf("document %d bytes=%q err=%v, want %q", i, data, err, want)
				}
			}
		})
	}
}

func TestIngestDuplicatesAlwaysReferenceExistingContent(t *testing.T) {
	dir := t.TempDir()
	results, err := Ingest([]Item{
		{Text: "same content", Filename: "one.txt", OwnerID: "alice"},
		{Text: "same content", Filename: "two.txt", OwnerID: "alice"},
		{Text: "same content", Filename: "three.txt", OwnerID: "bob"},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		data, err := os.ReadFile(strings.TrimPrefix(result.FilePath, "file://"))
		if err != nil || string(data) != "same content" {
			t.Errorf("duplicate references missing or wrong content: %q: %v", result.FilePath, err)
		}
	}
	if results[0].FilePath == results[2].FilePath || results[0].ID == results[2].ID {
		t.Error("different owners share storage identity")
	}
}
