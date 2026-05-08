package runreg

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegistry_SnapshotSortsNewestFirst(t *testing.T) {
	r := New()
	now := time.Now()
	r.Store("old", &Status{RunID: "old", Status: "COMPLETED", StartedAt: now.Add(-2 * time.Hour)})
	r.Store("new", &Status{RunID: "new", Status: "RUNNING", StartedAt: now})
	r.Store("mid", &Status{RunID: "mid", Status: "FAILED", StartedAt: now.Add(-1 * time.Hour)})

	snap := r.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot len = %d, want 3", len(snap))
	}
	if snap[0].RunID != "new" || snap[1].RunID != "mid" || snap[2].RunID != "old" {
		t.Errorf("unexpected order: %s, %s, %s", snap[0].RunID, snap[1].RunID, snap[2].RunID)
	}
}

func TestRegistry_SnapshotEmpty(t *testing.T) {
	r := New()
	if got := r.Snapshot(); len(got) != 0 {
		t.Errorf("empty registry Snapshot returned %d entries", len(got))
	}
}

func TestRegistry_StoreLoadRoundTrip(t *testing.T) {
	r := New()
	s := &Status{
		RunID:     "abc",
		Status:    "RUNNING",
		Stage:     "chunk",
		Chunks:    3,
		StartedAt: time.Unix(0, 0),
	}
	r.Store("abc", s)

	got, ok := r.Load("abc")
	if !ok {
		t.Fatal("Load after Store returned ok=false")
	}
	if got != s {
		t.Errorf("Load returned a different pointer (got %p want %p)", got, s)
	}
}

func TestRegistry_LoadAbsent(t *testing.T) {
	r := New()
	got, ok := r.Load("missing")
	if ok {
		t.Error("Load on empty registry returned ok=true")
	}
	if got != nil {
		t.Errorf("Load on empty registry returned non-nil pointer: %+v", got)
	}
}

func TestRegistry_StoreOverwrites(t *testing.T) {
	r := New()
	first := &Status{RunID: "x", Status: "RUNNING"}
	second := &Status{RunID: "x", Status: "COMPLETED"}
	r.Store("x", first)
	r.Store("x", second)
	got, _ := r.Load("x")
	if got != second {
		t.Errorf("Store should overwrite; got %p want %p", got, second)
	}
}

func TestRegistry_MutationThroughLoadedPointerIsVisible(t *testing.T) {
	// This is the pre-refactor contract: background goroutines mutate the
	// struct they stored and readers see those changes through Load. Lock
	// it in so future refactors that copy the value by mistake blow up here.
	r := New()
	s := &Status{RunID: "r1", Status: "RUNNING"}
	r.Store("r1", s)
	s.Status = "COMPLETED"
	s.Message = "done"

	got, _ := r.Load("r1")
	if got.Status != "COMPLETED" || got.Message != "done" {
		t.Errorf("Load did not observe mutations: %+v", got)
	}
}

func TestRegistry_ConcurrentStoreLoad(t *testing.T) {
	// Smoke test: hammer Store+Load from many goroutines, expect no races
	// under -race and no panics from the underlying sync.Map.
	r := New()
	var wg sync.WaitGroup
	const goroutines = 32
	const perG = 200
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				id := "run-" + string(rune('a'+g%26))
				r.Store(id, &Status{RunID: id, Status: "RUNNING"})
				r.Load(id)
			}
		}(g)
	}
	wg.Wait()
}

// PruneTerminalOlderThan evicts only COMPLETED/FAILED entries past cutoff.
// Running entries and young terminal entries must survive.
func TestRegistry_PruneTerminalOlderThan(t *testing.T) {
	r := New()
	now := time.Now()

	// Old terminal — should be evicted.
	r.Store("old-done", &Status{RunID: "old-done", Status: "COMPLETED", StartedAt: now.Add(-2 * time.Hour)})
	r.Store("old-failed", &Status{RunID: "old-failed", Status: "FAILED", StartedAt: now.Add(-90 * time.Minute)})
	// Old but still running — kept.
	r.Store("stuck", &Status{RunID: "stuck", Status: "RUNNING", StartedAt: now.Add(-2 * time.Hour)})
	// Recent terminal — kept (within TTL).
	r.Store("fresh-done", &Status{RunID: "fresh-done", Status: "COMPLETED", StartedAt: now.Add(-5 * time.Minute)})

	evicted := r.PruneTerminalOlderThan(time.Hour)
	if evicted != 2 {
		t.Errorf("evicted = %d, want 2", evicted)
	}

	for _, id := range []string{"old-done", "old-failed"} {
		if _, ok := r.Load(id); ok {
			t.Errorf("%s should have been evicted", id)
		}
	}
	for _, id := range []string{"stuck", "fresh-done"} {
		if _, ok := r.Load(id); !ok {
			t.Errorf("%s should have survived", id)
		}
	}
}

// Janitor ticks on schedule and can be stopped cleanly.
func TestRegistry_StartJanitorStops(t *testing.T) {
	r := New()
	// Plant something that will definitely get evicted.
	r.Store("goner", &Status{
		RunID:     "goner",
		Status:    "COMPLETED",
		StartedAt: time.Now().Add(-time.Hour),
	})
	// Short interval so we don't wait long.
	stop := r.StartJanitor(10*time.Millisecond, time.Minute)

	// Wait up to 200ms for the entry to disappear.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := r.Load("goner"); !ok {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, ok := r.Load("goner"); ok {
		t.Fatal("janitor did not evict the stale terminal run within 200ms")
	}

	// Stop must return promptly.
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop() didn't return within 1s")
	}
}

func TestRegistry_StartJanitorZeroIntervalNoop(t *testing.T) {
	// Passing 0 is legal — useful for tests that don't need a janitor.
	// The returned stop function must still be safely callable.
	r := New()
	stop := r.StartJanitor(0, 0)
	stop() // must not hang
}

func TestStatus_JSONTagsMatchPreRefactor(t *testing.T) {
	// The REST/SSE clients consume these fields verbatim; changing a tag
	// would be a silent breaking change. Guard them here.
	s := Status{
		RunID:     "id1",
		Status:    "COMPLETED",
		Stage:     "done",
		Message:   "ok",
		Chunks:    1,
		Entities:  2,
		Edges:     3,
		ElapsedMs: 42,
		StartedAt: time.Unix(1, 0).UTC(),
	}
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := []string{
		`"pipeline_run_id":"id1"`,
		`"status":"COMPLETED"`,
		`"stage":"done"`,
		`"message":"ok"`,
		`"chunks_created":1`,
		`"entities_extracted":2`,
		`"edges_extracted":3`,
		`"elapsed_ms":42`,
	}
	got := string(out)
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("marshal missing %s; got %s", w, got)
		}
	}
}
