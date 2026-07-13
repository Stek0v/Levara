package audit

import "sync"

// AsyncSink removes serialization and filesystem latency from the MCP request
// path. Graceful Close drains every queued event. If the bounded queue is full,
// Log falls back to the underlying sink synchronously rather than losing the
// durable event.
type AsyncSink struct {
	sink   Sink
	q      chan Entry
	done   chan struct{}
	mu     sync.RWMutex
	closed bool
}

func NewAsyncSink(sink Sink, queueSize int) *AsyncSink {
	if queueSize <= 0 {
		queueSize = 8192
	}
	a := &AsyncSink{sink: sink, q: make(chan Entry, queueSize), done: make(chan struct{})}
	go func() {
		defer close(a.done)
		for entry := range a.q {
			a.sink.Log(entry)
		}
	}()
	return a
}

func (a *AsyncSink) Log(entry Entry) {
	if a == nil || a.sink == nil {
		return
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.closed {
		return
	}
	select {
	case a.q <- entry:
	default:
		// Durability has priority over latency once the safety buffer fills.
		a.sink.Log(entry)
	}
}

func (a *AsyncSink) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.q)
	}
	a.mu.Unlock()
	<-a.done
	if closer, ok := a.sink.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}
