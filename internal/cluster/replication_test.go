package cluster

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// T-6 tests for the replication fan-out layer.
//
// The most important invariant here is sequence monotonicity: each WAL entry
// that Broadcast emits must carry a unique, strictly-increasing Seq, otherwise
// replicas can't detect gaps or order entries correctly. Concurrent Broadcast
// calls must not race on seq — this test locks that in.

func TestReplicationServer_Role(t *testing.T) {
	rs := NewReplicationServer("n1", nil, nil)
	if rs.Role() != "primary" {
		t.Errorf("default Role = %q, want primary", rs.Role())
	}
	rs.SetRole("replica")
	if rs.Role() != "replica" {
		t.Errorf("after SetRole, Role = %q, want replica", rs.Role())
	}
}

func TestReplicationServer_PrimaryAddr(t *testing.T) {
	rs := NewReplicationServer("n1", nil, nil)
	if rs.PrimaryAddr() != "" {
		t.Errorf("default PrimaryAddr = %q, want empty", rs.PrimaryAddr())
	}
	rs.SetPrimaryAddr("10.0.0.1:8080")
	if rs.PrimaryAddr() != "10.0.0.1:8080" {
		t.Errorf("PrimaryAddr = %q", rs.PrimaryAddr())
	}
}

func TestReplicationServer_AddRemoveReplica(t *testing.T) {
	rs := NewReplicationServer("n1", nil, nil)
	if got := rs.ReplicaCount(); got != 0 {
		t.Errorf("initial ReplicaCount = %d, want 0", got)
	}

	ch := rs.AddReplica("r1")
	if ch == nil {
		t.Fatal("AddReplica returned nil channel")
	}
	if got := rs.ReplicaCount(); got != 1 {
		t.Errorf("after AddReplica, count = %d, want 1", got)
	}

	rs.AddReplica("r2")
	if got := rs.ReplicaCount(); got != 2 {
		t.Errorf("after 2nd AddReplica, count = %d, want 2", got)
	}

	rs.RemoveReplica("r1")
	if got := rs.ReplicaCount(); got != 1 {
		t.Errorf("after RemoveReplica, count = %d, want 1", got)
	}

	// The channel returned for r1 should be closed after RemoveReplica.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel for removed replica returned a value, not closed signal")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("channel for removed replica not closed within 100ms")
	}

	rs.RemoveReplica("r1") // idempotent: no panic on already-removed
}

func TestReplicationServer_Broadcast_EmptyListeners(t *testing.T) {
	// Broadcast with zero replicas must not panic and must not block.
	rs := NewReplicationServer("n1", nil, nil)
	done := make(chan struct{})
	go func() {
		rs.Broadcast(WALEntry{Op: 1, ID: "x"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Broadcast on empty listeners blocked")
	}
}

func TestReplicationServer_Broadcast_AssignsMonotonicSeq(t *testing.T) {
	rs := NewReplicationServer("n1", nil, nil)
	ch := rs.AddReplica("r1")

	for i := 0; i < 5; i++ {
		rs.Broadcast(WALEntry{Op: 1, ID: "x"})
	}

	for i := uint64(1); i <= 5; i++ {
		select {
		case e := <-ch:
			if e.Seq != i {
				t.Errorf("entry %d: Seq = %d, want %d", i, e.Seq, i)
			}
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("channel empty at iteration %d", i)
		}
	}
}

// TestReplicationServer_Broadcast_ConcurrentNoLostSeq is the regression test
// for the Broadcast race: before the fix, two goroutines calling Broadcast
// both held mu.RLock and incremented rs.seq unprotected, producing duplicate
// Seq values (and dropping the "monotonic unique" invariant that replicas
// rely on for gap detection).
//
// Under -race this surfaces as a DATA RACE on rs.seq; without -race it
// surfaces as duplicate Seq values in the emitted stream.
func TestReplicationServer_Broadcast_ConcurrentNoLostSeq(t *testing.T) {
	rs := NewReplicationServer("n1", nil, nil)
	ch := rs.AddReplica("r1")

	const (
		goroutines  = 8
		perRoutine  = 25
		totalEmits  = goroutines * perRoutine
	)

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				rs.Broadcast(WALEntry{Op: 1, ID: "x"})
			}
		}()
	}

	// Drain the channel in parallel so the broadcaster doesn't block on the
	// 10K buffer (unlikely but possible if the test scales up).
	seen := make(map[uint64]int, totalEmits)
	var drainDone atomic.Bool
	var drainWg sync.WaitGroup
	drainWg.Add(1)
	go func() {
		defer drainWg.Done()
		for !drainDone.Load() || len(ch) > 0 {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				seen[e.Seq]++
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()

	wg.Wait()
	drainDone.Store(true)
	drainWg.Wait()

	if len(seen) != totalEmits {
		t.Errorf("unique Seq count = %d, want %d (duplicates/lost entries indicate race)",
			len(seen), totalEmits)
	}
	for seq, cnt := range seen {
		if cnt != 1 {
			t.Errorf("Seq %d appeared %d times (must be unique)", seq, cnt)
		}
	}
}

func TestReplicationServer_Broadcast_DropsWhenReplicaSlow(t *testing.T) {
	// When a replica's channel is full, Broadcast drops the entry instead of
	// blocking. We simulate this with a tiny-capacity channel substituted via
	// AddReplica's return value — but AddReplica returns a 10K-buffer channel
	// by design, so we instead add the replica and don't drain: with
	// Seq fan-out this never fills in practice, so this test is a smoke that
	// the method doesn't deadlock when replicas are attached but silent.
	rs := NewReplicationServer("n1", nil, nil)
	_ = rs.AddReplica("slow")
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			rs.Broadcast(WALEntry{Op: 1, ID: "x"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Broadcast blocked with slow replica attached (deadlock)")
	}
}
