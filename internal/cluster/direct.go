package cluster

import "github.com/stek0v/levara/internal/store"

// DirectNode implements store.ShardHandler by calling Levara directly,
// bypassing Raft consensus. Durability is provided by WAL.
// Optionally broadcasts writes to replicas via Repl.
type DirectNode struct {
	DB   *store.Levara
	Repl *ReplicationServer // nil = no replication
}

func (dn *DirectNode) Insert(id string, vector []float32, data interface{}) error {
	if err := dn.DB.Insert(id, vector, data); err != nil {
		return err
	}
	if dn.Repl != nil && dn.Repl.ReplicaCount() > 0 {
		dn.Repl.Broadcast(WALEntryFromInsert(id, vector, data))
	}
	return nil
}

func (dn *DirectNode) BatchInsert(records []store.BatchItem) []error {
	errs := dn.DB.BatchInsert(records)
	if dn.Repl != nil && dn.Repl.ReplicaCount() > 0 {
		for _, r := range records {
			dn.Repl.Broadcast(WALEntryFromInsert(r.ID, r.Vector, r.Data))
		}
	}
	return errs
}

func (dn *DirectNode) Search(query []float32, topK int) []store.VectroRecord {
	return dn.DB.Search(query, topK)
}

func (dn *DirectNode) Delete(id string) error {
	if err := dn.DB.Delete(id); err != nil {
		return err
	}
	if dn.Repl != nil && dn.Repl.ReplicaCount() > 0 {
		dn.Repl.Broadcast(WALEntryFromDelete(id))
	}
	return nil
}

func (dn *DirectNode) BatchDelete(ids []string) []error {
	if dn.Repl == nil {
		return dn.DB.BatchDelete(ids)
	}
	// Keep replica registration outside a batch that won't be broadcast.
	dn.Repl.mu.RLock()
	if len(dn.Repl.listeners) == 0 {
		defer dn.Repl.mu.RUnlock()
		return dn.DB.BatchDelete(ids)
	}
	dn.Repl.mu.RUnlock()

	// ponytail: active replication keeps per-ID deletes until batch results
	// identify successful IDs, so failures never become replica deletes.
	var errs []error
	for _, id := range ids {
		if err := dn.Delete(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
