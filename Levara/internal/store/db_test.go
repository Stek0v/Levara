package store

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestDeleteByID(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-delete-test-*")
	defer os.RemoveAll(dir)

	db, err := NewLevara(64, dir+"/meta.bin")
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	defer db.Close()

	// Insert
	vec := randVecForTest(64)
	err = db.Insert("test-1", vec, map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Verify exists
	_, _, exists := db.Get("test-1")
	if !exists {
		t.Fatal("Record should exist after insert")
	}

	// Delete
	err = db.Delete("test-1")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify gone
	_, _, exists = db.Get("test-1")
	if exists {
		t.Fatal("Record should NOT exist after delete")
	}

	// Search should not find it
	time.Sleep(100 * time.Millisecond) // let HNSW indexer run
	results := db.Search(vec, 10)
	for _, r := range results {
		if r.ID == "test-1" {
			t.Fatal("Deleted record found in search results")
		}
	}

	// Delete non-existent should error
	err = db.Delete("nonexistent")
	if err == nil {
		t.Fatal("Delete non-existent should return error")
	}
}

func TestDeleteWALRecovery(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-wal-del-test-*")
	defer os.RemoveAll(dir)

	dbPath := dir + "/meta.bin"

	// Phase 1: Insert + Delete
	db, _ := NewLevara(64, dbPath)
	vec := randVecForTest(64)
	db.Insert("keep-me", randVecForTest(64), map[string]any{"status": "keep"})
	db.Insert("delete-me", vec, map[string]any{"status": "delete"})
	db.Delete("delete-me")
	db.Close()

	// Phase 2: Recover from WAL
	db2, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	defer db2.Close()

	// "keep-me" should survive
	_, _, exists := db2.Get("keep-me")
	if !exists {
		t.Fatal("keep-me should survive WAL recovery")
	}

	// "delete-me" should NOT survive
	_, _, exists = db2.Get("delete-me")
	if exists {
		t.Fatal("delete-me should NOT survive WAL recovery")
	}
}

// TestInsertDeleteInsertWALRecovery verifies that a re-Insert after Delete of the
// same ID survives WAL recovery. The buggy 2-pass recovery (pre-T16) would collect
// all deleted IDs first, then skip any Insert that matches — losing the final Insert.
func TestInsertDeleteInsertWALRecovery(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-wal-ridi-test-*")
	defer os.RemoveAll(dir)
	dbPath := dir + "/meta.bin"

	vec1 := randVecForTest(64)
	vec2 := randVecForTest(64)

	// Phase 1: Insert(id=1, v1) → Delete(id=1) → Insert(id=1, v2)
	db, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	if err := db.Insert("re-id", vec1, map[string]any{"version": 1}); err != nil {
		t.Fatalf("Insert v1: %v", err)
	}
	if err := db.Delete("re-id"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.Insert("re-id", vec2, map[string]any{"version": 2}); err != nil {
		t.Fatalf("Insert v2: %v", err)
	}
	db.Close()

	// Phase 2: Recover from WAL — the second Insert must survive.
	db2, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	defer db2.Close()

	gotVec, gotMeta, exists := db2.Get("re-id")
	if !exists {
		t.Fatal("re-id lost after WAL recovery (Insert→Delete→Insert)")
	}
	if len(gotVec) != 64 {
		t.Fatalf("wrong vector dim: %d", len(gotVec))
	}
	// Must be v2, not v1 — asserts both the vector and the metadata to guard
	// against a future regression that restores the index entry but points it
	// at the first Insert's arena slot.
	if !vecEqual(gotVec, vec2) {
		t.Fatalf("recovered vector != vec2 (Insert→Delete→Insert picked wrong slot)")
	}
	if got := string(gotMeta); got == "" || !contains(got, `"version":2`) {
		t.Fatalf("expected version=2 metadata, got %q", got)
	}
}

// vecEqual compares two vectors bit-for-bit (no tolerance — the test seed
// uses rand.Float32, so there's no floating-point arithmetic in play).
func vecEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestInsertInsertDeleteWALRecovery verifies the reverse order: final Delete wins.
func TestInsertInsertDeleteWALRecovery(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-wal-iid-test-*")
	defer os.RemoveAll(dir)
	dbPath := dir + "/meta.bin"

	db, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	db.Insert("id", randVecForTest(64), map[string]any{"v": 1})
	db.Insert("id", randVecForTest(64), map[string]any{"v": 2})
	db.Delete("id")
	db.Close()

	db2, err := NewLevara(64, dbPath)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	defer db2.Close()

	if _, _, exists := db2.Get("id"); exists {
		t.Fatal("id should NOT exist after Insert→Insert→Delete recovery")
	}
}

// contains is a tiny helper so we don't pull strings in the test file for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestBatchDelete(t *testing.T) {
	dir, _ := os.MkdirTemp("", "levara-batch-del-test-*")
	defer os.RemoveAll(dir)

	db, _ := NewLevara(64, dir+"/meta.bin")
	defer db.Close()

	// Insert 10 records
	for i := 0; i < 10; i++ {
		db.Insert(fmt.Sprintf("rec-%d", i), randVecForTest(64),
			map[string]any{"index": i})
	}

	// Delete odd-numbered
	ids := []string{"rec-1", "rec-3", "rec-5", "rec-7", "rec-9"}
	errs := db.BatchDelete(ids)
	if len(errs) > 0 {
		t.Fatalf("BatchDelete errors: %v", errs)
	}

	// Verify even survive, odd don't
	for i := 0; i < 10; i++ {
		_, _, exists := db.Get(fmt.Sprintf("rec-%d", i))
		if i%2 == 0 && !exists {
			t.Fatalf("rec-%d should exist (even)", i)
		}
		if i%2 == 1 && exists {
			t.Fatalf("rec-%d should NOT exist (odd, deleted)", i)
		}
	}
}
