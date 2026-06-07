package llmcache

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentPutGetReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	// Create and populate
	pc, err := NewPersistent(100, path)
	if err != nil {
		t.Fatal(err)
	}
	key := Key("model", "prompt", "sys", 0)
	pc.Put(key, "response_text", "model")
	pc.Close()

	// Reload from disk
	pc2, err := NewPersistent(100, path)
	if err != nil {
		t.Fatal(err)
	}
	defer pc2.Close()

	resp, hit := pc2.Get(key)
	if !hit {
		t.Fatal("expected cache hit after reload")
	}
	if resp != "response_text" {
		t.Errorf("expected 'response_text', got %q", resp)
	}
}

func TestPersistentFileCreated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	pc, _ := NewPersistent(100, path)
	pc.Put("k1", "v1", "m")
	pc.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Error("file should not be empty")
	}
}

func TestPersistentMultipleEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.jsonl")

	pc, _ := NewPersistent(100, path)
	for i := 0; i < 50; i++ {
		pc.Put(Key("m", "p"+string(rune(i)), "s", 0), "resp", "m")
	}
	pc.Close()

	pc2, _ := NewPersistent(100, path)
	defer pc2.Close()

	if pc2.Stats().Size != 50 {
		t.Errorf("expected 50 entries after reload, got %d", pc2.Stats().Size)
	}
}
