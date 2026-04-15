package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubProvider is a no-op Provider used to exercise RateLimiter without network I/O.
type stubProvider struct {
	calls atomic.Int32
	err   error
}

func (s *stubProvider) Name() string { return "stub" }
func (s *stubProvider) ChatCompletion(ctx context.Context, _ CompletionRequest) (*CompletionResponse, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return &CompletionResponse{Content: "ok", Model: "stub"}, nil
}

func TestRateLimiter_ConcurrentAcquire_NoRace(t *testing.T) {
	s := &stubProvider{}
	rl := NewRateLimiter(s, 5, 100*time.Millisecond)
	defer rl.Close()

	// 20 concurrent callers compete for 5 tokens; with refill every 20ms
	// they should all complete within ~500ms. We use a tight timeout to force
	// the rate-limiter path (ctx.Done branch) if the token bucket is broken.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := rl.ChatCompletion(ctx, CompletionRequest{Model: "m"})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("unexpected error: %v", err)
	}
	if got := s.calls.Load(); got != 20 {
		t.Errorf("expected 20 provider calls, got %d", got)
	}
}

func TestRateLimiter_CtxCancelReleasesWaiter(t *testing.T) {
	s := &stubProvider{}
	rl := NewRateLimiter(s, 1, time.Hour) // effectively 1 token forever
	defer rl.Close()

	// First call grabs the only token.
	if _, err := rl.ChatCompletion(context.Background(), CompletionRequest{}); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call must block until ctx is cancelled, not burn CPU and not leak.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := rl.ChatCompletion(ctx, CompletionRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("returned too early (%v) — was ctx path taken?", elapsed)
	}
}

func TestRateLimiter_CloseIdempotent(t *testing.T) {
	// Prior to closeOnce fix, the second Close would panic with
	// "close of closed channel". This test locks in that guarantee.
	rl := NewRateLimiter(&stubProvider{}, 1, time.Second)
	rl.Close()
	rl.Close() // must not panic
	rl.Close()
}

func TestRateLimiter_ConcurrentClose(t *testing.T) {
	// Race detector catches data races on closeOnce misuse under concurrent Close.
	rl := NewRateLimiter(&stubProvider{}, 1, time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); rl.Close() }()
	}
	wg.Wait()
}
