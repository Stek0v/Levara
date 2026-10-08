package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const v3SnapshotVersion = 1

var errV3SnapshotMismatch = errors.New("v3 HNSW snapshot mismatch")

type v3SnapshotNode struct {
	ID          string     `json:"id"`
	Layer       int        `json:"layer"`
	Offset      uint32     `json:"offset"`
	Connections [][]uint32 `json:"connections"`
	Deleted     bool       `json:"deleted,omitempty"`
}

type v3SnapshotFile struct {
	Magic          string           `json:"magic"`
	Version        int              `json:"version"`
	Dimension      int              `json:"dimension"`
	Config         HNSWConfig       `json:"config"`
	WALPrefixBytes int64            `json:"wal_prefix_bytes"`
	WALPrefixSHA   string           `json:"wal_prefix_sha256"`
	EntryNodeID    string           `json:"entry_node_id"`
	EntryOffset    uint32           `json:"entry_offset"`
	MaxLayer       int              `json:"max_layer"`
	Nodes          []v3SnapshotNode `json:"nodes"`
	PayloadSHA     string           `json:"payload_sha256"`
}

type v3SnapshotWriteStats struct {
	SnapshotBytes int64
	PeakTempBytes int64
}

func writeV3HNSWSnapshot(db *Levara, snapshotPath string, walPrefix []byte) (v3SnapshotWriteStats, error) {
	if err := WalkWALStrict(context.Background(), bytes.NewReader(walPrefix), db.dim, int64(len(walPrefix)), func(byte, SnapshotRecord) error { return nil }); err != nil {
		return v3SnapshotWriteStats{}, fmt.Errorf("snapshot WAL prefix boundary: %w", err)
	}
	db.mu.RLock()
	db.pendingMu.RLock()
	if len(db.pendingVecs) != 0 {
		db.pendingMu.RUnlock()
		db.mu.RUnlock()
		return v3SnapshotWriteStats{}, errors.New("snapshot requires a fully indexed store")
	}
	h := db.hnsw
	h.RLock()
	file := v3SnapshotFile{
		Magic: "levara-hnsw", Version: v3SnapshotVersion, Dimension: db.dim,
		Config: db.hnswCfg, WALPrefixBytes: int64(len(walPrefix)),
		WALPrefixSHA: sha256String(walPrefix), EntryNodeID: h.EntryNodeID, MaxLayer: h.MaxLayer,
	}
	if entry := h.Nodes[h.EntryNodeID]; entry != nil {
		file.EntryOffset = entry.ArenaOffset
	}
	for offset, node := range h.nodesByIdx {
		if node == nil {
			continue
		}
		node.RLock()
		copyNode := v3SnapshotNode{ID: node.ID, Layer: node.Layer, Offset: uint32(offset), Deleted: h.isDeleted(uint32(offset))}
		copyNode.Connections = make([][]uint32, len(node.Connections))
		for layer := range node.Connections {
			copyNode.Connections[layer] = append([]uint32(nil), node.Connections[layer]...)
		}
		node.RUnlock()
		file.Nodes = append(file.Nodes, copyNode)
	}
	h.RUnlock()
	db.pendingMu.RUnlock()
	db.mu.RUnlock()
	payload, err := json.Marshal(file)
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	file.PayloadSHA = sha256String(payload)
	data, err := json.Marshal(file)
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	data = append(data, '\n')
	dir := filepath.Dir(snapshotPath)
	dirInfo, err := os.Lstat(dir)
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return v3SnapshotWriteStats{}, errors.New("snapshot directory must be a real directory")
	}
	if info, err := os.Lstat(snapshotPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return v3SnapshotWriteStats{}, errors.New("snapshot destination is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return v3SnapshotWriteStats{}, err
	}
	tmp, err := os.CreateTemp(dir, ".hnsw-snapshot-*")
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	if err := os.Rename(tmpName, snapshotPath); err != nil {
		return v3SnapshotWriteStats{}, err
	}
	directory, err := os.Open(dir)
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return v3SnapshotWriteStats{}, err
	}
	return v3SnapshotWriteStats{SnapshotBytes: int64(len(data)), PeakTempBytes: int64(len(data))}, nil
}

type v3WALNodeState struct {
	ID      string
	Deleted bool
}

func snapshotMismatch(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errV3SnapshotMismatch, fmt.Sprintf(format, args...))
}

func validateV3SnapshotEnvelope(snapshotPath string, dim int, cfg HNSWConfig, wal []byte, allowTail bool) (v3SnapshotFile, []v3WALNodeState, error) {
	info, err := os.Lstat(snapshotPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<30 {
		return v3SnapshotFile{}, nil, snapshotMismatch("invalid snapshot file")
	}
	data, err := os.ReadFile(snapshotPath)
	if err != nil {
		return v3SnapshotFile{}, nil, snapshotMismatch("read: %v", err)
	}
	var file v3SnapshotFile
	if err := json.Unmarshal(data, &file); err != nil {
		return v3SnapshotFile{}, nil, snapshotMismatch("decode: %v", err)
	}
	wantPayload := file.PayloadSHA
	file.PayloadSHA = ""
	payload, err := json.Marshal(file)
	if err != nil || file.Magic != "levara-hnsw" || file.Version != v3SnapshotVersion || file.Dimension != dim || file.Config != cfg || wantPayload != sha256String(payload) {
		return v3SnapshotFile{}, nil, snapshotMismatch("envelope, config, or payload digest")
	}
	file.PayloadSHA = wantPayload
	if file.WALPrefixBytes < 0 || file.WALPrefixBytes > int64(len(wal)) || !allowTail && file.WALPrefixBytes != int64(len(wal)) {
		return v3SnapshotFile{}, nil, snapshotMismatch("WAL prefix length")
	}
	prefix := wal[:file.WALPrefixBytes]
	if sha256String(prefix) != file.WALPrefixSHA {
		return v3SnapshotFile{}, nil, snapshotMismatch("WAL prefix digest")
	}
	states := make([]v3WALNodeState, 0, len(file.Nodes))
	active := make(map[string]uint32)
	err = WalkWALStrict(context.Background(), bytes.NewReader(prefix), dim, int64(len(prefix)), func(op byte, record SnapshotRecord) error {
		switch op {
		case OpInsert:
			if old, ok := active[record.ID]; ok {
				states[old].Deleted = true
			}
			offset := uint32(len(states))
			states = append(states, v3WALNodeState{ID: record.ID})
			active[record.ID] = offset
		case OpDelete:
			if old, ok := active[record.ID]; ok {
				states[old].Deleted = true
				delete(active, record.ID)
			}
		}
		return nil
	})
	if err != nil {
		return v3SnapshotFile{}, nil, snapshotMismatch("WAL prefix frame boundary: %v", err)
	}
	if len(file.Nodes) > len(states) {
		return v3SnapshotFile{}, nil, snapshotMismatch("graph node count %d exceeds WAL insert count %d", len(file.Nodes), len(states))
	}
	byOffset := make(map[uint32]v3SnapshotNode, len(file.Nodes))
	latestGraphOffset := make(map[string]uint32, len(file.Nodes))
	maxActiveLayer := -1
	for nodeIndex, node := range file.Nodes {
		if int(node.Offset) >= len(states) {
			return v3SnapshotFile{}, nil, snapshotMismatch("node offset out of range")
		}
		if nodeIndex > 0 && file.Nodes[nodeIndex-1].Offset >= node.Offset {
			return v3SnapshotFile{}, nil, snapshotMismatch("graph nodes are not in increasing WAL offset order")
		}
		if _, duplicate := byOffset[node.Offset]; duplicate {
			return v3SnapshotFile{}, nil, snapshotMismatch("duplicate node offset")
		}
		want := states[node.Offset]
		if node.ID != want.ID || node.Deleted != want.Deleted || node.Layer < 0 || len(node.Connections) != node.Layer+1 {
			return v3SnapshotFile{}, nil, snapshotMismatch("node identity, tombstone, or layer mismatch at offset %d", node.Offset)
		}
		if !node.Deleted && node.Layer > maxActiveLayer {
			maxActiveLayer = node.Layer
		}
		byOffset[node.Offset] = node
		latestGraphOffset[node.ID] = node.Offset
	}
	// The async indexer removes a pending insert when that ID is deleted or
	// replaced. Such historical WAL offsets may therefore never have been
	// published into HNSW. Every final active offset must be present, while an
	// omitted offset is valid only when the WAL-derived final state is deleted.
	for offset, state := range states {
		if !state.Deleted {
			if _, ok := byOffset[uint32(offset)]; !ok {
				return v3SnapshotFile{}, nil, snapshotMismatch("active WAL offset %d (%q) missing from graph", offset, state.ID)
			}
		}
	}
	if len(file.Nodes) == 0 {
		if file.EntryNodeID != "" || file.MaxLayer != -1 {
			return v3SnapshotFile{}, nil, snapshotMismatch("nonempty entry for empty graph")
		}
	} else {
		entry, ok := byOffset[file.EntryOffset]
		activeEntryOffset, activeEntry := active[file.EntryNodeID]
		mappedEntryOffset, mappedEntry := latestGraphOffset[file.EntryNodeID]
		if !ok || file.EntryNodeID == "" || entry.ID != file.EntryNodeID || !mappedEntry || mappedEntryOffset != file.EntryOffset || entry.Layer != file.MaxLayer {
			return v3SnapshotFile{}, nil, snapshotMismatch("entry node mismatch")
		}
		if entry.Deleted && activeEntry || !entry.Deleted && (!activeEntry || activeEntryOffset != file.EntryOffset) {
			return v3SnapshotFile{}, nil, snapshotMismatch("entry node does not match final WAL state")
		}
		if maxActiveLayer > file.MaxLayer {
			return v3SnapshotFile{}, nil, snapshotMismatch("active layer %d exceeds entry max layer %d", maxActiveLayer, file.MaxLayer)
		}
	}
	for _, node := range file.Nodes {
		for layerIndex, layer := range node.Connections {
			degreeLimit := cfg.M
			if layerIndex == 0 {
				degreeLimit = cfg.M0
			}
			if len(layer) > degreeLimit {
				return v3SnapshotFile{}, nil, snapshotMismatch("degree exceeds config")
			}
			targets := make(map[uint32]struct{}, len(layer))
			for _, target := range layer {
				targetNode, ok := byOffset[target]
				if !ok || target == node.Offset || targetNode.Layer < layerIndex {
					return v3SnapshotFile{}, nil, snapshotMismatch("invalid layer target")
				}
				if _, duplicate := targets[target]; duplicate {
					return v3SnapshotFile{}, nil, snapshotMismatch("duplicate layer edge")
				}
				targets[target] = struct{}{}
			}
		}
	}
	return file, states, nil
}

func buildV3HNSWFromValidatedSnapshot(file v3SnapshotFile, states []v3WALNodeState, arena *VectorArena, cfg HNSWConfig) (*HNSWIndex, error) {
	if arena.Size() != len(states) {
		return nil, snapshotMismatch("arena size %d != graph size %d", arena.Size(), len(states))
	}
	h := NewHNSWIndex(arena, cfg)
	for _, saved := range file.Nodes {
		node := &HNSWNode{ID: saved.ID, Layer: saved.Layer, ArenaOffset: saved.Offset, Connections: saved.Connections}
		h.Nodes[saved.ID] = node
		h.registerNode(node)
		if saved.Deleted {
			h.deletedSet.Store(saved.Offset, struct{}{})
		}
	}
	h.EntryNodeID, h.MaxLayer = file.EntryNodeID, file.MaxLayer
	return h, nil
}

func loadV3HNSWSnapshot(snapshotPath string, arena *VectorArena, dim int, cfg HNSWConfig, wal []byte, allowTail bool) (*HNSWIndex, int64, error) {
	file, states, err := validateV3SnapshotEnvelope(snapshotPath, dim, cfg, wal, allowTail)
	if err != nil {
		return nil, 0, err
	}
	h, err := buildV3HNSWFromValidatedSnapshot(file, states, arena, cfg)
	return h, file.WALPrefixBytes, err
}

func openV3SnapshotLevara(dim int, storagePath, snapshotPath string, allowTail bool, configs ...HNSWConfig) (*Levara, error) {
	cfg := DefaultHNSWConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	walBytes, err := os.ReadFile(storagePath + ".wal")
	if err != nil {
		return nil, err
	}
	file, states, err := validateV3SnapshotEnvelope(snapshotPath, dim, cfg, walBytes, allowTail)
	if err != nil {
		return nil, err
	}
	wal, err := OpenWal(storagePath + ".wal")
	if err != nil {
		return nil, err
	}
	failWAL := func(err error) (*Levara, error) { _ = wal.Close(); return nil, err }
	if err := wal.prepareRecovery(dim); err != nil {
		return failWAL(err)
	}
	walBytes, err = os.ReadFile(storagePath + ".wal")
	if err != nil {
		return failWAL(err)
	}
	file, states, err = validateV3SnapshotEnvelope(snapshotPath, dim, cfg, walBytes, allowTail)
	if err != nil {
		return failWAL(err)
	}
	ds, err := NewDiskStore(storagePath)
	if err != nil {
		return failWAL(err)
	}
	fail := func(err error) (*Levara, error) { _ = wal.Close(); _ = ds.Close(); return nil, err }
	if err := ds.Truncate(); err != nil {
		return fail(err)
	}
	db := &Levara{index: map[string]uint32{}, revIndex: make([]string, 0, 10000), arena: NewVectorArena(dim), metaLocs: map[uint32]FileLocation{}, disk: ds, dim: dim, wal: wal, hnswCfg: cfg, indexSignal: make(chan struct{}, 1)}
	var replayErr error
	prefixLength := file.WALPrefixBytes
	apply := func(op byte, record SnapshotRecord, graph bool) error {
		switch op {
		case OpInsert:
			loc, err := ds.Write(record.Data)
			if err != nil {
				return err
			}
			if graph {
				return db.insertInMemory(record.ID, record.Vector, loc)
			}
			idx, err := db.arena.Add(record.Vector)
			if err != nil {
				return err
			}
			if old, ok := db.index[record.ID]; ok {
				db.revIndex[old] = ""
				delete(db.metaLocs, old)
			}
			db.index[record.ID] = idx
			for int(idx) >= len(db.revIndex) {
				db.revIndex = append(db.revIndex, make([]string, 1024)...)
			}
			db.revIndex[idx], db.metaLocs[idx] = record.ID, loc
		case OpDelete:
			if idx, ok := db.index[record.ID]; ok {
				delete(db.index, record.ID)
				db.revIndex[idx] = ""
				delete(db.metaLocs, idx)
				if graph {
					db.hnsw.MarkDeleted(idx)
				}
			}
		}
		return nil
	}
	if err := WalkWALStrict(context.Background(), bytes.NewReader(walBytes[:prefixLength]), dim, prefixLength, func(op byte, record SnapshotRecord) error { return apply(op, record, false) }); err != nil {
		return fail(err)
	}
	snapshot, err := buildV3HNSWFromValidatedSnapshot(file, states, db.arena, cfg)
	if err != nil {
		return fail(err)
	}
	db.hnsw = snapshot
	prefixBytes := file.WALPrefixBytes
	if prefixBytes < int64(len(walBytes)) {
		tail := walBytes[prefixBytes:]
		if err := WalkWALStrict(context.Background(), bytes.NewReader(tail), dim, int64(len(tail)), func(op byte, record SnapshotRecord) error {
			if replayErr != nil {
				return replayErr
			}
			replayErr = apply(op, record, true)
			return replayErr
		}); err != nil {
			return fail(err)
		}
	}
	db.walCheckpointBaseline = int64(len(walBytes))
	go db.indexerLoop()
	return db, nil
}

func openV3SnapshotOrRebuild(dim int, storagePath, snapshotPath string, allowTail bool, configs ...HNSWConfig) (*Levara, bool, error) {
	db, err := openV3SnapshotLevara(dim, storagePath, snapshotPath, allowTail, configs...)
	if err == nil {
		return db, true, nil
	}
	if !errors.Is(err, errV3SnapshotMismatch) {
		return nil, false, err
	}
	cfg := DefaultHNSWConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	db, err = NewLevara(dim, storagePath, cfg)
	return db, false, err
}

func sha256String(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
