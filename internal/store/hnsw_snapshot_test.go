package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestV3HNSWSnapshotExactTailAndFallback(t *testing.T) {
	const dim = 4
	root := t.TempDir()
	path := filepath.Join(root, "meta.bin")
	snapshotPath := filepath.Join(root, "hnsw.snapshot")
	db, err := NewLevara(dim, path, DefaultHNSWConfig())
	if err != nil {
		t.Fatal(err)
	}
	vectors := map[string][]float32{
		"first": {1, 0, 0, 0}, "second": {0, 1, 0, 0}, "third": {0, 0, 1, 0},
	}
	for _, id := range []string{"first", "second", "third"} {
		if err := db.Insert(id, vectors[id], map[string]string{"id": id}); err != nil {
			t.Fatal(err)
		}
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	keepID := db.hnsw.EntryNodeID
	var goneID, replaceID string
	for _, id := range []string{"first", "second", "third"} {
		if id == keepID {
			continue
		}
		if goneID == "" {
			goneID = id
		} else {
			replaceID = id
		}
	}
	if keepID == "" || goneID == "" || replaceID == "" {
		t.Fatalf("invalid entry fixture: keep=%q gone=%q replace=%q", keepID, goneID, replaceID)
	}
	if err := db.Delete(goneID); err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(replaceID); err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(replaceID, []float32{0, 0, 0, 1}, map[string]string{"id": "replace-v2"}); err != nil {
		t.Fatal(err)
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	prefix, err := os.ReadFile(path + ".wal")
	if err != nil {
		t.Fatal(err)
	}
	stats, err := writeV3HNSWSnapshot(db, snapshotPath, prefix)
	if err != nil || stats.SnapshotBytes <= 0 || stats.PeakTempBytes != stats.SnapshotBytes {
		t.Fatalf("snapshot write: stats=%+v err=%v", stats, err)
	}
	secondSnapshot := filepath.Join(root, "hnsw-second.snapshot")
	if _, err := writeV3HNSWSnapshot(db, secondSnapshot, prefix); err != nil {
		t.Fatal(err)
	}
	firstBytes, _ := os.ReadFile(snapshotPath)
	secondBytes, _ := os.ReadFile(secondSnapshot)
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("same graph and WAL prefix produced a nondeterministic snapshot")
	}
	// Exactness is against the snapshotted graph, including its randomized
	// topology and float32 scores; an independent rebuild is not the oracle.
	want := resultDigest(db.Search(vectors[keepID], 10))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	exact, err := openV3SnapshotLevara(dim, path, snapshotPath, false)
	if err != nil {
		t.Fatalf("valid exact snapshot rejected before fallback: %v", err)
	}
	if got := resultDigest(exact.Search(vectors[keepID], 10)); got != want {
		t.Fatalf("snapshot result digest changed: got %s want %s", got, want)
	}
	if exact.Count() != 2 {
		t.Fatalf("exact count=%d want 2", exact.Count())
	}
	_ = exact.Close()

	tailDB, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := tailDB.Insert("tail", []float32{-1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tailDB.Delete(keepID); err != nil {
		t.Fatal(err)
	}
	if err := tailDB.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openV3SnapshotLevara(dim, path, snapshotPath, false); !errors.Is(err, errV3SnapshotMismatch) {
		t.Fatalf("exact mode accepted WAL tail: %v", err)
	}
	tailed, err := openV3SnapshotLevara(dim, path, snapshotPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if tailed.Count() != 2 {
		t.Fatalf("tail count=%d want 2", tailed.Count())
	}
	if _, _, ok := tailed.Get("tail"); !ok {
		t.Fatal("ordered suffix insert missing")
	}
	if _, _, ok := tailed.Get(keepID); ok {
		t.Fatal("ordered suffix delete missing")
	}
	_ = tailed.Close()

	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	fallback, used, err := openV3SnapshotOrRebuild(dim, path, snapshotPath, true)
	if err != nil || used || fallback.Count() != 2 {
		t.Fatalf("corrupt snapshot fallback: used=%v count=%d err=%v", used, fallback.Count(), err)
	}
	_ = fallback.Close()
}

func TestV3HNSWSnapshotRejectsMismatchAndRepairsIncompleteTail(t *testing.T) {
	const dim = 4
	root := t.TempDir()
	path := filepath.Join(root, "meta.bin")
	snapshotPath := filepath.Join(root, "hnsw.snapshot")
	db, err := NewLevara(dim, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Insert("one", []float32{1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	prefix, _ := os.ReadFile(path + ".wal")
	if _, err := writeV3HNSWSnapshot(db, snapshotPath, prefix); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, _, err := openV3SnapshotOrRebuild(2, path, snapshotPath, false); err == nil {
		t.Fatal("wrong dimension accepted")
	}
	wrongConfig := DefaultHNSWConfig()
	wrongConfig.EfSearchMin++
	arena := NewVectorArena(dim)
	for range 1 {
		_, _ = arena.Add([]float32{1, 0, 0, 0})
	}
	if _, _, err := loadV3HNSWSnapshot(snapshotPath, arena, dim, wrongConfig, prefix, false); !errors.Is(err, errV3SnapshotMismatch) {
		t.Fatalf("wrong config accepted: %v", err)
	}
	wrongConstruction := DefaultHNSWConfig()
	wrongConstruction.EfConstruction = 128
	if _, _, err := loadV3HNSWSnapshot(snapshotPath, arena, dim, wrongConstruction, prefix, false); !errors.Is(err, errV3SnapshotMismatch) {
		t.Fatalf("wrong efConstruction accepted: %v", err)
	}
	symlinkPath := filepath.Join(root, "snapshot-link")
	if err := os.Symlink(snapshotPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, err := writeV3HNSWSnapshot(db, symlinkPath, prefix); err == nil {
		t.Fatal("snapshot publisher accepted symlink destination")
	}
	realDir := filepath.Join(root, "real-snapshot-dir")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkDir := filepath.Join(root, "snapshot-dir-link")
	if err := os.Symlink(realDir, symlinkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := writeV3HNSWSnapshot(db, filepath.Join(symlinkDir, "snapshot"), prefix); err == nil {
		t.Fatal("snapshot publisher accepted symlink directory")
	}

	frame := startupTestFrame(t, OpInsert, "tail", []float32{0, 1, 0, 0}, nil, FileLocation{})
	wal := append(append([]byte(nil), prefix...), frame...)
	wal = append(wal, frame[:2]...)
	if err := os.WriteFile(path+".wal", wal, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := openV3SnapshotLevara(dim, path, snapshotPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.Close()
	if loaded.Count() != 2 {
		t.Fatalf("incomplete-tail repair count=%d want 2", loaded.Count())
	}
	repaired, _ := os.ReadFile(path + ".wal")
	if !bytes.Equal(repaired, wal[:len(wal)-2]) {
		t.Fatal("incomplete tail was not repaired to the exact frame boundary")
	}
}

func TestV3HNSWSnapshotChecksumValidMutationsFallback(t *testing.T) {
	const dim = 4
	cfg := HNSWConfig{M: 2, M0: 4, EfSearchMult: 20, EfSearchMin: 100, LevelMult: DefaultHNSWConfig().LevelMult}
	root := t.TempDir()
	path := filepath.Join(root, "meta.bin")
	snapshotPath := filepath.Join(root, "hnsw.snapshot")
	db, err := NewLevara(dim, path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		vector := []float32{float32(i + 1), float32(12 - i), 1, -1}
		if err := db.Insert(snapshotRecordID(i), vector, nil); err != nil {
			t.Fatal(err)
		}
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	if err := db.Delete(snapshotRecordID(2)); err != nil {
		t.Fatal(err)
	}
	if err := db.Insert(snapshotRecordID(2), []float32{-1, 0, 0, 0}, nil); err != nil {
		t.Fatal(err)
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	walBytes, _ := os.ReadFile(path + ".wal")
	if _, err := writeV3HNSWSnapshot(db, snapshotPath, walBytes); err != nil {
		t.Fatal(err)
	}
	wantCount := db.Count()
	wantRecords, err := db.AllRecordsChecked()
	if err != nil {
		t.Fatal(err)
	}
	wantQuery, _, ok := db.Get(snapshotRecordID(0))
	if !ok {
		t.Fatal("query record missing")
	}
	wantSelf := db.Search(wantQuery, 1)
	if len(wantSelf) != 1 || wantSelf[0].ID != snapshotRecordID(0) {
		t.Fatal("source self-query failed")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	originalSnapshot, _ := os.ReadFile(snapshotPath)

	mutations := map[string]func(*v3SnapshotFile){
		"swapped_ids": func(file *v3SnapshotFile) {
			file.Nodes[0].ID, file.Nodes[1].ID = file.Nodes[1].ID, file.Nodes[0].ID
		},
		"omitted_node": func(file *v3SnapshotFile) {
			for i, node := range file.Nodes {
				if !node.Deleted {
					file.Nodes = append(file.Nodes[:i], file.Nodes[i+1:]...)
					return
				}
			}
			t.Fatal("fixture has no active node to omit")
		},
		"forged_tombstone": func(file *v3SnapshotFile) { file.Nodes[0].Deleted = !file.Nodes[0].Deleted },
		"impossible_max":   func(file *v3SnapshotFile) { file.MaxLayer++ },
		"invalid_layer":    func(file *v3SnapshotFile) { file.Nodes[0].Layer++ },
		"duplicate_edge": func(file *v3SnapshotFile) {
			file.Nodes[0].Connections[0] = []uint32{file.Nodes[1].Offset, file.Nodes[1].Offset}
		},
		"self_edge": func(file *v3SnapshotFile) { file.Nodes[0].Connections[0] = []uint32{file.Nodes[0].Offset} },
		"excessive_degree": func(file *v3SnapshotFile) {
			file.Nodes[0].Connections[0] = []uint32{1, 2, 3, 4, 5}
		},
		"lower_layer_entry_substitution": func(file *v3SnapshotFile) {
			activeNodes := make([]int, 0, 2)
			for i := range file.Nodes {
				if !file.Nodes[i].Deleted {
					activeNodes = append(activeNodes, i)
					if len(activeNodes) == 2 {
						break
					}
				}
			}
			if len(activeNodes) != 2 {
				t.Fatal("fixture needs two active nodes for entry substitution")
			}
			low, high := &file.Nodes[activeNodes[0]], &file.Nodes[activeNodes[1]]
			low.Layer, low.Connections = 0, low.Connections[:1]
			if high.Layer == 0 {
				high.Layer = 1
				high.Connections = append(high.Connections, nil)
			}
			file.EntryNodeID, file.EntryOffset, file.MaxLayer = low.ID, low.Offset, low.Layer
		},
		"historical_duplicate_id_entry_substitution": func(file *v3SnapshotFile) {
			activeOffsets := make(map[string]uint32)
			for _, node := range file.Nodes {
				if !node.Deleted {
					activeOffsets[node.ID] = node.Offset
				}
			}
			for _, node := range file.Nodes {
				if latest, ok := activeOffsets[node.ID]; node.Deleted && ok && latest != node.Offset {
					file.EntryNodeID, file.EntryOffset, file.MaxLayer = node.ID, node.Offset, node.Layer
					return
				}
			}
			t.Fatal("fixture has no historical duplicate ID")
		},
		"missing_entry_offset": func(file *v3SnapshotFile) {
			file.EntryOffset = uint32(len(file.Nodes) + len(walBytes))
		},
		"wrong_entry_offset": func(file *v3SnapshotFile) {
			for _, node := range file.Nodes {
				if node.ID != file.EntryNodeID {
					file.EntryOffset = node.Offset
					return
				}
			}
			t.Fatal("fixture has no alternate entry offset")
		},
		"mid_frame_prefix": func(file *v3SnapshotFile) {
			file.WALPrefixBytes -= 2
			file.WALPrefixSHA = sha256String(walBytes[:file.WALPrefixBytes])
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var file v3SnapshotFile
			if err := json.Unmarshal(originalSnapshot, &file); err != nil {
				t.Fatal(err)
			}
			mutate(&file)
			writeV3SnapshotEnvelope(t, snapshotPath, file)
			metaBefore, _ := os.ReadFile(path)
			if opened, err := openV3SnapshotLevara(dim, path, snapshotPath, false, cfg); opened != nil || !errors.Is(err, errV3SnapshotMismatch) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("mutation did not fail as snapshot mismatch: %v", err)
			}
			metaAfter, _ := os.ReadFile(path)
			if !bytes.Equal(metaBefore, metaAfter) {
				t.Fatal("invalid snapshot changed metadata before fallback")
			}
			fallback, used, err := openV3SnapshotOrRebuild(dim, path, snapshotPath, false, cfg)
			if err != nil || used || fallback.Count() != wantCount {
				t.Fatalf("fallback used=%v count=%d err=%v", used, fallback.Count(), err)
			}
			for _, want := range wantRecords {
				vector, metadata, ok := fallback.Get(want.ID)
				if !ok || !float32SlicesEqual(vector, want.Vector) || !bytes.Equal(metadata, want.Data) {
					t.Fatalf("fallback state mismatch for %s", want.ID)
				}
			}
			results := fallback.Search(wantQuery, 1)
			if len(results) != 1 || results[0].ID != snapshotRecordID(0) || math.Float32bits(results[0].Score) != math.Float32bits(wantSelf[0].Score) {
				t.Fatalf("fallback exact self-query result=%v", results)
			}
			_ = fallback.Close()
		})
	}
}

func TestV3HNSWSnapshotDeletedRuntimeEntryParity(t *testing.T) {
	const dim = 4
	tests := []struct {
		name   string
		mutate func(t *testing.T, db *Levara, ids []string, entryID string)
	}{
		{
			name: "delete_actual_entry",
			mutate: func(t *testing.T, db *Levara, _ []string, entryID string) {
				if err := db.Delete(entryID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "delete_all_records",
			mutate: func(t *testing.T, db *Levara, ids []string, _ string) {
				for _, id := range ids {
					if err := db.Delete(id); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "delete_reinsert_actual_entry_id",
			mutate: func(t *testing.T, db *Levara, _ []string, entryID string) {
				if err := db.Delete(entryID); err != nil {
					t.Fatal(err)
				}
				if err := db.Insert(entryID, []float32{-1, 0, 0, 0}, map[string]string{"version": "reinserted"}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "meta.bin")
			snapshotPath := filepath.Join(root, hnswSnapshotFilename)
			db, err := NewLevara(dim, path)
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{"one", "two", "three", "four", "five", "six"}
			for i, id := range ids {
				vector := []float32{float32(i + 1), float32(len(ids) - i), float32(i%2 + 1), 1}
				if err := db.Insert(id, vector, map[string]string{"id": id}); err != nil {
					t.Fatal(err)
				}
			}
			waitSnapshotIndexReady(t, db, 10*time.Second)
			entryID := db.hnsw.EntryNodeID
			if entryID == "" {
				t.Fatal("fixture has no runtime entry")
			}
			tc.mutate(t, db, ids, entryID)
			waitSnapshotIndexReady(t, db, 10*time.Second)
			queries := [][]float32{{1, 0, 0, 0}, {0, 1, 0, 0}, {-1, 0, 0, 0}}
			wantDigests := make([]string, len(queries))
			for i, query := range queries {
				wantDigests[i] = resultDigest(db.Search(query, 10))
			}
			wantCount := db.Count()
			wantEntryID, wantMaxLayer := db.hnsw.EntryNodeID, db.hnsw.MaxLayer
			walBytes, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeV3HNSWSnapshot(db, snapshotPath, walBytes); err != nil {
				t.Fatal(err)
			}
			var envelope v3SnapshotFile
			snapshotBytes, err := os.ReadFile(snapshotPath)
			if err != nil {
				t.Fatalf("read snapshot envelope: %v", err)
			}
			if err := json.Unmarshal(snapshotBytes, &envelope); err != nil {
				t.Fatalf("decode snapshot envelope: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			loaded, err := openV3SnapshotLevara(dim, path, snapshotPath, false)
			if err != nil {
				t.Fatalf("valid runtime entry snapshot rejected: %v", err)
			}
			defer loaded.Close()
			entry := loaded.hnsw.Nodes[loaded.hnsw.EntryNodeID]
			if loaded.Count() != wantCount || loaded.hnsw.EntryNodeID != wantEntryID || loaded.hnsw.MaxLayer != wantMaxLayer || entry == nil || entry.ArenaOffset != envelope.EntryOffset {
				t.Fatalf("entry/count parity changed: count=%d/%d entry=%q/%q layer=%d/%d offset=%v/%d", loaded.Count(), wantCount, loaded.hnsw.EntryNodeID, wantEntryID, loaded.hnsw.MaxLayer, wantMaxLayer, entry, envelope.EntryOffset)
			}
			for i, query := range queries {
				if got := resultDigest(loaded.Search(query, 10)); got != wantDigests[i] {
					t.Fatalf("query %d digest changed: got %s want %s", i, got, wantDigests[i])
				}
			}
		})
	}
}

func TestV3HNSWSnapshotAllowsDeletedWALInsertNeverPublished(t *testing.T) {
	const dim = 2
	cfg := DefaultHNSWConfig()
	root := t.TempDir()
	snapshotPath := filepath.Join(root, "hnsw.snapshot")
	walBytes := append([]byte(nil), startupTestFrame(t, OpInsert, "deleted-before-index", []float32{1, 0}, nil, FileLocation{})...)
	walBytes = append(walBytes, startupTestFrame(t, OpDelete, "deleted-before-index", nil, nil, FileLocation{})...)
	walBytes = append(walBytes, startupTestFrame(t, OpInsert, "active", []float32{0, 1}, nil, FileLocation{})...)

	file := v3SnapshotFile{
		Magic:          "levara-hnsw",
		Version:        v3SnapshotVersion,
		Dimension:      dim,
		Config:         cfg,
		WALPrefixBytes: int64(len(walBytes)),
		WALPrefixSHA:   sha256String(walBytes),
		EntryNodeID:    "active",
		EntryOffset:    1,
		MaxLayer:       0,
		Nodes: []v3SnapshotNode{{
			ID: "active", Layer: 0, Offset: 1, Connections: [][]uint32{{}},
		}},
	}
	writeV3SnapshotEnvelope(t, snapshotPath, file)
	arena := NewVectorArena(dim)
	if _, err := arena.Add([]float32{1, 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := arena.Add([]float32{0, 1}); err != nil {
		t.Fatal(err)
	}
	loaded, prefixBytes, err := loadV3HNSWSnapshot(snapshotPath, arena, dim, cfg, walBytes, false)
	if err != nil {
		t.Fatalf("delete-before-index snapshot rejected: %v", err)
	}
	if prefixBytes != int64(len(walBytes)) || loaded.nodeByOffset(0) != nil || loaded.nodeByOffset(1) == nil {
		t.Fatalf("loaded graph mismatch: prefix=%d deleted=%v active=%v", prefixBytes, loaded.nodeByOffset(0), loaded.nodeByOffset(1))
	}
}

func TestV3HNSWSnapshotEmptyAndZeroVectorParity(t *testing.T) {
	const dim = 4
	for _, tc := range []struct {
		name   string
		insert bool
	}{
		{name: "empty"},
		{name: "zero_vector", insert: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "meta.bin")
			snapshotPath := filepath.Join(root, hnswSnapshotFilename)
			db, err := NewLevara(dim, path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.insert {
				if err := db.Insert("zero", make([]float32, dim), map[string]string{"kind": "zero"}); err != nil {
					t.Fatal(err)
				}
				waitSnapshotIndexReady(t, db, 10*time.Second)
			}
			want := resultDigest(db.Search(make([]float32, dim), 10))
			walBytes, err := os.ReadFile(path + ".wal")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writeV3HNSWSnapshot(db, snapshotPath, walBytes); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			loaded, err := openV3SnapshotLevara(dim, path, snapshotPath, false)
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			wantCount := 0
			if tc.insert {
				wantCount = 1
				vector, _, ok := loaded.Get("zero")
				if !ok || !float32SlicesEqual(vector, make([]float32, dim)) {
					t.Fatalf("zero vector changed: vector=%v ok=%v", vector, ok)
				}
			}
			if loaded.Count() != wantCount || resultDigest(loaded.Search(make([]float32, dim), 10)) != want {
				t.Fatal("empty/zero-vector snapshot parity changed")
			}
		})
	}
}

func TestCollectionManagerHNSWSnapshotOptInAndPublicationFailure(t *testing.T) {
	const dim = 4
	root := t.TempDir()
	seed, err := NewCollectionManager(dim, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Create("selected"); err != nil {
		t.Fatal(err)
	}
	if err := seed.Insert("selected", "first", []float32{1, 0, 0, 0}, map[string]string{"v": "first"}); err != nil {
		t.Fatal(err)
	}
	seedDB, err := seed.Get("selected")
	if err != nil {
		t.Fatal(err)
	}
	waitSnapshotIndexReady(t, seedDB, 10*time.Second)
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	snapshotPath := filepath.Join(root, "collections", "selected", hnswSnapshotFilename)
	if _, err := os.Lstat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default manager published snapshot: %v", err)
	}

	options := CollectionManagerOptions{HNSWSnapshots: true}
	firstOptIn, err := NewCollectionManagerWithOptions(dim, root, options)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(snapshotPath); err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("first opt-in rebuild did not publish regular snapshot: info=%v err=%v", info, err)
	}
	db, err := firstOptIn.Get("selected")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Insert("tail", []float32{0, 1, 0, 0}, map[string]string{"v": "tail"}); err != nil {
		t.Fatal(err)
	}
	waitSnapshotIndexReady(t, db, 10*time.Second)
	want := resultDigest(db.Search([]float32{0, 1, 0, 0}, 10))
	if err := firstOptIn.Close(); err != nil {
		t.Fatal(err)
	}

	tailRestart, err := NewCollectionManagerWithOptions(dim, root, options)
	if err != nil {
		t.Fatal(err)
	}
	tailDB, err := tailRestart.Get("selected")
	if err != nil {
		t.Fatal(err)
	}
	if tailDB.Count() != 2 || resultDigest(tailDB.Search([]float32{0, 1, 0, 0}, 10)) != want {
		t.Fatal("snapshot plus ordered WAL tail changed records or exact search results")
	}
	if err := tailRestart.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(snapshotPath, 0o700); err != nil {
		t.Fatal(err)
	}
	publicationFailure, err := NewCollectionManagerWithOptions(dim, root, options)
	if err != nil {
		t.Fatalf("snapshot publication failure made authoritative WAL unavailable: %v", err)
	}
	degradedDB, err := publicationFailure.Get("selected")
	if err != nil {
		t.Fatal(err)
	}
	if degradedDB.Count() != 2 {
		t.Fatalf("publication failure fallback count=%d want 2", degradedDB.Count())
	}
	if err := publicationFailure.Close(); err != nil {
		t.Fatal(err)
	}
}

func float32SlicesEqual(a, b []float32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float32bits(a[i]) != math.Float32bits(b[i]) {
			return false
		}
	}
	return true
}

func writeV3SnapshotEnvelope(t *testing.T, path string, file v3SnapshotFile) {
	t.Helper()
	file.PayloadSHA = ""
	payload, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	file.PayloadSHA = sha256String(payload)
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resultDigest(results []VectroRecord) string {
	var data []byte
	for _, result := range results {
		data = append(data, result.ID...)
		data = append(data, 0)
		var score [4]byte
		binary.LittleEndian.PutUint32(score[:], math.Float32bits(result.Score))
		data = append(data, score[:]...)
	}
	return sha256String(data)
}

func waitSnapshotIndexReady(t *testing.T, db *Levara, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		db.pendingMu.RLock()
		pending := len(db.pendingVecs)
		db.pendingMu.RUnlock()
		if pending == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for HNSW indexer")
}

func snapshotRecordID(i int) string { return fmt.Sprintf("record-%09d", i) }
