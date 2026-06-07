package llm

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// RateLimiter wraps a Provider with request rate limiting.
// Uses token bucket algorithm: tokens channel has capacity = maxRequests,
// a background goroutine refills tokens at rate interval/maxRequests.
type RateLimiter struct {
	provider    Provider
	tokens      chan struct{}
	interval    time.Duration
	maxRequests int
	available   atomic.Int32 // exported for health endpoint
	stopRefill  chan struct{}
	closeOnce   sync.Once
}

// NewRateLimiter wraps provider with rate limiting.
// maxRequests per interval (e.g. 60 requests per 60s).
func NewRateLimiter(provider Provider, maxRequests int, interval time.Duration) *RateLimiter {
	rl := &RateLimiter{
		provider:    provider,
		tokens:      make(chan struct{}, maxRequests),
		interval:    interval,
		maxRequests: maxRequests,
		stopRefill:  make(chan struct{}),
	}

	// Pre-fill bucket
	for range maxRequests {
		rl.tokens <- struct{}{}
	}
	rl.available.Store(int32(maxRequests))

	// Background refill goroutine
	refillInterval := interval / time.Duration(maxRequests)
	go func() {
		ticker := time.NewTicker(refillInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				select {
				case rl.tokens <- struct{}{}:
					rl.available.Add(1)
				default:
					// bucket full, discard
				}
			case <-rl.stopRefill:
				return
			}
		}
	}()

	return rl
}

// ChatCompletion implements Provider with rate limiting.
// Acquires a token before delegating to the wrapped provider.
// Returns error if ctx is cancelled while waiting for a token.
func (rl *RateLimiter) ChatCompletion(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	// Acquire token (block if empty, respect ctx cancellation)
	select {
	case <-rl.tokens:
		rl.available.Add(-1)
	case <-ctx.Done():
		return nil, fmt.Errorf("rate limiter: %w", ctx.Err())
	}

	return rl.provider.ChatCompletion(ctx, req)
}

// Name returns "ratelimited:" + provider.Name().
func (rl *RateLimiter) Name() string {
	return "ratelimited:" + rl.provider.Name()
}

// AvailableTokens returns current number of available tokens.
func (rl *RateLimiter) AvailableTokens() int {
	return int(rl.available.Load())
}

// MaxRequests returns the configured max requests per interval.
func (rl *RateLimiter) MaxRequests() int {
	return rl.maxRequests
}

// Interval returns the configured rate limit interval.
func (rl *RateLimiter) Interval() time.Duration {
	return rl.interval
}

// Close stops the background refill goroutine. Safe to call multiple times.
func (rl *RateLimiter) Close() {
	rl.closeOnce.Do(func() { close(rl.stopRefill) })
}
