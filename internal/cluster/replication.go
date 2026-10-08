// Package cluster provides WAL-based replication for Levara multi-node deployments.
//
// Architecture:
//
//	Primary: accepts writes → WAL fsync → streams WAL entries to replicas
//	Replica: receives WAL stream → replays entries to local DB
//
// Communication uses HTTP streaming (SSE-like) to avoid proto regeneration.
// Each WAL entry is sent as a JSON line over a long-lived HTTP connection.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stek0v/levara/internal/store"
)

// WALEntry is one replicated entry sent from primary to replica.
type WALEntry struct {
	Op       byte            `json:"op"` // store.OpInsert or store.OpDelete
	ID       string          `json:"id"`
	Vector   []float32       `json:"vector,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	Seq      uint64          `json:"seq"` // monotonic sequence number
}

// ReplicationServer streams WAL entries to replicas.
//
// Snapshot admission and DirectNode mutation/fanout share mu.
type ReplicationServer struct {
	mu          sync.RWMutex
	wal         *store.WAL
	db          *store.Levara
	listeners   map[string]chan WALEntry // replicaID → entry channel
	seq         atomic.Uint64
	nodeID      string
	role        string // "primary" or "replica"
	primaryAddr string // for replicas: address of primary
}

// NewReplicationServer creates a new replication server.
func NewReplicationServer(nodeID string, wal *store.WAL, db *store.Levara) *ReplicationServer {
	return &ReplicationServer{
		wal:       wal,
		db:        db,
		listeners: make(map[string]chan WALEntry),
		nodeID:    nodeID,
		role:      "primary",
	}
}

// Role returns current node role.
func (rs *ReplicationServer) Role() string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.role
}

// SetRole sets node role (primary/replica).
func (rs *ReplicationServer) SetRole(role string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.role = role
}

// PrimaryAddr returns the primary's address (for replicas).
func (rs *ReplicationServer) PrimaryAddr() string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.primaryAddr
}

// SetPrimaryAddr sets the primary's address.
func (rs *ReplicationServer) SetPrimaryAddr(addr string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.primaryAddr = addr
}

// Broadcast sends a WAL entry to all connected replicas.
func (rs *ReplicationServer) Broadcast(entry WALEntry) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.broadcastLocked(entry)
}
func (rs *ReplicationServer) broadcastLocked(entry WALEntry) {
	entry.Seq = rs.seq.Add(1)
	for rid, ch := range rs.listeners {
		select {
		case ch <- entry:
		default:
			close(ch)
			delete(rs.listeners, rid)
			log.Printf("[replication] replica %s overflow; fresh snapshot required", rid)
		}
	}
}
func (rs *ReplicationServer) invalidateLocked() {
	for rid, ch := range rs.listeners {
		close(ch)
		delete(rs.listeners, rid)
	}
}

// AddReplica registers a new replica listener.
func (rs *ReplicationServer) AddReplica(replicaID string) chan WALEntry {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.addReplicaLocked(replicaID)
}
func (rs *ReplicationServer) addReplicaLocked(replicaID string) chan WALEntry {
	if old, ok := rs.listeners[replicaID]; ok {
		close(old)
	}
	ch := make(chan WALEntry, 10000) // ponytail: overflow reconnects from snapshot.
	rs.listeners[replicaID] = ch
	log.Printf("[replication] replica %s connected", replicaID)
	return ch
}

// RemoveReplica unregisters a replica.
func (rs *ReplicationServer) RemoveReplica(replicaID string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if ch, ok := rs.listeners[replicaID]; ok {
		close(ch)
		delete(rs.listeners, replicaID)
		log.Printf("[replication] replica %s disconnected", replicaID)
	}
}

// ReplicaCount returns number of connected replicas.
func (rs *ReplicationServer) ReplicaCount() int {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.listeners)
}

// HandleStreamWAL is an HTTP handler for replicas to receive WAL entries.
// GET /cluster/wal/stream?replica_id=xxx
// Returns newline-delimited JSON (NDJSON).
func (rs *ReplicationServer) HandleStreamWAL(w http.ResponseWriter, r *http.Request) {
	replicaID := r.URL.Query().Get("replica_id")
	if replicaID == "" {
		http.Error(w, "replica_id required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// ponytail: one lock fences supported single-shard snapshot and writes.
	rs.mu.Lock()
	records, err := rs.db.AllRecordsChecked()
	if err != nil {
		rs.mu.Unlock()
		http.Error(w, "snapshot source unavailable", http.StatusInternalServerError)
		return
	}
	ch := rs.addReplicaLocked(replicaID)
	snapshot := replicationSnapshot{Version: 1, Kind: "snapshot", Seq: rs.seq.Load(), Records: records}
	rs.mu.Unlock()
	defer func() {
		rs.mu.Lock()
		defer rs.mu.Unlock()
		if rs.listeners[replicaID] == ch {
			close(ch)
			delete(rs.listeners, replicaID)
		}
	}()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	encoder := json.NewEncoder(w)
	if err := encoder.Encode(snapshot); err != nil {
		return
	}
	flusher.Flush()
	for {
		select {
		case entry, ok := <-ch:
			if !ok {
				return // channel closed
			}
			if err := encoder.Encode(entry); err != nil {
				return // client disconnected
			}
			flusher.Flush()
		case <-r.Context().Done():
			return // client disconnected
		}
	}
}

// HandleSnapshot is an HTTP handler that sends full DB snapshot to a joining replica.
// GET /cluster/snapshot
func (rs *ReplicationServer) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	rs.mu.Lock()
	records, err := rs.db.AllRecordsChecked()
	rs.mu.Unlock()
	if err != nil {
		http.Error(w, "snapshot source unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(records)
}

// HandleClusterState returns cluster info.
// GET /cluster/state
func (rs *ReplicationServer) HandleClusterState(w http.ResponseWriter, r *http.Request) {
	rs.mu.RLock()
	replicas := make([]string, 0, len(rs.listeners))
	for rid := range rs.listeners {
		replicas = append(replicas, rid)
	}
	rs.mu.RUnlock()

	state := map[string]any{
		"node_id":       rs.nodeID,
		"role":          rs.Role(),
		"primary_addr":  rs.PrimaryAddr(),
		"replicas":      replicas,
		"replica_count": len(replicas),
		"wal_seq":       rs.seq.Load(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state)
}

// ReplicaClient connects to primary and replays WAL entries to local DB.
type ReplicaClient struct {
	primaryAddr string
	nodeID      string
	db          *store.Levara
	collections *store.CollectionManager
	cancel      context.CancelFunc
	done        chan struct{}
}

// NewReplicaClient creates a replica that connects to primary for WAL streaming.
func NewReplicaClient(primaryAddr, nodeID string, db *store.Levara, collections *store.CollectionManager) *ReplicaClient {
	return &ReplicaClient{
		primaryAddr: primaryAddr,
		nodeID:      nodeID,
		db:          db,
		collections: collections,
	}
}

type replicationSnapshot struct {
	Version int                    `json:"version"`
	Kind    string                 `json:"kind"`
	Seq     uint64                 `json:"seq"`
	Records []store.SnapshotRecord `json:"records"`
}

// Start returns after restoring the first snapshot on the live stream.
func (rc *ReplicaClient) Start(ctx context.Context) error {
	ctx, rc.cancel = context.WithCancel(ctx)
	resp, decoder, seq, err := rc.openStream(ctx)
	if err != nil {
		rc.cancel()
		return err
	}
	rc.done = make(chan struct{})
	go func() {
		defer close(rc.done)
		rc.streamLoop(ctx, resp, decoder, seq)
	}()
	return nil
}
func (rc *ReplicaClient) Stop() {
	if rc.cancel != nil {
		rc.cancel()
	}
	if rc.done != nil {
		<-rc.done
	}
}
func (rc *ReplicaClient) openStream(ctx context.Context) (*http.Response, *json.Decoder, uint64, error) {
	endpoint := fmt.Sprintf("http://%s/cluster/wal/stream?replica_id=%s", rc.primaryAddr, url.QueryEscape(rc.nodeID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	fail := func(err error) (*http.Response, *json.Decoder, uint64, error) {
		resp.Body.Close()
		return nil, nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("WAL stream HTTP %d", resp.StatusCode))
	}
	decoder := json.NewDecoder(resp.Body)
	var snapshot struct {
		Version int
		Kind    string
		Seq     *uint64
		Records []store.SnapshotRecord
	}
	if err := decoder.Decode(&snapshot); err != nil {
		return fail(err)
	}
	if snapshot.Version != 1 || snapshot.Kind != "snapshot" || snapshot.Records == nil || snapshot.Seq == nil {
		return fail(fmt.Errorf("unsupported replication snapshot"))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := rc.db.RestoreSnapshot(snapshot.Records); err != nil {
		return fail(fmt.Errorf("snapshot restore: %w", err))
	}
	return resp, decoder, *snapshot.Seq, nil
}
func (rc *ReplicaClient) streamLoop(ctx context.Context, resp *http.Response, decoder *json.Decoder, seq uint64) {
	backoff := time.Second
	for {
		if resp != nil {
			err := rc.readStream(ctx, decoder, seq)
			resp.Body.Close()
			resp = nil
			if ctx.Err() != nil {
				return
			}
			log.Printf("[replica] stream interrupted: %v; restoring fresh snapshot", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		var err error
		resp, decoder, seq, err = rc.openStream(ctx)
		if err != nil {
			log.Printf("[replica] reconnect: %v", err)
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		} else {
			backoff = time.Second
		}
	}
}
func (rc *ReplicaClient) readStream(ctx context.Context, decoder *json.Decoder, seq uint64) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var entry WALEntry
		if err := decoder.Decode(&entry); err != nil {
			return err
		}
		if seq == ^uint64(0) || entry.Seq != seq+1 || entry.ID == "" {
			return fmt.Errorf("invalid replication sequence or identity")
		}
		var err error
		switch entry.Op {
		case store.OpInsert:
			err = rc.db.Insert(entry.ID, entry.Vector, entry.Metadata)
		case store.OpDelete:
			err = rc.db.Delete(entry.ID)
		default:
			return fmt.Errorf("unsupported replication operation %d", entry.Op)
		}
		if err != nil {
			return fmt.Errorf("replication apply %s: %w", entry.ID, err)
		}
		seq = entry.Seq
	}
}

// WALEntryFromInsert creates a WAL entry for an insert operation.
func WALEntryFromInsert(id string, vector []float32, metadata interface{}) WALEntry {
	var meta json.RawMessage
	switch v := metadata.(type) {
	case json.RawMessage:
		meta = v
	case []byte:
		meta = json.RawMessage(v)
	case string:
		if len(v) > 0 && (v[0] == '{' || v[0] == '[') {
			meta = json.RawMessage(v)
		} else {
			data, _ := json.Marshal(v)
			meta = json.RawMessage(data)
		}
	default:
		data, _ := json.Marshal(metadata)
		meta = json.RawMessage(data)
	}
	// Copy vector to avoid unsafe pointer issues
	vecCopy := make([]float32, len(vector))
	copy(vecCopy, vector)
	return WALEntry{Op: store.OpInsert, ID: id, Vector: vecCopy, Metadata: append(json.RawMessage(nil), meta...)}
}

// WALEntryFromDelete creates a WAL entry for a delete operation.
func WALEntryFromDelete(id string) WALEntry {
	return WALEntry{Op: store.OpDelete, ID: id}
}
