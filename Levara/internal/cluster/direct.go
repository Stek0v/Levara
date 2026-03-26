package cluster

import "github.com/stek0v/cognevra/internal/store"

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
	errs := dn.DB.BatchDelete(ids)
	if dn.Repl != nil && dn.Repl.ReplicaCount() > 0 {
		for _, id := range ids {
			dn.Repl.Broadcast(WALEntryFromDelete(id))
		}
	}
	return errs
}
