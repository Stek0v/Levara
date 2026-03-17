package cluster

import "github.com/rupamthxt/vectradb/internal/store"

// DirectNode implements store.ShardHandler by calling VectraDB directly,
// bypassing Raft consensus. Durability is provided by WAL.
// Use for single-node deployments where replication is not needed.
type DirectNode struct {
	DB *store.VectraDB
}

func (dn *DirectNode) Insert(id string, vector []float32, data interface{}) error {
	return dn.DB.Insert(id, vector, data)
}

func (dn *DirectNode) BatchInsert(records []store.BatchItem) []error {
	return dn.DB.BatchInsert(records)
}

func (dn *DirectNode) Search(query []float32, topK int) []store.VectroRecord {
	return dn.DB.Search(query, topK)
}
