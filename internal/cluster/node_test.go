package cluster

import (
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stek0v/levara/internal/store"
)

type batchDeleteFSM struct {
	raft.FSM
	calls atomic.Int32
	err   error
}

func (f *batchDeleteFSM) Apply(log *raft.Log) interface{} {
	f.calls.Add(1)
	if f.err != nil {
		return f.err
	}
	return f.FSM.Apply(log)
}

func TestRaftNodeBatchDelete(t *testing.T) {
	for _, mode := range []string{"follower", "leader", "fsm-error"} {
		t.Run(mode, func(t *testing.T) {
			fsm, db, cleanup := newFSMWithDB(t, 2)
			defer cleanup()
			for _, id := range []string{"a", "b", "keep"} {
				if err := db.Insert(id, []float32{1, 0}, nil); err != nil {
					t.Fatal(err)
				}
			}
			wrapped := &batchDeleteFSM{FSM: fsm}
			if mode == "fsm-error" {
				wrapped.err = errors.New("apply rejected")
			}
			config := raft.DefaultConfig()
			config.LocalID = "test"
			config.LogOutput = io.Discard
			config.HeartbeatTimeout = 20 * time.Millisecond
			config.ElectionTimeout = 20 * time.Millisecond
			config.LeaderLeaseTimeout = 20 * time.Millisecond
			addr, transport := raft.NewInmemTransport("")
			defer transport.Close()
			logs := raft.NewInmemStore()
			r, err := raft.NewRaft(config, wrapped, logs, logs, raft.NewInmemSnapshotStore(), transport)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { r.Shutdown().Error() }()
			node := &RaftNode{Raft: r, FSM: fsm, DB: db}
			if mode != "follower" {
				if err := r.BootstrapCluster(raft.Configuration{Servers: []raft.Server{{ID: config.LocalID, Address: addr}}}).Error(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for r.State() != raft.Leader && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if r.State() != raft.Leader {
					t.Fatal("leader election timed out")
				}
			}
			c := store.NewCluster([]store.ShardHandler{node})
			ids := []string{"a", "missing", "a", "b"}
			errs := c.BatchDelete(ids)
			wantErrors := len(ids)
			if mode == "leader" {
				wantErrors = 2
				if db.Count() != 1 {
					t.Fatalf("count = %d, want only survivor", db.Count())
				}
			} else if db.Count() != 3 {
				t.Fatal("failed batch changed records")
			}
			if len(errs) != wantErrors {
				t.Fatalf("errors = %v, want %d failures", errs, wantErrors)
			}
			wantCalls := int32(1)
			if mode == "follower" {
				wantCalls = 0
			}
			if got := wrapped.calls.Load(); got != wantCalls {
				t.Fatalf("FSM calls = %d, want %d", got, wantCalls)
			}
			if errs := node.BatchDelete(nil); len(errs) != 0 || wrapped.calls.Load() != wantCalls {
				t.Fatalf("empty batch issued command or failed: %v", errs)
			}
		})
	}
}
