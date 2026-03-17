package store

import (
	"encoding/json"
	"fmt"
	"sync"
)

type VectroRecord struct {
	ID    string
	Score float32
	Data  json.RawMessage
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
		index:    make(map[string]uint32),
		revIndex: make([]string, 0, 10000),
		arena:    localArena,
		metaLocs: make(map[uint32]FileLocation),
		disk:     ds,
		dim:      dim,
		wal:      wal,
		hnsw:     NewHNSWIndex(localArena),
	}

	fmt.Println("Replaying WAL to restore data....")
	count := 0
	err = wal.Recover(func(id string, vector []float32, meta []byte, loc FileLocation) {
		db.insertInMemory(id, vector, loc)
		count++
	})
	fmt.Printf("Recovered %d records from WAL\n", count)

	return db, nil
}

func (db *VectraDB) Insert(id string, vector []float32, data any) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	bytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("Failed to marshal metadata: %w", err)
	}

	idx, err := db.arena.Add(vector)
	if err != nil {
		return err
	}

	loc, err := db.disk.Write(bytes)
	if err != nil {
		return err
	}

	if err := db.wal.WriteEntry(OpInsert, id, vector, bytes, loc); err != nil {
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

	db.hnsw.Add(vector, id, idx)

	return nil
}

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
	db.mu.Lock()
	defer db.mu.Unlock()
	var errs []error
	for _, rec := range records {
		bytes, err := json.Marshal(rec.Data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: marshal: %w", rec.ID, err))
			continue
		}
		idx, err := db.arena.Add(rec.Vector)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: arena: %w", rec.ID, err))
			continue
		}
		loc, err := db.disk.Write(bytes)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: disk: %w", rec.ID, err))
			continue
		}
		if err := db.wal.WriteEntryNoFlush(OpInsert, rec.ID, rec.Vector, bytes, loc); err != nil {
			errs = append(errs, fmt.Errorf("%s: wal: %w", rec.ID, err))
			continue
		}
		db.index[rec.ID] = idx
		if int(idx) >= len(db.revIndex) {
			ns := make([]string, int(idx)+1024)
			copy(ns, db.revIndex)
			db.revIndex = ns
		}
		db.revIndex[idx] = rec.ID
		db.metaLocs[idx] = loc
		db.hnsw.Add(rec.Vector, rec.ID, idx)
	}
	if err := db.wal.Flush(); err != nil {
		errs = append(errs, fmt.Errorf("wal flush: %w", err))
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
	db.mu.RLock()
	defer db.mu.RUnlock()

	records := db.hnsw.Search(query, topK)

	// Enrich results with metadata from disk
	for i, rec := range records {
		if idx, ok := db.index[rec.ID]; ok {
			if loc, ok := db.metaLocs[idx]; ok {
				if meta, err := db.disk.Read(loc); err == nil {
					records[i].Data = meta
				}
			}
		}
	}

	return records
}
