package cluster

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/hashicorp/raft"
	"github.com/stek0v/cognevra/internal/store"
)

// Command is what we replicate across the network
type Command struct {
	Op     string          `json:"op"` // "insert" | "batch_insert" | "delete" | "batch_delete"
	Id     string          `json:"id"`
	Vector []float32       `json:"vector"`
	Data   json.RawMessage `json:"data"`

	// Batch payload — used when Op == "batch_insert"
	Records []BatchRecord `json:"records,omitempty"`

	// Delete payload — used when Op == "batch_delete"
	IDs []string `json:"ids,omitempty"`
}

// BatchRecord is one entry inside a batch_insert Command.
type BatchRecord struct {
	Id     string          `json:"id"`
	Vector []float32       `json:"vector"`
	Data   json.RawMessage `json:"data"`
}

type FSM struct {
	db *store.Levara
}

func NewFSM(db *store.Levara) *FSM {
	return &FSM{db: db}
}

func (f *FSM) Apply(log *raft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(log.Data, &cmd); err != nil {
		return fmt.Errorf("failed to unmarshal command: %w", err)
	}

	switch cmd.Op {
	case "insert":
		return f.db.Insert(cmd.Id, cmd.Vector, cmd.Data)
	case "batch_insert":
		items := make([]store.BatchItem, len(cmd.Records))
		for i, rec := range cmd.Records {
			items[i] = store.BatchItem{ID: rec.Id, Vector: rec.Vector, Data: rec.Data}
		}
		if errs := f.db.BatchInsert(items); len(errs) > 0 {
			return errs
		}
		return nil
	case "delete":
		return f.db.Delete(cmd.Id)
	case "batch_delete":
		if errs := f.db.BatchDelete(cmd.IDs); len(errs) > 0 {
			return errs
		}
		return nil
	default:
		return fmt.Errorf("unknown command: %s", cmd.Op)
	}
}

func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	// Serialize current DB state: all records (id, vector, metadata)
	records := f.db.AllRecords()
	data, err := json.Marshal(records)
	if err != nil {
		return nil, fmt.Errorf("snapshot marshal: %w", err)
	}
	return &LevaraSnapshot{data: data}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("snapshot read: %w", err)
	}
	var records []store.SnapshotRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return fmt.Errorf("snapshot unmarshal: %w", err)
	}
	// Clear existing data and replay from snapshot
	f.db.Clear()
	for _, r := range records {
		if err := f.db.Insert(r.ID, r.Vector, r.Data); err != nil {
			return fmt.Errorf("snapshot restore %s: %w", r.ID, err)
		}
	}
	return nil
}

// LevaraSnapshot implements raft.FSMSnapshot with actual data.
type LevaraSnapshot struct {
	data []byte
}

func (s *LevaraSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.data); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *LevaraSnapshot) Release() {}
