package runreg

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

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
