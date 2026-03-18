package store

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type VectroRecord struct {
	ID    string
	Score float32
	Data  json.RawMessage
}

// pendingItem holds data for records accepted but not yet indexed by HNSW.
type pendingItem struct {
	vector []float32
	id     string
	idx    uint32
}

type VectraDB struct {
	mu       sync.RWMutex
	index    map[string]uint32
	revIndex []string

	// Hot Path Storage
	arena *VectorArena

	// Cold Path Storage
	metaLocs map[uint32]FileLocation

	disk *DiskStore

	dim int

	wal *WAL

	hnsw *HNSWIndex

	// Async HNSW indexing: records land here after durable write,
	// background goroutine drains them into hnsw.Add().
	pendingMu   sync.RWMutex
	pendingVecs []pendingItem
	indexSignal chan struct{}
}

func NewVectraDB(dim int, storagePath string) (*VectraDB, error) {

	ds, err := NewDiskStore(storagePath)
	if err != nil {
		return nil, fmt.Errorf("Failed to init disk store at %s: %w", storagePath, err)
	}
	walPath := storagePath + ".wal"
	wal, err := OpenWal(walPath)
	if err != nil {
		return nil, err
	}
	localArena := NewVectorArena(dim)

	db := &VectraDB{
		index:       make(map[string]uint32),
		revIndex:    make([]string, 0, 10000),
		arena:       localArena,
		metaLocs:    make(map[uint32]FileLocation),
		disk:        ds,
		dim:         dim,
		wal:         wal,
		hnsw:        NewHNSWIndex(localArena),
		indexSignal: make(chan struct{}, 1),
	}

	// Truncate meta.bin before WAL recovery: WAL contains full metadata,
	// so we rebuild the disk store from scratch to guarantee valid offsets.
	if err := ds.Truncate(); err != nil {
		return nil, fmt.Errorf("failed to truncate disk store for recovery: %w", err)
	}

	fmt.Println("Replaying WAL to restore data....")
	insertCount := 0
	deleteCount := 0
	deletedIDs := make(map[string]struct{})

	// 2-pass recovery: first collect deletes, then apply inserts (skip deleted)
	err = wal.RecoverEx(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		switch op {
		case OpInsert:
			if _, deleted := deletedIDs[id]; deleted {
				return // Skip records that were later deleted
			}
			newLoc, writeErr := db.disk.Write(meta)
			if writeErr != nil {
				fmt.Printf("WAL recovery: failed to write metadata for %s: %v\n", id, writeErr)
				return
			}
			db.insertInMemory(id, vector, newLoc)
			insertCount++
		case OpDelete:
			deletedIDs[id] = struct{}{}
			// If already inserted during this recovery, remove it
			if idx, ok := db.index[id]; ok {
				delete(db.index, id)
				if int(idx) < len(db.revIndex) {
					db.revIndex[idx] = ""
				}
				delete(db.metaLocs, idx)
				deleteCount++
			}
		}
	})
	fmt.Printf("Recovered %d records (%d deleted) from WAL\n", insertCount, deleteCount)

	// Start background HNSW indexer.
	go db.indexerLoop()

	return db, nil
}

// indexerLoop drains pendingVecs into hnsw.Add in the background.
func (db *VectraDB) indexerLoop() {
	for range db.indexSignal {
		for {
			db.pendingMu.Lock()
			if len(db.pendingVecs) == 0 {
				db.pendingMu.Unlock()
				break
			}
			batch := db.pendingVecs
			db.pendingVecs = nil
			db.pendingMu.Unlock()

			for _, p := range batch {
				db.hnsw.Add(p.vector, p.id, p.idx)
			}
		}
	}
}

// signalIndexer wakes up the background HNSW indexer (non-blocking).
func (db *VectraDB) signalIndexer() {
	select {
	case db.indexSignal <- struct{}{}:
	default:
	}
}

func (db *VectraDB) Insert(id string, vector []float32, data any) error {
	db.mu.Lock()

	bytes, err := json.Marshal(data)
	if err != nil {
		db.mu.Unlock()
		return fmt.Errorf("Failed to marshal metadata: %w", err)
	}

	idx, err := db.arena.Add(vector)
	if err != nil {
		db.mu.Unlock()
		return err
	}

	loc, err := db.disk.Write(bytes)
	if err != nil {
		db.mu.Unlock()
		return err
	}

	if err := db.wal.WriteEntry(OpInsert, id, vector, bytes, loc); err != nil {
		db.mu.Unlock()
		return fmt.Errorf("wal write: %w", err)
	}

	db.index[id] = idx
	if int(idx) >= len(db.revIndex) {
		newSlice := make([]string, int(idx)+1024)
		copy(newSlice, db.revIndex)
		db.revIndex = newSlice
	}
	db.revIndex[idx] = id
	db.metaLocs[idx] = loc

	// Durable write done — release db.mu, enqueue for async HNSW indexing.
	db.mu.Unlock()

	db.pendingMu.Lock()
	db.pendingVecs = append(db.pendingVecs, pendingItem{vector: vector, id: id, idx: idx})
	db.pendingMu.Unlock()
	db.signalIndexer()

	return nil
}

// insertInMemory is used during WAL recovery — synchronous HNSW.Add (no async).
func (db *VectraDB) insertInMemory(id string, vector []float32, loc FileLocation) error {
	idx, err := db.arena.Add(vector)
	if err != nil {
		return err
	}

	db.index[id] = idx
	if int(idx) >= len(db.revIndex) {
		newSlice := make([]string, int(idx)+1024)
		copy(newSlice, db.revIndex)
		db.revIndex = newSlice
	}
	db.revIndex[idx] = id
	db.metaLocs[idx] = loc

	db.hnsw.Add(vector, id, idx)

	return nil
}

func (db *VectraDB) BatchInsert(records []BatchItem) []error {
	// Phase 0: marshal metadata outside lock (CPU-bound, no db state needed).
	type prepared struct {
		rec   BatchItem
		bytes []byte
	}
	prepped := make([]prepared, 0, len(records))
	var errs []error
	for _, rec := range records {
		bytes, err := json.Marshal(rec.Data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: marshal: %w", rec.ID, err))
			continue
		}
		prepped = append(prepped, prepared{rec: rec, bytes: bytes})
	}

	// Phase 1: durable write (arena + disk + WAL + index maps) under db.mu.
	db.mu.Lock()
	toIndex := make([]pendingItem, 0, len(prepped))

	for _, p := range prepped {
		idx, err := db.arena.Add(p.rec.Vector)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: arena: %w", p.rec.ID, err))
			continue
		}
		loc, err := db.disk.Write(p.bytes)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: disk: %w", p.rec.ID, err))
			continue
		}
		if err := db.wal.WriteEntryNoFlush(OpInsert, p.rec.ID, p.rec.Vector, p.bytes, loc); err != nil {
			errs = append(errs, fmt.Errorf("%s: wal: %w", p.rec.ID, err))
			continue
		}
		db.index[p.rec.ID] = idx
		if int(idx) >= len(db.revIndex) {
			ns := make([]string, int(idx)+1024)
			copy(ns, db.revIndex)
			db.revIndex = ns
		}
		db.revIndex[idx] = p.rec.ID
		db.metaLocs[idx] = loc
		toIndex = append(toIndex, pendingItem{vector: p.rec.Vector, id: p.rec.ID, idx: idx})
	}
	if err := db.wal.Flush(); err != nil {
		errs = append(errs, fmt.Errorf("wal flush: %w", err))
	}
	db.mu.Unlock()

	// Phase 2: enqueue for async HNSW indexing.
	if len(toIndex) > 0 {
		db.pendingMu.Lock()
		db.pendingVecs = append(db.pendingVecs, toIndex...)
		db.pendingMu.Unlock()
		db.signalIndexer()
	}

	return errs
}

func (db *VectraDB) Get(id string) ([]float32, []byte, bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	idx, exists := db.index[id]
	if !exists {
		return nil, nil, false
	}

	vec, _ := db.arena.Get(idx)
	metaLoc := db.metaLocs[idx]
	meta, err := db.disk.Read(metaLoc)
	if err != nil {
		return vec, nil, true
	}
	return vec, meta, true
}

func (db *VectraDB) Search(query []float32, topK int) []VectroRecord {
	normQ := normalizeVec(query)

	// HNSW search (indexed records).
	db.mu.RLock()
	records := db.hnsw.Search(normQ, topK)
	db.mu.RUnlock()

	// Brute-force scan of pending records not yet in HNSW.
	db.pendingMu.RLock()
	pending := db.pendingVecs
	db.pendingMu.RUnlock()

	if len(pending) > 0 {
		for _, p := range pending {
			d := dist(normQ, p.vector)
			records = append(records, VectroRecord{
				ID:    p.id,
				Score: 1 - d, // cosine similarity
				Data:  json.RawMessage("{}"),
			})
		}
		sort.Slice(records, func(i, j int) bool {
			return records[i].Score > records[j].Score
		})
		if len(records) > topK {
			records = records[:topK]
		}
	}

	// Enrich results with metadata from disk.
	db.mu.RLock()
	for i, rec := range records {
		if idx, ok := db.index[rec.ID]; ok {
			if loc, ok := db.metaLocs[idx]; ok {
				if meta, err := db.disk.Read(loc); err == nil {
					records[i].Data = meta
				}
			}
		}
	}
	db.mu.RUnlock()

	return records
}

// Delete removes a record by ID (index + HNSW tombstone + WAL).
func (db *VectraDB) Delete(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	idx, ok := db.index[id]
	if !ok {
		return fmt.Errorf("record %q not found", id)
	}

	// WAL: write delete entry for crash recovery
	if err := db.wal.WriteEntry(OpDelete, id, nil, nil, FileLocation{}); err != nil {
		return fmt.Errorf("wal delete: %w", err)
	}

	// Remove from in-memory maps
	delete(db.index, id)
	if int(idx) < len(db.revIndex) {
		db.revIndex[idx] = ""
	}
	delete(db.metaLocs, idx)

	// Mark deleted in HNSW (tombstone — search will skip)
	db.hnsw.MarkDeleted(idx)

	// Also remove from pending vectors (if not yet indexed)
	db.pendingMu.Lock()
	for i, p := range db.pendingVecs {
		if p.id == id {
			db.pendingVecs = append(db.pendingVecs[:i], db.pendingVecs[i+1:]...)
			break
		}
	}
	db.pendingMu.Unlock()

	return nil
}

// BatchDelete removes multiple records by ID.
func (db *VectraDB) BatchDelete(ids []string) []error {
	var errs []error
	for _, id := range ids {
		if err := db.Delete(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Close flushes WAL and disk store, ensuring all data is persisted.
func (db *VectraDB) Close() error {
	if err := db.wal.Close(); err != nil {
		db.disk.Close()
		return fmt.Errorf("wal close: %w", err)
	}
	return db.disk.Close()
}
