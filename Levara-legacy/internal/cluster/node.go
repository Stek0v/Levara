package cluster

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
	"github.com/stek0v/cognevra/internal/metrics"
	"github.com/stek0v/cognevra/internal/store"
)

const (
	RaftTimeout = 500 * time.Millisecond
)

type RaftNode struct {
	Raft *raft.Raft
	FSM  *FSM
	// we keep a reference to the database for read only operations
	DB *store.Cognevra
}

func NewRaftNode(shardID int, nodeID string, baseDir string, raftPort int, db *store.Cognevra) (*RaftNode, error) {
	fsm := NewFSM(db)

	raftDir := filepath.Join(baseDir, fmt.Sprintf("shard_%d", shardID), "raft")
	os.MkdirAll(raftDir, 0755)

	config := raft.DefaultConfig()
	config.LocalID = raft.ServerID(fmt.Sprintf("%s-shard-%d", nodeID, shardID))
	config.HeartbeatTimeout = 200 * time.Millisecond
	config.ElectionTimeout = 300 * time.Millisecond
	config.CommitTimeout = 10 * time.Millisecond
	config.SnapshotThreshold = 65536

	addr := fmt.Sprintf("127.0.0.1:%d", raftPort)
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(addr, tcpAddr, 3, time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}

	logStore, err := raftboltdb.NewBoltStore(filepath.Join(raftDir, "logs.dat"))
	if err != nil {
		return nil, err
	}

	stableStore, err := raftboltdb.NewBoltStore(filepath.Join(raftDir, "stable.dat"))
	if err != nil {
		return nil, err
	}

	snapshotStore, err := raft.NewFileSnapshotStore(raftDir, 1, os.Stderr)
	if err != nil {
		return nil, err
	}

	raftNode, err := raft.NewRaft(config, fsm, logStore, stableStore, snapshotStore, transport)
	if err != nil {
		return nil, err
	}

	rn := &RaftNode{
		Raft: raftNode,
		FSM:  fsm,
		DB:   db,
	}

	// periodically reflect raft state in telemetry gauge
	go func() {
		prev := -1
		for {
			st := int(rn.Raft.State())
			if st != prev {
				metrics.RaftState.Set(float64(st))
				prev = st
			}
			time.Sleep(time.Second)
		}
	}()

	return rn, nil
}

func (rn *RaftNode) Insert(id string, vector []float32, data interface{}) error {
	if rn.Raft.State() != raft.Leader {
		return fmt.Errorf("not the leader of this shard")
	}

	var raw json.RawMessage
	switch v := data.(type) {
	case json.RawMessage:
		raw = v
	case []byte:
		raw = json.RawMessage(v)
	default:
		jsonData, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("failed to marshal data: %v", err)
		}
		raw = json.RawMessage(jsonData)
	}
	cmd := Command{
		Op:     "insert",
		Id:     id,
		Vector: vector,
		Data:   raw,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	future := rn.Raft.Apply(b, RaftTimeout)
	if err := future.Error(); err != nil {
		return err
	}

	if fsmErr, ok := future.Response().(error); ok {
		return fsmErr
	}
	return nil
}

// BatchInsert implements store.ShardHandler.BatchInsert.
// It converts store.BatchItem slice to Raft Command and applies in one round-trip.
func (rn *RaftNode) BatchInsert(items []store.BatchItem) []error {
	records := make([]BatchRecord, 0, len(items))
	for _, item := range items {
		// Avoid double-marshal: if Data is already json.RawMessage, use directly
		var raw json.RawMessage
		switch v := item.Data.(type) {
		case json.RawMessage:
			raw = v
		case []byte:
			raw = json.RawMessage(v)
		default:
			data, err := json.Marshal(item.Data)
			if err != nil {
				return []error{fmt.Errorf("marshal %s: %w", item.ID, err)}
			}
			raw = json.RawMessage(data)
		}
		records = append(records, BatchRecord{
			Id:     item.ID,
			Vector: item.Vector,
			Data:   raw,
		})
	}
	return rn.batchInsertRaft(records)
}

// batchInsertRaft applies all records in a single Raft round-trip.
func (rn *RaftNode) batchInsertRaft(records []BatchRecord) []error {
	if rn.Raft.State() != raft.Leader {
		return []error{fmt.Errorf("not the leader of this shard")}
	}

	cmd := Command{
		Op:      "batch_insert",
		Records: records,
	}

	b, err := json.Marshal(cmd)
	if err != nil {
		return []error{err}
	}

	future := rn.Raft.Apply(b, RaftTimeout)
	if err := future.Error(); err != nil {
		return []error{err}
	}

	// FSM returns nil on full success or []error on partial failure
	if resp := future.Response(); resp != nil {
		if errs, ok := resp.([]error); ok {
			return errs
		}
		if singleErr, ok := resp.(error); ok {
			return []error{singleErr}
		}
	}
	return nil
}

func (rn *RaftNode) Search(query []float32, topK int) []store.VectroRecord {
	return rn.DB.Search(query, topK)
}

func (rn *RaftNode) Delete(id string) error {
	return rn.DB.Delete(id)
}

func (rn *RaftNode) BatchDelete(ids []string) []error {
	return rn.DB.BatchDelete(ids)
}
