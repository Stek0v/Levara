package store

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestClear_DrainsPendingVecs_NoIndexerPanic regresses a flaky panic seen in
// internal/cluster's snapshot/restore tests under `go test -race ./...`:
//
//	panic: runtime error: index out of range [N] with length M
//	  goroutine running indexerLoop → HNSWIndex.Add → searchLayerTopK
//	  → Arena.GetNoLock (db.go:207, hnsw.go:361)
//
// Root cause: Insert releases db.mu *before* appending to db.pendingVecs under
// db.pendingMu. A concurrent Clear() (e.g. from FSM.Restore) takes db.mu,
// swaps in a fresh empty arena+hnsw, and returns — but never touches
// pendingVecs. The indexer goroutine then drains a stale idx against the new
// (empty) arena, and Arena.GetNoLock indexes past len(a.pages).
//
// Fix: Clear() resets pendingVecs under pendingMu after the arena swap; and
// GetNoLock has a defensive bounds check. This test forces the window with
// runtime.Gosched() pressure: many writers issue Inserts while a single
// goroutine repeatedly Clears. Without the fix this panics within seconds.
func TestClear_DrainsPendingVecs_NoIndexerPanic(t *testing.T) {
	dir := t.TempDir()
	db, err := NewLevara(16, dir+"/meta.bin")
	if err != nil {
		t.Fatalf("NewLevara: %v", err)
	}
	defer db.Close()

	const (
		writers  = 8
		duration = 500 * time.Millisecond
	)

	stop := make(chan struct{})
	var inserts, clears atomic.Int64

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			vec := make([]float32, 16)
			for j := range vec {
				vec[j] = float32(id+j) * 0.01
			}
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				key := fmt.Sprintf("w%d-i%d", id, i)
				_ = db.Insert(key, vec, map[string]any{"k": key})
				inserts.Add(1)
				i++
				runtime.Gosched()
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			db.Clear()
			clears.Add(1)
			runtime.Gosched()
			time.Sleep(time.Microsecond * 200)
		}
	}()

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	// Give the indexer a moment to drain the final post-Clear batch.
	time.Sleep(50 * time.Millisecond)

	if inserts.Load() == 0 {
		t.Fatal("no inserts ran — timing issue")
	}
	if clears.Load() == 0 {
		t.Fatal("no clears ran — timing issue")
	}
	t.Logf("survived %d inserts / %d clears with no indexer panic",
		inserts.Load(), clears.Load())
}
