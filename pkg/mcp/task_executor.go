package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const taskActionMaxBytes = 64 << 10

// TaskAction is a declarative, bounded tool call; descriptions never execute.
type TaskAction struct {
	Kind       string          `json:"kind"`
	Name       string          `json:"name"`
	Arguments  map[string]any  `json:"arguments"`
	Assertions []TaskAssertion `json:"assertions"`
}
type TaskAssertion struct {
	Pointer string          `json:"pointer"`
	Equals  json.RawMessage `json:"equals"`
}

func parseTaskAction(data []byte) (TaskAction, error) {
	var a TaskAction
	if len(data) > taskActionMaxBytes {
		return a, errors.New("action exceeds 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&a); err != nil {
		return a, fmt.Errorf("invalid action: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return a, errors.New("action must be a single JSON object")
	}
	if a.Kind != "mcp_tool" || a.Name == "" || a.Arguments == nil || len(a.Assertions) == 0 || len(a.Assertions) > 16 {
		return a, errors.New("action requires kind=mcp_tool, name, arguments object and 1-16 assertions")
	}
	var fields struct {
		Assertions []map[string]json.RawMessage `json:"assertions"`
	}
	if err := json.Unmarshal(data, &fields); err != nil {
		return a, err
	}
	for _, assertion := range fields.Assertions {
		if raw, present := assertion["pointer"]; !present || len(raw) == 0 || raw[0] != '"' {
			return a, errors.New("assertion pointer is required")
		}
	}
	for _, as := range a.Assertions {
		if _, err := taskPointerParts(as.Pointer); err != nil {
			return a, err
		}
		if len(as.Equals) == 0 {
			return a, errors.New("assertion equals is required")
		}
	}
	return a, nil
}

func taskPointerParts(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("assertion pointer must be an RFC 6901 JSON pointer")
	}
	parts := strings.Split(pointer[1:], "/")
	for i, p := range parts {
		for j := 0; j < len(p); j++ {
			if p[j] == '~' {
				if j+1 >= len(p) || (p[j+1] != '0' && p[j+1] != '1') {
					return nil, errors.New("invalid JSON pointer escape")
				}
				j++
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

func checkTaskAssertions(a TaskAction, output []byte) error {
	var value any
	d := json.NewDecoder(bytes.NewReader(output))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return errors.New("tool output is not JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("tool output is not a single JSON value")
	}
	for _, assertion := range a.Assertions {
		parts, _ := taskPointerParts(assertion.Pointer)
		got := value
		for _, part := range parts {
			switch x := got.(type) {
			case map[string]any:
				var exists bool
				got, exists = x[part]
				if !exists {
					return fmt.Errorf("assertion path missing: %s", assertion.Pointer)
				}
			case []any:
				i, err := strconv.Atoi(part)
				if err != nil || i < 0 || i >= len(x) || strconv.Itoa(i) != part {
					return fmt.Errorf("invalid assertion array index: %s", assertion.Pointer)
				}
				got = x[i]
			default:
				return fmt.Errorf("assertion path missing: %s", assertion.Pointer)
			}
		}
		var expected any
		d := json.NewDecoder(bytes.NewReader(assertion.Equals))
		d.UseNumber()
		if err := d.Decode(&expected); err != nil {
			return err
		}
		if !reflect.DeepEqual(got, expected) {
			return fmt.Errorf("assertion mismatch: %s", assertion.Pointer)
		}
	}
	return nil
}

type taskLeaseContextKey struct{}
type taskLeaseIdentity struct{ task, step, actor string }
type taskExecutionContextKey struct{}

// TaskExecution carries the persisted action and the identity of this lease
// attempt. ActorID fences execution; OwnerID alone supplies authorization.
type TaskExecution struct {
	TaskID, StepID, ActorID, OwnerID string
	AuthorityJSON                    string
	Action                           TaskAction
	Attempt                          int
	criteria                         []string
	actionJSON                       string
	deps                             Deps
}

// TaskExecutionFromContext is only present on calls issued by the executor.
func TaskExecutionFromContext(ctx context.Context) *TaskExecution {
	e, _ := ctx.Value(taskExecutionContextKey{}).(*TaskExecution)
	return e
}

// Fence serializes the actual effect with lease transitions and task changes.
// fn must use tx for SQL reads: opening another connection can deadlock a
// single-connection SQLite pool. Filesystem effects are bounded by the adapter.
func (e *TaskExecution) Fence(ctx context.Context, fn func(*sql.Tx) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if taskOwner(ctx) == "" || taskOwner(ctx) != e.OwnerID {
		return errors.New("task execution owner mismatch")
	}
	tx, err := e.deps.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Lock the lease before tasks, matching the public step transition order.
	result, err := tx.ExecContext(ctx, e.deps.Q(`UPDATE task_leases SET actor_id=actor_id WHERE task_id=$1 AND step_id=$2 AND actor_id=$3`), e.TaskID, e.StepID, e.ActorID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("task execution lease replaced")
	}
	result, err = tx.ExecContext(ctx, e.deps.Q(`UPDATE tasks SET version=version WHERE id=$1 AND owner_id=$2`), e.TaskID, e.OwnerID)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil || n != 1 {
		return errors.New("task execution owner changed")
	}
	var action, authority string
	var attempts int
	err = tx.QueryRowContext(ctx, e.deps.Q(`SELECT s.action_json,t.authority_json,s.attempts FROM tasks t
 JOIN task_steps s ON s.task_id=t.id JOIN task_leases l ON l.task_id=t.id AND l.step_id=s.id
 WHERE t.id=$1 AND s.id=$2 AND t.owner_id=$3 AND l.actor_id=$4 AND l.expires_at>$5
 AND s.status='active' AND t.status='running'`), e.TaskID, e.StepID, e.OwnerID, e.ActorID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&action, &authority, &attempts)
	if err != nil || action != e.actionJSON || authority != e.AuthorityJSON || attempts != e.Attempt {
		return errors.New("task execution authority or live lease changed")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// TaskToolDispatcher is implemented by the server adapter, which enforces
// manifest, profile, principal and actual file access before returning bytes.
type TaskToolDispatcher func(context.Context, *TaskExecution) (ToolResult, error)
type taskExecutor struct {
	deps     Deps
	dispatch TaskToolDispatcher
}

func NewTaskExecutor(deps Deps, dispatch TaskToolDispatcher) TaskStepExecutor {
	return &taskExecutor{deps: deps, dispatch: dispatch}
}

func (r *taskExecutor) ExecuteStep(ctx context.Context, taskID, stepID, description string, deadline time.Time) (bool, string, error) {
	if r.deps.DB() == nil || r.dispatch == nil {
		return false, "", errors.New("task executor not configured")
	}
	lease, ok := ctx.Value(taskLeaseContextKey{}).(taskLeaseIdentity)
	if !ok || lease.task != taskID || lease.step != stepID || lease.actor == "" || taskOwner(ctx) == "" {
		return false, "", errors.New("authenticated owner and claimed lease context required")
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	e := &TaskExecution{TaskID: taskID, StepID: stepID, ActorID: lease.actor, OwnerID: taskOwner(ctx), deps: r.deps}
	var criteria string
	var leaseExpiry any
	err := r.deps.DB().QueryRowContext(ctx, r.deps.Q(`SELECT t.authority_json,s.action_json,s.attempts,s.criterion_ids_json,l.expires_at FROM tasks t
 JOIN task_steps s ON s.task_id=t.id JOIN task_leases l ON l.task_id=t.id AND l.step_id=s.id
 WHERE t.id=$1 AND s.id=$2 AND t.owner_id=$3 AND l.actor_id=$4 AND l.expires_at>$5 AND t.status='running' AND s.status='active'`), taskID, stepID, e.OwnerID, e.ActorID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&e.AuthorityJSON, &e.actionJSON, &e.Attempt, &criteria, &leaseExpiry)
	if err != nil {
		return false, "", errors.New("task execution lease or owner unavailable")
	}
	expires, err := taskExecutionExpiry(leaseExpiry)
	if err != nil {
		return false, "", err
	}
	ctx, leaseCancel := context.WithDeadline(ctx, expires)
	defer leaseCancel()
	e.Action, err = parseTaskAction([]byte(e.actionJSON))
	if err != nil {
		return false, "", err
	}
	if err = json.Unmarshal([]byte(criteria), &e.criteria); err != nil || len(e.criteria) == 0 {
		return false, "", errors.New("executable step requires criterion_ids")
	}
	var policy workerTaskPolicy
	if err = json.Unmarshal([]byte(e.AuthorityJSON), &policy); err != nil || !policy.AutoRun {
		return false, "", errors.New("auto_run authority required")
	}
	allowed := false
	for _, name := range policy.AllowedTools {
		if name == e.Action.Name {
			allowed = true
		}
	}
	if !allowed {
		return false, "", errors.New("tool absent from task allowed_tools")
	}
	ctx = context.WithValue(ctx, taskExecutionContextKey{}, e)
	result, err := r.dispatch(ctx, e)
	if err != nil {
		return false, "", err
	}
	if ctx.Err() != nil {
		return false, "", ctx.Err()
	}
	if result.IsError {
		return false, "", errors.New("task tool returned an error")
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		return false, "", errors.New("task tool must return one JSON text result")
	}
	output := []byte(result.Content[0].Text)
	if len(output) > 1<<20 {
		return false, "", errors.New("task output exceeds 1 MiB")
	}
	assertionErr := checkTaskAssertions(e.Action, output)
	status := "pass"
	observation := "tool output matched all assertions"
	if assertionErr != nil {
		status = "fail"
		observation = assertionErr.Error()
	}
	var version int
	if err = r.deps.DB().QueryRowContext(ctx, r.deps.Q(`SELECT version FROM tasks WHERE id=$1 AND owner_id=$2`), taskID, e.OwnerID).Scan(&version); err != nil {
		return false, "", err
	}
	sum := sha256.Sum256([]byte(e.actionJSON))
	outSum := sha256.Sum256(output)
	receipt := ToolTaskReceipt(ctx, r.deps, map[string]any{"task_id": taskID, "step_id": stepID, "actor_id": e.ActorID, "base_version": version,
		"idempotency_key": fmt.Sprintf("executor:%s:%d:%s", stepID, e.Attempt, hex.EncodeToString(sum[:])), "receipt_type": "observation", "status": status, "criterion_ids": stringsToAny(e.criteria), "observation": observation,
		"metadata": map[string]any{"step_id": stepID, "lease_actor": e.ActorID, "attempt": e.Attempt, "tool": e.Action.Name, "action_sha256": hex.EncodeToString(sum[:]), "output_sha256": hex.EncodeToString(outSum[:]), "output": json.RawMessage(output)}})
	if receipt.IsError {
		return false, "", errors.New("execution receipt rejected")
	}
	return assertionErr == nil, observation, assertionErr
}
func stringsToAny(items []string) []any {
	out := make([]any, len(items))
	for i, s := range items {
		out[i] = s
	}
	return out
}

func taskExecutionExpiry(value any) (time.Time, error) {
	if t, ok := value.(time.Time); ok {
		return t, nil
	}
	text, ok := value.(string)
	if !ok {
		return time.Time{}, errors.New("invalid task lease expiry")
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07"} {
		if t, err := time.Parse(layout, text); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("invalid task lease expiry")
}
