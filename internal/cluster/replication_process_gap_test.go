package cluster

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/stek0v/levara/internal/store"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Draft destination: internal/cluster/replication_process_gap_test.go.
// Independent processes and one default store only; no Raft/collections claim.
type replicationGapConfig struct{ Role, Root, Primary string }
type replicationGapCommand struct {
	Op, ID   string
	Vector   []float32
	Metadata json.RawMessage
}
type replicationGapReply struct {
	Error, Address string
	Count          int
	Records        []store.SnapshotRecord
}

func TestReplicationProcessGapChild(t *testing.T) {
	raw := os.Getenv("LEVARA_REPLICATION_PROCESS_GAP")
	if raw == "" {
		return
	}
	log.SetOutput(os.Stderr)
	var cfg replicationGapConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.NewLevara(2, cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	writer := json.NewEncoder(os.Stdout)
	var rs *ReplicationServer
	var direct *DirectNode
	var startup atomic.Value
	ready := replicationGapReply{}
	switch cfg.Role {
	case "primary":
		rs = NewReplicationServer("process-primary", nil, db)
		direct = &DirectNode{DB: db, Repl: rs}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/cluster/snapshot", rs.HandleSnapshot)
		mux.HandleFunc("/cluster/wal/stream", rs.HandleStreamWAL)
		server := &http.Server{Handler: mux}
		defer server.Close()
		go func() {
			if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
				log.Printf("child HTTP: %v", err)
			}
		}()
		ready.Address = listener.Addr().String()
	case "replica":
		client := NewReplicaClient(cfg.Primary, "process-replica", db, nil)
		defer client.Stop()
		// Do not make parent commands wait for old Start readiness: a future
		// versioned snapshot-first Start may await stream admission itself.
		go func() {
			if err := client.Start(ctx); err != nil {
				startup.Store(err.Error())
				log.Printf("child Start: %v", err)
			}
		}()
	case "reader":
		// No ReplicaClient, no primary URL and no snapshot fetch.
	default:
		t.Fatalf("unknown child role %q", cfg.Role)
	}
	if err := writer.Encode(ready); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var command replicationGapCommand
		reply := replicationGapReply{}
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			reply.Error = err.Error()
		} else {
			switch command.Op {
			case "insert":
				if direct == nil {
					reply.Error = "insert requires primary"
				} else if err := direct.Insert(command.ID, command.Vector, command.Metadata); err != nil {
					reply.Error = err.Error()
				}
			case "delete":
				if direct == nil {
					reply.Error = "delete requires primary"
				} else if err := direct.Delete(command.ID); err != nil {
					reply.Error = err.Error()
				}
			case "read":
				reply.Records = db.AllRecords()
			case "count":
				if rs == nil {
					reply.Error = "count requires primary"
				} else {
					reply.Count = rs.ReplicaCount()
				}
			case "stop":
				writer.Encode(reply)
				return
			default:
				reply.Error = "unknown child command"
			}
		}
		if value := startup.Load(); value != nil && reply.Error == "" {
			reply.Error = value.(string)
		}
		if err := writer.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type replicationGapProcess struct {
	command  *exec.Cmd
	input    io.WriteCloser
	replies  chan replicationGapReply
	exited   chan error
	killOnce sync.Once
}

func replicationGapStart(t *testing.T, ctx context.Context, cfg replicationGapConfig) *replicationGapProcess {
	t.Helper()
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReplicationProcessGapChild$")
	command.Env = append(os.Environ(), "LEVARA_REPLICATION_PROCESS_GAP="+string(encoded))
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
	child := &replicationGapProcess{command: command, input: input, replies: make(chan replicationGapReply, 32), exited: make(chan error, 1)}
	if err := command.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			// Native store recovery and test framework logs are not protocol replies.
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var reply replicationGapReply
			if err := json.Unmarshal(line, &reply); err != nil {
				reply.Error = "invalid child JSON: " + err.Error()
			}
			select {
			case child.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() { child.exited <- command.Wait(); close(child.exited) }()
	t.Cleanup(func() { child.kill(t) })
	return child
}
func (p *replicationGapProcess) receive(t *testing.T, ctx context.Context) replicationGapReply {
	t.Helper()
	select {
	case reply := <-p.replies:
		if reply.Error != "" {
			t.Fatal(reply.Error)
		}
		return reply
	case err := <-p.exited:
		t.Fatalf("child exited before reply: %v", err)
	case <-ctx.Done():
		t.Fatalf("child command deadline: %v", ctx.Err())
	}
	return replicationGapReply{}
}
func (p *replicationGapProcess) call(t *testing.T, ctx context.Context, command replicationGapCommand) replicationGapReply {
	t.Helper()
	if err := json.NewEncoder(p.input).Encode(command); err != nil {
		t.Fatal(err)
	}
	return p.receive(t, ctx)
}
func (p *replicationGapProcess) kill(t *testing.T) {
	t.Helper()
	p.killOnce.Do(func() {
		// SIGKILL on Unix: deferred Close cannot flush/checkpoint a passing oracle.
		_ = p.command.Process.Kill()
		_ = p.input.Close()
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			t.Error("killed child did not exit")
		}
	})
}

type replicationGapProxy struct {
	server              *httptest.Server
	transport           *http.Transport
	target              *url.URL
	mu                  sync.Mutex
	partition           bool
	active              map[uint64]context.CancelFunc
	next                uint64
	admission, attempt  chan struct{}
	attempted, released sync.Once
	snapshots           atomic.Int64
}

func newReplicationGapProxy(t *testing.T, address string, hold bool) *replicationGapProxy {
	t.Helper()
	target, err := url.Parse("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	p := &replicationGapProxy{target: target, transport: http.DefaultTransport.(*http.Transport).Clone(), active: map[uint64]context.CancelFunc{}, admission: make(chan struct{}), attempt: make(chan struct{})}
	if !hold {
		p.release()
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(func() { p.partitioned(true); p.release(); p.server.Close(); p.transport.CloseIdleConnections() })
	return p
}
func (p *replicationGapProxy) address() string { return strings.TrimPrefix(p.server.URL, "http://") }
func (p *replicationGapProxy) release()        { p.released.Do(func() { close(p.admission) }) }
func (p *replicationGapProxy) partitioned(block bool) {
	p.mu.Lock()
	p.partition = block
	if block {
		for _, cancel := range p.active {
			cancel()
		}
	}
	p.mu.Unlock()
}
func (p *replicationGapProxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/cluster/wal/stream" {
		p.attempted.Do(func() { close(p.attempt) })
		select {
		case <-p.admission:
		case <-r.Context().Done():
			return
		}
	}
	p.mu.Lock()
	if p.partition {
		p.mu.Unlock()
		http.Error(w, "controlled partition", http.StatusServiceUnavailable)
		return
	}
	id := p.next
	p.next++
	ctx, cancel := context.WithCancel(r.Context())
	p.active[id] = cancel
	p.mu.Unlock()
	defer func() { cancel(); p.mu.Lock(); delete(p.active, id); p.mu.Unlock() }()
	forwarded := r.Clone(ctx)
	forwarded.RequestURI = ""
	forwarded.URL.Scheme, forwarded.URL.Host = p.target.Scheme, p.target.Host
	forwarded.Host = p.target.Host
	// Query cursor/epoch fields and versioned snapshot payloads stay opaque.
	response, err := p.transport.RoundTrip(forwarded)
	if err != nil {
		http.Error(w, "upstream interrupted", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if flush, ok := w.(http.Flusher); ok {
		flush.Flush()
	}
	buffer := make([]byte, 4096)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF && r.URL.Path == "/cluster/snapshot" && response.StatusCode == 200 {
				p.snapshots.Add(1)
			}
			return
		}
	}
}

func replicationGapRecords(records []store.SnapshotRecord) []store.SnapshotRecord {
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records
}
func replicationGapWait(t *testing.T, ctx context.Context, description string, condition func() bool) {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatalf("native convergence failed: %s", description)
		case <-ctx.Done():
			t.Fatalf("%s: %v", description, ctx.Err())
		}
	}
}

func TestReplicationIndependentProcessMissingHistoryAndAdmissionGap(t *testing.T) {
	for _, mode := range []string{"disconnect", "snapshot_admission"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			root := t.TempDir()
			primary := replicationGapStart(t, ctx, replicationGapConfig{Role: "primary", Root: filepath.Join(root, "primary")})
			ready := primary.receive(t, ctx)
			if ready.Address == "" {
				t.Fatal("primary has no actual loopback listener")
			}
			for _, record := range []store.SnapshotRecord{
				{ID: "keep", Vector: []float32{1, 0}, Data: json.RawMessage(`{"state":"keep","n":1}`)},
				{ID: "victim", Vector: []float32{0, 1}, Data: json.RawMessage(`{"state":"delete","n":2}`)},
			} {
				primary.call(t, ctx, replicationGapCommand{Op: "insert", ID: record.ID, Vector: record.Vector, Metadata: record.Data})
			}
			initial := replicationGapRecords(primary.call(t, ctx, replicationGapCommand{Op: "read"}).Records)
			proxy := newReplicationGapProxy(t, ready.Address, mode == "snapshot_admission")
			replicaRoot := filepath.Join(root, "replica")
			replica := replicationGapStart(t, ctx, replicationGapConfig{Role: "replica", Root: replicaRoot, Primary: proxy.address()})
			replica.receive(t, ctx) // DB opened, independent of Start/admission readiness
			if mode == "disconnect" {
				replicationGapWait(t, ctx, "initial snapshot restored", func() bool {
					return reflect.DeepEqual(initial, replicationGapRecords(replica.call(t, ctx, replicationGapCommand{Op: "read"}).Records))
				})
				replicationGapWait(t, ctx, "initial subscriber admitted", func() bool { return primary.call(t, ctx, replicationGapCommand{Op: "count"}).Count == 1 })
				proxy.partitioned(true)
				replicationGapWait(t, ctx, "cancelled stream removed subscriber", func() bool { return primary.call(t, ctx, replicationGapCommand{Op: "count"}).Count == 0 })
			} else {
				select {
				case <-proxy.attempt:
				case <-ctx.Done():
					t.Fatal("stream did not reach actual admission barrier")
				}
				// Old Start reaches this barrier after consuming its HTTP snapshot. A
				// future integrated stream bootstrap reaches it before receiving any
				// snapshot. Neither protocol needs Start readiness or another request.
				if primary.call(t, ctx, replicationGapCommand{Op: "count"}).Count != 0 {
					t.Fatal("barrier admitted an upstream subscriber")
				}
			}
			// Neither write has a listener. Never Broadcast these entries again.
			primary.call(t, ctx, replicationGapCommand{Op: "insert", ID: "gap-insert", Vector: []float32{-1, 0}, Metadata: json.RawMessage(`{"state":"missing-history","n":3}`)})
			primary.call(t, ctx, replicationGapCommand{Op: "delete", ID: "victim"})
			expected := replicationGapRecords(primary.call(t, ctx, replicationGapCommand{Op: "read"}).Records)
			if len(expected) != 2 || expected[0].ID != "gap-insert" || expected[1].ID != "keep" {
				t.Fatalf("primary mutation control=%+v", expected)
			}
			if mode == "disconnect" {
				proxy.partitioned(false)
			} else {
				proxy.release()
			}
			replicationGapWait(t, ctx, "actual subscriber readmitted", func() bool { return primary.call(t, ctx, replicationGapCommand{Op: "count"}).Count == 1 })
			var observed []store.SnapshotRecord
			defer func() {
				if t.Failed() {
					t.Logf("primary=%+v last replica=%+v", expected, observed)
				}
			}()
			replicationGapWait(t, ctx, "exact IDs/vectors/metadata without rebroadcast", func() bool {
				observed = replicationGapRecords(replica.call(t, ctx, replicationGapCommand{Op: "read"}).Records)
				return reflect.DeepEqual(expected, observed)
			})
			snapshotCount := proxy.snapshots.Load()
			replica.kill(t)
			reader := replicationGapStart(t, ctx, replicationGapConfig{Role: "reader", Root: replicaRoot})
			reader.receive(t, ctx)
			durable := replicationGapRecords(reader.call(t, ctx, replicationGapCommand{Op: "read"}).Records)
			if !reflect.DeepEqual(expected, durable) {
				t.Fatalf("post-SIGKILL native WAL=%+v want=%+v", durable, expected)
			}
			if proxy.snapshots.Load() != snapshotCount {
				t.Fatal("durability reader fetched a snapshot")
			}
			reader.kill(t)
		})
	}
}
