package store

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestDeleteByID(t *testing.T) {
	dir, _ := os.MkdirTemp("", "cognevra-delete-test-*")
	defer os.RemoveAll(dir)

	db, err := NewCognevra(64, dir+"/meta.bin")
	if err != nil {
		t.Fatalf("NewCognevra: %v", err)
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
	dir, _ := os.MkdirTemp("", "cognevra-wal-del-test-*")
	defer os.RemoveAll(dir)

	dbPath := dir + "/meta.bin"

	// Phase 1: Insert + Delete
	db, _ := NewCognevra(64, dbPath)
	vec := randVecForTest(64)
	db.Insert("keep-me", randVecForTest(64), map[string]any{"status": "keep"})
	db.Insert("delete-me", vec, map[string]any{"status": "delete"})
	db.Delete("delete-me")
	db.Close()

	// Phase 2: Recover from WAL
	db2, err := NewCognevra(64, dbPath)
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

func TestBatchDelete(t *testing.T) {
	dir, _ := os.MkdirTemp("", "cognevra-batch-del-test-*")
	defer os.RemoveAll(dir)

	db, _ := NewCognevra(64, dir+"/meta.bin")
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
