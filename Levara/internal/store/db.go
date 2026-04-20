// Package store implements Levara's storage engine: HNSW index, WAL, arena, and disk store.
//
// The main entry point is [NewLevara], which returns a [Levara] instance ready for
// concurrent insert, search, and delete operations. Durability is provided by a
// write-ahead log ([WAL]) with group-commit fsync, while hot vectors live in a
// memory-mapped arena ([VectorArena]) and metadata is persisted in an append-only
// file ([DiskStore]). HNSW graph construction happens asynchronously in the background
// so that write latency is not gated on graph linking.
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
)

// VectroRecord is a single result returned by [Levara.Search].
// Score is the cosine similarity (higher = more similar).
// Data contains the raw JSON metadata stored at insert time.
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

// Levara is the core storage engine combining an in-memory HNSW index with a
// durable WAL and append-only metadata store. It is safe for concurrent use.
//
// Writes are two-phase: data is written durably (arena + disk + WAL) under db.mu,
// then HNSW graph construction happens asynchronously via a background goroutine.
// Searches over records not yet indexed fall back to a brute-force scan of the
// pending queue so no results are missed.
type Levara struct {
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

	hnsw    *HNSWIndex
	hnswCfg HNSWConfig

	// Async HNSW indexing: records land here after durable write,
	// background goroutine drains them into hnsw.Add().
	pendingMu   sync.RWMutex
	pendingVecs []pendingItem
	indexSignal chan struct{}
	closeOnce   sync.Once
}

// NewLevara creates a new Levara instance.
//
// dim is the fixed vector dimension; all inserted vectors must match it.
// storagePath is the path to the metadata file (meta.bin); sibling files (.wal) are created
// automatically. An optional [HNSWConfig] may be provided to tune graph parameters;
// [DefaultHNSWConfig] is used when omitted.
//
// On startup, NewLevara replays the WAL to restore all previously inserted records
// and starts a background goroutine for async HNSW indexing.
func NewLevara(dim int, storagePath string, cfg ...HNSWConfig) (*Levara, error) {
	hnswCfg := DefaultHNSWConfig()
	if len(cfg) > 0 {
		hnswCfg = cfg[0]
	}

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

	db := &Levara{
		index:       make(map[string]uint32),
		revIndex:    make([]string, 0, 10000),
		arena:       localArena,
		metaLocs:    make(map[uint32]FileLocation),
		disk:        ds,
		dim:         dim,
		wal:         wal,
		hnsw:        NewHNSWIndex(localArena, hnswCfg),
		hnswCfg:     hnswCfg,
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

	// Single-pass sequential replay: WAL order is authoritative. Applying each
	// entry in order correctly handles Insert→Delete→Insert of the same ID
	// (the final Insert wins). The previous 2-pass scheme with a deletedIDs
	// prepass dropped any Insert whose ID was ever deleted, silently losing
	// re-inserted records after recovery.
	//
	// Arena slots of deleted records remain allocated — this matches runtime
	// Delete semantics (Delete only tombstones in HNSW). Checkpoint() is the
	// only path that reclaims them.
	err = wal.RecoverEx(func(op byte, id string, vector []float32, meta []byte, loc FileLocation) {
		switch op {
		case OpInsert:
			// Dim guard before disk write so a corrupt/migrated WAL entry
			// doesn't leak metadata bytes on disk before the arena rejects it.
			if len(vector) != db.dim {
				fmt.Printf("WAL recovery: skipping %s — vector dim %d != expected %d\n",
					id, len(vector), db.dim)
				return
			}
			newLoc, writeErr := db.disk.Write(meta)
			if writeErr != nil {
				fmt.Printf("WAL recovery: failed to write metadata for %s: %v\n", id, writeErr)
				return
			}
			if err := db.insertInMemory(id, vector, newLoc); err != nil {
				// Keep insertCount honest — partially-failed inserts leave the
				// id unindexed, and continuing silently would make a later
				// Delete for the same id appear to be a no-op (see T16 review C1).
				fmt.Printf("WAL recovery: insertInMemory failed for %s: %v\n", id, err)
				return
			}
			insertCount++
		case OpDelete:
			if idx, ok := db.index[id]; ok {
				delete(db.index, id)
				if int(idx) < len(db.revIndex) {
					db.revIndex[idx] = ""
				}
				delete(db.metaLocs, idx)
				// Match normal Delete semantics: tombstone in HNSW so search skips it.
				db.hnsw.MarkDeleted(idx)
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
//
// Locking: reading db.hnsw without a lock is a data race with Clear() which
// atomically-but-not-really swaps db.hnsw and db.arena (see fsm.Restore caller).
// We snapshot the current hnsw pointer under db.mu.RLock() per batch, then
// release the lock before calling hnsw.Add. If Clear runs concurrently and
// swaps the pointer, the old hnsw captured here will absorb the batch — those
// entries become orphaned but not corrupted (old arena + old hnsw stay alive
// via our local reference until the batch drains). Consistency is preserved
// because each hnsw owns its own arena reference.
func (db *Levara) indexerLoop() {
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

			db.mu.RLock()
			hnsw := db.hnsw
			db.mu.RUnlock()

			for _, p := range batch {
				hnsw.Add(p.vector, p.id, p.idx)
			}
		}
	}
}

// signalIndexer wakes up the background HNSW indexer (non-blocking).
func (db *Levara) signalIndexer() {
	select {
	case db.indexSignal <- struct{}{}:
	default:
	}
}

// Insert durably stores a single record (vector + JSON-serializable metadata) and
// enqueues it for async HNSW indexing. It blocks until the WAL fsync completes.
func (db *Levara) Insert(id string, vector []float32, data any) error {
	// Marshal metadata outside the lock (pure computation, no dependency on locked state).
	var bytes []byte
	var err error
	switch v := data.(type) {
	case json.RawMessage:
		bytes = v
	case []byte:
		bytes = v
	case string:
		// If the string is already valid JSON, store it directly.
		// This prevents double-encoding (e.g., `"{\"key\":\"val\"}"` → `"\"{ ... }\""`).
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			bytes = []byte(v)
		} else {
			bytes, err = json.Marshal(v)
		}
	default:
		bytes, err = json.Marshal(data)
	}
	if err != nil {
		return fmt.Errorf("Failed to marshal metadata: %w", err)
	}

	// Write metadata to disk outside db.mu — DiskStore has its own internal
	// mutex, and the returned FileLocation is an immutable (offset, length)
	// pair that doesn't depend on db state. Moving it here reduces db.mu hold
	// time by ~5μs per Insert (buffered file.Write latency).
	loc, err := db.disk.Write(bytes)
	if err != nil {
		return err
	}

	db.mu.Lock()

	idx, err := db.arena.Add(vector)
	if err != nil {
		db.mu.Unlock()
		return err
	}

	if err := db.wal.WriteEntryNoFlush(OpInsert, id, vector, bytes, loc); err != nil {
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

	// Release db.mu before fsync — group commit coalesces across goroutines.
	db.mu.Unlock()

	// Group commit: fsync outside db.mu lock
	if err := db.wal.FlushAsync(); err != nil {
		return fmt.Errorf("wal flush: %w", err)
	}

	db.pendingMu.Lock()
	db.pendingVecs = append(db.pendingVecs, pendingItem{vector: vector, id: id, idx: idx})
	db.pendingMu.Unlock()
	db.signalIndexer()

	return nil
}

// insertInMemory is used during WAL recovery — synchronous HNSW.Add (no async).
func (db *Levara) insertInMemory(id string, vector []float32, loc FileLocation) error {
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

// BatchInsert durably stores multiple records in a single group-commit WAL fsync.
// Metadata marshalling is done outside the lock for parallelism. Returns a slice of
// per-record errors (nil entries mean success); the slice is nil if all records succeeded.
func (db *Levara) BatchInsert(records []BatchItem) []error {
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

	// Phase 0b: write metadata to disk outside db.mu — DiskStore has its own
	// internal mutex. For a batch of 50 items this saves ~250μs of lock time
	// (50 × ~5μs per buffered Write).
	type diskPrep struct {
		rec   BatchItem
		bytes []byte
		loc   FileLocation
	}
	diskPrepped := make([]diskPrep, 0, len(prepped))
	for _, p := range prepped {
		loc, err := db.disk.Write(p.bytes)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: disk: %w", p.rec.ID, err))
			continue
		}
		diskPrepped = append(diskPrepped, diskPrep{rec: p.rec, bytes: p.bytes, loc: loc})
	}

	// Phase 1: arena + WAL + index maps under db.mu.
	db.mu.Lock()
	toIndex := make([]pendingItem, 0, len(diskPrepped))

	for _, d := range diskPrepped {
		idx, err := db.arena.Add(d.rec.Vector)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: arena: %w", d.rec.ID, err))
			continue
		}
		if err := db.wal.WriteEntryNoFlush(OpInsert, d.rec.ID, d.rec.Vector, d.bytes, d.loc); err != nil {
			errs = append(errs, fmt.Errorf("%s: wal: %w", d.rec.ID, err))
			continue
		}
		db.index[d.rec.ID] = idx
		if int(idx) >= len(db.revIndex) {
			ns := make([]string, int(idx)+1024)
			copy(ns, db.revIndex)
			db.revIndex = ns
		}
		db.revIndex[idx] = d.rec.ID
		db.metaLocs[idx] = d.loc
		toIndex = append(toIndex, pendingItem{vector: d.rec.Vector, id: d.rec.ID, idx: idx})
	}
	db.mu.Unlock()

	// Group commit: fsync outside db.mu lock
	if err := db.wal.FlushAsync(); err != nil {
		errs = append(errs, fmt.Errorf("wal flush: %w", err))
	}

	// Phase 2: enqueue for async HNSW indexing.
	if len(toIndex) > 0 {
		db.pendingMu.Lock()
		db.pendingVecs = append(db.pendingVecs, toIndex...)
		db.pendingMu.Unlock()
		db.signalIndexer()
	}

	return errs
}

// Get returns the vector and raw JSON metadata for the record with the given id.
// The third return value is false when the id is not found.
func (db *Levara) Get(id string) ([]float32, []byte, bool) {
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

// Search returns the topK nearest records to the query vector using HNSW beam search.
// Records not yet indexed (pending async HNSW insert) are covered by a brute-force fallback.
// Results are sorted by descending cosine similarity.
func (db *Levara) Search(query []float32, topK int) []VectroRecord {
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
func (db *Levara) Delete(id string) error {
	db.mu.Lock()

	idx, ok := db.index[id]
	if !ok {
		db.mu.Unlock()
		return fmt.Errorf("record %q not found", id)
	}

	// WAL: write delete entry (buffered, no flush yet)
	if err := db.wal.WriteEntryNoFlush(OpDelete, id, nil, nil, FileLocation{}); err != nil {
		db.mu.Unlock()
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

	// Release db.mu before fsync — group commit coalesces across goroutines.
	db.mu.Unlock()

	// Group commit: fsync outside db.mu lock
	if err := db.wal.FlushAsync(); err != nil {
		return fmt.Errorf("wal flush: %w", err)
	}

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
func (db *Levara) BatchDelete(ids []string) []error {
	var errs []error
	for _, id := range ids {
		if err := db.Delete(id); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}

// Close flushes WAL and disk store, ensuring all data is persisted.
// AllIDs returns all record IDs in the collection.
func (db *Levara) AllIDs() []string {
	db.mu.RLock()
	defer db.mu.RUnlock()
	ids := make([]string, 0, len(db.index))
	for id := range db.index {
		ids = append(ids, id)
	}
	return ids
}

// Count returns the number of records.
func (db *Levara) Count() int {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return len(db.index)
}

// SnapshotRecord holds one entry for Raft snapshot serialization.
type SnapshotRecord struct {
	ID     string          `json:"id"`
	Vector []float32       `json:"vector"`
	Data   json.RawMessage `json:"data"`
}

// AllRecords returns all records for Raft snapshot. Thread-safe.
func (db *Levara) AllRecords() []SnapshotRecord {
	db.mu.RLock()
	defer db.mu.RUnlock()
	records := make([]SnapshotRecord, 0, len(db.index))
	for id, idx := range db.index {
		vec, _ := db.arena.Get(idx)
		metaLoc := db.metaLocs[idx]
		meta, _ := db.disk.Read(metaLoc)
		vecCopy := make([]float32, len(vec))
		copy(vecCopy, vec)
		records = append(records, SnapshotRecord{
			ID:     id,
			Vector: vecCopy,
			Data:   json.RawMessage(meta),
		})
	}
	return records
}

// Clear removes all in-memory data. Used during Raft snapshot restore.
func (db *Levara) Clear() {
	db.mu.Lock()
	defer db.mu.Unlock()
	// Delete all existing records
	for id := range db.index {
		delete(db.index, id)
	}
	db.index = make(map[string]uint32)
	db.revIndex = make([]string, 0, 10000)
	db.metaLocs = make(map[uint32]FileLocation)
	db.arena = NewVectorArena(db.dim)
	db.hnsw = NewHNSWIndex(db.arena, db.hnswCfg)
}

func (db *Levara) Close() error {
	db.closeOnce.Do(func() {
		close(db.indexSignal)
	})
	if err := db.wal.Close(); err != nil {
		db.disk.Close()
		return fmt.Errorf("wal close: %w", err)
	}
	return db.disk.Close()
}

// Checkpoint compacts the WAL by writing only live records to a fresh file.
// This eliminates deleted entries and reduces recovery time.
// Must be called when no writes are in progress (holds db.mu for full duration).
func (db *Levara) Checkpoint() error {
	db.mu.Lock()
	defer db.mu.Unlock()

	walPath := db.wal.Path()
	tmpPath := walPath + ".compact"

	// Create new WAL file
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("checkpoint: create tmp: %w", err)
	}
	writer := bufio.NewWriter(tmpFile)

	// Write all live records
	count := 0
	for id, idx := range db.index {
		vec, err := db.arena.Get(idx)
		if err != nil || vec == nil {
			continue
		}
		metaLoc, ok := db.metaLocs[idx]
		if !ok {
			continue
		}
		meta, err := db.disk.Read(metaLoc)
		if err != nil {
			meta = []byte("{}")
		}

		// Write entry to tmp WAL (same binary format)
		if err := writeWALEntryTo(writer, OpInsert, id, vec, meta, metaLoc); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("checkpoint: write entry %s: %w", id, err)
		}
		count++
	}

	// Flush and sync
	if err := writer.Flush(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("checkpoint: flush: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("checkpoint: sync: %w", err)
	}
	tmpFile.Close()

	// Close current WAL
	db.wal.Close()

	// Atomic swap: rename tmp -> WAL
	if err := os.Rename(tmpPath, walPath); err != nil {
		return fmt.Errorf("checkpoint: rename: %w", err)
	}

	// Reopen WAL (starts fresh fsyncLoop)
	newWal, err := OpenWal(walPath)
	if err != nil {
		return fmt.Errorf("checkpoint: reopen: %w", err)
	}
	db.wal = newWal

	fmt.Printf("Checkpoint complete: %d live records written to compacted WAL\n", count)
	return nil
}
