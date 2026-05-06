package cluster

import "github.com/stek0v/cognevra/internal/store"

// DirectNode implements store.ShardHandler by calling Cognevra directly,
// bypassing Raft consensus. Durability is provided by WAL.
// Use for single-node deployments where replication is not needed.
type DirectNode struct {
	DB *store.Cognevra
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

func (dn *DirectNode) Delete(id string) error {
	return dn.DB.Delete(id)
}

func (dn *DirectNode) BatchDelete(ids []string) []error {
	return dn.DB.BatchDelete(ids)
}
