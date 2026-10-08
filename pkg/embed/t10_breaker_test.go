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

func TestT10CallerFailureDoesNotTripBreaker(t *testing.T) {
	for _, kind := range []string{"cancel", "deadline", "guard"} {
		t.Run(kind, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "m", 1, 1)
			denied := errors.New("authority revoked")
			for range 3 {
				ctx := context.Background()
				client := c
				switch kind {
				case "guard":
					client = c.WithGuard(func(context.Context) (func(), error) { return nil, denied })
				case "cancel":
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				default:
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
				if _, err := client.EmbedSingle(ctx, "failed"); err == nil {
					t.Fatal("caller rejection was ignored")
				}
			}
			if _, err := c.EmbedSingle(context.Background(), "healthy"); err != nil || hits.Load() != 1 {
				t.Fatalf("caller failures poisoned healthy provider: %v hits=%d", err, hits.Load())
			}
		})
	}
}

func TestT10SingleHalfOpenProbe(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
			<-finish
		}
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
	}))
	defer srv.Close()
	defer close(finish)
	c := NewClient(srv.URL, "m", 1, 1).WithPriorityGate(NewPriorityGate(8))
	c.breaker.RecordFailure()
	c.breaker.RecordFailure()
	c.breaker.RecordFailure()
	c.breaker.mu.Lock()
	c.breaker.openUntil = time.Now().Add(-time.Second)
	c.breaker.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := c.EmbedSingle(context.Background(), "probe"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe never reached provider")
	}
	for range 5 {
		if _, err := c.WithBackground().EmbedSingle(context.Background(), "other"); !errors.Is(err, ErrBreakerOpen) {
			t.Errorf("concurrent half-open request accepted: %v", err)
		}
	}
	finish <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := c.EmbedSingle(context.Background(), "recovered"); err != nil || hits.Load() != 2 {
		t.Fatalf("probe did not recover: err=%v hits=%d", err, hits.Load())
	}
}

func TestT10ProbeAbortAllowsReplacement(t *testing.T) {
	for _, kind := range []string{"guard", "cancel", "cache"} {
		t.Run(kind, func(t *testing.T) {
			var hits atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.Write([]byte(`{"data":[{"index":0,"embedding":[1,0]}]}`))
			}))
			defer server.Close()
			c := NewClient(server.URL, "m", 1, 1)
			for range 3 {
				c.breaker.RecordFailure()
			}
			c.breaker.mu.Lock()
			c.breaker.openUntil = time.Now().Add(-time.Second)
			c.breaker.mu.Unlock()
			client, ctx := c, context.Background()
			denied := errors.New("revoked")
			switch kind {
			case "guard":
				client = c.WithGuard(func(context.Context) (func(), error) { return nil, denied })
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			default:
				c.WithCache(NewCache(8))
				c.cache.Put(c.cacheKey("abort"), []float32{1, 0})
			}
			_, err := client.EmbedSingle(ctx, "abort")
			if kind == "guard" && (!errors.Is(err, ErrGuardRejected) || !errors.Is(err, denied)) {
				t.Fatalf("guard identity lost: %v", err)
			}
			if c.breaker.State() != "half-open" {
				t.Fatal("caller abort changed provider health")
			}
			if _, err := c.EmbedSingle(context.Background(), "replacement"); err != nil || hits.Load() != 1 {
				t.Fatalf("probe leaked: err=%v hits=%d", err, hits.Load())
			}
		})
	}
}

func TestT10ActiveCallerCancellationDoesNotTripBreaker(t *testing.T) {
	entered := make(chan struct{}, 3)
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-finish
	}))
	defer server.Close()
	defer close(finish)
	c := NewClient(server.URL, "m", 1, 1)
	for range 3 {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := c.EmbedSingle(ctx, "cancel"); done <- err }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("provider not entered")
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	}
	if c.breaker.State() != "closed" {
		t.Fatal("active caller cancellation opened breaker")
	}
}

func TestT10MalformedProviderResponseTripsBreaker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[]}`)) }))
	defer server.Close()
	c := NewClient(server.URL, "m", 1, 1)
	for range 3 {
		if _, err := c.EmbedSingle(context.Background(), "bad"); err == nil {
			t.Fatal("malformed response accepted")
		}
	}
	if _, err := c.EmbedSingle(context.Background(), "blocked"); !errors.Is(err, ErrBreakerOpen) {
		t.Fatalf("malformed provider did not open breaker: %v", err)
	}
}

func TestT10ModelCopyPreservesSharedState(t *testing.T) {
	c := NewClient("http://example.invalid", "a", 4, 2).WithCache(NewCache(8)).WithPriorityGate(NewPriorityGate(2)).WithBackground().WithQueryAlias().AsQuery()
	copy := c.WithModel("b")
	if c.Model() != "a" || copy.wireModel() != "b:query" || copy.cacheKey("same") == c.cacheKey("same") || copy.breaker != c.breaker || copy.httpClient != c.httpClient || copy.gate != c.gate || copy.cache != c.cache || !copy.background {
		t.Fatal("model copy changed shared state or aliased encoder cache")
	}
}

func TestT10StaleCompletionDoesNotReleaseProbe(t *testing.T) {
	b := NewBreaker(1, time.Second)
	old, _, err := b.acquire()
	if err != nil {
		t.Fatal(err)
	}
	b.RecordFailure()
	b.mu.Lock()
	b.openUntil = time.Now().Add(-time.Second)
	b.mu.Unlock()
	probe, reserved, err := b.acquire()
	if err != nil || !reserved {
		t.Fatalf("probe not acquired: %v", err)
	}
	b.complete(old, false)
	b.abort(old)
	if !errors.Is(b.Allow(), ErrBreakerOpen) {
		t.Fatal("stale request released active probe")
	}
	b.complete(probe, false)
	if b.Allow() != nil {
		t.Fatal("successful current probe did not close breaker")
	}
}
