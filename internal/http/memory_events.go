// memory_events.go — in-process pub/sub for memory mutations, consumed by
// the SSE endpoint at GET /memories/stream so agents can subscribe instead
// of polling /memories.
package http

import (
	"sync"
	"sync/atomic"
)

// MemoryEvent is a single mutation observed on /memories. Kind is one of
// "memory.saved" or "memory.deleted". Fields not relevant to the kind
// (Value on delete, Type on delete) are left empty.
type MemoryEvent struct {
	Kind      string `json:"kind"`
	Key       string `json:"key"`
	Value     string `json:"value,omitempty"`
	Type      string `json:"type,omitempty"`
	OwnerID   string `json:"owner_id,omitempty"`
	Timestamp string `json:"timestamp"`
}

// memoryBus is the package-level broadcaster. Subscribers are channels
// fanned out from Publish under a read lock. Bounded per-subscriber buffer
// — slow consumers drop events rather than blocking the writer.
type memoryBus struct {
	mu      sync.RWMutex
	subs    map[uint64]chan MemoryEvent
	nextID  uint64
	dropped uint64
}

const memoryBusBuffer = 64

var memoryEvents = &memoryBus{subs: make(map[uint64]chan MemoryEvent)}

// Subscribe registers a new channel. The returned cancel function MUST be
// called to remove the subscriber and close its channel.
func (b *memoryBus) Subscribe() (<-chan MemoryEvent, func()) {
	ch := make(chan MemoryEvent, memoryBusBuffer)
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
		b.mu.Unlock()
	}
}

// Publish fans out the event to all current subscribers with a non-blocking
// send. If a subscriber's buffer is full the event is dropped for that
// subscriber and the global drop counter is incremented.
func (b *memoryBus) Publish(ev MemoryEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			atomic.AddUint64(&b.dropped, 1)
		}
	}
}

// Dropped returns the cumulative count of events dropped because a
// subscriber's buffer was full. Exposed for the metrics middleware.
func (b *memoryBus) Dropped() uint64 {
	return atomic.LoadUint64(&b.dropped)
}

// Subscribers returns the current subscriber count. Used by tests and
// debug endpoints.
func (b *memoryBus) Subscribers() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
