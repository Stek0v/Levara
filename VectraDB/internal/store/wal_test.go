package store

import (
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
)

func TestGroupCommitWAL_ConcurrentFlush(t *testing.T) {
	dir, _ := os.MkdirTemp("", "vectra-gc-concurrent-*")
	defer os.RemoveAll(dir)

	wal, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("OpenWal: %v", err)
	}

	const numGoroutines = 100
	var wg sync.WaitGroup
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("entry-%d", idx)
			vec := []float32{float32(idx), float32(idx + 1), float32(idx + 2), float32(idx + 3)}
			meta := []byte(fmt.Sprintf(`{"idx":%d}`, idx))

			if err := wal.WriteEntryNoFlush(OpInsert, id, vec, meta, FileLocation{Offset: int64(idx), Length: 4}); err != nil {
				errs[idx] = fmt.Errorf("write: %w", err)
				return
			}
			if err := wal.FlushAsync(); err != nil {
				errs[idx] = fmt.Errorf("flush: %w", err)
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}

	// Close and reopen to verify all entries persisted.
	wal.Close()

	wal2, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	var count int32
	err = wal2.Recover(func(id string, vector []float32, meta []byte, loc FileLocation) {
		atomic.AddInt32(&count, 1)
	})
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if count != numGoroutines {
		t.Fatalf("expected %d entries after recovery, got %d", numGoroutines, count)
	}
}

func TestGroupCommitWAL_Coalescing(t *testing.T) {
	dir, _ := os.MkdirTemp("", "vectra-gc-coalesce-*")
	defer os.RemoveAll(dir)

	wal, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("OpenWal: %v", err)
	}

	const numEntries = 50
	var wg sync.WaitGroup

	// Fire all 50 writes+flushes concurrently so fsyncLoop can coalesce them.
	for i := 0; i < numEntries; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := fmt.Sprintf("coalesce-%d", idx)
			vec := []float32{float32(idx), float32(idx)}
			wal.WriteEntryNoFlush(OpInsert, id, vec, nil, FileLocation{})
			wal.FlushAsync()
		}(i)
	}
	wg.Wait()

	syncs := wal.SyncCount()
	t.Logf("Coalescing: %d entries, %d fsyncs (%.1fx reduction)", numEntries, syncs, float64(numEntries)/float64(syncs))

	if syncs >= uint64(numEntries) {
		t.Fatalf("expected fewer fsyncs (%d) than entries (%d) — group commit not coalescing", syncs, numEntries)
	}

	wal.Close()
}

func TestGroupCommitWAL_DeleteRecovery(t *testing.T) {
	dir, _ := os.MkdirTemp("", "vectra-gc-delrec-*")
	defer os.RemoveAll(dir)

	wal, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("OpenWal: %v", err)
	}

	// Write insert + delete via group commit path
	wal.WriteEntryNoFlush(OpInsert, "keep", []float32{1, 2}, []byte(`{"k":1}`), FileLocation{Offset: 0, Length: 7})
	wal.FlushAsync()

	wal.WriteEntryNoFlush(OpInsert, "remove", []float32{3, 4}, []byte(`{"k":2}`), FileLocation{Offset: 7, Length: 7})
	wal.FlushAsync()

	wal.WriteEntryNoFlush(OpDelete, "remove", nil, nil, FileLocation{})
	wal.FlushAsync()

	wal.Close()

	// Recover and verify
	wal2, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer wal2.Close()

	entries := make(map[string]byte) // id -> last op
	wal2.RecoverEx(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		entries[id] = op
	})

	if entries["keep"] != OpInsert {
		t.Fatalf("expected 'keep' to be OpInsert, got %d", entries["keep"])
	}
	if entries["remove"] != OpDelete {
		t.Fatalf("expected 'remove' to be OpDelete, got %d", entries["remove"])
	}
}

func TestGroupCommitWAL_FlushAsyncBlocksUntilDurable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "vectra-gc-durable-*")
	defer os.RemoveAll(dir)

	wal, err := OpenWal(dir + "/test.wal")
	if err != nil {
		t.Fatalf("OpenWal: %v", err)
	}

	// Sequential: write + flush, then immediately verify on-disk presence.
	wal.WriteEntryNoFlush(OpInsert, "durable-1", []float32{1, 2, 3, 4}, []byte(`{}`), FileLocation{})
	err = wal.FlushAsync()
	if err != nil {
		t.Fatalf("FlushAsync: %v", err)
	}

	// syncCount should be >= 1
	if wal.SyncCount() == 0 {
		t.Fatal("expected at least 1 fsync after FlushAsync")
	}

	wal.Close()
}
