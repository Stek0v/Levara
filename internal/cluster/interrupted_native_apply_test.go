//go:build darwin || linux

package cluster

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stek0v/levara/internal/store"
)

// Draft destination: internal/cluster/interrupted_native_apply_test.go.
// Current native WAL is legacy [u32 payloadSize][payload], without CRC/version.
// A future WAL format requires adapting this explicit observer, not interpreting
// a modern header as a legacy frame. No Raft transport or network is involved.
const interruptedApplyMetadataBytes = 768 << 10

type interruptedApplyConfig struct{ Root string }
type interruptedApplyCommand struct {
	Op, ID, Phase string
	Vector        []float32
	Large         bool
}
type interruptedApplyReply struct {
	Error   string
	Ack     bool
	Records []store.SnapshotRecord
}

func interruptedApplyMetadata(phase string, large bool) json.RawMessage {
	payload := struct {
		Phase string `json:"phase"`
		Blob  string `json:"blob,omitempty"`
	}{Phase: phase}
	if large {
		payload.Blob = strings.Repeat("x", interruptedApplyMetadataBytes)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return encoded
}
func TestInterruptedNativeApplyChild(t *testing.T) {
	raw := os.Getenv("LEVARA_INTERRUPTED_NATIVE_APPLY")
	if raw == "" {
		return
	}
	log.SetOutput(os.Stderr)
	var cfg interruptedApplyConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	lifetime := time.AfterFunc(35*time.Second, func() { os.Exit(124) })
	defer lifetime.Stop()
	db, err := store.NewLevara(2, filepath.Join(cfg.Root, "meta.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fsm := NewFSM(db)
	writer := json.NewEncoder(os.Stdout)
	if err := writer.Encode(interruptedApplyReply{}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var command interruptedApplyCommand
		reply := interruptedApplyReply{}
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			reply.Error = err.Error()
		} else {
			switch command.Op {
			case "insert":
				payload, err := json.Marshal(Command{Op: "insert", Id: command.ID, Vector: command.Vector, Data: interruptedApplyMetadata(command.Phase, command.Large)})
				if err != nil {
					reply.Error = err.Error()
					break
				}
				// Real production FSM -> Levara.Insert -> native WAL flush and fsync.
				if result := fsm.Apply(&raft.Log{Data: payload}); result != nil {
					reply.Error = fmt.Sprint(result)
				} else {
					reply.Ack = true
				}
			case "read":
				reply.Records = db.AllRecords()
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

type interruptedApplyProcess struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	replies  chan interruptedApplyReply
	stopped  bool
	killOnce sync.Once
	waitErr  error
}

func interruptedApplyStart(t *testing.T, ctx context.Context, root string) *interruptedApplyProcess {
	t.Helper()
	cfg, err := json.Marshal(interruptedApplyConfig{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptedNativeApplyChild$")
	command.Env = append(os.Environ(), "LEVARA_INTERRUPTED_NATIVE_APPLY="+string(cfg))
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
	p := &interruptedApplyProcess{cmd: command, input: input, replies: make(chan interruptedApplyReply, 8)}
	if err := command.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
	// Do not run Cmd.Wait concurrently with Wait4(WUNTRACED): the parent must
	// consume the actual stopped wait-status before it later waits for death.
	go func() {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var reply interruptedApplyReply
			if err := json.Unmarshal(line, &reply); err != nil {
				reply.Error = err.Error()
			}
			select {
			case p.replies <- reply:
			case <-ctx.Done():
				return
			}
		}
	}()
	t.Cleanup(func() { p.kill(t) })
	ready := p.receive(t, ctx)
	if ready.Error != "" {
		t.Fatal(ready.Error)
	}
	return p
}
func (p *interruptedApplyProcess) send(t *testing.T, command interruptedApplyCommand) {
	t.Helper()
	if err := json.NewEncoder(p.input).Encode(command); err != nil {
		t.Fatal(err)
	}
}
func (p *interruptedApplyProcess) receive(t *testing.T, ctx context.Context) interruptedApplyReply {
	t.Helper()
	select {
	case reply := <-p.replies:
		return reply
	case <-ctx.Done():
		t.Fatalf("native apply child reply deadline: %v", ctx.Err())
	}
	return interruptedApplyReply{}
}
func (p *interruptedApplyProcess) kill(t *testing.T) error {
	t.Helper()
	p.killOnce.Do(func() {
		_ = p.cmd.Process.Kill()
		_ = p.input.Close()
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case p.waitErr = <-done:
		case <-time.After(5 * time.Second):
			t.Error("native apply child did not exit after SIGKILL")
		}
	})
	return p.waitErr
}
func interruptedApplySorted(records []store.SnapshotRecord) []store.SnapshotRecord {
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records
}

// Existing strict native validation checks the acknowledged frame lengths,
// dimension, metadata, finite vectors, and location bytes before we observe
// the actual next frame. Prefix order is WAL order, not map iteration order.
func interruptedApplyPrefixEnd(ctx context.Context, path string, expected []store.SnapshotRecord) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	position := 0
	err = store.WalkWALStrict(ctx, file, 2, 4<<20, func(op byte, record store.SnapshotRecord) error {
		if op != store.OpInsert || position >= len(expected) || !reflect.DeepEqual(record, expected[position]) {
			return errors.New("acknowledged native WAL prefix differs from exact receipt")
		}
		position++
		return nil
	})
	if err != nil {
		return 0, err
	}
	if position != len(expected) {
		return 0, errors.New("missing acknowledged native WAL record")
	}
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// This reads only the fixed-size header/ID/vector-length/metadata-length fields,
// never a growing giant frame. Success requires the correct target frame and
// a physical file size strictly between its header and declared frame end.
func interruptedApplyIncomplete(path string, start int64, id string, metadataLen int) (bool, int64, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, 0, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return false, 0, 0, err
	}
	prefixLen := 4 + 1 + 4 + len(id) + 4 + 8 + 4
	if info.Size() < start+int64(prefixLen) {
		return false, info.Size(), 0, nil
	}
	prefix := make([]byte, prefixLen)
	if _, err := file.ReadAt(prefix, start); err != nil {
		return false, 0, 0, err
	}
	declared := int64(binary.LittleEndian.Uint32(prefix[:4]))
	wantPayload := int64(1 + 4 + len(id) + 4 + 8 + 4 + metadataLen + 8 + 4)
	if declared != wantPayload || prefix[4] != store.OpInsert || int(binary.LittleEndian.Uint32(prefix[5:9])) != len(id) || string(prefix[9:9+len(id)]) != id {
		return false, 0, 0, errors.New("unexpected native tail format/target (not modern CRC-framed WAL)")
	}
	pos := 9 + len(id)
	if binary.LittleEndian.Uint32(prefix[pos:pos+4]) != 8 {
		return false, 0, 0, errors.New("target vector dimension mismatch")
	}
	pos += 4 + 8
	if int(binary.LittleEndian.Uint32(prefix[pos:pos+4])) != metadataLen {
		return false, 0, 0, errors.New("target metadata length mismatch")
	}
	end := start + 4 + declared
	return info.Size() < end, info.Size(), end, nil
}
func interruptedApplyConfirmStopped(t *testing.T, ctx context.Context, p *interruptedApplyProcess) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(p.cmd.Process.Pid, &status, syscall.WUNTRACED|syscall.WNOHANG, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if pid != 0 {
			// BSD's stopped status must be decoded from the native wait bits;
			// Go WaitStatus.Stopped can misclassify Darwin SIGSTOP status 4479.
			raw := uint32(status)
			if pid != p.cmd.Process.Pid || raw&0xff != 0x7f || syscall.Signal((raw>>8)&0xff) != syscall.SIGSTOP {
				t.Fatalf("child not actually SIGSTOP-stopped: pid=%d want=%d rawstatus=%#x", pid, p.cmd.Process.Pid, raw)
			}
			p.stopped = true
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("SIGSTOP wait-status deadline")
		default:
			runtime.Gosched()
		}
	}
}

func TestNativeFSMApplyInterruptedInsideWALFrame(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	child := interruptedApplyStart(t, ctx, root)
	prefix := []store.SnapshotRecord{
		{ID: "ack-A", Vector: []float32{1, 0}, Data: interruptedApplyMetadata("A", false)},
		{ID: "ack-B", Vector: []float32{0, 1}, Data: interruptedApplyMetadata("B", false)},
	}
	// Prefix order is WAL order; compare recovered state independently by ID.
	for _, record := range prefix {
		phase := "A"
		if record.ID == "ack-B" {
			phase = "B"
		}
		child.send(t, interruptedApplyCommand{Op: "insert", ID: record.ID, Phase: phase, Vector: record.Vector})
		reply := child.receive(t, ctx)
		if reply.Error != "" || !reply.Ack {
			t.Fatalf("durable prefix not acknowledged: %+v", reply)
		}
	}
	walPath := filepath.Join(root, "meta.bin.wal")
	start, err := interruptedApplyPrefixEnd(ctx, walPath, prefix)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := store.SnapshotRecord{ID: "uncertain-large", Vector: []float32{-1, 0}, Data: interruptedApplyMetadata("uncertain", true)}
	if len(uncertain.Data) > 1<<20 {
		t.Fatal("fixture exceeds native replay metadata bound")
	}
	child.send(t, interruptedApplyCommand{Op: "insert", ID: uncertain.ID, Phase: "uncertain", Large: true, Vector: uncertain.Vector})
	observationDeadline := time.NewTimer(6 * time.Second)
	defer observationDeadline.Stop()
	var stoppedSize, declaredEnd int64
	for {
		select {
		case reply := <-child.replies:
			t.Fatalf("native apply finished before an incomplete-frame boundary was captured; no interrupted-apply proof: %+v", reply)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-observationDeadline.C:
			t.Fatal("could not observe incomplete native WAL frame; no interrupted-apply proof")
		default:
		}
		incomplete, _, _, err := interruptedApplyIncomplete(walPath, start, uncertain.ID, len(uncertain.Data))
		if err != nil {
			t.Fatal(err)
		}
		if !incomplete {
			runtime.Gosched()
			continue
		}
		if err := child.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		interruptedApplyConfirmStopped(t, ctx, child)
		// Re-observe after the actual stopped status. The initial growth poll alone
		// could race with frame completion, and therefore cannot establish the kill
		// boundary. A completed frame here is a test failure, not a passing fallback.
		incomplete, stoppedSize, declaredEnd, err = interruptedApplyIncomplete(walPath, start, uncertain.ID, len(uncertain.Data))
		if err != nil {
			t.Fatal(err)
		}
		if !incomplete {
			t.Fatal("frame completed before confirmed SIGSTOP; cannot claim interrupted native apply")
		}
		select {
		case reply := <-child.replies:
			t.Fatalf("apply acknowledged before stopped incomplete frame: %+v", reply)
		default:
		}
		break
	}
	if !child.stopped || stoppedSize <= start || stoppedSize >= declaredEnd {
		t.Fatal("missing actual interrupted frame evidence")
	}
	t.Logf("actual stopped child pid=%d WAL prefix=%d file=%d declared-end=%d; no apply acknowledgement", child.cmd.Process.Pid, start, stoppedSize, declaredEnd)
	deathErr := child.kill(t)
	var exit *exec.ExitError
	if !errors.As(deathErr, &exit) {
		t.Fatalf("expected SIGKILL death, got %v", deathErr)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("expected actual SIGKILL wait status, got %v", exit.Sys())
	}
	// Reader is a separate OS process using only NewLevara/WAL replay. No Raft,
	// remote fetch, manually altered WAL, or parent-held native store is involved.
	reader := interruptedApplyStart(t, ctx, root)
	reader.send(t, interruptedApplyCommand{Op: "read"})
	recovered := reader.receive(t, ctx)
	if recovered.Error != "" {
		t.Fatal(recovered.Error)
	}
	checkRecovered := func(label string, reply interruptedApplyReply, required []store.SnapshotRecord) {
		t.Helper()
		if reply.Error != "" {
			t.Fatalf("%s: %s", label, reply.Error)
		}
		records := interruptedApplySorted(reply.Records)
		wanted := interruptedApplySorted(append([]store.SnapshotRecord(nil), required...))
		var actualRequired []store.SnapshotRecord
		seenUncertain := false
		for _, record := range records {
			if record.ID == uncertain.ID {
				if seenUncertain || !reflect.DeepEqual(record, uncertain) {
					t.Fatalf("%s: uncertain operation recovered as partial/invalid record", label)
				}
				seenUncertain = true
			} else {
				actualRequired = append(actualRequired, record)
			}
		}
		if !reflect.DeepEqual(actualRequired, wanted) {
			t.Fatalf("%s: acknowledged exact IDs/vectors/metadata lost or unexpected row recovered", label)
		}
		// Never require uncertain absence, including after continuation/reopening.
		t.Logf("%s: exact acknowledged records retained; uncertain complete-valid present=%v", label, seenUncertain)
	}
	checkRecovered("first independent replay", recovered, prefix)
	// Continue using the actual recovered native store. OpenWal uses O_APPEND;
	// retaining an incomplete tail can swallow this acknowledged new frame on
	// the next replay. No parent truncation/repair is permitted in this oracle.
	afterRecovery := store.SnapshotRecord{ID: "ack-after-recovery", Vector: []float32{1, 0}, Data: interruptedApplyMetadata("post-recovery", false)}
	reader.send(t, interruptedApplyCommand{Op: "insert", ID: afterRecovery.ID, Phase: "post-recovery", Vector: afterRecovery.Vector})
	acknowledgement := reader.receive(t, ctx)
	if acknowledgement.Error != "" || !acknowledgement.Ack {
		t.Fatalf("post-recovery native apply not acknowledged: %+v", acknowledgement)
	}
	reader.kill(t)
	third := interruptedApplyStart(t, ctx, root)
	third.send(t, interruptedApplyCommand{Op: "read"})
	required := append(append([]store.SnapshotRecord(nil), prefix...), afterRecovery)
	checkRecovered("third process replay after acknowledged continuation", third.receive(t, ctx), required)
}
