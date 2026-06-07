package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// T-1 regression tests: concurrent Insert/Search/Delete on the full Levara DB
// (arena + WAL + HNSW + metaLocs). Complements hnsw_race_test.go which covers
// the HNSW index in isolation — these exercise the full path through db.mu,
// pendingMu, and the async indexerLoop.

func newTempDB(t testing.TB, dim int) (*Levara, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-t1-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return db, func() {
		_ = db.Close()
		os.RemoveAll(dir)
	}
}

// TestDB_ConcurrentInsertSearch drives many writers + many readers against the
// full DB API. Intent: catch races between Insert's mutations (arena, index,
// revIndex, metaLocs, pendingVecs) and Search's traversal.
func TestDB_ConcurrentInsertSearch(t *testing.T) {
	const (
		dim      = 32
		seed     = 100
		writers  = 3
		readers  = 6
		duration = 300 * time.Millisecond
	)
	db, cleanup := newTempDB(t, dim)
	defer cleanup()

	for i := 0; i < seed; i++ {
		if err := db.Insert(fmt.Sprintf("seed-%d", i), randomVec(dim), nil); err != nil {
			t.Fatalf("seed insert: %v", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var inserts, searches atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(base) + 17))
			i := 0
			for ctx.Err() == nil {
				id := fmt.Sprintf("w%d-i%d", base, i)
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32()
				}
				if err := db.Insert(id, v, nil); err != nil {
					t.Errorf("insert: %v", err)
					return
				}
				inserts.Add(1)
				i++
			}
		}(w)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(s int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(s))
			q := make([]float32, dim)
			for ctx.Err() == nil {
				for j := range q {
					q[j] = rng.Float32()
				}
				_ = db.Search(q, 10)
				searches.Add(1)
			}
		}(int64(r) * 31)
	}

	wg.Wait()
	if inserts.Load() == 0 || searches.Load() == 0 {
		t.Fatalf("starvation: inserts=%d searches=%d", inserts.Load(), searches.Load())
	}
	t.Logf("inserts=%d searches=%d in %v (post-race)", inserts.Load(), searches.Load(), duration)
}

// TestDB_ConcurrentInsertDelete interleaves Insert and Delete of disjoint ID
// spaces so Delete does not race with same-ID Insert. Checks that Count() stays
// consistent with (insertsDone - deletesDone).
func TestDB_ConcurrentInsertDelete(t *testing.T) {
	const (
		dim      = 16
		duration = 250 * time.Millisecond
	)
	db, cleanup := newTempDB(t, dim)
	defer cleanup()

	// Prepopulate a pool of deletable IDs so Delete goroutine has work from t=0.
	const prepop = 500
	for i := 0; i < prepop; i++ {
		if err := db.Insert(fmt.Sprintf("del-%d", i), randomVec(dim), nil); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var ins, del atomic.Int64
	var wg sync.WaitGroup

	// Inserter — new IDs only, so Delete on del-* pool can't collide.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for ctx.Err() == nil {
			if err := db.Insert(fmt.Sprintf("ins-%d", i), randomVec(dim), nil); err != nil {
				t.Errorf("insert: %v", err)
				return
			}
			ins.Add(1)
			i++
		}
	}()

	// Deleter — pool is finite; stop gracefully once drained.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < prepop && ctx.Err() == nil; i++ {
			if err := db.Delete(fmt.Sprintf("del-%d", i)); err != nil {
				return
			}
			del.Add(1)
		}
	}()

	wg.Wait()

	gotCount := db.Count()
	wantCount := prepop + int(ins.Load()) - int(del.Load())
	if gotCount != wantCount {
		t.Errorf("Count=%d, want %d (prepop=%d + ins=%d - del=%d)",
			gotCount, wantCount, prepop, ins.Load(), del.Load())
	}
	t.Logf("ins=%d del=%d final_count=%d", ins.Load(), del.Load(), gotCount)
}

// TestDB_WALReplayFullCycle writes N records, closes the DB without explicit
// Checkpoint, reopens, and verifies all records come back via WAL replay.
// This is the minimum-viable "crash recovery" test: Close() replaces the more
// aggressive kill-9 scenario because WAL is group-commit-flushed on each
// Insert path.
func TestDB_WALReplayFullCycle(t *testing.T) {
	const (
		dim = 8
		n   = 120
	)
	dir, err := os.MkdirTemp("", "levara-t1-replay-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := dir + "/meta.bin"

	db, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(42))
	want := make(map[string][]float32, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("rec-%d", i)
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		want[id] = v
		if err := db.Insert(id, v, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Delete a handful to verify tombstones are replayed too.
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("rec-%d", i)
		if err := db.Delete(id); err != nil {
			t.Fatal(err)
		}
		delete(want, id)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: WAL replay should reconstruct the in-memory state.
	db2, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()

	if got := db2.Count(); got != len(want) {
		t.Errorf("Count after replay = %d, want %d", got, len(want))
	}
	missing := 0
	for id := range want {
		if _, _, ok := db2.Get(id); !ok {
			missing++
		}
	}
	if missing > 0 {
		t.Errorf("%d records missing after replay (of %d)", missing, len(want))
	}
	// Deleted IDs must NOT come back.
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("rec-%d", i)
		if _, _, ok := db2.Get(id); ok {
			t.Errorf("deleted id %q resurrected after replay", id)
		}
	}
}

// TestDB_CheckpointUnderLoad races Checkpoint against concurrent Inserts.
// Checkpoint serialises on db.mu, so correctness is the invariant — not perf.
// Post-checkpoint, reopening the DB must yield the same record set.
func TestDB_CheckpointUnderLoad(t *testing.T) {
	const (
		dim       = 16
		writers   = 2
		perWriter = 200
	)
	dir, err := os.MkdirTemp("", "levara-t1-ckpt-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := dir + "/meta.bin"

	db, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				id := fmt.Sprintf("w%d-%d", base, i)
				if err := db.Insert(id, randomVec(dim), nil); err != nil {
					t.Errorf("insert: %v", err)
					return
				}
				if i == perWriter/2 && base == 0 {
					if err := db.Checkpoint(); err != nil {
						t.Errorf("checkpoint: %v", err)
					}
				}
			}
		}(w)
	}
	wg.Wait()

	wantCount := writers * perWriter
	if got := db.Count(); got != wantCount {
		t.Errorf("Count after load = %d, want %d", got, wantCount)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen and confirm post-checkpoint WAL + replay produced the same set.
	db2, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db2.Close() }()
	if got := db2.Count(); got != wantCount {
		t.Errorf("Count after reopen = %d, want %d", got, wantCount)
	}
}
