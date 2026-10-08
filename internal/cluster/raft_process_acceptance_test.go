package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stek0v/levara/internal/store"
)

// Draft destination: internal/cluster/raft_process_acceptance_test.go.
// Three real processes, one shard. Local reads are convergence observations,
// not linearizable reads. Snapshot creation is checked; installation/compaction
// are not inferred from restart or from matching application records.
type raftProcessConfig struct {
	Role, Root, ID string
	Port           int
}
type raftProcessCommand struct {
	Op, ID  string
	Vector  []float32
	Data    json.RawMessage
	Servers []raft.Server
}
type raftProcessReply struct {
	Error, State, LeaderID         string
	Applied, Commit, SnapshotIndex uint64
	Ack, Uncertain                 bool
	Records                        []store.SnapshotRecord
}

func TestRaftProcessAcceptanceChild(t *testing.T) {
	raw := os.Getenv("LEVARA_RAFT_PROCESS_ACCEPTANCE")
	if raw == "" {
		return
	}
	log.SetOutput(os.Stderr)
	var cfg raftProcessConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewLevara(2, filepath.Join(cfg.Root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writer := json.NewEncoder(os.Stdout)
	var node *RaftNode
	if cfg.Role == "node" {
		node, err = NewRaftNode(0, cfg.ID, cfg.Root, cfg.Port, db, WithBindAddr("127.0.0.1"))
		if err != nil {
			// Surface production constructor failure as a bounded, precise first oracle.
			_ = writer.Encode(raftProcessReply{Error: "NewRaftNode: " + err.Error()})
			return
		}
		defer func() { _ = node.Raft.Shutdown().Error() }()
	} else if cfg.Role != "reader" {
		t.Fatalf("unknown role %q", cfg.Role)
	}
	lifetime := time.AfterFunc(35*time.Second, func() { os.Exit(124) })
	defer lifetime.Stop()
	if err := writer.Encode(raftProcessReply{}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var command raftProcessCommand
		reply := raftProcessReply{}
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			reply.Error = err.Error()
		} else {
			switch command.Op {
			case "read":
				reply.Records = db.AllRecords()
			case "state":
				if node == nil {
					reply.Error = "state requires node"
				} else {
					_, id := node.Raft.LeaderWithID()
					reply.State = node.Raft.State().String()
					reply.LeaderID = string(id)
					reply.Applied = node.Raft.AppliedIndex()
					reply.Commit = node.Raft.CommitIndex()
					reply.Records = db.AllRecords()
				}
			case "bootstrap":
				if node == nil {
					reply.Error = "bootstrap requires node"
				} else {
					err = node.Raft.BootstrapCluster(raft.Configuration{Servers: command.Servers}).Error()
					if err != nil {
						reply.Error = err.Error()
					}
				}
			case "insert", "bounded_insert", "delete":
				if node == nil {
					reply.Error = "write requires node"
					break
				}
				result := make(chan error, 1)
				go func() {
					var err error
					if command.Op == "delete" {
						err = node.Delete(command.ID)
					} else {
						err = node.Insert(command.ID, command.Vector, command.Data)
					}
					result <- err
				}()
				// Raft Apply's timeout is not proof of definitive rejection. A bounded
				// observer frees the control loop even if the real future remains pending.
				select {
				case err := <-result:
					if err != nil {
						reply.Error = err.Error()
					} else {
						reply.Ack = true
						reply.Applied = node.Raft.AppliedIndex()
					}
				case <-time.After(1500 * time.Millisecond):
					reply.Uncertain = true
				}
			case "snapshot":
				if node == nil {
					reply.Error = "snapshot requires node"
					break
				}
				future := node.Raft.Snapshot()
				if err := future.Error(); err != nil {
					reply.Error = err.Error()
					break
				}
				meta, reader, err := future.Open()
				if err != nil {
					reply.Error = err.Error()
					break
				}
				decodeErr := json.NewDecoder(reader).Decode(&reply.Records)
				closeErr := reader.Close()
				if decodeErr != nil {
					reply.Error = decodeErr.Error()
				} else if closeErr != nil {
					reply.Error = closeErr.Error()
				} else {
					reply.SnapshotIndex = meta.Index
				}
			default:
				reply.Error = "unknown command"
			}
		}
		if err := writer.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type raftAcceptanceProcess struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	replies  chan raftProcessReply
	exited   chan struct{}
	waitErr  error
	killOnce sync.Once
}

func raftAcceptanceStart(t *testing.T, ctx context.Context, cfg raftProcessConfig) *raftAcceptanceProcess {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRaftProcessAcceptanceChild$")
	command.Env = append(os.Environ(), "LEVARA_RAFT_PROCESS_ACCEPTANCE="+string(raw))
	command.Stderr = os.Stderr
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	p := &raftAcceptanceProcess{cmd: command, input: input, replies: make(chan raftProcessReply, 16), exited: make(chan struct{})}
	if err := command.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { p.kill(t) })
	// Drain before Wait closes StdoutPipe; no dropped last constructor error.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var reply raftProcessReply
			if err := json.Unmarshal(line, &reply); err != nil {
				reply.Error = "invalid protocol: " + err.Error()
			}
			select {
			case p.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { <-drained; p.waitErr = command.Wait(); close(p.exited) }()
	ready := p.receive(t, ctx)
	if ready.Error != "" {
		t.Fatal(ready.Error)
	}
	return p
}
func (p *raftAcceptanceProcess) receive(t *testing.T, ctx context.Context) raftProcessReply {
	t.Helper()
	// Prefer any final protocol error queued before the process exited.
	select {
	case reply := <-p.replies:
		return reply
	default:
	}
	select {
	case reply := <-p.replies:
		return reply
	case <-p.exited:
		select {
		case reply := <-p.replies:
			return reply
		default:
		}
		t.Fatalf("Raft child exited before reply: %v", p.waitErr)
	case <-ctx.Done():
		t.Fatalf("Raft child deadline: %v", ctx.Err())
	}
	return raftProcessReply{}
}
func (p *raftAcceptanceProcess) call(t *testing.T, ctx context.Context, command raftProcessCommand) raftProcessReply {
	t.Helper()
	if err := json.NewEncoder(p.input).Encode(command); err != nil {
		t.Fatal(err)
	}
	return p.receive(t, ctx)
}
func (p *raftAcceptanceProcess) kill(t *testing.T) {
	t.Helper()
	p.killOnce.Do(func() {
		_ = p.cmd.Process.Kill() // SIGKILL on Unix, no graceful native flush.
		_ = p.input.Close()
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			t.Error("Raft child failed to exit after kill")
		}
	})
}
func raftAcceptanceRecords(records []store.SnapshotRecord) []store.SnapshotRecord {
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records
}
func raftAcceptanceWait(t *testing.T, ctx context.Context, label string, check func() bool) {
	t.Helper()
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if check() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s: %v", label, ctx.Err())
		case <-deadline.C:
			t.Fatalf("%s: bounded state convergence failed", label)
		case <-ticker.C:
		}
	}
}

func TestRaftIndependentProcessQuorumRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	var configs [3]raftProcessConfig
	var children [3]*raftAcceptanceProcess
	var listeners [3]net.Listener
	var servers []raft.Server
	// Reserve distinct loopback ports together. Production constructor owns the
	// actual bind; close each reservation immediately before starting that node.
	for i := range configs {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = listener
		t.Cleanup(func() { listener.Close() })
		port := listener.Addr().(*net.TCPAddr).Port
		configs[i] = raftProcessConfig{Role: "node", Root: filepath.Join(root, fmt.Sprintf("node-%d", i)), ID: fmt.Sprintf("acceptance-%d", i), Port: port}
		servers = append(servers, raft.Server{Suffrage: raft.Voter, ID: raft.ServerID(configs[i].ID + "-shard-0"), Address: raft.ServerAddress(listener.Addr().String())})
	}
	for i := range children {
		listeners[i].Close()
		children[i] = raftAcceptanceStart(t, ctx, configs[i])
	}
	// Exactly one bootstrap, and the same three voters are never bootstrapped on
	// restart. Followers receive the initial configuration through real Raft.
	if reply := children[0].call(t, ctx, raftProcessCommand{Op: "bootstrap", Servers: servers}); reply.Error != "" {
		t.Fatal(reply.Error)
	}
	leader := -1
	findLeader := func() bool {
		found := -1
		states := make([]raftProcessReply, 3)
		for i, p := range children {
			if p == nil {
				continue
			}
			states[i] = p.call(t, ctx, raftProcessCommand{Op: "state"})
			if states[i].Error != "" {
				t.Fatal(states[i].Error)
			}
			if states[i].State == "Leader" {
				if found != -1 {
					return false
				}
				found = i
			}
		}
		if found < 0 {
			return false
		}
		expectedID := configs[found].ID + "-shard-0"
		for i, p := range children {
			if p != nil && (states[i].LeaderID != expectedID || states[i].Applied == 0) {
				return false
			}
		}
		leader = found
		return true
	}
	raftAcceptanceWait(t, ctx, "initial single leader", findLeader)
	expected := []store.SnapshotRecord{}
	var lastAck uint64
	write := func(command raftProcessCommand) {
		t.Helper()
		reply := children[leader].call(t, ctx, command)
		if reply.Error != "" || !reply.Ack || reply.Uncertain {
			t.Fatalf("write not acknowledged: %+v", reply)
		}
		lastAck = reply.Applied
		next := expected[:0]
		for _, record := range expected {
			if record.ID != command.ID {
				next = append(next, record)
			}
		}
		expected = next
		if command.Op != "delete" {
			expected = append(expected, store.SnapshotRecord{ID: command.ID, Vector: command.Vector, Data: command.Data})
		}
		expected = raftAcceptanceRecords(expected)
	}
	converge := func() bool {
		for i, p := range children {
			if p == nil {
				continue
			}
			reply := p.call(t, ctx, raftProcessCommand{Op: "state"})
			if reply.Error != "" {
				t.Fatal(reply.Error)
			}
			if reply.Applied < lastAck || !reflect.DeepEqual(raftAcceptanceRecords(reply.Records), expected) {
				t.Logf("node %d applied=%d want >=%d records=%s want=%s", i, reply.Applied, lastAck, mustRaftAcceptanceJSON(reply.Records), mustRaftAcceptanceJSON(expected))
				return false
			}
		}
		return true
	}
	write(raftProcessCommand{Op: "insert", ID: "stable", Vector: []float32{1, 0}, Data: json.RawMessage(`{"version":"A"}`)})
	write(raftProcessCommand{Op: "insert", ID: "replace", Vector: []float32{0, 1}, Data: json.RawMessage(`{"version":"old"}`)})
	write(raftProcessCommand{Op: "insert", ID: "victim", Vector: []float32{-1, 0}, Data: json.RawMessage(`{"deleted":false}`)})
	write(raftProcessCommand{Op: "insert", ID: "replace", Vector: []float32{-1, 0}, Data: json.RawMessage(`{"version":"new"}`)})
	write(raftProcessCommand{Op: "delete", ID: "victim"})
	raftAcceptanceWait(t, ctx, "initial exact application state", converge)

	killedLeader := leader
	children[killedLeader].kill(t)
	children[killedLeader] = nil
	raftAcceptanceWait(t, ctx, "survivor elected leader", findLeader)
	raftAcceptanceWait(t, ctx, "acknowledged state survives leader kill", converge)
	write(raftProcessCommand{Op: "insert", ID: "after-election", Vector: []float32{0, 1}, Data: json.RawMessage(`{"term":"survivor"}`)})
	children[killedLeader] = raftAcceptanceStart(t, ctx, configs[killedLeader])
	raftAcceptanceWait(t, ctx, "same-root restarted leader catches up", converge)
	raftAcceptanceWait(t, ctx, "leader before quorum removal", findLeader)
	retained := leader
	for i, p := range children {
		if i != retained {
			p.kill(t)
			children[i] = nil
		}
	}
	// A timed-out real Apply can later commit; absence is not an oracle.
	negative := children[retained].call(t, ctx, raftProcessCommand{Op: "bounded_insert", ID: "uncertain", Vector: []float32{1, 0}, Data: json.RawMessage(`{"version":"uncertain"}`)})
	if negative.Ack {
		t.Fatalf("one of three voters acknowledged write without quorum: %+v", negative)
	}
	if negative.Error == "" && !negative.Uncertain {
		t.Fatalf("missing explicit failed/uncertain outcome: %+v", negative)
	}
	t.Logf("quorum-loss result (not a durable absence claim): %+v", negative)
	for i := range children {
		if i != retained {
			children[i] = raftAcceptanceStart(t, ctx, configs[i])
		}
	}
	raftAcceptanceWait(t, ctx, "quorum restored single leader", findLeader)
	// Reconcile the uncertain ID by an acknowledged later replacement. This
	// does not assume whether the earlier interrupted request committed.
	write(raftProcessCommand{Op: "insert", ID: "uncertain", Vector: []float32{-1, 0}, Data: json.RawMessage(`{"version":"settled"}`)})
	raftAcceptanceWait(t, ctx, "restored quorum exact state", converge)

	snapshotLeader := leader
	image := children[leader].call(t, ctx, raftProcessCommand{Op: "snapshot"})
	if image.Error != "" || image.SnapshotIndex == 0 {
		t.Fatalf("actual Raft snapshot failed: %+v", image)
	}
	if !reflect.DeepEqual(raftAcceptanceRecords(image.Records), expected) {
		t.Fatalf("actual persisted snapshot differs: %s", mustRaftAcceptanceJSON(image.Records))
	}
	t.Logf("actual Raft SnapshotFuture image index=%d; no installation/compaction claim", image.SnapshotIndex)
	children[snapshotLeader].kill(t)
	children[snapshotLeader] = raftAcceptanceStart(t, ctx, configs[snapshotLeader])
	raftAcceptanceWait(t, ctx, "abrupt snapshot-node restart convergence", converge)
	// Kill all native writers before standalone WAL readers, which cannot fetch,
	// bootstrap, replay Raft logs, or install a remote snapshot.
	for i, p := range children {
		p.kill(t)
		children[i] = nil
	}
	for _, cfg := range configs {
		cfg.Role = "reader"
		reader := raftAcceptanceStart(t, ctx, cfg)
		reply := reader.call(t, ctx, raftProcessCommand{Op: "read"})
		if reply.Error != "" || !reflect.DeepEqual(raftAcceptanceRecords(reply.Records), expected) {
			t.Fatalf("abrupt native store reopen mismatch: %+v", reply)
		}
		reader.kill(t)
	}
}
func mustRaftAcceptanceJSON(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return err.Error()
	}
	return string(data)
}
