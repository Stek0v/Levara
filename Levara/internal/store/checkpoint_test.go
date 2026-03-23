package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpoint_BasicCompaction(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.bin")

	db, err := NewLevara(4, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert 100 records
	for i := 0; i < 100; i++ {
		if err := db.Insert(fmt.Sprintf("rec-%d", i), randomVec(4), map[string]any{"i": i}); err != nil {
			t.Fatalf("insert rec-%d: %v", i, err)
		}
	}

	// Delete 50
	for i := 0; i < 50; i++ {
		if err := db.Delete(fmt.Sprintf("rec-%d", i)); err != nil {
			t.Fatalf("delete rec-%d: %v", i, err)
		}
	}

	// WAL has 150 entries (100 insert + 50 delete)
	walPath := dbPath + ".wal"
	infoBefore, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL before: %v", err)
	}

	// Checkpoint
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// WAL should be smaller (only 50 live records)
	infoAfter, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL after: %v", err)
	}
	if infoAfter.Size() >= infoBefore.Size() {
		t.Errorf("WAL not compacted: before=%d, after=%d", infoBefore.Size(), infoAfter.Size())
	}
	t.Logf("WAL size: %d -> %d (%.1f%% reduction)", infoBefore.Size(), infoAfter.Size(),
		float64(infoBefore.Size()-infoAfter.Size())/float64(infoBefore.Size())*100)

	// Verify: close and reopen, all 50 live records present
	db.Close()

	db2, err := NewLevara(4, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	// Live records (50-99) should exist
	for i := 50; i < 100; i++ {
		_, _, found := db2.Get(fmt.Sprintf("rec-%d", i))
		if !found {
			t.Errorf("rec-%d should exist after checkpoint recovery", i)
		}
	}

	// Deleted records (0-49) should NOT exist
	for i := 0; i < 50; i++ {
		_, _, found := db2.Get(fmt.Sprintf("rec-%d", i))
		if found {
			t.Errorf("rec-%d should be deleted after checkpoint recovery", i)
		}
	}
}

func TestCheckpoint_WALSizeReduction(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.bin")

	db, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert 1000 records with dim=64
	for i := 0; i < 1000; i++ {
		if err := db.Insert(fmt.Sprintf("rec-%d", i), randomVec(64), map[string]any{"i": i}); err != nil {
			t.Fatalf("insert rec-%d: %v", i, err)
		}
	}
	// Delete 900
	for i := 0; i < 900; i++ {
		if err := db.Delete(fmt.Sprintf("rec-%d", i)); err != nil {
			t.Fatalf("delete rec-%d: %v", i, err)
		}
	}

	walPath := dbPath + ".wal"
	walBefore, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL before: %v", err)
	}

	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	walAfter, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat WAL after: %v", err)
	}

	ratio := float64(walAfter.Size()) / float64(walBefore.Size())
	t.Logf("WAL reduction: %d -> %d (%.1f%%)", walBefore.Size(), walAfter.Size(), ratio*100)

	if ratio > 0.2 { // Should be ~10% (100/1000 live records)
		t.Errorf("Expected >80%% reduction, got %.1f%%", (1-ratio)*100)
	}
}

func TestCheckpoint_SearchAfterCompaction(t *testing.T) {
	const dim = 64
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.bin")

	db, err := NewLevara(dim, dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Insert target + noise records
	target := randomVec(dim)
	db.Insert("target", target, map[string]any{"label": "target"})
	for i := 0; i < 50; i++ {
		db.Insert(fmt.Sprintf("noise-%d", i), randomVec(dim), nil)
	}

	// Checkpoint (doesn't need HNSW to be fully built — just reads arena + disk)
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Close and reopen — recovery replays compacted WAL with synchronous HNSW.Add
	db.Close()
	db, err = NewLevara(dim, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db.Close()

	// Verify via Get that target survived
	_, _, found := db.Get("target")
	if !found {
		t.Fatal("target should exist after checkpoint recovery")
	}

	// Search should find target (recovery uses synchronous HNSW.Add, no async needed)
	results := db.Search(target, 5)
	foundTarget := false
	for _, r := range results {
		if r.ID == "target" {
			foundTarget = true
			break
		}
	}
	if !foundTarget {
		ids := make([]string, len(results))
		for i, r := range results {
			ids[i] = r.ID
		}
		t.Errorf("search after checkpoint: target not in top-5 results: %v", ids)
	}
}

func TestCheckpoint_ContinuedWritesAfter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "meta.bin")

	db, err := NewLevara(4, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Insert some records
	for i := 0; i < 20; i++ {
		db.Insert(fmt.Sprintf("before-%d", i), randomVec(4), nil)
	}

	// Checkpoint
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	// Insert more records after checkpoint
	for i := 0; i < 20; i++ {
		if err := db.Insert(fmt.Sprintf("after-%d", i), randomVec(4), nil); err != nil {
			t.Fatalf("insert after checkpoint: %v", err)
		}
	}

	// Close and reopen — all 40 records should survive
	db.Close()
	db2, err := NewLevara(4, dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	for i := 0; i < 20; i++ {
		_, _, found := db2.Get(fmt.Sprintf("before-%d", i))
		if !found {
			t.Errorf("before-%d should exist after reopen", i)
		}
		_, _, found = db2.Get(fmt.Sprintf("after-%d", i))
		if !found {
			t.Errorf("after-%d should exist after reopen", i)
		}
	}
}
