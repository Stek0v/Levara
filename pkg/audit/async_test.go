package audit

import (
	"sync"
	"testing"
)

type collectingSink struct {
	mu      sync.Mutex
	entries []Entry
}

func (s *collectingSink) Log(entry Entry) {
	s.mu.Lock()
	s.entries = append(s.entries, entry)
	s.mu.Unlock()
}

func TestAsyncSinkCloseDrainsQueue(t *testing.T) {
	sink := &collectingSink{}
	async := NewAsyncSink(sink, 8)
	for i := 0; i < 1000; i++ {
		async.Log(Entry{RequestID: string(rune(i + 1)), Tool: "heartbeat"})
	}
	if err := async.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(sink.entries); got != 1000 {
		t.Fatalf("drained %d entries, want 1000", got)
	}
	async.Log(Entry{Tool: "ignored-after-close"})
}
