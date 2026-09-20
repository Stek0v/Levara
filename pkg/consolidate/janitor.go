package consolidate

import (
	"context"
	"sync"
	"time"
)

// Runner performs one consolidation sweep (typically over all collections).
type Runner interface {
	RunOnce(ctx context.Context) error
}

// StartJanitor ticks the Runner every interval until the returned stop() is called.
// Mirrors pkg/runreg.StartJanitor's stop-func contract.
func StartJanitor(ctx context.Context, r Runner, interval time.Duration) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				_ = r.RunOnce(ctx)
			}
		}
	}()
	// Idempotent stop: a deferred call plus an explicit shutdown call must
	// not race into a double close (same failure mode as runreg.StartJanitor).
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}
