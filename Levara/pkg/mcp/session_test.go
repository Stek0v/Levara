package mcp

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionStore_CreateGetDelete(t *testing.T) {
	s := NewSessionStore()
	sess := s.Create()
	if sess.ID == "" {
		t.Fatal("Create returned empty ID")
	}
	if !strings.HasPrefix(sess.ID, "mcp-") {
		t.Errorf("ID %q does not use mcp- prefix", sess.ID)
	}
	if got := s.Get(sess.ID); got != sess {
		t.Errorf("Get after Create did not return the same pointer")
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}

	s.Delete(sess.ID)
	if s.Get(sess.ID) != nil {
		t.Error("Get after Delete should return nil")
	}
	if s.Count() != 0 {
		t.Errorf("Count after Delete = %d, want 0", s.Count())
	}
}

func TestSessionStore_Adopt_HonorsClientID(t *testing.T) {
	s := NewSessionStore()
	const clientID = "mcp-from-a-previous-process"
	sess := s.Adopt(clientID)
	if sess.ID != clientID {
		t.Errorf("Adopt minted id %q, want the client-supplied %q", sess.ID, clientID)
	}
	if got := s.Get(clientID); got != sess {
		t.Error("adopted session not retrievable by its id")
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1", s.Count())
	}
	// SSECh must be usable (non-nil, buffered) like a Create()d session.
	select {
	case sess.SSECh <- []byte("x"):
	default:
		t.Error("adopted session SSECh not writable")
	}
}

func TestSessionStore_Adopt_Idempotent(t *testing.T) {
	s := NewSessionStore()
	const clientID = "mcp-replayed"
	first := s.Adopt(clientID)
	first.UserID = "alice"
	second := s.Adopt(clientID)
	if first != second {
		t.Error("second Adopt of the same id returned a different session")
	}
	if second.UserID != "alice" {
		t.Errorf("re-adopt clobbered owner: UserID = %q, want alice", second.UserID)
	}
	if s.Count() != 1 {
		t.Errorf("Count = %d, want 1 (no accretion on re-adopt)", s.Count())
	}
}

func TestSessionStore_Adopt_EmptyIDFallsBackToCreate(t *testing.T) {
	s := NewSessionStore()
	sess := s.Adopt("")
	if !strings.HasPrefix(sess.ID, "mcp-") {
		t.Errorf("Adopt(\"\") id = %q, want a freshly-minted mcp- id", sess.ID)
	}
}

func TestSessionStore_GetEmptyIDReturnsNil(t *testing.T) {
	s := NewSessionStore()
	if s.Get("") != nil {
		t.Error("Get(\"\") should return nil without a lookup")
	}
}

func TestSessionStore_DeleteAbsentIsNoop(t *testing.T) {
	s := NewSessionStore()
	// Should not panic, should not fire OnCountChange either.
	var fired atomic.Bool
	s.OnCountChange = func(n int) { fired.Store(true) }
	s.Delete("does-not-exist")
	if fired.Load() {
		t.Error("OnCountChange fired for absent Delete")
	}
}

func TestSessionStore_CreateSSECh_Buffered(t *testing.T) {
	s := NewSessionStore()
	sess := s.Create()
	// Channel should accept sends without blocking up to cap=100.
	for i := 0; i < 100; i++ {
		select {
		case sess.SSECh <- []byte("x"):
		default:
			t.Fatalf("SSECh blocked at send %d; expected cap ≥ 100", i)
		}
	}
}

func TestSessionStore_DeleteClosesSSECh(t *testing.T) {
	s := NewSessionStore()
	sess := s.Create()
	s.Delete(sess.ID)
	select {
	case _, ok := <-sess.SSECh:
		if ok {
			t.Error("SSECh should be closed after Delete")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("SSECh not closed within 100ms of Delete")
	}
}

func TestSessionStore_OnCountChangeFires(t *testing.T) {
	s := NewSessionStore()
	var counts []int
	var mu sync.Mutex
	s.OnCountChange = func(n int) {
		mu.Lock()
		counts = append(counts, n)
		mu.Unlock()
	}

	a := s.Create()
	b := s.Create()
	s.Delete(a.ID)
	s.Delete(b.ID)

	mu.Lock()
	defer mu.Unlock()
	want := []int{1, 2, 1, 0}
	if len(counts) != len(want) {
		t.Fatalf("got %d hook fires, want %d (counts=%v)", len(counts), len(want), counts)
	}
	for i := range want {
		if counts[i] != want[i] {
			t.Errorf("counts[%d] = %d, want %d", i, counts[i], want[i])
		}
	}
}

func TestSessionStore_CleanupIdle_EvictsOld(t *testing.T) {
	s := NewSessionStore()
	old := s.Create()
	old.CreatedAt = time.Now().Add(-2 * time.Hour) // rewind to force eviction
	fresh := s.Create()

	evicted := s.CleanupIdle(time.Hour)
	if evicted != 1 {
		t.Errorf("evicted = %d, want 1", evicted)
	}
	if s.Get(old.ID) != nil {
		t.Error("old session still present after CleanupIdle")
	}
	if s.Get(fresh.ID) == nil {
		t.Error("fresh session evicted by mistake")
	}

	// SSECh of the evicted session must be closed.
	select {
	case _, ok := <-old.SSECh:
		if ok {
			t.Error("evicted session SSECh should be closed")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("evicted SSECh not closed")
	}
}

func TestSessionStore_CleanupIdle_NothingToEvictDoesNotFireHook(t *testing.T) {
	s := NewSessionStore()
	s.Create() // not old enough
	var fired atomic.Int32
	s.OnCountChange = func(n int) { fired.Add(1) }
	evicted := s.CleanupIdle(time.Hour)
	if evicted != 0 {
		t.Errorf("evicted = %d, want 0", evicted)
	}
	if fired.Load() != 0 {
		t.Errorf("OnCountChange fired %d times on zero-eviction sweep", fired.Load())
	}
}

func TestSessionStore_ConcurrentCreateDelete(t *testing.T) {
	// Race detector catches any map or mutex misuse.
	s := NewSessionStore()
	const goroutines = 16
	const perRoutine = 50

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids := make([]string, perRoutine)
			for j := 0; j < perRoutine; j++ {
				ids[j] = s.Create().ID
			}
			for _, id := range ids {
				s.Delete(id)
			}
		}()
	}
	wg.Wait()
	if got := s.Count(); got != 0 {
		t.Errorf("Count after concurrent Create+Delete = %d, want 0", got)
	}
}
