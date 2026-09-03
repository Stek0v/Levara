package bm25

import (
	"fmt"
	"sync"
	"testing"
)

// Concurrent GetOrCreate + Get + Snapshot + Delete must not trip the race
// detector or panic (finding H2, 2026-09-03 review: shared BM25 map raced
// between cognify writers and search/autosave readers).
func TestIndexRegistryConcurrentAccess(t *testing.T) {
	reg := NewIndexRegistry()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				coll := fmt.Sprintf("c%d", i%16)
				switch worker % 4 {
				case 0:
					if idx := reg.GetOrCreate(coll, nil); idx == nil {
						t.Error("GetOrCreate returned nil")
					}
				case 1:
					_ = reg.Get(coll)
				case 2:
					_ = reg.Snapshot()
				case 3:
					_ = reg.Names()
				}
			}
		}(w)
	}
	wg.Wait()
	if reg.Len() != 16 {
		t.Fatalf("Len=%d, want 16", reg.Len())
	}
}

func TestIndexRegistryInsertDelete(t *testing.T) {
	reg := NewIndexRegistry()
	idx := NewIndex()
	reg.Insert("alpha", idx)
	if reg.Get("alpha") != idx {
		t.Fatal("Insert/Get roundtrip failed")
	}
	if got := reg.Delete("alpha"); got != idx {
		t.Fatal("Delete returned wrong index")
	}
	if reg.Get("alpha") != nil {
		t.Fatal("deleted index still present")
	}
	if reg.Delete("missing") != nil {
		t.Fatal("Delete of missing collection should return nil")
	}
}

func TestIndexRegistryGetOrCreateAttachesOnce(t *testing.T) {
	reg := NewIndexRegistry()
	attached := 0
	attach := func(coll string, idx *Index) { attached++ }
	a := reg.GetOrCreate("x", attach)
	b := reg.GetOrCreate("x", attach)
	if a != b {
		t.Fatal("GetOrCreate returned different indexes for same collection")
	}
	if attached != 1 {
		t.Fatalf("attach callback ran %d times, want 1", attached)
	}
}
