package consolidate

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingRunner struct {
	mu sync.Mutex
	n  int
}

func (c *countingRunner) RunOnce(_ context.Context) error {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return nil
}

func TestJanitor_TicksThenStops(t *testing.T) {
	r := &countingRunner{}
	stop := StartJanitor(context.Background(), r, 10*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	stop()
	r.mu.Lock()
	n := r.n
	r.mu.Unlock()
	if n < 2 {
		t.Fatalf("janitor ticked %d times in 35ms@10ms, want >= 2", n)
	}
}
