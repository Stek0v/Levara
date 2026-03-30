// hnsw.go — Adapter wrapping internal/store.CollectionManager as VectorStore.
package vectorstore

import (
	"github.com/stek0v/cognevra/internal/store"
)

// HNSWStore wraps CollectionManager to implement VectorStore.
type HNSWStore struct {
	cm *store.CollectionManager
}

// NewHNSWStore creates a VectorStore backed by in-process HNSW indexes.
func NewHNSWStore(cm *store.CollectionManager) *HNSWStore {
	return &HNSWStore{cm: cm}
}

func (h *HNSWStore) Insert(collection, id string, vector []float32, metadata interface{}) error {
	return h.cm.Insert(collection, id, vector, metadata)
}

func (h *HNSWStore) Search(collection string, queryVec []float32, topK int) ([]SearchResult, error) {
	results, err := h.cm.Search(collection, queryVec, topK)
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, r := range results {
		out = append(out, SearchResult{
			ID:       r.ID,
			Score:    r.Score,
			Metadata: r.Metadata,
		})
	}
	return out, nil
}

func (h *HNSWStore) Delete(collection, id string) error {
	return h.cm.Delete(collection, id)
}

func (h *HNSWStore) Has(collection string) bool {
	return h.cm.Has(collection)
}

func (h *HNSWStore) Create(collection string) error {
	return h.cm.Create(collection)
}

func (h *HNSWStore) List() []string {
	return h.cm.List()
}

func (h *HNSWStore) Count(collection string) int {
	meta := h.cm.GetMeta(collection)
	if meta == nil {
		return 0
	}
	return meta.RecordCount
}

func (h *HNSWStore) Close() error {
	return h.cm.Close()
}
