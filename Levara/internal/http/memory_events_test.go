package http

import (
	"sync"
	"testing"
	"time"
)

func TestMemoryBus_SubscribePublishReceive(t *testing.T) {
	bus := &memoryBus{subs: make(map[uint64]chan MemoryEvent)}
	ch, cancel := bus.Subscribe()
	defer cancel()

	if got := bus.Subscribers(); got != 1 {
		t.Fatalf("subscribers = %d, want 1", got)
	}

	want := MemoryEvent{Kind: "memory.saved", Key: "k", Value: "v", OwnerID: "u1", Timestamp: "t"}
	bus.Publish(want)

	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("did not receive event within 1s")
	}
}

func TestMemoryBus_FanOutToMultipleSubscribers(t *testing.T) {
	bus := &memoryBus{subs: make(map[uint64]chan MemoryEvent)}
	const n = 5
	chans := make([]<-chan MemoryEvent, n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		chans[i], cancels[i] = bus.Subscribe()
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	bus.Publish(MemoryEvent{Kind: "memory.saved", Key: "k"})

	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			select {
			case ev := <-chans[i]:
				if ev.Key != "k" {
					t.Errorf("subscriber %d: key = %q, want k", i, ev.Key)
				}
			case <-time.After(time.Second):
				t.Errorf("subscriber %d did not receive within 1s", i)
			}
		}()
	}
	wg.Wait()
}

func TestMemoryBus_CancelRemovesSubscriber(t *testing.T) {
	bus := &memoryBus{subs: make(map[uint64]chan MemoryEvent)}
	_, cancel := bus.Subscribe()
	if got := bus.Subscribers(); got != 1 {
		t.Fatalf("subscribers = %d, want 1", got)
	}
	cancel()
	if got := bus.Subscribers(); got != 0 {
		t.Fatalf("after cancel subscribers = %d, want 0", got)
	}
	// cancel must be idempotent — second call should not panic.
	cancel()
}

func TestMemoryBus_SlowSubscriberDrops(t *testing.T) {
	bus := &memoryBus{subs: make(map[uint64]chan MemoryEvent)}
	_, cancel := bus.Subscribe()
	defer cancel()

	// Fill the buffer + 5 extra publishes that have nowhere to go.
	for i := 0; i < memoryBusBuffer+5; i++ {
		bus.Publish(MemoryEvent{Kind: "memory.saved"})
	}

	if got := bus.Dropped(); got < 5 {
		t.Fatalf("Dropped() = %d, want >= 5", got)
	}
}

func TestMemoryBus_PublishWithNoSubscribersIsNoop(t *testing.T) {
	bus := &memoryBus{subs: make(map[uint64]chan MemoryEvent)}
	bus.Publish(MemoryEvent{Kind: "memory.saved", Key: "k"})
	if got := bus.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0 (no subs ≠ drop)", got)
	}
}
