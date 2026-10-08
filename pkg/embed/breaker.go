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

// BreakerOpenError reports a rejected request, not a failed provider attempt.
// RetryAt lets durable callers defer work until the existing cooldown expires.
type BreakerOpenError struct {
	RetryAt time.Time
}

func (e *BreakerOpenError) Error() string { return ErrBreakerOpen.Error() }
func (e *BreakerOpenError) Unwrap() error { return ErrBreakerOpen }

// Breaker is a consecutive-failure circuit breaker with a half-open
// probe: after `threshold` consecutive failures it opens for `cooldown`,
// then lets a single probe through; a successful probe closes it again.
type Breaker struct {
	mu         sync.Mutex
	failures   int
	probing    bool
	generation uint64
	openUntil  time.Time
	threshold  int
	cooldown   time.Duration
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
	_, _, err := b.acquire()
	return err
}

func (b *Breaker) acquire() (uint64, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkLocked(); err != nil {
		return 0, false, err
	}
	probe := !b.openUntil.IsZero()
	if probe {
		b.probing = true
	}
	return b.generation, probe, nil
}

// check is a non-reserving check before waiting for admission.
func (b *Breaker) check() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkLocked()
}

func (b *Breaker) checkLocked() error {
	if b.probing || time.Now().Before(b.openUntil) {
		return &BreakerOpenError{RetryAt: b.openUntil}
	}
	return nil
}

// abort releases a probe that made no provider health observation.
func (b *Breaker) abort(generation uint64) {
	b.mu.Lock()
	if generation == b.generation {
		b.probing = false
	}
	b.mu.Unlock()
}

// complete ignores observations from requests admitted before a later opening.
func (b *Breaker) complete(generation uint64, failed bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if generation != b.generation {
		return
	}
	if failed {
		b.recordFailureLocked()
	} else {
		b.recordSuccessLocked()
	}
}

func (b *Breaker) RecordSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recordSuccessLocked()
}

func (b *Breaker) recordSuccessLocked() {
	b.probing = false
	b.failures = 0
	b.openUntil = time.Time{}
}

func (b *Breaker) RecordFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recordFailureLocked()
}

func (b *Breaker) recordFailureLocked() {
	b.probing = false
	b.failures++
	if b.failures >= b.threshold {
		b.generation++
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
