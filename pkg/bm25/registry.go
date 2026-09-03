package bm25

import "sync"

// IndexRegistry is a concurrency-safe wrapper around the shared
// collection → *Index map. Before this type the map was a bare
// map[string]*Index shared between search/doctor/autosave readers and
// cognify/workspace writers; a concurrent cognify insert while a search
// ranged the map could panic with "concurrent map read and map write"
// (finding H2, 2026-09-03 review). Individual Index values remain
// internally thread-safe (Index has its own RWMutex); only the map
// structure needs the registry lock.
type IndexRegistry struct {
	mu      sync.RWMutex
	indexes map[string]*Index
}

// NewIndexRegistry creates an empty registry.
func NewIndexRegistry() *IndexRegistry {
	return &IndexRegistry{indexes: make(map[string]*Index)}
}

// NewIndexRegistryFrom wraps an existing map. The supplied map becomes
// owned by the registry — callers must stop accessing it directly.
func NewIndexRegistryFrom(indexes map[string]*Index) *IndexRegistry {
	if indexes == nil {
		return NewIndexRegistry()
	}
	return &IndexRegistry{indexes: indexes}
}

// Get returns the index for a collection, or nil.
func (r *IndexRegistry) Get(collection string) *Index {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.indexes[collection]
}

// GetOrCreate returns the existing index or creates and stores a new one.
// The optional onCreate callback runs while the registry is still locked,
// before the index becomes visible to other goroutines — use it to attach
// persistence (SnapshotStore.Attach) so no mutation can be lost between
// creation and attachment.
func (r *IndexRegistry) GetOrCreate(collection string, onCreate func(collection string, idx *Index)) *Index {
	if r == nil || collection == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if idx, ok := r.indexes[collection]; ok && idx != nil {
		return idx
	}
	idx := NewIndex()
	if onCreate != nil {
		onCreate(collection, idx)
	}
	r.indexes[collection] = idx
	return idx
}

// Insert stores a pre-built index under a collection, replacing any
// existing entry. Used at startup to register indexes loaded from disk.
func (r *IndexRegistry) Insert(collection string, idx *Index) {
	if r == nil || collection == "" || idx == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.indexes[collection] = idx
}

// Delete removes a collection's index. Returns the removed index, or nil
// when absent.
func (r *IndexRegistry) Delete(collection string) *Index {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.indexes[collection]
	delete(r.indexes, collection)
	return idx
}

// Len returns the number of registered collections.
func (r *IndexRegistry) Len() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.indexes)
}

// Snapshot returns the collection → index mapping as a point-in-time copy.
// Mutating the returned map does not affect the registry; the *Index values
// are shared and internally synchronized.
func (r *IndexRegistry) Snapshot() map[string]*Index {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*Index, len(r.indexes))
	for k, v := range r.indexes {
		out[k] = v
	}
	return out
}

// Names returns the sorted list of registered collections.
func (r *IndexRegistry) Names() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.indexes))
	for name := range r.indexes {
		out = append(out, name)
	}
	// small n; sort for stable doctor/ops output
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
