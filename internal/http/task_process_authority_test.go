//go:build darwin || linux

package http

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspkg "github.com/stek0v/levara/pkg/access"
	mcppkg "github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/workspace"
)

// Native SQL + real MCPDeps + production core tools, independent OS processes.
// Private verified-JWT contexts model the authenticated middleware boundary;
// these are direct-core tests, not public HTTP authentication/transport tests.
type taskProcessAuthorityConfig struct {
	TrustedLocal                                  bool
	FirstProject                                  string
	Dialect                                       DBProvider
	SQLitePath, Database, Schema, Workspace, Gate string
}
type taskProcessAuthorityCommand struct {
	Op   string
	Args map[string]any
	Text string
}
type taskProcessAuthorityReply struct {
	Error, Checkpoint, Digest string
	Result                    *mcppkg.ToolResult
}

func taskProcessAuthorityContext(ctx context.Context, cfg APIConfig) context.Context {
	ctx = context.WithValue(ctx, mcppkg.UserIDKey, "owner")
	ctx = context.WithValue(ctx, mcppkg.TenantIDKey, "a")
	return context.WithValue(ctx, searchEgressKey{}, searchEgress{cfg: cfg, actor: accesspkg.Actor{UserID: "owner", TenantID: "a"}, kind: "jwt", expiresAt: time.Now().Add(time.Hour).Unix()})
}
func TestTaskProcessAuthorityChild(t *testing.T) {
	raw := os.Getenv("LEVARA_TASK_PROCESS_AUTHORITY")
	if raw == "" {
		return
	}
	log.SetOutput(os.Stderr)
	var config taskProcessAuthorityConfig
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		t.Fatal(err)
	}
	SetDBProvider(config.Dialect)
	var db *sql.DB
	var err error
	switch config.Dialect {
	case DBSQLite:
		db, err = sql.Open("sqlite3", "file:"+config.SQLitePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	case DBPostgres:
		pg, parseErr := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		pg.Database = config.Database
		pg.RuntimeParams["search_path"] = config.Schema
		db = stdlib.OpenDB(*pg)
	default:
		t.Fatal("unsupported task child dialect")
	}
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	lifetime := time.AfterFunc(80*time.Second, func() { os.Exit(124) })
	defer lifetime.Stop()
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	cfg := APIConfig{DB: db, WorkspacePath: config.Workspace, RequireAuth: !config.TrustedLocal}
	deps := NewMCPDeps(cfg)
	writer := json.NewEncoder(os.Stdout)
	if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "ready"}); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var command taskProcessAuthorityCommand
		reply := taskProcessAuthorityReply{}
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			reply.Error = err.Error()
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			verified := taskProcessAuthorityContext(ctx, cfg)
			switch command.Op {
			case "executor_write_before_receipt", "executor_recover":
				reply = taskExecutorProcessRecoveryCommand(ctx, config, cfg, command, writer)
			case "complete_local_serialized":
				reply = taskLocalSerializedComplete(ctx, config, cfg, deps, command, writer)
			case "claim_gate":
				// Actual independent-process checkpoints precede one common file release.
				// Neither contender may enter ToolTaskStep before the parent releases it.
				if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "claim_waiting"}); err != nil {
					cancel()
					t.Fatal(err)
				}
				wait := time.NewTicker(time.Millisecond)
				for {
					if _, err := os.Stat(config.Gate); err == nil {
						break
					} else if !errors.Is(err, os.ErrNotExist) {
						reply.Error = err.Error()
						break
					}
					select {
					case <-ctx.Done():
						reply.Error = ctx.Err().Error()
					case <-wait.C:
					}
					if reply.Error != "" {
						break
					}
				}
				wait.Stop()
				if reply.Error == "" {
					result := mcppkg.ToolTaskStep(verified, deps, command.Args)
					reply.Result = &result
				}
			case "step":
				result := mcppkg.ToolTaskStep(verified, deps, command.Args)
				reply.Result = &result
			case "receipt":
				result := mcppkg.ToolTaskReceipt(verified, deps, command.Args)
				reply.Result = &result
			case "validate":
				result := mcppkg.ToolTaskValidate(verified, deps, command.Args)
				reply.Result = &result
			case "complete":
				result := mcppkg.ToolTaskComplete(verified, deps, command.Args)
				reply.Result = &result
			case "replace_file_locked":
				reply = taskPostHashCooperativeReplace(ctx, config, command, writer)
			case "replace_file":
				// Fixed private fixture target. Server owner scope is never derived from
				// command actor_id; that field is only the ToolTaskStep lease identity.
				target := filepath.Join(config.Workspace, "projects", "alpha", "main", "proof.md")
				temp, err := os.CreateTemp(filepath.Dir(target), ".task-proof-*")
				if err != nil {
					reply.Error = err.Error()
					break
				}
				name := temp.Name()
				if _, err = temp.WriteString(command.Text); err == nil {
					err = temp.Sync()
				}
				closeErr := temp.Close()
				if err == nil {
					err = closeErr
				}
				if err == nil {
					err = os.Rename(name, target)
				}
				if err != nil {
					os.Remove(name)
					reply.Error = err.Error()
					break
				}
				// Observable byte replacement; no power-loss durability claim.
				actual, err := os.ReadFile(target)
				if err != nil {
					reply.Error = err.Error()
					break
				}
				reply.Digest = fmt.Sprintf("sha256:%x", sha256.Sum256(actual))
			default:
				reply.Error = "unknown task process command"
			}
			cancel()
		}
		if err := writer.Encode(reply); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
}

type taskAuthorityProcess struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	replies  chan taskProcessAuthorityReply
	exited   chan struct{}
	waitErr  error
	killOnce sync.Once
}

func taskAuthorityStart(t *testing.T, ctx context.Context, config taskProcessAuthorityConfig) *taskAuthorityProcess {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTaskProcessAuthorityChild$")
	command.Env = append(os.Environ(), "LEVARA_TASK_PROCESS_AUTHORITY="+string(raw))
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
	p := &taskAuthorityProcess{cmd: command, input: input, replies: make(chan taskProcessAuthorityReply, 8), exited: make(chan struct{})}
	if err := command.Start(); err != nil {
		input.Close()
		t.Fatal(err)
	}
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
			var reply taskProcessAuthorityReply
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
	go func() { <-drained; p.waitErr = command.Wait(); close(p.exited) }()
	t.Cleanup(func() { p.kill(t) })
	ready := p.receive(t, ctx)
	if ready.Error != "" || ready.Checkpoint != "ready" {
		t.Fatalf("task child readiness: %+v", ready)
	}
	return p
}
func (p *taskAuthorityProcess) send(t *testing.T, command taskProcessAuthorityCommand) {
	t.Helper()
	if err := json.NewEncoder(p.input).Encode(command); err != nil {
		t.Fatal(err)
	}
}
func (p *taskAuthorityProcess) receive(t *testing.T, ctx context.Context) taskProcessAuthorityReply {
	t.Helper()
	select {
	case reply := <-p.replies:
		return reply
	default:
	}
	select {
	case reply := <-p.replies:
		if reply.Error != "" {
			t.Fatal(reply.Error)
		}
		return reply
	case <-p.exited:
		select {
		case reply := <-p.replies:
			if reply.Error != "" {
				t.Fatal(reply.Error)
			}
			return reply
		default:
		}
		t.Fatalf("task child exited before response: %v", p.waitErr)
	case <-ctx.Done():
		t.Fatalf("task child response deadline: %v", ctx.Err())
	}
	return taskProcessAuthorityReply{}
}
func (p *taskAuthorityProcess) call(t *testing.T, ctx context.Context, command taskProcessAuthorityCommand) taskProcessAuthorityReply {
	t.Helper()
	p.send(t, command)
	return p.receive(t, ctx)
}
func (p *taskAuthorityProcess) kill(t *testing.T) error {
	t.Helper()
	p.killOnce.Do(func() {
		_ = p.cmd.Process.Kill()
		_ = p.input.Close()
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			t.Error("task child failed to exit after SIGKILL")
		}
	})
	return p.waitErr
}
func taskAuthorityConfig(t *testing.T, f *documentHTTPFixture) taskProcessAuthorityConfig {
	t.Helper()
	config := taskProcessAuthorityConfig{Dialect: GetDBProvider(), Workspace: t.TempDir()}
	config.Gate = filepath.Join(config.Workspace, "claim-release")
	f.db.SetMaxOpenConns(1)
	if config.Dialect == DBSQLite {
		rows, err := f.db.Query("PRAGMA database_list")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var seq int
			var name, file string
			if err := rows.Scan(&seq, &name, &file); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if name == "main" {
				config.SQLitePath = file
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if config.SQLitePath == "" {
			t.Fatal("task process fixture needs actual shared SQLite file")
		}
	} else {
		if err := f.db.QueryRow("SELECT current_database(),current_schema()").Scan(&config.Database, &config.Schema); err != nil {
			t.Fatal(err)
		}
	}
	return config
}
func taskAuthorityPayload(t *testing.T, result *mcppkg.ToolResult) map[string]any {
	t.Helper()
	if result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("task result failure: %+v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content[0].Text), &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
func taskAuthorityDenied(t *testing.T, reply taskProcessAuthorityReply) {
	t.Helper()
	if reply.Result == nil || !reply.Result.IsError {
		t.Fatalf("lease mutation unexpectedly accepted: %+v", reply)
	}
}
func taskAuthorityOpen(t *testing.T, ctx context.Context, deps mcppkg.Deps, key string) (string, int) {
	t.Helper()
	opened := taskAuthorityPayload(t, func() *mcppkg.ToolResult {
		r := mcppkg.ToolTaskOpen(ctx, deps, map[string]any{"collection": "task-process-fixture", "room": "task-runtime", "objective": "verify independent-process authority", "risk_level": "low", "idempotency_key": key, "definition_of_done": []any{map[string]any{"criterion_id": "artifact", "description": "actual fixture bytes match"}}})
		return &r
	}())
	return opened["task_id"].(string), int(opened["version"].(float64))
}
func taskAuthorityDate(t *testing.T, value any) time.Time {
	t.Helper()
	// Preserve PostgreSQL microseconds: timestampString formats PG to seconds.
	if instant, ok := value.(time.Time); ok {
		return instant.UTC()
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestampString(value))
	if err != nil {
		t.Fatalf("persisted lease timestamp=%v: %v", value, err)
	}
	return parsed
}

func TestTaskIndependentProcessLeaseWinnerAndNaturalReclaim(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
		defer cancel()
		config := taskAuthorityConfig(t, f)
		cfg := APIConfig{DB: f.db, RequireAuth: true, WorkspacePath: config.Workspace}
		verified := taskProcessAuthorityContext(ctx, cfg)
		deps := NewMCPDeps(cfg)
		taskID, version := taskAuthorityOpen(t, verified, deps, "process-lease")
		planned := mcppkg.ToolTaskPlan(verified, deps, map[string]any{"task_id": taskID, "base_version": version, "steps": []any{map[string]any{"step_id": "only", "description": "single native lease"}}})
		version = int(taskAuthorityPayload(t, &planned)["version"].(float64))
		contenders := []*taskAuthorityProcess{taskAuthorityStart(t, ctx, config), taskAuthorityStart(t, ctx, config)}
		actors := []string{"process-A", "process-B"}
		for i, p := range contenders {
			p.send(t, taskProcessAuthorityCommand{Op: "claim_gate", Args: map[string]any{"task_id": taskID, "step_id": "only", "action": "claim", "actor_id": actors[i], "base_version": version, "lease_seconds": 30}})
		}
		for _, p := range contenders {
			if reply := p.receive(t, ctx); reply.Checkpoint != "claim_waiting" {
				t.Fatalf("missing actual claim barrier: %+v", reply)
			}
		}
		if err := os.WriteFile(config.Gate, []byte("release"), 0600); err != nil {
			t.Fatal(err)
		}
		winner := -1
		for i, p := range contenders {
			reply := p.receive(t, ctx)
			if reply.Result == nil {
				t.Fatal("missing independent claim result")
			}
			if !reply.Result.IsError {
				if winner != -1 {
					t.Fatal("two independent processes claimed the same live step")
				}
				winner = i
				if got := int(taskAuthorityPayload(t, reply.Result)["version"].(float64)); got != version+1 {
					t.Fatalf("winning version %d want %d", got, version+1)
				}
			}
		}
		if winner < 0 {
			t.Fatal("no independent-process claim winner")
		}
		version++
		var actor, status string
		var persistedVersion, leases, attempts, claims int
		var expiryValue, createdValue any
		if err := f.db.QueryRow(Q("SELECT actor_id,expires_at,created_at FROM task_leases WHERE task_id=$1 AND step_id='only'"), taskID).Scan(&actor, &expiryValue, &createdValue); err != nil {
			t.Fatal(err)
		}
		expiry := taskAuthorityDate(t, expiryValue)
		created := taskAuthorityDate(t, createdValue)
		if actor != actors[winner] || expiry.Sub(created) < 30*time.Second-time.Microsecond {
			t.Fatal("winner/minimum natural lease not persisted")
		}
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM task_leases WHERE task_id=$1"), taskID).Scan(&leases); err != nil || leases != 1 {
			t.Fatalf("lease rows=%d error=%v", leases, err)
		}
		death := contenders[winner].kill(t)
		var exit *exec.ExitError
		if !errors.As(death, &exit) {
			t.Fatalf("winner did not die via SIGKILL: %v", death)
		}
		bits, ok := exit.Sys().(syscall.WaitStatus)
		if !ok || !bits.Signaled() || bits.Signal() != syscall.SIGKILL {
			t.Fatalf("unexpected winner death status %v", exit.Sys())
		}
		survivor := contenders[1-winner]
		// A live lease must remain exclusive even after its owning process dies.
		taskAuthorityDenied(t, survivor.call(t, ctx, taskProcessAuthorityCommand{Op: "step", Args: map[string]any{"task_id": taskID, "step_id": "only", "action": "claim", "actor_id": "replacement", "base_version": version}}))
		timer := time.NewTimer(time.Until(expiry) + time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		// Observe the same persisted expiry naturally elapsed; never UPDATE it.
		if err := f.db.QueryRow(Q("SELECT actor_id,expires_at FROM task_leases WHERE task_id=$1 AND step_id='only'"), taskID).Scan(&actor, &expiryValue); err != nil {
			t.Fatal(err)
		}
		if actor != actors[winner] || !taskAuthorityDate(t, expiryValue).Equal(expiry) || time.Now().Before(expiry) {
			t.Fatal("natural expiry checkpoint not observed")
		}
		reclaimed := survivor.call(t, ctx, taskProcessAuthorityCommand{Op: "step", Args: map[string]any{"task_id": taskID, "step_id": "only", "action": "claim", "actor_id": "replacement", "base_version": version, "lease_seconds": 30}})
		nextVersion := int(taskAuthorityPayload(t, reclaimed.Result)["version"].(float64))
		if nextVersion != version+1 {
			t.Fatalf("reclaim version=%d want%d", nextVersion, version+1)
		}
		version = nextVersion
		stale := taskAuthorityStart(t, ctx, config)
		for _, action := range []string{"renew", "pass"} {
			taskAuthorityDenied(t, stale.call(t, ctx, taskProcessAuthorityCommand{Op: "step", Args: map[string]any{"task_id": taskID, "step_id": "only", "action": action, "actor_id": actors[winner], "base_version": version}}))
		}
		taskAuthorityDenied(t, stale.call(t, ctx, taskProcessAuthorityCommand{Op: "receipt", Args: map[string]any{"task_id": taskID, "step_id": "only", "actor_id": actors[winner], "base_version": version, "idempotency_key": "stale-execution", "receipt_type": "observation", "status": "pass", "criterion_ids": []any{"artifact"}, "observation": "stale lease must not produce a passing receipt"}}))
		var staleReceipts int
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM task_receipts WHERE task_id=$1"), taskID).Scan(&staleReceipts); err != nil || staleReceipts != 0 {
			t.Fatalf("stale actor persisted receipt count=%d error=%v", staleReceipts, err)
		}
		taskAuthorityDenied(t, survivor.call(t, ctx, taskProcessAuthorityCommand{Op: "step", Args: map[string]any{"task_id": taskID, "step_id": "only", "action": "renew", "actor_id": "replacement", "base_version": version - 1}}))
		if err := f.db.QueryRow(Q("SELECT actor_id FROM task_leases WHERE task_id=$1 AND step_id='only'"), taskID).Scan(&actor); err != nil || actor != "replacement" {
			t.Fatalf("reclaimed lease actor %q error=%v", actor, err)
		}
		if err := f.db.QueryRow(Q("SELECT status,attempts FROM task_steps WHERE task_id=$1 AND id='only'"), taskID).Scan(&status, &attempts); err != nil || status != "active" || attempts != 2 {
			t.Fatalf("step status=%s attempts=%d error=%v", status, attempts, err)
		}
		if err := f.db.QueryRow(Q("SELECT version FROM tasks WHERE id=$1"), taskID).Scan(&persistedVersion); err != nil || persistedVersion != version {
			t.Fatalf("denial changed current version %d want%d error=%v", persistedVersion, version, err)
		}
		if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM task_events WHERE task_id=$1 AND event_type='step_claim'"), taskID).Scan(&claims); err != nil || claims != 2 {
			t.Fatalf("claim events=%d error=%v", claims, err)
		}
	})
}

type taskAuthorityLedger struct {
	Status, ReceiptID, Digest, Revision, ReceiptStatus     string
	Version, Receipts, Events, Candidates, Links, Promoted int
}

func taskAuthorityLedgerRead(t *testing.T, db *sql.DB, taskID string) taskAuthorityLedger {
	t.Helper()
	snapshot := taskAuthorityLedger{}
	if err := db.QueryRow(Q("SELECT status,version FROM tasks WHERE id=$1"), taskID).Scan(&snapshot.Status, &snapshot.Version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(Q("SELECT id,artifact_digest,workspace_revision,status FROM task_receipts WHERE task_id=$1"), taskID).Scan(&snapshot.ReceiptID, &snapshot.Digest, &snapshot.Revision, &snapshot.ReceiptStatus); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		Query  string
		Target *int
	}{
		{"SELECT COUNT(*) FROM task_receipts WHERE task_id=$1", &snapshot.Receipts},
		{"SELECT COUNT(*) FROM task_events WHERE task_id=$1", &snapshot.Events},
		{"SELECT COUNT(*) FROM task_memory_candidates WHERE task_id=$1", &snapshot.Candidates},
		{"SELECT COUNT(*) FROM task_memory_links WHERE task_id=$1", &snapshot.Links},
		{"SELECT COUNT(*) FROM memories WHERE source_task_id=$1", &snapshot.Promoted},
	} {
		if err := db.QueryRow(Q(item.Query), taskID).Scan(item.Target); err != nil {
			t.Fatal(err)
		}
	}
	return snapshot
}
func TestTaskIndependentProcessArtifactBytesGateCompletion(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		config := taskAuthorityConfig(t, f)
		cfg := APIConfig{DB: f.db, RequireAuth: true, WorkspacePath: config.Workspace}
		verified := taskProcessAuthorityContext(ctx, cfg)
		deps := NewMCPDeps(cfg)
		path := filepath.Join(config.Workspace, "projects", "alpha", "main", "proof.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		bytesA, bytesB := "real artifact A", "different artifact B"
		if err := os.WriteFile(path, []byte(bytesA), 0600); err != nil {
			t.Fatal(err)
		}
		digestA := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bytesA)))
		taskID, version := taskAuthorityOpen(t, verified, deps, "process-artifact")
		receipt := mcppkg.ToolTaskReceipt(verified, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "actual-A", "receipt_type": "artifact", "status": "pass", "criterion_ids": []any{"artifact"}, "evidence_uri": "file://" + path, "artifact_digest": digestA, "workspace_revision": "fixture-revision-A"})
		version = int(taskAuthorityPayload(t, &receipt)["version"].(float64))
		evaluator := taskAuthorityStart(t, ctx, config)
		validationArgs := map[string]any{"task_id": taskID, "mode": "completion"}
		positive := taskAuthorityPayload(t, evaluator.call(t, ctx, taskProcessAuthorityCommand{Op: "validate", Args: validationArgs}).Result)
		if positive["valid"] != true {
			t.Fatalf("actual-byte positive fixture invalid: %+v", positive)
		}
		before := taskAuthorityLedgerRead(t, f.db, taskID)
		mutator := taskAuthorityStart(t, ctx, config)
		replaced := mutator.call(t, ctx, taskProcessAuthorityCommand{Op: "replace_file", Text: bytesB})
		if replaced.Digest == digestA || replaced.Digest != fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bytesB))) {
			t.Fatal("separate process did not attest actual changed bytes")
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != bytesB {
			t.Fatalf("actual mutation=%q error=%v", actual, err)
		}
		invalid := taskAuthorityPayload(t, evaluator.call(t, ctx, taskProcessAuthorityCommand{Op: "validate", Args: validationArgs}).Result)
		if invalid["valid"] != false || !reflect.DeepEqual(invalid["failed_receipts"], []any{"artifact"}) {
			t.Fatalf("actual changed digest not rejected: %+v", invalid)
		}
		denied := taskAuthorityPayload(t, evaluator.call(t, ctx, taskProcessAuthorityCommand{Op: "complete", Args: map[string]any{"task_id": taskID, "expected_version": version, "actor_id": "completion-audit"}}).Result)
		if denied["ok"] != false {
			t.Fatalf("changed bytes completion accepted: %+v", denied)
		}
		if after := taskAuthorityLedgerRead(t, f.db, taskID); !reflect.DeepEqual(before, after) {
			t.Fatalf("failed completion mutated ledger: before=%+v after=%+v", before, after)
		}
		restored := mutator.call(t, ctx, taskProcessAuthorityCommand{Op: "replace_file", Text: bytesA})
		if restored.Digest != digestA {
			t.Fatal("actual restoration digest mismatch")
		}
		done := taskAuthorityPayload(t, evaluator.call(t, ctx, taskProcessAuthorityCommand{Op: "complete", Args: map[string]any{"task_id": taskID, "expected_version": version, "actor_id": "completion-audit"}}).Result)
		if done["ok"] != true || done["status"] != "completed" || int(done["version"].(float64)) != version+1 {
			t.Fatalf("restored actual-byte positive completion failed: %+v", done)
		}
		after := taskAuthorityLedgerRead(t, f.db, taskID)
		if after.Status != "completed" || after.Digest != digestA || after.ReceiptID != before.ReceiptID || after.Receipts != 1 {
			t.Fatalf("completion corrupted immutable receipt: %+v", after)
		}
	})
}

func taskPostHashCooperativeReplace(ctx context.Context, config taskProcessAuthorityConfig, command taskProcessAuthorityCommand, writer *json.Encoder) taskProcessAuthorityReply {
	if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "write_waiting"}); err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	release, err := workspace.LockProject(ctx, config.Workspace, "alpha")
	if err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	defer release()
	target := filepath.Join(config.Workspace, "projects", "alpha", "main", "proof.md")
	temp, err := os.CreateTemp(filepath.Dir(target), ".posthash-proof-*")
	if err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	defer os.Remove(temp.Name())
	if _, err = temp.WriteString(command.Text); err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temp.Name(), target)
	}
	if err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	actual, err := os.ReadFile(target)
	if err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	return taskProcessAuthorityReply{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(actual))}
}

type taskPostHashDeps struct {
	mcppkg.Deps
	verified chan struct{}
	resume   chan struct{}
	once     sync.Once
}

func (d *taskPostHashDeps) VerifyArtifact(ctx context.Context, evidenceURI, expectedDigest string) error {
	verifier, ok := d.Deps.(mcppkg.ArtifactVerifier)
	if !ok {
		return errors.New("production deps lacks actual artifact verifier")
	}
	if err := verifier.VerifyArtifact(ctx, evidenceURI, expectedDigest); err != nil {
		return err
	}
	// The checkpoint is after the production verifier has read and hashed A.
	d.once.Do(func() { close(d.verified) })
	select {
	case <-d.resume:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestTaskCompletionRetainsArtifactLockAfterRealHash(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		config := taskAuthorityConfig(t, f)
		cfg := APIConfig{DB: f.db, RequireAuth: true, WorkspacePath: config.Workspace}
		verified := taskProcessAuthorityContext(ctx, cfg)
		deps := NewMCPDeps(cfg)
		path := filepath.Join(config.Workspace, "projects", "alpha", "main", "proof.md")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		bytesA, bytesB := "post-hash original A", "post-hash replacement B"
		if err := os.WriteFile(path, []byte(bytesA), 0600); err != nil {
			t.Fatal(err)
		}
		digestA := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bytesA)))
		digestB := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(bytesB)))
		taskID, version := taskAuthorityOpen(t, verified, deps, "posthash-cooperative-lock")
		receipt := mcppkg.ToolTaskReceipt(verified, deps, map[string]any{
			"task_id": taskID, "base_version": version, "idempotency_key": "real-A",
			"receipt_type": "artifact", "status": "pass", "criterion_ids": []any{"artifact"},
			"evidence_uri": "file://" + path, "artifact_digest": digestA, "workspace_revision": "posthash-A",
		})
		version = int(taskAuthorityPayload(t, &receipt)["version"].(float64))
		// Child starts before completion holds any pool-1 SQL transaction.
		mutator := taskAuthorityStart(t, ctx, config)
		guard := &taskPostHashDeps{Deps: deps, verified: make(chan struct{}), resume: make(chan struct{})}
		var resumeOnce sync.Once
		resume := func() { resumeOnce.Do(func() { close(guard.resume) }) }
		defer resume()
		completed := make(chan mcppkg.ToolResult, 1)
		go func() {
			completed <- mcppkg.ToolTaskComplete(verified, guard, map[string]any{
				"task_id": taskID, "expected_version": version, "actor_id": "posthash-audit",
			})
		}()
		select {
		case <-guard.verified:
		case result := <-completed:
			t.Fatalf("completion exited before real hash checkpoint: %+v", result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		mutator.send(t, taskProcessAuthorityCommand{Op: "replace_file_locked", Text: bytesB})
		if reply := mutator.receive(t, ctx); reply.Checkpoint != "write_waiting" {
			t.Fatalf("missing native writer admission: %+v", reply)
		}
		// A real independent descriptor attempts the same native process lock.
		probeCtx, probeCancel := context.WithTimeout(ctx, 25*time.Millisecond)
		unlock, lockErr := workspace.LockProject(probeCtx, config.Workspace, "alpha")
		probeCancel()
		if lockErr == nil {
			unlock()
			// On the unfixed implementation, observe the child's actual B bytes
			// before allowing completion to commit; never substitute a fake sleep.
			replaced := mutator.receive(t, ctx)
			actual, err := os.ReadFile(path)
			if replaced.Digest != digestB || err != nil || string(actual) != bytesB {
				t.Fatalf("unfixed mutation not observed: %+v bytes=%q err=%v", replaced, actual, err)
			}
			resume()
			select {
			case result := <-completed:
				t.Fatalf("artifact project lock released after real hash; B replaced A before commit, completion=%+v", result)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if !errors.Is(lockErr, context.DeadlineExceeded) {
			t.Fatalf("unexpected native lock error: %v", lockErr)
		}
		select {
		case reply := <-mutator.replies:
			t.Fatalf("cooperative writer crossed retained lock: %+v", reply)
		default:
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != bytesA {
			t.Fatalf("artifact changed while completion guarded: %q %v", actual, err)
		}
		// Do not query pool-1 SQL while completion's transaction is held.
		resume()
		var result mcppkg.ToolResult
		select {
		case result = <-completed:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		done := taskAuthorityPayload(t, &result)
		if done["ok"] != true || done["status"] != "completed" || int(done["version"].(float64)) != version+1 {
			t.Fatalf("guarded native completion failed: %+v", done)
		}
		replaced := mutator.receive(t, ctx)
		actual, err = os.ReadFile(path)
		if replaced.Digest != digestB || err != nil || string(actual) != bytesB {
			t.Fatalf("writer did not proceed after commit: %+v bytes=%q err=%v", replaced, actual, err)
		}
		ledger := taskAuthorityLedgerRead(t, f.db, taskID)
		if ledger.Status != "completed" || ledger.Version != version+1 || ledger.Digest != digestA || ledger.Receipts != 1 {
			t.Fatalf("immutable completion evidence changed: %+v", ledger)
		}
		if f.db.Stats().InUse != 0 {
			t.Fatal("completion leaked native SQL connection")
		}
	})
}

func TestTaskCompletionCancellationReleasesArtifactGuard(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, mode := range []string{"cancel", "credential-expiry"} {
			t.Run(mode, func(t *testing.T) {
				config := taskAuthorityConfig(t, f)
				cfg := APIConfig{DB: f.db, RequireAuth: true, WorkspacePath: config.Workspace}
				parent, parentCancel := context.WithTimeout(context.Background(), 6*time.Second)
				defer parentCancel()
				ctx, cancel := context.WithCancel(parent)
				defer cancel()
				verified := taskProcessAuthorityContext(ctx, cfg)
				if mode == "credential-expiry" {
					e := verified.Value(searchEgressKey{}).(searchEgress)
					e.expiresAt = time.Now().Add(2 * time.Second).Unix()
					verified = context.WithValue(verified, searchEgressKey{}, e)
				}
				deps := NewMCPDeps(cfg)
				file := filepath.Join(config.Workspace, "projects", "alpha", "main", "proof.md")
				if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
					t.Fatal(err)
				}
				data := "cancellation artifact"
				if err := os.WriteFile(file, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
				digest := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(data)))
				taskID, version := taskAuthorityOpen(t, verified, deps, "cancel-"+mode)
				receipt := mcppkg.ToolTaskReceipt(verified, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "A", "receipt_type": "artifact", "status": "pass", "criterion_ids": []any{"artifact"}, "workspace_revision": "A", "evidence_uri": "file://" + file, "artifact_digest": digest})
				version = int(taskAuthorityPayload(t, &receipt)["version"].(float64))
				before := taskAuthorityLedgerRead(t, f.db, taskID)
				guard := &taskPostHashDeps{Deps: deps, verified: make(chan struct{}), resume: make(chan struct{})}
				done := make(chan mcppkg.ToolResult, 1)
				go func() {
					done <- mcppkg.ToolTaskComplete(verified, guard, map[string]any{"task_id": taskID, "expected_version": version})
				}()
				select {
				case <-guard.verified:
				case result := <-done:
					t.Fatalf("completion ended before actual hash: %+v", result)
				case <-parent.Done():
					t.Fatal(parent.Err())
				}
				if mode == "cancel" {
					cancel()
				}
				select {
				case result := <-done:
					if !result.IsError && taskAuthorityPayload(t, &result)["ok"] != false {
						t.Fatalf("cancelled/expired completion accepted: %+v", result)
					}
				case <-parent.Done():
					t.Fatal("completion did not release after cancellation or credential expiry")
				}
				checkCtx, checkCancel := context.WithTimeout(context.Background(), time.Second)
				defer checkCancel()
				connection, err := f.db.Conn(checkCtx)
				if err != nil {
					t.Fatalf("pool1 not released: %v", err)
				}
				connection.Close()
				release, err := workspace.LockProject(checkCtx, config.Workspace, "alpha")
				if err != nil {
					t.Fatalf("artifact guard leaked: %v", err)
				}
				release()
				if after := taskAuthorityLedgerRead(t, f.db, taskID); !reflect.DeepEqual(before, after) {
					t.Fatalf("cancelled completion changed ledger: before=%+v after=%+v", before, after)
				}
				actual, err := os.ReadFile(file)
				if err != nil || string(actual) != data {
					t.Fatal("cancelled completion changed artifact")
				}
				close(guard.resume)
				positiveCtx := taskProcessAuthorityContext(context.Background(), cfg)
				retry := mcppkg.ToolTaskComplete(positiveCtx, deps, map[string]any{"task_id": taskID, "expected_version": version})
				if taskAuthorityPayload(t, &retry)["ok"] != true {
					t.Fatalf("fresh authority retry failed: %+v", retry)
				}
			})
		}
	})
}

func TestTaskCompletedReplayRechecksAuthorityNative(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cfg := APIConfig{DB: f.db, RequireAuth: true}
		verified := taskProcessAuthorityContext(ctx, cfg)
		deps := NewMCPDeps(cfg)
		taskID, version := taskAuthorityOpen(t, verified, deps, "terminal-authority")
		receipt := mcppkg.ToolTaskReceipt(verified, deps, map[string]any{"task_id": taskID, "base_version": version, "idempotency_key": "observed", "receipt_type": "observation", "status": "pass", "criterion_ids": []any{"artifact"}})
		version = int(taskAuthorityPayload(t, &receipt)["version"].(float64))
		result := mcppkg.ToolTaskComplete(verified, deps, map[string]any{"task_id": taskID, "expected_version": version})
		taskAuthorityPayload(t, &result)
		before := taskAuthorityLedgerRead(t, f.db, taskID)
		f.exec("UPDATE users SET is_active=FALSE WHERE id='owner'")
		result = mcppkg.ToolTaskComplete(verified, deps, map[string]any{"task_id": taskID, "expected_version": version})
		if !result.IsError {
			t.Fatalf("revoked terminal replay returned success: %+v", result)
		}
		f.exec("UPDATE users SET is_active=TRUE WHERE id='owner'")
		if after := taskAuthorityLedgerRead(t, f.db, taskID); !reflect.DeepEqual(before, after) {
			t.Fatal("revoked replay changed immutable ledger")
		}
		result = mcppkg.ToolTaskComplete(verified, deps, map[string]any{"task_id": taskID, "expected_version": version})
		positive := taskAuthorityPayload(t, &result)
		if positive["ok"] != true || positive["already_completed"] != true || int(positive["version"].(float64)) != before.Version {
			t.Fatalf("live terminal replay failed: %+v", positive)
		}
	})
}

type taskLocalSerializationDeps struct {
	mcppkg.Deps
	config  taskProcessAuthorityConfig
	writer  *json.Encoder
	once    sync.Once
	gateErr error
}

func (d *taskLocalSerializationDeps) VerifyArtifact(ctx context.Context, uri, digest string) error {
	v, ok := d.Deps.(mcppkg.ArtifactVerifier)
	if !ok {
		return errors.New("native artifact verifier unavailable")
	}
	if err := v.VerifyArtifact(ctx, uri, digest); err != nil {
		return err
	}
	d.once.Do(func() {
		// Production hashing has acquired the actual retained first project guard.
		project := filepath.Base(filepath.Dir(filepath.Dir(strings.TrimPrefix(uri, "file://"))))
		if project != d.config.FirstProject {
			d.gateErr = fmt.Errorf("first actual project %s want %s", project, d.config.FirstProject)
			return
		}
		if err := d.writer.Encode(taskProcessAuthorityReply{Checkpoint: "first_hash_" + project}); err != nil {
			d.gateErr = err
			return
		}
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(d.config.Gate); err == nil {
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				d.gateErr = err
				return
			}
			select {
			case <-ctx.Done():
				d.gateErr = ctx.Err()
				return
			case <-ticker.C:
			}
		}
	})
	return d.gateErr
}

func taskLocalSerializedComplete(ctx context.Context, config taskProcessAuthorityConfig, cfg APIConfig, deps mcppkg.Deps, command taskProcessAuthorityCommand, writer *json.Encoder) taskProcessAuthorityReply {
	if !config.TrustedLocal || cfg.RequireAuth {
		return taskProcessAuthorityReply{Error: "local serialization child was not configured trusted local"}
	}
	if err := writer.Encode(taskProcessAuthorityReply{Checkpoint: "completion_started"}); err != nil {
		return taskProcessAuthorityReply{Error: err.Error()}
	}
	// Owner is the fixed private fixture identity, never taken from actor_id.
	local := context.WithValue(ctx, mcppkg.UserIDKey, "owner")
	d := &taskLocalSerializationDeps{Deps: deps, config: config, writer: writer}
	result := mcppkg.ToolTaskComplete(local, d, command.Args)
	return taskProcessAuthorityReply{Result: &result}
}

func TestTaskTrustedLocalCompletionSerializesReverseProjects(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		config := taskAuthorityConfig(t, f)
		config.TrustedLocal = true
		cfg := APIConfig{DB: f.db, RequireAuth: false, WorkspacePath: config.Workspace}
		deps := NewMCPDeps(cfg)
		local := context.WithValue(ctx, mcppkg.UserIDKey, "owner")
		paths, digests := map[string]string{}, map[string]string{}
		for _, project := range []string{"alpha", "beta"} {
			paths[project] = filepath.Join(config.Workspace, "projects", project, "main", "proof.md")
			if err := os.MkdirAll(filepath.Dir(paths[project]), 0700); err != nil {
				t.Fatal(err)
			}
			content := []byte("native reverse-project evidence " + project)
			if err := os.WriteFile(paths[project], content, 0600); err != nil {
				t.Fatal(err)
			}
			digests[project] = fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		}
		open := func(key, first, second string) (string, int) {
			opened := mcppkg.ToolTaskOpen(local, deps, map[string]any{
				"collection": "task-process-fixture", "room": "task-runtime", "objective": "serialize native reverse-project completion", "risk_level": "low", "idempotency_key": key,
				"definition_of_done": []any{map[string]any{"criterion_id": "a-first", "description": "first real artifact"}, map[string]any{"criterion_id": "z-second", "description": "second real artifact"}},
			})
			payload := taskAuthorityPayload(t, &opened)
			id, version := payload["task_id"].(string), int(payload["version"].(float64))
			for i, project := range []string{first, second} {
				criterion := []string{"a-first", "z-second"}[i]
				receipt := mcppkg.ToolTaskReceipt(local, deps, map[string]any{
					"task_id": id, "base_version": version, "idempotency_key": project,
					"receipt_type": "artifact", "status": "pass", "criterion_ids": []any{criterion},
					"evidence_uri": "file://" + paths[project], "artifact_digest": digests[project], "workspace_revision": "local-reverse-fixture",
				})
				version = int(taskAuthorityPayload(t, &receipt)["version"].(float64))
			}
			return id, version
		}
		idA, versionA := open("local-alpha-beta", "alpha", "beta")
		idB, versionB := open("local-beta-alpha", "beta", "alpha")
		// The independent probe has its own native pool-1 connection; it never
		// borrows the fixture connection while either completion holds SQL.
		var probe *sql.DB
		if config.Dialect == DBSQLite {
			var err error
			probe, err = sql.Open("sqlite3", "file:"+config.SQLitePath+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
		} else {
			pg, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
			if err != nil {
				t.Fatal(err)
			}
			pg.Database = config.Database
			pg.RuntimeParams["search_path"] = config.Schema
			probe = stdlib.OpenDB(*pg)
		}
		probe.SetMaxOpenConns(1)
		defer probe.Close()
		writeProbe := func(probeCtx context.Context) error {
			tx, err := probe.BeginTx(probeCtx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			statement := "UPDATE tasks SET id=id WHERE 1=0"
			if config.Dialect != DBSQLite {
				statement = "LOCK TABLE memories IN SHARE ROW EXCLUSIVE MODE"
			}
			_, err = tx.ExecContext(probeCtx, statement)
			return err
		}
		if err := writeProbe(ctx); err != nil {
			t.Fatalf("native writer probe positive control: %v", err)
		}
		configA, configB := config, config
		configA.FirstProject, configB.FirstProject = "alpha", "beta"
		configA.Gate = filepath.Join(config.Workspace, "release-alpha-beta")
		configB.Gate = filepath.Join(config.Workspace, "release-beta-alpha")
		// Start both independent processes before the first completion takes SQL.
		a, b := taskAuthorityStart(t, ctx, configA), taskAuthorityStart(t, ctx, configB)
		release := func(path string) {
			if err := os.WriteFile(path, []byte("release"), 0600); err != nil {
				t.Error(err)
			}
		}
		defer release(configA.Gate)
		defer release(configB.Gate)
		a.send(t, taskProcessAuthorityCommand{Op: "complete_local_serialized", Args: map[string]any{"task_id": idA, "expected_version": versionA}})
		if reply := a.receive(t, ctx); reply.Checkpoint != "completion_started" {
			t.Fatalf("A admission: %+v", reply)
		}
		if reply := a.receive(t, ctx); reply.Checkpoint != "first_hash_alpha" {
			t.Fatalf("A actual first project guard: %+v", reply)
		}
		b.send(t, taskProcessAuthorityCommand{Op: "complete_local_serialized", Args: map[string]any{"task_id": idB, "expected_version": versionB}})
		if reply := b.receive(t, ctx); reply.Checkpoint != "completion_started" {
			t.Fatalf("B concurrent admission: %+v", reply)
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, 25*time.Millisecond)
		probeErr := writeProbe(probeCtx)
		probeCancel()
		if probeErr == nil {
			t.Fatal("first local completion holds an artifact guard without native SQL writer reservation")
		}
		if !errors.Is(probeErr, context.DeadlineExceeded) && !strings.Contains(strings.ToLower(probeErr.Error()), "locked") && !strings.Contains(strings.ToLower(probeErr.Error()), "busy") {
			t.Fatalf("unexpected native writer contention: %v", probeErr)
		}
		select {
		case reply := <-b.replies:
			t.Fatalf("second completion acquired reverse project before SQL serialization: %+v", reply)
		default:
		}
		release(configA.Gate)
		doneA := taskAuthorityPayload(t, a.receive(t, ctx).Result)
		if doneA["ok"] != true || doneA["status"] != "completed" || int(doneA["version"].(float64)) != versionA+1 {
			t.Fatalf("A did not complete both artifacts: %+v", doneA)
		}
		if reply := b.receive(t, ctx); reply.Checkpoint != "first_hash_beta" {
			t.Fatalf("B actual reverse first guard after A commit: %+v", reply)
		}
		release(configB.Gate)
		doneB := taskAuthorityPayload(t, b.receive(t, ctx).Result)
		if doneB["ok"] != true || doneB["status"] != "completed" || int(doneB["version"].(float64)) != versionB+1 {
			t.Fatalf("B did not complete both artifacts: %+v", doneB)
		}
		if err := writeProbe(ctx); err != nil {
			t.Fatalf("native writer reservation leaked after completion: %v", err)
		}
		for _, id := range []string{idA, idB} {
			var status string
			var receipts int
			if err := f.db.QueryRow(Q("SELECT status FROM tasks WHERE id=$1"), id).Scan(&status); err != nil || status != "completed" {
				t.Fatalf("native terminal status=%s err=%v", status, err)
			}
			if err := f.db.QueryRow(Q("SELECT COUNT(*) FROM task_receipts WHERE task_id=$1 AND status='pass'"), id).Scan(&receipts); err != nil || receipts != 2 {
				t.Fatalf("immutable real-artifact receipts=%d err=%v", receipts, err)
			}
		}
		if f.db.Stats().InUse != 0 || probe.Stats().InUse != 0 {
			t.Fatal("local serialization leaked native SQL connection")
		}
	})
}
