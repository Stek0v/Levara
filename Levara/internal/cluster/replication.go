// Package cluster provides WAL-based replication for Levara multi-node deployments.
//
// Architecture:
//   Primary: accepts writes → WAL fsync → streams WAL entries to replicas
//   Replica: receives WAL stream → replays entries to local DB
//
// Communication uses HTTP streaming (SSE-like) to avoid proto regeneration.
// Each WAL entry is sent as a JSON line over a long-lived HTTP connection.
package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stek0v/levara/internal/store"
)

// WALEntry is one replicated entry sent from primary to replica.
type WALEntry struct {
	Op       byte              `json:"op"`       // store.OpInsert or store.OpDelete
	ID       string            `json:"id"`
	Vector   []float32         `json:"vector,omitempty"`
	Metadata json.RawMessage   `json:"metadata,omitempty"`
	Seq      uint64            `json:"seq"` // monotonic sequence number
}

// ReplicationServer streams WAL entries to replicas.
//
// seq uses atomic.Uint64 rather than living under mu because Broadcast holds
// only mu.RLock — concurrent Broadcasts would otherwise race on the sequence
// increment and produce duplicate Seq values, breaking replicas' gap
// detection. See TestReplicationServer_Broadcast_ConcurrentNoLostSeq.
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
	entry.Seq = rs.seq.Add(1)
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	for rid, ch := range rs.listeners {
		select {
		case ch <- entry:
		default:
			log.Printf("[replication] replica %s channel full, dropping entry seq=%d", rid, entry.Seq)
		}
	}
}

// AddReplica registers a new replica listener.
func (rs *ReplicationServer) AddReplica(replicaID string) chan WALEntry {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	ch := make(chan WALEntry, 10000) // buffer 10K entries
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

	ch := rs.AddReplica(replicaID)
	defer rs.RemoveReplica(replicaID)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	encoder := json.NewEncoder(w)

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
	records := rs.db.AllRecords()
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

// Start begins replication: first fetches snapshot, then streams WAL.
func (rc *ReplicaClient) Start(ctx context.Context) error {
	ctx, rc.cancel = context.WithCancel(ctx)

	// 1. Fetch initial snapshot from primary
	log.Printf("[replica] fetching snapshot from %s...", rc.primaryAddr)
	if err := rc.fetchSnapshot(ctx); err != nil {
		return fmt.Errorf("snapshot fetch: %w", err)
	}
	log.Printf("[replica] snapshot loaded, starting WAL stream...")

	// 2. Stream WAL entries
	go rc.streamLoop(ctx)
	return nil
}

// Stop stops the replica client.
func (rc *ReplicaClient) Stop() {
	if rc.cancel != nil {
		rc.cancel()
	}
}

func (rc *ReplicaClient) fetchSnapshot(ctx context.Context) error {
	url := fmt.Sprintf("http://%s/cluster/snapshot", rc.primaryAddr)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("snapshot HTTP %d", resp.StatusCode)
	}

	var records []store.SnapshotRecord
	if err := json.NewDecoder(resp.Body).Decode(&records); err != nil {
		return err
	}

	// Clear local DB and replay snapshot
	rc.db.Clear()
	for _, r := range records {
		if err := rc.db.Insert(r.ID, r.Vector, r.Data); err != nil {
			log.Printf("[replica] snapshot insert %s: %v", r.ID, err)
		}
	}
	log.Printf("[replica] snapshot restored: %d records", len(records))
	return nil
}

func (rc *ReplicaClient) streamLoop(ctx context.Context) {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := rc.streamOnce(ctx)
		if err != nil {
			log.Printf("[replica] WAL stream error: %v, reconnecting in %v", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
		} else {
			backoff = time.Second
		}
	}
}

func (rc *ReplicaClient) streamOnce(ctx context.Context) error {
	url := fmt.Sprintf("http://%s/cluster/wal/stream?replica_id=%s", rc.primaryAddr, rc.nodeID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("WAL stream HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB line buffer
	applied := 0

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var entry WALEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			log.Printf("[replica] WAL entry unmarshal error: %v", err)
			continue
		}

		switch entry.Op {
		case store.OpInsert:
			// Make a copy of vector to avoid unsafe pointer issues
			vec := make([]float32, len(entry.Vector))
			copy(vec, entry.Vector)
			if err := rc.db.Insert(entry.ID, vec, entry.Metadata); err != nil {
				log.Printf("[replica] insert %s: %v", entry.ID, err)
			} else {
				applied++
			}
		case store.OpDelete:
			rc.db.Delete(entry.ID)
			applied++
		}

		if applied > 0 && applied%1000 == 0 {
			log.Printf("[replica] applied %d WAL entries", applied)
		}
	}

	return scanner.Err()
}

// WALEntryFromInsert creates a WAL entry for an insert operation.
func WALEntryFromInsert(id string, vector []float32, metadata interface{}) WALEntry {
	var meta json.RawMessage
	switch v := metadata.(type) {
	case json.RawMessage:
		meta = v
	case []byte:
		meta = json.RawMessage(v)
	default:
		data, _ := json.Marshal(metadata)
		meta = json.RawMessage(data)
	}
	// Copy vector to avoid unsafe pointer issues
	vecCopy := make([]float32, len(vector))
	copy(vecCopy, vector)
	return WALEntry{Op: store.OpInsert, ID: id, Vector: vecCopy, Metadata: meta}
}

// WALEntryFromDelete creates a WAL entry for a delete operation.
func WALEntryFromDelete(id string) WALEntry {
	return WALEntry{Op: store.OpDelete, ID: id}
}

