// hnsw.go — Adapter wrapping internal/store.CollectionManager as VectorStore.
package vectorstore

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"

	"github.com/stek0v/levara/internal/store"
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

func (h *HNSWStore) BatchUpsert(collection string, records []UpsertRecord) []error {
	items := make([]store.BatchItem, 0, len(records))
	for _, r := range records {
		items = append(items, store.BatchItem{
			ID:     r.ID,
			Vector: r.Vector,
			Data:   r.Metadata,
		})
	}
	return h.cm.BatchInsert(collection, items)
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
			Metadata: []byte(r.Data),
		})
	}
	return out, nil
}

func (h *HNSWStore) Get(collection, id string) (StoredRecord, bool, error) {
	db, err := h.cm.Get(collection)
	if err != nil {
		return StoredRecord{}, false, err
	}
	vec, meta, ok := db.Get(id)
	if !ok {
		return StoredRecord{}, false, nil
	}
	return StoredRecord{ID: id, Vector: vec, Metadata: meta}, true, nil
}

func (h *HNSWStore) Scan(collection string) ([]StoredRecord, error) {
	ids, vecs, metas, err := h.cm.AllRecords(collection)
	if err != nil {
		return nil, err
	}
	out := make([]StoredRecord, 0, len(ids))
	for i, id := range ids {
		out = append(out, StoredRecord{
			ID:       id,
			Vector:   vecs[i],
			Metadata: metas[i],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (h *HNSWStore) Delete(collection, id string) error {
	return h.cm.Delete(collection, id)
}

func (h *HNSWStore) DeleteMany(collection string, ids []string) []error {
	return h.cm.BatchDelete(collection, ids)
}

func (h *HNSWStore) DeleteByFilter(collection string, filter MetadataFilter) ([]string, []error) {
	if len(filter) == 0 {
		return nil, []error{ErrEmptyFilter}
	}
	records, err := h.Scan(collection)
	if err != nil {
		return nil, []error{err}
	}
	ids := make([]string, 0)
	for _, r := range records {
		if metadataMatches(r.Metadata, filter) {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	return ids, h.DeleteMany(collection, ids)
}

func (h *HNSWStore) Has(collection string) bool {
	return h.cm.Has(collection)
}

func (h *HNSWStore) Create(collection string) error {
	return h.cm.Create(collection)
}

func (h *HNSWStore) Drop(collection string) error {
	return h.cm.Drop(collection)
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

func (h *HNSWStore) Metadata(collection string) (CollectionMeta, bool) {
	meta := h.cm.GetMeta(collection)
	if meta == nil {
		return CollectionMeta{}, false
	}
	return CollectionMeta{
		Name:           meta.Name,
		RecordCount:    meta.RecordCount,
		Dimension:      meta.EmbeddingDim,
		Model:          meta.EmbeddingModel,
		DistanceMetric: meta.DistanceMetric,
	}, true
}

func (h *HNSWStore) Checkpoint() error {
	return h.cm.Checkpoint()
}

func (h *HNSWStore) Close() error {
	return h.cm.Close()
}

func metadataMatches(raw []byte, filter MetadataFilter) bool {
	if len(filter) == 0 {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		return false
	}
	for key, want := range filter {
		got, ok := meta[key]
		if !ok || !metadataValueMatches(got, want) {
			return false
		}
	}
	return true
}

func metadataValueMatches(got, want any) bool {
	if gotSlice, ok := toAnySlice(got); ok {
		if wantSlice, ok := toAnySlice(want); ok {
			for _, wantItem := range wantSlice {
				if !sliceContainsValue(gotSlice, wantItem) {
					return false
				}
			}
			return true
		}
		return sliceContainsValue(gotSlice, want)
	}
	if wantSlice, ok := toAnySlice(want); ok {
		return len(wantSlice) == 1 && scalarEqual(got, wantSlice[0])
	}
	return scalarEqual(got, want)
}

func toAnySlice(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []string:
		out := make([]any, len(s))
		for i, item := range s {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func sliceContainsValue(items []any, want any) bool {
	for _, item := range items {
		if scalarEqual(item, want) {
			return true
		}
	}
	return false
}

func scalarEqual(got, want any) bool {
	if reflect.DeepEqual(got, want) {
		return true
	}
	if gotNum, ok := numeric(got); ok {
		if wantNum, ok := numeric(want); ok {
			return gotNum == wantNum
		}
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	default:
		return 0, false
	}
}
