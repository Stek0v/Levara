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
	if err := b.Allow(); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("two failures must open, got %v", err)
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
