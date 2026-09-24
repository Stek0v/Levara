package embed

import (
	"errors"
	"sync"
	"time"
)

// ErrBreakerOpen is returned while the embed endpoint is believed down:
// callers fail fast instead of waiting out the per-request timeout on
// every call (observed 2026-09-24: a wedged embed service made every
// recall wait the full 30s for hours; the MCP layer surfaced it as
// opaque TaskGroup timeouts).
var ErrBreakerOpen = errors.New("embedding service circuit breaker open — failing fast, will retry shortly")

// Breaker is a consecutive-failure circuit breaker with a half-open
// probe: after `threshold` consecutive failures it opens for `cooldown`,
// then lets a single probe through; a successful probe closes it again.
type Breaker struct {
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	threshold int
	cooldown  time.Duration
}

func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	if threshold < 1 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &Breaker{threshold: threshold, cooldown: cooldown}
}

// Allow reports whether a request may proceed. A nil error means closed
// or half-open (probe allowed).
func (b *Breaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().After(b.openUntil) {
		return nil
	}
	return ErrBreakerOpen
}

func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.openUntil = time.Time{}
}

func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures >= b.threshold {
		b.openUntil = time.Now().Add(b.cooldown)
	}
}

// State returns "closed", "open", or "half-open" (for status output).
func (b *Breaker) State() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return "closed"
	}
	if time.Now().After(b.openUntil) {
		return "half-open"
	}
	return "open"
}
