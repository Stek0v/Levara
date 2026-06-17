package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stek0v/levara/pkg/embcontract"
)

// ErrDimMismatch is returned by Search when the query vector's dimension
// differs from the collection's. Callers (consolidate, recall) use errors.Is
// to distinguish a genuine embed-incompatible collection from other failures
// and surface it instead of degrading to a silent empty result.
var ErrDimMismatch = errors.New("query dimension mismatch")

// ErrEmbeddingContractMismatch is returned when a write carries an embedding
// version that differs from the target collection's vector-space contract.
var ErrEmbeddingContractMismatch = errors.New("embedding contract mismatch")

const (
	EmbeddingVersionMetaKey  = embcontract.MetadataVersionKey
	EmbeddingContractMetaKey = embcontract.MetadataContractKey
)

type EmbeddingContract = embcontract.Contract

// CollectionMeta stores metadata about a collection's embedding configuration.
// Persisted as collection_meta.json in each collection directory.
type CollectionMeta struct {
	Name              string             `json:"name"`
	EmbeddingModel    string             `json:"embedding_model"`
	EmbeddingDim      int                `json:"embedding_dim"`
	DistanceMetric    string             `json:"distance_metric"` // cosine, l2, dot
	EmbeddingVersion  string             `json:"embedding_version,omitempty"`
	EmbeddingContract *EmbeddingContract `json:"embedding_contract,omitempty"`
	Domain            string             `json:"domain,omitempty"` // optional domain tag for routing (e.g., "medical", "scientific", "legal")
	RecordCount       int                `json:"record_count"`
	CreatedAt         string             `json:"created_at"`
	UpdatedAt         string             `json:"updated_at"`
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
	// defaultModel is stamped onto collections auto-created via the lazy
	// Insert -> getOrCreate path so they don't end up with an empty
	// embedding_model (findings P2.1). Set from the server's EMBEDDING_MODEL.
	defaultModel    string
	defaultContract EmbeddingContract
	afterInsertHook func(collection, id string, meta any)
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
				_ = saveCollectionMeta(colDir, meta)
			} else {
				ensureCollectionContract(meta)
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

// SetDefaultModel sets the embedding model stamped onto collections created
// through the lazy auto-create path (Create / getOrCreate). Wired from the
// server's EMBEDDING_MODEL after construction so sidecar collections born from
// a memory write carry their embedder instead of an empty string (P2.1).
func (cm *CollectionManager) SetDefaultModel(model string) {
	cm.mu.Lock()
	cm.defaultModel = model
	cm.mu.Unlock()
}

// SetDefaultEmbeddingContract sets the vector-space contract stamped onto
// lazily-created collections and records.
func (cm *CollectionManager) SetDefaultEmbeddingContract(contract EmbeddingContract) {
	cm.mu.Lock()
	cm.defaultContract = contract.Normalized()
	cm.mu.Unlock()
}

// SetAfterInsertHook registers a best-effort callback invoked after successful
// collection writes. The hook runs outside CollectionManager locks and must not
// mutate source records. It is used for temporary embedding migration
// dual-write windows while a shadow ANN index catches up to live traffic.
func (cm *CollectionManager) SetAfterInsertHook(hook func(collection, id string, meta any)) {
	cm.mu.Lock()
	cm.afterInsertHook = hook
	cm.mu.Unlock()
}

func (cm *CollectionManager) afterInsert(collection, id string, meta any) {
	cm.mu.RLock()
	hook := cm.afterInsertHook
	cm.mu.RUnlock()
	if hook != nil {
		hook(collection, id, meta)
	}
}

// Create creates a new collection. Returns error if it already exists.
// It stamps the manager's configured default embedding model so lazily
// auto-created collections don't end up with empty embedder metadata (P2.1).
func (cm *CollectionManager) Create(name string) error {
	cm.mu.RLock()
	model := cm.defaultModel
	cm.mu.RUnlock()
	return cm.CreateWithMeta(name, model, "")
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
	contract := cm.defaultContract
	if contract.Empty() || contract.Encoder != embeddingModel || contract.Dim != dim || contract.Metric != strings.ToLower(distanceMetric) {
		contract = embcontract.FromEnv(embeddingModel, dim, distanceMetric)
	}
	contract = contract.Normalized()
	meta := &CollectionMeta{
		Name:           name,
		EmbeddingModel: embeddingModel,
		EmbeddingDim:   dim,
		DistanceMetric: distanceMetric,
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if !contract.Empty() {
		meta.EmbeddingVersion = contract.Fingerprint()
		meta.EmbeddingContract = &contract
	}
	_ = saveCollectionMeta(colDir, meta)
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

// Rename atomically renames a collection on disk. The collection is briefly
// closed during the rename — readers/writers calling Get/Insert against the
// old name during this window will see "not found" (the maps are updated
// only after the on-disk rename + reopen succeeds). Used by the
// blue-green embed-model migration (see docs/MIGRATION-EMBED-POTION.md
// Phase 4.5) where the shadow collection is promoted to the live name
// in a sub-second window.
func (cm *CollectionManager) Rename(oldName, newName string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("collection name must be non-empty")
	}
	if oldName == newName {
		return fmt.Errorf("source and target names are identical: %q", oldName)
	}
	// Guard against path traversal. Collection names map directly to a
	// directory under cm.basePath; a "../" or "/" in the name would let
	// callers point Rename outside the collections root.
	for _, n := range []string{oldName, newName} {
		if strings.ContainsAny(n, "/\\") || n == "." || n == ".." || strings.Contains(n, "..") {
			return fmt.Errorf("invalid collection name: %q", n)
		}
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	db, exists := cm.collections[oldName]
	if !exists {
		return fmt.Errorf("collection %q not found", oldName)
	}
	if _, taken := cm.collections[newName]; taken {
		return fmt.Errorf("target collection %q already exists", newName)
	}

	// Belt-and-braces: if the target directory exists on disk but isn't in
	// our in-memory map, we'd happily clobber it with os.Rename. Refuse.
	newDir := filepath.Join(cm.basePath, newName)
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("target collection directory %q already exists on disk", newName)
	}

	// Close the live db to release WAL/disk file handles before renaming
	// the directory. If reopen fails after rename, we restore the original
	// state by renaming back — best-effort, since a failure here means the
	// on-disk layout no longer matches the in-memory map.
	if err := db.Close(); err != nil {
		return fmt.Errorf("close source for rename: %w", err)
	}
	delete(cm.collections, oldName)

	oldDir := filepath.Join(cm.basePath, oldName)
	if err := os.Rename(oldDir, newDir); err != nil {
		// Try to reopen the source under its old name so the manager isn't
		// left with a dangling entry. If this fails too, the caller has a
		// real on-disk problem and needs to restart.
		dbPath := filepath.Join(oldDir, "meta.bin")
		if reopened, rErr := NewLevara(db.dim, dbPath, cm.hnswCfg); rErr == nil {
			cm.collections[oldName] = reopened
		}
		return fmt.Errorf("rename %q→%q on disk: %w", oldName, newName, err)
	}

	newDbPath := filepath.Join(newDir, "meta.bin")
	reopened, err := NewLevara(db.dim, newDbPath, cm.hnswCfg)
	if err != nil {
		// Roll back the directory rename. If THAT fails, the collection is
		// stranded under the new on-disk name but absent from the map —
		// the operator will need to restart Levara so NewCollectionManager
		// re-discovers it.
		_ = os.Rename(newDir, oldDir)
		return fmt.Errorf("reopen %q after rename: %w", newName, err)
	}
	cm.collections[newName] = reopened

	// Update meta to reflect the new name. Old meta is removed from the
	// map; the on-disk meta file is re-saved under the new directory.
	if m, ok := cm.metas[oldName]; ok {
		m.Name = newName
		cm.metas[newName] = m
		_ = saveCollectionMeta(newDir, m)
	}
	delete(cm.metas, oldName)

	return nil
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

// ListByDomain returns collections matching a domain tag. Empty domain returns all.
func (cm *CollectionManager) ListByDomain(domain string) []string {
	if domain == "" {
		return cm.List()
	}
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	domain = strings.ToLower(domain)
	var names []string
	for name := range cm.collections {
		if m, ok := cm.metas[name]; ok && strings.ToLower(m.Domain) == domain {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return cm.List() // fallback: return all if no domain match
	}
	sort.Strings(names)
	return names
}

// SetDomain updates the domain tag for a collection.
func (cm *CollectionManager) SetDomain(name, domain string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if m, ok := cm.metas[name]; ok {
		m.Domain = domain
		colDir := filepath.Join(cm.basePath, name)
		_ = saveCollectionMeta(colDir, m)
	}
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
	m := cm.metas[collection]
	if m == nil {
		m = cm.defaultMetaForVectorLocked(collection, len(vec))
	}
	if m != nil && m.EmbeddingDim > 0 && len(vec) != m.EmbeddingDim {
		cm.mu.RUnlock()
		return fmt.Errorf("dimension mismatch: vector dim=%d, collection %q expects dim=%d (model=%s)",
			len(vec), collection, m.EmbeddingDim, m.EmbeddingModel)
	}
	if err := validateEmbeddingContract(collection, m, meta); err != nil {
		cm.mu.RUnlock()
		return err
	}
	meta = stampEmbeddingMetadata(m, meta)
	cm.mu.RUnlock()

	db, err := cm.getOrCreate(collection)
	if err != nil {
		return err
	}
	if err := db.Insert(id, vec, meta); err != nil {
		return err
	}
	cm.refreshRecordCount(collection, db)
	cm.afterInsert(collection, id, meta)
	return nil
}

// BatchInsert inserts records into a collection (auto-creates if not exists).
func (cm *CollectionManager) BatchInsert(collection string, records []BatchItem) []error {
	cm.mu.RLock()
	m := cm.metas[collection]
	if m == nil && len(records) > 0 {
		m = cm.defaultMetaForVectorLocked(collection, len(records[0].Vector))
	}
	for _, r := range records {
		if m != nil && m.EmbeddingDim > 0 && len(r.Vector) != m.EmbeddingDim {
			cm.mu.RUnlock()
			return []error{fmt.Errorf("dimension mismatch: vector dim=%d, collection %q expects dim=%d (model=%s)",
				len(r.Vector), collection, m.EmbeddingDim, m.EmbeddingModel)}
		}
		if err := validateEmbeddingContract(collection, m, r.Data); err != nil {
			cm.mu.RUnlock()
			return []error{err}
		}
	}
	for i := range records {
		records[i].Data = stampEmbeddingMetadata(m, records[i].Data)
	}
	cm.mu.RUnlock()

	db, err := cm.getOrCreate(collection)
	if err != nil {
		return []error{err}
	}
	errs := db.BatchInsert(records)
	cm.refreshRecordCount(collection, db)
	if len(errs) == 0 {
		for _, r := range records {
			cm.afterInsert(collection, r.ID, r.Data)
		}
	}
	return errs
}

// refreshRecordCount updates the cached RecordCount on the collection's meta
// after a write. Uses db.Count() which is thread-safe (holds db.mu.RLock
// internally) instead of reading len(db.index) racily. Without this, GetMeta
// returns stale counts after Delete/BatchInsert/BatchDelete.
func (cm *CollectionManager) refreshRecordCount(collection string, db *Levara) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if m, ok := cm.metas[collection]; ok {
		m.RecordCount = db.Count()
	}
}

// Search searches within a specific collection.
func (cm *CollectionManager) Search(collection string, query []float32, topK int) ([]VectroRecord, error) {
	db, err := cm.Get(collection)
	if err != nil {
		return nil, err
	}
	// Guard against a query whose dimension differs from the collection's.
	// Without this, dist()/vek32.Dot panics "slices must be of equal length"
	// with no recover(), crashing the whole process — e.g. a 768-dim embedder
	// querying a 256-dim memory collection (consolidate, recall).
	if len(query) != db.dim {
		return nil, fmt.Errorf("%w: query dim %d != collection %q dim %d", ErrDimMismatch, len(query), collection, db.dim)
	}
	return db.Search(query, topK), nil
}

// HasRecord reports whether a record with the given id exists in the
// collection's index. This is a synchronous map lookup (db.Get), NOT a
// vector search — so it reflects the write the instant Insert returns,
// before the async HNSW indexer has linked the node. Used by the memory
// write path to verify a just-inserted vector actually landed.
// Returns false when the collection is absent or the id is unknown.
func (cm *CollectionManager) HasRecord(collection, id string) bool {
	db, err := cm.Get(collection)
	if err != nil {
		return false
	}
	_, _, ok := db.Get(id)
	return ok
}

// Delete removes a record from a collection.
func (cm *CollectionManager) Delete(collection, id string) error {
	db, err := cm.Get(collection)
	if err != nil {
		return err
	}
	if err := db.Delete(id); err != nil {
		return err
	}
	cm.refreshRecordCount(collection, db)
	return nil
}

// BatchDelete removes multiple records from a collection.
func (cm *CollectionManager) BatchDelete(collection string, ids []string) []error {
	db, err := cm.Get(collection)
	if err != nil {
		return []error{err}
	}
	errs := db.BatchDelete(ids)
	cm.refreshRecordCount(collection, db)
	return errs
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
	ensureCollectionContract(meta)
	colDir := filepath.Join(cm.basePath, name)
	return saveCollectionMeta(colDir, meta)
}

// UpdateEmbeddingContract replaces the stored vector-space contract for a
// collection after a successful re-embed or explicit operator metadata update.
func (cm *CollectionManager) UpdateEmbeddingContract(name string, contract EmbeddingContract) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	meta, ok := cm.metas[name]
	if !ok {
		return fmt.Errorf("collection %q not found", name)
	}
	contract = contract.Normalized()
	meta.EmbeddingModel = contract.Encoder
	meta.EmbeddingDim = contract.Dim
	meta.DistanceMetric = contract.Metric
	meta.EmbeddingVersion = contract.Fingerprint()
	meta.EmbeddingContract = &contract
	colDir := filepath.Join(cm.basePath, name)
	return saveCollectionMeta(colDir, meta)
}

func ensureCollectionContract(meta *CollectionMeta) {
	if meta == nil {
		return
	}
	if meta.DistanceMetric == "" {
		meta.DistanceMetric = "cosine"
	}
	contract := embcontract.FromEnv(meta.EmbeddingModel, meta.EmbeddingDim, meta.DistanceMetric).Normalized()
	if contract.Empty() {
		return
	}
	if meta.EmbeddingContract == nil {
		meta.EmbeddingContract = &contract
	}
	if meta.EmbeddingVersion == "" {
		meta.EmbeddingVersion = meta.EmbeddingContract.Fingerprint()
	}
}

func validateEmbeddingContract(collection string, meta *CollectionMeta, recordMeta any) error {
	if meta == nil || meta.EmbeddingVersion == "" {
		return nil
	}
	incoming := embcontract.VersionFromMetadata(recordMeta)
	if incoming == "" || incoming == meta.EmbeddingVersion {
		return nil
	}
	return fmt.Errorf("%w: collection %q expects %s, record has %s", ErrEmbeddingContractMismatch, collection, meta.EmbeddingVersion, incoming)
}

func stampEmbeddingMetadata(meta *CollectionMeta, recordMeta any) any {
	if meta == nil || meta.EmbeddingVersion == "" || meta.EmbeddingContract == nil {
		return recordMeta
	}
	if embcontract.VersionFromMetadata(recordMeta) != "" {
		return recordMeta
	}
	return embcontract.StampMetadata(recordMeta, *meta.EmbeddingContract)
}

func (cm *CollectionManager) defaultMetaForVectorLocked(collection string, dim int) *CollectionMeta {
	contract := cm.defaultContract
	if contract.Empty() {
		contract = embcontract.FromEnv(cm.defaultModel, dim, "cosine")
	}
	contract.Dim = dim
	contract = contract.Normalized()
	if contract.Empty() {
		return nil
	}
	return &CollectionMeta{
		Name:              collection,
		EmbeddingModel:    contract.Encoder,
		EmbeddingDim:      dim,
		DistanceMetric:    contract.Metric,
		EmbeddingVersion:  contract.Fingerprint(),
		EmbeddingContract: &contract,
	}
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
