package cluster

import (
	"os"
	"testing"

	"github.com/stek0v/cognevra/internal/store"
)

// T-6: DirectNode is the single-node path (no Raft); it must forward all
// operations to the embedded store.Levara AND trigger replication when a
// ReplicationServer is attached and has active replicas.

func newDirectNode(t testing.TB, dim int, withRepl bool) (*DirectNode, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-direct-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	dn := &DirectNode{DB: db}
	if withRepl {
		dn.Repl = NewReplicationServer("primary", nil, db)
	}
	return dn, func() {
		_ = db.Close()
		os.RemoveAll(dir)
	}
}

func TestDirectNode_InsertSearchDelete(t *testing.T) {
	dn, cleanup := newDirectNode(t, 2, false)
	defer cleanup()

	if err := dn.Insert("x", []float32{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := dn.Insert("y", []float32{0, 1}, nil); err != nil {
		t.Fatal(err)
	}

	// Search returns something (may be empty if HNSW indexer is still running)
	results := dn.Search([]float32{1, 0}, 2)
	if results == nil {
		t.Error("Search returned nil slice")
	}

	if err := dn.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if dn.DB.Count() != 1 {
		t.Errorf("Count after Delete = %d, want 1", dn.DB.Count())
	}
}

func TestDirectNode_BatchInsertDelete(t *testing.T) {
	dn, cleanup := newDirectNode(t, 2, false)
	defer cleanup()

	errs := dn.BatchInsert([]store.BatchItem{
		{ID: "a", Vector: []float32{1, 0}},
		{ID: "b", Vector: []float32{0, 1}},
		{ID: "c", Vector: []float32{1, 1}},
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("BatchInsert[%d]: %v", i, err)
		}
	}
	if dn.DB.Count() != 3 {
		t.Errorf("post-BatchInsert Count = %d, want 3", dn.DB.Count())
	}

	errs = dn.BatchDelete([]string{"a", "c"})
	for i, err := range errs {
		if err != nil {
			t.Errorf("BatchDelete[%d]: %v", i, err)
		}
	}
	if dn.DB.Count() != 1 {
		t.Errorf("post-BatchDelete Count = %d, want 1", dn.DB.Count())
	}
}

// TestDirectNode_BroadcastOnlyWhenReplicasAttached verifies that DirectNode
// does NOT call Broadcast when ReplicaCount is zero — the conditional in
// Insert/Delete/Batch* paths guards against unnecessary work (and, more
// importantly, Broadcast holding rs.mu RLock on a hot write path).
func TestDirectNode_BroadcastOnlyWhenReplicasAttached(t *testing.T) {
	dn, cleanup := newDirectNode(t, 2, true) // withRepl = true
	defer cleanup()

	// Zero replicas attached yet. Broadcast must NOT run, which means seq
	// must stay at 0.
	if err := dn.Insert("a", []float32{1, 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := dn.Delete("a"); err != nil {
		t.Fatal(err)
	}
	if seq := dn.Repl.seq.Load(); seq != 0 {
		t.Errorf("seq = %d, want 0 (no replicas => no broadcast)", seq)
	}

	// Attach a replica and repeat: now Broadcast should fire.
	ch := dn.Repl.AddReplica("r1")
	if err := dn.Insert("b", []float32{1, 1}, nil); err != nil {
		t.Fatal(err)
	}
	if seq := dn.Repl.seq.Load(); seq != 1 {
		t.Errorf("after 1 Insert with replica attached, seq = %d, want 1", seq)
	}

	// Drain the replica channel so the test doesn't leave a buffered entry.
	select {
	case e := <-ch:
		if e.ID != "b" {
			t.Errorf("broadcast entry ID = %q, want b", e.ID)
		}
	default:
		t.Error("expected one entry in replica channel")
	}
}
