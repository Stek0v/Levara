package embed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBreakerStates(t *testing.T) {
	b := NewBreaker(2, 20*time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("closed breaker must allow: %v", err)
	}
	b.RecordFailure()
	if err := b.Allow(); err != nil {
		t.Fatalf("one failure must not open: %v", err)
	}
	b.RecordFailure()
	err := b.Allow()
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("two failures must open, got %v", err)
	}
	var blocked *BreakerOpenError
	if !errors.As(err, &blocked) || !blocked.RetryAt.Equal(b.openUntil) {
		t.Fatalf("blocked call must carry cooldown deadline: %v", err)
	}
	time.Sleep(25 * time.Millisecond)
	if err := b.Allow(); err != nil {
		t.Fatalf("half-open after cooldown must allow probe: %v", err)
	}
	b.RecordSuccess()
	if b.State() != "closed" || b.Allow() != nil {
		t.Fatal("successful probe must close the breaker")
	}
}

func TestClientRechecksBreakerAfterGateWait(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	gate := NewPriorityGate(1)
	if err := gate.acquire(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	c := NewClient(srv.URL, "m", 1, 1).WithPriorityGate(gate)
	done := make(chan error, 1)
	go func() {
		_, err := c.EmbedSingle(context.Background(), "queued")
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for gate.waitingForeground() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if gate.waitingForeground() != 1 {
		gate.release()
		t.Fatal("client did not queue at gate")
	}
	for range 3 {
		c.breaker.RecordFailure()
	}
	gate.release()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBreakerOpen) || hits.Load() != 0 {
			t.Fatalf("queued request must respect newly open breaker: err=%v hits=%d", err, hits.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("queued request did not return")
	}
}

func TestClientRecoversAfterBreakerCooldown(t *testing.T) {
	var hits atomic.Int64
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !healthy.Load() {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "m", 1, 1)
	for range 3 {
		if _, err := c.EmbedSingle(context.Background(), "x"); err == nil {
			t.Fatal("provider failure was ignored")
		}
	}
	_, err := c.EmbedSingle(context.Background(), "x")
	var blocked *BreakerOpenError
	if !errors.As(err, &blocked) || time.Until(blocked.RetryAt) < 25*time.Second || hits.Load() != 3 {
		t.Fatalf("default cooldown not preserved: err=%v hits=%d", err, hits.Load())
	}
	// Advance the breaker's deadline directly; no 30-second test sleep.
	c.breaker.mu.Lock()
	c.breaker.openUntil = time.Now().Add(-time.Second)
	c.breaker.mu.Unlock()
	healthy.Store(true)
	vec, err := c.EmbedSingle(context.Background(), "x")
	if err != nil || len(vec) != 2 || hits.Load() != 4 || c.breaker.State() != "closed" {
		t.Fatalf("provider did not recover: vec=%v err=%v hits=%d state=%s", vec, err, hits.Load(), c.breaker.State())
	}
}

func TestClientFailsFastWhenBreakerOpen(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		time.Sleep(2 * time.Second) // exceeds the 300ms timeout below
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "m", 4, 1).WithTimeout(300 * time.Millisecond)
	// three consecutive timeouts trip the breaker (default threshold 3)
	for i := 0; i < 3; i++ {
		if _, err := c.EmbedTexts(context.Background(), []string{"x"}); err == nil {
			t.Fatalf("expected timeout error on call %d", i)
		}
	}
	start := time.Now()
	_, err := c.EmbedTexts(context.Background(), []string{"x"})
	if !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("expected ErrBreakerOpen, got %v", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatalf("fail-fast took %v — breaker not short-circuiting", time.Since(start))
	}
	if hits.Load() != 3 {
		t.Fatalf("breaker must prevent the 4th HTTP call, server saw %d", hits.Load())
	}
}
