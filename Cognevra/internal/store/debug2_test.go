package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestDebugCheckpointSearch(t *testing.T) {
	const dim = 64
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.bin")

	db, err := NewCognevra(dim, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	target := randomVec(dim)
	db.Insert("target", target, map[string]any{"label": "target"})
	for i := 0; i < 50; i++ {
		db.Insert(fmt.Sprintf("noise-%d", i), randomVec(dim), nil)
	}
	time.Sleep(200 * time.Millisecond)

	// Search before checkpoint
	r1 := db.Search(target, 1)
	t.Logf("Before checkpoint: %d results", len(r1))

	// Checkpoint
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Search AFTER checkpoint, BEFORE close (same db instance)
	r2 := db.Search(target, 1)
	t.Logf("After checkpoint, before close: %d results", len(r2))

	db.Close()

	// Reopen
	db2, err := NewCognevra(dim, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	t.Logf("Records after checkpoint recovery: %d", len(db2.index))
	t.Logf("HNSW nodes: %d, entry: %s", len(db2.hnsw.Nodes), db2.hnsw.EntryNodeID)

	r3 := db2.Search(target, 5)
	t.Logf("After checkpoint recovery: %d results", len(r3))
	for _, r := range r3 {
		t.Logf("  %s: %.4f", r.ID, r.Score)
	}
}
