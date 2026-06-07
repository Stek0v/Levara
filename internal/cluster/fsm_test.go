package cluster

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/raft"
	"github.com/stek0v/levara/internal/store"
)

// T-6 regression tests for the Raft FSM layer. The Apply / Snapshot / Restore
// contract is what lets Levara survive a leader change: if any path here
// regresses, cluster membership silently diverges — hard to detect live, easy
// to catch in unit tests.

func newFSMWithDB(t testing.TB, dim int) (*FSM, *store.Levara, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-fsm-*")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.NewLevara(dim, dir+"/meta.bin")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	return NewFSM(db), db, func() {
		_ = db.Close()
		os.RemoveAll(dir)
	}
}

// mkApply wraps a Command in a raft.Log so Apply can decode it.
func mkApply(t testing.TB, f *FSM, cmd Command) any {
	t.Helper()
	data, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return f.Apply(&raft.Log{Data: data})
}

// ──────────────────────────────────────────────────────────────────
// Command JSON round-trip — any schema drift breaks replication silently.
// ──────────────────────────────────────────────────────────────────

func TestCommand_JSONRoundtrip_Insert(t *testing.T) {
	src := Command{
		Op:     "insert",
		Id:     "rec-1",
		Vector: []float32{0.1, 0.2, 0.3},
		Data:   json.RawMessage(`{"title":"hello"}`),
	}
	raw, _ := json.Marshal(src)
	var got Command
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Op != src.Op || got.Id != src.Id || len(got.Vector) != 3 {
		t.Errorf("roundtrip lost fields: %+v", got)
	}
	if string(got.Data) != `{"title":"hello"}` {
		t.Errorf("Data = %s", got.Data)
	}
}

func TestCommand_JSONRoundtrip_BatchInsert(t *testing.T) {
	src := Command{
		Op: "batch_insert",
		Records: []BatchRecord{
			{Id: "a", Vector: []float32{1}, Data: json.RawMessage(`{"n":1}`)},
			{Id: "b", Vector: []float32{2}, Data: json.RawMessage(`{"n":2}`)},
		},
	}
	raw, _ := json.Marshal(src)
	var got Command
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 || got.Records[0].Id != "a" || got.Records[1].Id != "b" {
		t.Errorf("records lost: %+v", got.Records)
	}
}

func TestCommand_JSONRoundtrip_BatchDelete(t *testing.T) {
	src := Command{Op: "batch_delete", IDs: []string{"a", "b", "c"}}
	raw, _ := json.Marshal(src)
	var got Command
	_ = json.Unmarshal(raw, &got)
	if len(got.IDs) != 3 {
		t.Errorf("IDs = %v", got.IDs)
	}
}

// ──────────────────────────────────────────────────────────────────
// FSM.Apply — happy paths
// ──────────────────────────────────────────────────────────────────

func TestFSM_Apply_Insert(t *testing.T) {
	f, db, cleanup := newFSMWithDB(t, 4)
	defer cleanup()

	result := mkApply(t, f, Command{
		Op:     "insert",
		Id:     "x",
		Vector: []float32{1, 2, 3, 4},
		Data:   json.RawMessage(`{"k":"v"}`),
	})
	if result != nil {
		t.Errorf("Apply insert returned %v, want nil", result)
	}
	if db.Count() != 1 {
		t.Errorf("Count = %d, want 1", db.Count())
	}
}

func TestFSM_Apply_BatchInsert(t *testing.T) {
	f, db, cleanup := newFSMWithDB(t, 2)
	defer cleanup()

	result := mkApply(t, f, Command{
		Op: "batch_insert",
		Records: []BatchRecord{
			{Id: "r1", Vector: []float32{1, 0}, Data: json.RawMessage(`null`)},
			{Id: "r2", Vector: []float32{0, 1}, Data: json.RawMessage(`null`)},
			{Id: "r3", Vector: []float32{1, 1}, Data: json.RawMessage(`null`)},
		},
	})
	if result != nil {
		t.Errorf("batch_insert returned %v, want nil", result)
	}
	if db.Count() != 3 {
		t.Errorf("Count = %d, want 3", db.Count())
	}
}

func TestFSM_Apply_Delete(t *testing.T) {
	f, db, cleanup := newFSMWithDB(t, 2)
	defer cleanup()

	mkApply(t, f, Command{Op: "insert", Id: "doomed", Vector: []float32{1, 2}})
	if db.Count() != 1 {
		t.Fatal("pre-delete Count != 1")
	}

	result := mkApply(t, f, Command{Op: "delete", Id: "doomed"})
	if result != nil {
		t.Errorf("delete returned %v, want nil", result)
	}
	if db.Count() != 0 {
		t.Errorf("Count = %d, want 0", db.Count())
	}
}

func TestFSM_Apply_BatchDelete(t *testing.T) {
	f, db, cleanup := newFSMWithDB(t, 2)
	defer cleanup()
	for _, id := range []string{"a", "b", "c"} {
		mkApply(t, f, Command{Op: "insert", Id: id, Vector: []float32{1, 0}})
	}
	result := mkApply(t, f, Command{Op: "batch_delete", IDs: []string{"a", "c"}})
	if result != nil {
		t.Errorf("batch_delete returned %v", result)
	}
	if db.Count() != 1 {
		t.Errorf("Count = %d, want 1 (b survives)", db.Count())
	}
}

// ──────────────────────────────────────────────────────────────────
// FSM.Apply — error paths
// ──────────────────────────────────────────────────────────────────

func TestFSM_Apply_UnknownOp(t *testing.T) {
	f, _, cleanup := newFSMWithDB(t, 2)
	defer cleanup()
	result := mkApply(t, f, Command{Op: "frobnicate"})
	err, ok := result.(error)
	if !ok {
		t.Fatalf("want error, got %T: %v", result, result)
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("err = %v, want 'unknown command'", err)
	}
}

func TestFSM_Apply_MalformedJSON(t *testing.T) {
	f, _, cleanup := newFSMWithDB(t, 2)
	defer cleanup()
	result := f.Apply(&raft.Log{Data: []byte(`{"op":`)}) // truncated
	err, ok := result.(error)
	if !ok {
		t.Fatalf("want error, got %T", result)
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("err = %v, want 'unmarshal'", err)
	}
}

// ──────────────────────────────────────────────────────────────────
// Snapshot + Restore round-trip — leader change resilience
// ──────────────────────────────────────────────────────────────────

// inMemorySink implements raft.SnapshotSink for tests.
type inMemorySink struct {
	buf      bytes.Buffer
	canceled bool
	closed   bool
}

func (s *inMemorySink) Write(p []byte) (int, error) { return s.buf.Write(p) }
func (s *inMemorySink) Close() error                { s.closed = true; return nil }
func (s *inMemorySink) ID() string                  { return "test-snapshot" }
func (s *inMemorySink) Cancel() error               { s.canceled = true; return nil }

func TestFSM_SnapshotRestore_Roundtrip(t *testing.T) {
	f1, db1, cleanup1 := newFSMWithDB(t, 3)
	defer cleanup1()

	for i, id := range []string{"a", "b", "c"} {
		mkApply(t, f1, Command{
			Op: "insert", Id: id,
			Vector: []float32{float32(i), float32(i + 1), float32(i + 2)},
			Data:   json.RawMessage(`"` + id + `"`),
		})
	}
	if db1.Count() != 3 {
		t.Fatalf("pre-snapshot count = %d", db1.Count())
	}

	// Capture snapshot
	snap, err := f1.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	sink := &inMemorySink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !sink.closed || sink.canceled {
		t.Errorf("sink state closed=%v canceled=%v, want closed=true canceled=false",
			sink.closed, sink.canceled)
	}
	snap.Release()

	// Restore into a fresh FSM — mimics a replica catching up from leader.
	f2, db2, cleanup2 := newFSMWithDB(t, 3)
	defer cleanup2()

	// Pre-insert into the second DB to verify Restore wipes existing state.
	mkApply(t, f2, Command{Op: "insert", Id: "pre-existing", Vector: []float32{9, 9, 9}})
	if db2.Count() != 1 {
		t.Fatal("pre-restore sanity failed")
	}

	if err := f2.Restore(io.NopCloser(bytes.NewReader(sink.buf.Bytes()))); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if db2.Count() != 3 {
		t.Errorf("restored Count = %d, want 3 (pre-existing should be wiped)", db2.Count())
	}
	for _, id := range []string{"a", "b", "c"} {
		if _, _, ok := db2.Get(id); !ok {
			t.Errorf("id %q missing after Restore", id)
		}
	}
	if _, _, ok := db2.Get("pre-existing"); ok {
		t.Error("pre-existing record survived Restore — Clear not called")
	}
}

func TestFSM_Restore_CorruptData(t *testing.T) {
	f, _, cleanup := newFSMWithDB(t, 2)
	defer cleanup()
	err := f.Restore(io.NopCloser(bytes.NewReader([]byte(`{not valid json`))))
	if err == nil {
		t.Fatal("Restore on corrupt data should error")
	}
	if !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("err = %v, want 'unmarshal'", err)
	}
}
