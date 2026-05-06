package bm25

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentAddSearchReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bm25.jsonl")

	pi, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	pi.Add("d1", "quantum computers use qubits", "")
	pi.Add("d2", "natural language processing", "")
	pi.Close()

	// Reload
	pi2, err := NewPersistent(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pi2.Close()

	if pi2.Size() != 2 {
		t.Fatalf("expected 2 docs after reload, got %d", pi2.Size())
	}

	results := pi2.Search("quantum qubits", 3)
	if len(results) == 0 || results[0].ID != "d1" {
		t.Errorf("expected d1 as top result, got %v", results)
	}
}

func TestPersistentFileCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bm25.jsonl")

	pi, _ := NewPersistent(path)
	pi.Add("d1", "test document", "")
	pi.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("file should not be empty")
	}
}

func TestPersistentRussian(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bm25.jsonl")

	pi, _ := NewPersistent(path)
	pi.Add("r1", "Телепат Эмбер читает мысли", "")
	pi.Close()

	pi2, _ := NewPersistent(path)
	defer pi2.Close()

	results := pi2.Search("телепат", 3)
	if len(results) == 0 || results[0].ID != "r1" {
		t.Errorf("expected r1, got %v", results)
	}
}
