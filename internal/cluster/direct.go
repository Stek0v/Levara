package cluster

import (
	"encoding/json"
	"fmt"

	"github.com/stek0v/levara/internal/store"
)

// DirectNode implements store.ShardHandler by calling Levara directly,
// bypassing Raft consensus. Durability is provided by WAL.
// Optionally broadcasts writes to replicas via Repl.
type DirectNode struct {
	DB   *store.Levara
	Repl *ReplicationServer // nil = no replication
}

func (dn *DirectNode) Insert(id string, vector []float32, data interface{}) error {
	if dn.Repl != nil {
		dn.Repl.mu.Lock()
		defer dn.Repl.mu.Unlock()
	}
	if err := dn.DB.Insert(id, vector, data); err != nil {
		if dn.Repl != nil {
			dn.Repl.invalidateLocked()
		}
		return err
	}
	if dn.Repl != nil && len(dn.Repl.listeners) > 0 {
		dn.Repl.broadcastLocked(WALEntryFromInsert(id, vector, data))
	}
	return nil
}

func (dn *DirectNode) BatchInsert(records []store.BatchItem) []error {
	if dn.Repl == nil {
		return dn.DB.BatchInsert(records)
	}
	dn.Repl.mu.Lock()
	defer dn.Repl.mu.Unlock()
	if len(dn.Repl.listeners) == 0 {
		return dn.DB.BatchInsert(records)
	}
	// ponytail: native batch errors lack positional IDs; active replication uses successful per-record writes.
	var errs []error
	for _, r := range records {
		metadata, err := json.Marshal(r.Data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: marshal: %w", r.ID, err))
			continue
		}
		data := json.RawMessage(metadata)
		if err := dn.DB.Insert(r.ID, r.Vector, data); err != nil {
			errs = append(errs, err)
			dn.Repl.invalidateLocked()
			continue
		}
		if len(dn.Repl.listeners) > 0 {
			dn.Repl.broadcastLocked(WALEntryFromInsert(r.ID, r.Vector, data))
		}
	}
	return errs
}

func (dn *DirectNode) Search(query []float32, topK int) []store.VectroRecord {
	return dn.DB.Search(query, topK)
}

func (dn *DirectNode) Delete(id string) error {
	if dn.Repl != nil {
		dn.Repl.mu.Lock()
		defer dn.Repl.mu.Unlock()
	}
	_, _, existed := dn.DB.Get(id)
	if err := dn.DB.Delete(id); err != nil {
		if dn.Repl != nil && existed {
			dn.Repl.invalidateLocked()
		}
		return err
	}
	if dn.Repl != nil && len(dn.Repl.listeners) > 0 {
		dn.Repl.broadcastLocked(WALEntryFromDelete(id))
	}
	return nil
}

func (dn *DirectNode) BatchDelete(ids []string) []error {
	if dn.Repl == nil {
		return dn.DB.BatchDelete(ids)
	}
	dn.Repl.mu.Lock()
	defer dn.Repl.mu.Unlock()
	if len(dn.Repl.listeners) == 0 {
		return dn.DB.BatchDelete(ids)
	}
	var errs []error
	for _, id := range ids {
		_, _, existed := dn.DB.Get(id)
		if err := dn.DB.Delete(id); err != nil {
			errs = append(errs, err)
			if existed {
				dn.Repl.invalidateLocked()
			}
			continue
		}
		if len(dn.Repl.listeners) > 0 {
			dn.Repl.broadcastLocked(WALEntryFromDelete(id))
		}
	}
	return errs
}
