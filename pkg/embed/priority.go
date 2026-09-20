// priority.go — P4: embedder admission control.
//
// One shared embed client serves latency-sensitive foreground calls
// (search query vectors, memory recall) and throughput-oriented
// background batches (cognify corpus embedding). Against a
// single-worker upstream the background flood queues ahead of
// foreground requests and query embedding times out (observed live:
// 20s timeouts while cognify ran). The gate gives foreground requests
// queue priority and caps in-flight background requests, so a query
// waits behind at most the already-sent batches, never the whole
// backlog.
package embed

import (
	"context"
	"sync"
)

// PriorityGate bounds in-flight embedding requests and lets foreground
// requests jump past waiting background requests. It does not preempt
// requests already sent to the upstream — those finish first by design.
type PriorityGate struct {
	mu        sync.Mutex
	cond      *sync.Cond
	inflight  int
	max       int
	bgWaiting int
	fgWaiting int
	closed    bool
}

// NewPriorityGate allows at most max concurrent embed requests; background
// requests additionally pause whenever a foreground request is waiting.
func NewPriorityGate(max int) *PriorityGate {
	if max < 1 {
		max = 1
	}
	g := &PriorityGate{max: max}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquire blocks until the request may proceed. background requests yield
// to any waiting foreground request first.
func (g *PriorityGate) acquire(ctx context.Context, background bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if background {
		g.bgWaiting++
	} else {
		g.fgWaiting++
	}
	for {
		if g.closed {
			g.doneWaiting(background)
			return context.Canceled
		}
		if ctx.Err() != nil {
			g.doneWaiting(background)
			return ctx.Err()
		}
		// Foreground proceeds when any slot is free. Background proceeds
		// only when no foreground is waiting AND a slot is free.
		if g.inflight < g.max && (!background || g.fgWaiting == 0) {
			g.inflight++
			g.doneWaiting(background)
			return nil
		}
		g.cond.Wait()
	}
}

func (g *PriorityGate) doneWaiting(background bool) {
	if background {
		g.bgWaiting--
	} else {
		g.fgWaiting--
	}
}

// release frees a slot acquired by a successful acquire.
func (g *PriorityGate) release() {
	g.mu.Lock()
	g.inflight--
	g.cond.Broadcast()
	g.mu.Unlock()
}

// waitingForeground reports parked foreground waiters (test/diagnostic hook).
func (g *PriorityGate) waitingForeground() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fgWaiting
}

// Close wakes every waiter (used on shutdown).
func (g *PriorityGate) Close() {
	g.mu.Lock()
	g.closed = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

// gate wraps a client's EmbedTexts with admission control. The parent
// client holds the gate; foreground is the default, WithBackground
// returns a copy whose requests are marked background.
func (c *Client) acquireGate(ctx context.Context) (func(), error) {
	if c.gate == nil {
		return func() {}, nil
	}
	background := c.background
	if err := c.gate.acquire(ctx, background); err != nil {
		return nil, err
	}
	return c.gate.release, nil
}

// WithPriorityGate attaches a shared admission gate. The client keeps
// foreground status.
func (c *Client) WithPriorityGate(g *PriorityGate) *Client {
	cp := *c
	cp.gate = g
	cp.background = false
	return &cp
}

// WithBackground returns a copy whose requests use the gate's background
// lane: capped concurrency plus yielding to waiting foreground requests.
func (c *Client) WithBackground() *Client {
	cp := *c
	cp.background = true
	return &cp
}

// BackgroundConcurrency reports the effective background lane size:
// min(gate capacity, client concurrency). Zero when no gate is attached.
func (c *Client) BackgroundConcurrency() int {
	if c == nil || c.gate == nil {
		return 0
	}
	if c.concurrency < c.gate.max {
		return c.concurrency
	}
	return c.gate.max
}
