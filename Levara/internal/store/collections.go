package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// CollectionMeta stores metadata about a collection's embedding configuration.
// Persisted as collection_meta.json in each collection directory.
type CollectionMeta struct {
	Name             string `json:"name"`
	EmbeddingModel   string `json:"embedding_model"`
	EmbeddingDim     int    `json:"embedding_dim"`
	DistanceMetric   string `json:"distance_metric"` // cosine, l2, dot
	EmbeddingVersion string `json:"embedding_version,omitempty"`
	RecordCount      int    `json:"record_count"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

const collectionMetaFile = "collection_meta.json"

func loadCollectionMeta(colDir string) (*CollectionMeta, error) {
	path := filepath.Join(colDir, collectionMetaFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta CollectionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func saveCollectionMeta(colDir string, meta *CollectionMeta) error {
	meta.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(colDir, collectionMetaFile), data, 0644)
}

// CollectionManager manages multiple Levara instances, one per collection.
// Each collection has its own HNSW index, WAL, Arena, and DiskStore.
type CollectionManager struct {
	mu          sync.RWMutex
	collections map[string]*Levara
	metas       map[string]*CollectionMeta
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
		collections: make(map[string]*Levara),
		metas:       make(map[string]*CollectionMeta),
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
			colDir := filepath.Join(collectionsDir, name)

			// Determine dimension: from meta.json if exists, otherwise global default
			colDim := dim
			meta, metaErr := loadCollectionMeta(colDir)
			if metaErr == nil && meta.EmbeddingDim > 0 {
				colDim = meta.EmbeddingDim
			}

			dbPath := filepath.Join(colDir, "meta.bin")
			db, err := NewLevara(colDim, dbPath, hnswCfg)
			if err != nil {
				fmt.Printf("WARNING: failed to load collection %q (dim=%d): %v\n", name, colDim, err)
				continue
			}
			cm.collections[name] = db

			// Create metadata for legacy collections (no meta.json)
			if metaErr != nil {
				meta = &CollectionMeta{
					Name:           name,
					EmbeddingDim:   colDim,
					DistanceMetric: "cosine",
					RecordCount:    len(db.index),
					CreatedAt:      time.Now().UTC().Format(time.RFC3339),
				}
				saveCollectionMeta(colDir, meta)
			}
			meta.RecordCount = len(db.index)
			cm.metas[name] = meta

			fmt.Printf("Loaded collection %q (%d records, dim=%d, model=%s)\n",
				name, len(db.index), meta.EmbeddingDim, meta.EmbeddingModel)
		}
	}

	// Startup validation summary
	dimCounts := make(map[int]int)
	modelCounts := make(map[string]int)
	var warnings []string
	for name, meta := range cm.metas {
		dimCounts[meta.EmbeddingDim]++
		if meta.EmbeddingModel != "" {
			modelCounts[meta.EmbeddingModel]++
		} else {
			modelCounts["(unknown)"]++
		}
		if meta.EmbeddingDim != dim && meta.EmbeddingDim > 0 {
			warnings = append(warnings, fmt.Sprintf("  %s: dim=%d (server=%d)", name, meta.EmbeddingDim, dim))
		}
	}
	if len(cm.metas) > 0 {
		fmt.Printf("Collection summary: %d collections loaded\n", len(cm.metas))
		for d, n := range dimCounts {
			fmt.Printf("  dim=%d: %d collections\n", d, n)
		}
		for m, n := range modelCounts {
			fmt.Printf("  model=%s: %d collections\n", m, n)
		}
		if len(warnings) > 0 {
			fmt.Printf("WARNING: %d collections have non-default dimensions:\n", len(warnings))
			for _, w := range warnings {
				fmt.Println(w)
			}
		}
	}

	return cm, nil
}

// Create creates a new collection. Returns error if it already exists.
func (cm *CollectionManager) Create(name string) error {
	return cm.CreateWithMeta(name, "", "")
}

// CreateWithMeta creates a collection with embedding model metadata.
func (cm *CollectionManager) CreateWithMeta(name, embeddingModel, distanceMetric string) error {
	return cm.CreateWithDim(name, cm.dim, embeddingModel, distanceMetric)
}

// CreateWithDim creates a collection with a specific dimension (can differ from global default).
func (cm *CollectionManager) CreateWithDim(name string, dim int, embeddingModel, distanceMetric string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.collections[name]; exists {
		return nil // idempotent
	}

	if dim <= 0 {
		dim = cm.dim
	}

	colDir := filepath.Join(cm.basePath, name)
	if err := os.MkdirAll(colDir, 0755); err != nil {
		return fmt.Errorf("create collection dir: %w", err)
	}

	dbPath := filepath.Join(colDir, "meta.bin")
	db, err := NewLevara(dim, dbPath, cm.hnswCfg)
	if err != nil {
		return fmt.Errorf("create collection %q: %w", name, err)
	}

	cm.collections[name] = db

	if distanceMetric == "" {
		distanceMetric = "cosine"
	}
	meta := &CollectionMeta{
		Name:           name,
		EmbeddingModel: embeddingModel,
		EmbeddingDim:   dim,
		DistanceMetric: distanceMetric,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	saveCollectionMeta(colDir, meta)
	cm.metas[name] = meta

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
	delete(cm.metas, name)

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

// Get returns the Levara instance for a collection.
func (cm *CollectionManager) Get(name string) (*Levara, error) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	db, exists := cm.collections[name]
	if !exists {
		return nil, fmt.Errorf("collection %q not found", name)
	}
	return db, nil
}

// getOrCreate returns existing collection or creates it with default dim.
func (cm *CollectionManager) getOrCreate(name string) (*Levara, error) {
	cm.mu.RLock()
	db, exists := cm.collections[name]
	cm.mu.RUnlock()

	if exists {
		return db, nil
	}

	if err := cm.Create(name); err != nil {
		return nil, err
	}
	return cm.Get(name)
}

// GetOrCreateWithDim returns existing collection or creates it with specific dim.
func (cm *CollectionManager) GetOrCreateWithDim(name string, dim int, model string) (*Levara, error) {
	cm.mu.RLock()
	db, exists := cm.collections[name]
	cm.mu.RUnlock()

	if exists {
		return db, nil
	}

	if err := cm.CreateWithDim(name, dim, model, "cosine"); err != nil {
		return nil, err
	}
	return cm.Get(name)
}

// Dim returns the dimension of a specific collection (or global default).
func (cm *CollectionManager) Dim(name string) int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if meta, ok := cm.metas[name]; ok && meta.EmbeddingDim > 0 {
		return meta.EmbeddingDim
	}
	return cm.dim
}

// DefaultDim returns the global default dimension.
func (cm *CollectionManager) DefaultDim() int {
	return cm.dim
}

// Insert inserts a record into a collection (auto-creates if not exists).
// Validates vector dimension against collection metadata before insert.
func (cm *CollectionManager) Insert(collection, id string, vec []float32, meta interface{}) error {
	// Pre-check dimension against collection metadata
	cm.mu.RLock()
	if m, ok := cm.metas[collection]; ok && m.EmbeddingDim > 0 && len(vec) != m.EmbeddingDim {
		cm.mu.RUnlock()
		return fmt.Errorf("dimension mismatch: vector dim=%d, collection %q expects dim=%d (model=%s)",
			len(vec), collection, m.EmbeddingDim, m.EmbeddingModel)
	}
	cm.mu.RUnlock()

	db, err := cm.getOrCreate(collection)
	if err != nil {
		return err
	}
	if err := db.Insert(id, vec, meta); err != nil {
		return err
	}
	cm.mu.RLock()
	if m, ok := cm.metas[collection]; ok {
		m.RecordCount = len(db.index)
	}
	cm.mu.RUnlock()
	return nil
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

// GetMeta returns metadata for a collection.
func (cm *CollectionManager) GetMeta(name string) *CollectionMeta {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.metas[name]
}

// ListWithMeta returns all collections with their metadata.
func (cm *CollectionManager) ListWithMeta() []CollectionMeta {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	result := make([]CollectionMeta, 0, len(cm.metas))
	for _, m := range cm.metas {
		// Refresh record count
		if db, ok := cm.collections[m.Name]; ok {
			m.RecordCount = len(db.index)
		}
		result = append(result, *m)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// UpdateMeta updates metadata for a collection (e.g., after re-embedding).
func (cm *CollectionManager) UpdateMeta(name string, model, distanceMetric, version string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	meta, ok := cm.metas[name]
	if !ok {
		return fmt.Errorf("collection %q not found", name)
	}
	if model != "" {
		meta.EmbeddingModel = model
	}
	if distanceMetric != "" {
		meta.DistanceMetric = distanceMetric
	}
	if version != "" {
		meta.EmbeddingVersion = version
	}
	colDir := filepath.Join(cm.basePath, name)
	return saveCollectionMeta(colDir, meta)
}

// AllRecords returns all (id, vector, metadata) from a collection. Used for re-embedding.
func (cm *CollectionManager) AllRecords(collection string) ([]string, [][]float32, [][]byte, error) {
	db, err := cm.Get(collection)
	if err != nil {
		return nil, nil, nil, err
	}
	ids := db.AllIDs()
	vecs := make([][]float32, len(ids))
	metas := make([][]byte, len(ids))
	for i, id := range ids {
		vec, meta, ok := db.Get(id)
		if ok {
			vecs[i] = vec
			metas[i] = meta
		}
	}
	return ids, vecs, metas, nil
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

// Checkpoint compacts WAL for all collections.
func (cm *CollectionManager) Checkpoint() error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	var errs []error
	for name, db := range cm.collections {
		if err := db.Checkpoint(); err != nil {
			errs = append(errs, fmt.Errorf("collection %q: %w", name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("checkpoint errors: %v", errs)
	}
	return nil
}
