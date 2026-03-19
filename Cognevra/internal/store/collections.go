package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// CollectionManager manages multiple Cognevra instances, one per collection.
// Each collection has its own HNSW index, WAL, Arena, and DiskStore.
type CollectionManager struct {
	mu          sync.RWMutex
	collections map[string]*Cognevra
	dim         int
	basePath    string
	hnswCfg     HNSWConfig
}

// NewCollectionManager creates a manager for named collections.
// Existing collections are loaded from disk on startup.
// An optional HNSWConfig can be provided; DefaultHNSWConfig() is used otherwise.
func NewCollectionManager(dim int, basePath string, cfg ...HNSWConfig) (*CollectionManager, error) {
	hnswCfg := DefaultHNSWConfig()
	if len(cfg) > 0 {
		hnswCfg = cfg[0]
	}

	collectionsDir := filepath.Join(basePath, "collections")
	if err := os.MkdirAll(collectionsDir, 0755); err != nil {
		return nil, fmt.Errorf("create collections dir: %w", err)
	}

	cm := &CollectionManager{
		collections: make(map[string]*Cognevra),
		dim:         dim,
		basePath:    collectionsDir,
		hnswCfg:     hnswCfg,
	}

	// Load existing collections from disk
	entries, err := os.ReadDir(collectionsDir)
	if err != nil {
		return nil, fmt.Errorf("read collections dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			name := e.Name()
			dbPath := filepath.Join(collectionsDir, name, "meta.bin")
			db, err := NewCognevra(dim, dbPath, hnswCfg)
			if err != nil {
				fmt.Printf("WARNING: failed to load collection %q: %v\n", name, err)
				continue
			}
			cm.collections[name] = db
			fmt.Printf("Loaded collection %q (%d records)\n", name, len(db.index))
		}
	}

	return cm, nil
}

// Create creates a new collection. Returns error if it already exists.
func (cm *CollectionManager) Create(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.collections[name]; exists {
		return nil // idempotent
	}

	colDir := filepath.Join(cm.basePath, name)
	if err := os.MkdirAll(colDir, 0755); err != nil {
		return fmt.Errorf("create collection dir: %w", err)
	}

	dbPath := filepath.Join(colDir, "meta.bin")
	db, err := NewCognevra(cm.dim, dbPath, cm.hnswCfg)
	if err != nil {
		return fmt.Errorf("create collection %q: %w", name, err)
	}

	cm.collections[name] = db
	return nil
}

// Drop removes a collection and its data from disk.
func (cm *CollectionManager) Drop(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	db, exists := cm.collections[name]
	if !exists {
		return fmt.Errorf("collection %q not found", name)
	}

	db.Close()
	delete(cm.collections, name)

	colDir := filepath.Join(cm.basePath, name)
	return os.RemoveAll(colDir)
}

// List returns sorted collection names.
func (cm *CollectionManager) List() []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	names := make([]string, 0, len(cm.collections))
	for name := range cm.collections {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Has checks if a collection exists.
func (cm *CollectionManager) Has(name string) bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	_, exists := cm.collections[name]
	return exists
}

// Get returns the Cognevra instance for a collection.
func (cm *CollectionManager) Get(name string) (*Cognevra, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	db, exists := cm.collections[name]
	if !exists {
		return nil, fmt.Errorf("collection %q not found", name)
	}
	return db, nil
}

// getOrCreate returns existing collection or creates it.
func (cm *CollectionManager) getOrCreate(name string) (*Cognevra, error) {
	cm.mu.RLock()
	db, exists := cm.collections[name]
	cm.mu.RUnlock()

	if exists {
		return db, nil
	}

	// Need to create — upgrade to write lock
	if err := cm.Create(name); err != nil {
		return nil, err
	}
	return cm.Get(name)
}

// Insert inserts a record into a collection (auto-creates if not exists).
func (cm *CollectionManager) Insert(collection, id string, vec []float32, meta interface{}) error {
	db, err := cm.getOrCreate(collection)
	if err != nil {
		return err
	}
	return db.Insert(id, vec, meta)
}

// BatchInsert inserts records into a collection (auto-creates if not exists).
func (cm *CollectionManager) BatchInsert(collection string, records []BatchItem) []error {
	db, err := cm.getOrCreate(collection)
	if err != nil {
		return []error{err}
	}
	return db.BatchInsert(records)
}

// Search searches within a specific collection.
func (cm *CollectionManager) Search(collection string, query []float32, topK int) ([]VectroRecord, error) {
	db, err := cm.Get(collection)
	if err != nil {
		return nil, err
	}
	return db.Search(query, topK), nil
}

// Delete removes a record from a collection.
func (cm *CollectionManager) Delete(collection, id string) error {
	db, err := cm.Get(collection)
	if err != nil {
		return err
	}
	return db.Delete(id)
}

// BatchDelete removes multiple records from a collection.
func (cm *CollectionManager) BatchDelete(collection string, ids []string) []error {
	db, err := cm.Get(collection)
	if err != nil {
		return []error{err}
	}
	return db.BatchDelete(ids)
}

// Close flushes and closes all collections.
func (cm *CollectionManager) Close() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	var firstErr error
	for name, db := range cm.collections {
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close collection %q: %w", name, err)
		}
	}
	return firstErr
}

// Count returns the number of collections.
func (cm *CollectionManager) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.collections)
}
