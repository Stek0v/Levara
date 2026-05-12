package store

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHNSW_ConcurrentSearchAdd_NoRace is the regression test for F-6.
//
// Prior to the fix, HNSWIndex.Search released h.RLock before traversing the
// graph, letting HNSWIndex.Add concurrently mutate newNode.Connections without
// synchronisation. Under `go test -race` this flagged immediately:
//
//	WARNING: DATA RACE
//	  Read at ... by goroutine N: searchLayerTopK hnsw.go:361
//	  Previous write ... by goroutine M: Add hnsw.go:270
//
// The fix holds h.RLock for the whole Search call. This test drives concurrent
// inserts and searches to prove the fix sticks — it must pass under -race.
func TestHNSW_ConcurrentSearchAdd_NoRace(t *testing.T) {
	const (
		dim      = 32
		seedN    = 200 // pre-populate so Search has something to traverse
		writers  = 2
		readers  = 8
		duration = 300 * time.Millisecond
	)

	arena := NewVectorArena(dim)
	idx := NewHNSWIndex(arena, DefaultHNSWConfig())

	seed := func(i int) {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rand.Float32() - 0.5
		}
		off, err := arena.Add(v)
		if err != nil {
			t.Fatalf("seed Add: %v", err)
		}
		idx.Add(v, uuidish(i), off)
	}
	for i := 0; i < seedN; i++ {
		seed(i)
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var searches, inserts atomic.Int64
	var wg sync.WaitGroup

	// Writers: keep adding new vectors
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(base) + 1))
			i := seedN + base*100000
			for ctx.Err() == nil {
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32() - 0.5
				}
				off, err := arena.Add(v)
				if err != nil {
					return
				}
				idx.Add(v, uuidish(i), off)
				i++
				inserts.Add(1)
			}
		}(w)
	}

	// Readers: keep searching
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			q := make([]float32, dim)
			for ctx.Err() == nil {
				for j := range q {
					q[j] = rng.Float32() - 0.5
				}
				_ = idx.Search(q, 10)
				searches.Add(1)
			}
		}(int64(r) * 7919)
	}

	wg.Wait()

	if searches.Load() == 0 {
		t.Error("no searches ran — timing issue in test")
	}
	if inserts.Load() == 0 {
		t.Error("no inserts ran — timing issue in test")
	}
	t.Logf("completed %d searches / %d inserts in %v without data race",
		searches.Load(), inserts.Load(), duration)
}

func TestHNSW_ReinsertDeletedEntryRefreshesEntryLayer(t *testing.T) {
	arena := NewVectorArena(3)
	idx := NewHNSWIndex(arena, DefaultHNSWConfig())
	levelRolls := []float64{
		0.8,      // replacement gets layer 0
		0.8,      // first neighbor gets layer 0
		0.2, 0.8, // second neighbor gets layer 1
	}
	idx.randFloat64 = func() float64 {
		if len(levelRolls) == 0 {
			return 0.8
		}
		next := levelRolls[0]
		levelRolls = levelRolls[1:]
		return next
	}

	oldVec := []float32{1, 0, 0}
	oldOffset, err := arena.Add(oldVec)
	if err != nil {
		t.Fatalf("old arena Add: %v", err)
	}
	oldEntry := &HNSWNode{
		ID:          "same-id",
		Layer:       3,
		Connections: make([][]uint32, 4),
		ArenaOffset: oldOffset,
	}
	idx.Nodes[oldEntry.ID] = oldEntry
	idx.registerNode(oldEntry)
	idx.EntryNodeID = oldEntry.ID
	idx.MaxLayer = oldEntry.Layer
	idx.MarkDeleted(oldOffset)

	replacementVec := []float32{0, 1, 0}
	replacementOffset, err := arena.Add(replacementVec)
	if err != nil {
		t.Fatalf("replacement arena Add: %v", err)
	}
	idx.Add(replacementVec, "same-id", replacementOffset)

	entry := idx.Nodes[idx.EntryNodeID]
	if entry == nil {
		t.Fatal("entry node should exist after replacing deleted entry")
	}
	if idx.MaxLayer > entry.Layer {
		t.Fatalf("stale MaxLayer=%d exceeds entry layer=%d", idx.MaxLayer, entry.Layer)
	}

	for i := 0; i < 8; i++ {
		vec := []float32{float32(i + 1), float32(i % 2), 1}
		offset, err := arena.Add(vec)
		if err != nil {
			t.Fatalf("neighbor arena Add: %v", err)
		}
		idx.Add(vec, fmt.Sprintf("neighbor-%d", i), offset)
	}

	results := idx.Search(replacementVec, 3)
	for _, result := range results {
		if result.ID == "same-id" {
			return
		}
	}
	t.Fatalf("replacement ID missing from search results: %+v", results)
}

// uuidish builds a short deterministic id without needing UUIDv4.
func uuidish(i int) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 8)
	for k := 0; k < 8; k++ {
		b[k] = hex[(i>>(4*k))&0xf]
	}
	return string(b)
}
