package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	taskDefaultBootstrapTokens = 600
	taskMaxLeaseSeconds        = 3600
)

var absoluteDateRE = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func intArg(args map[string]any, key string, fallback int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return fallback
	}
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key].(bool)
	if !ok {
		return fallback
	}
	return v
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		if values, ok := args[key].([]string); ok {
			return values
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func jsonArg(args map[string]any, key string, fallback any) string {
	v, ok := args[key]
	if !ok {
		v = fallback
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func taskOwner(ctx context.Context) string { return extractOwnerID(ctx) }

func taskActor(ctx context.Context, args map[string]any) string {
	if actor := stringArg(args, "actor_id"); actor != "" {
		return actor
	}
	if owner := taskOwner(ctx); owner != "" {
		return owner
	}
	return "anonymous"
}

func taskEvent(ctx context.Context, exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, rewrite func(string) string, taskID, actor, eventType string, payload any) error {
	b, _ := json.Marshal(payload)
	_, err := exec.ExecContext(ctx, rewrite(`INSERT INTO task_events(id,task_id,actor_id,event_type,payload_json,created_at)
		VALUES($1,$2,$3,$4,$5,$6)`), uuid.NewString(), taskID, actor, eventType, string(b), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// ToolTaskOpen creates a task once per owner/collection/idempotency key.
func ToolTaskOpen(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, room, objective := stringArg(args, "collection"), stringArg(args, "room"), stringArg(args, "objective")
	idempotency := stringArg(args, "idempotency_key")
	if collection == "" || room == "" || objective == "" || idempotency == "" {
		return toolError("'collection', 'room', 'objective', and 'idempotency_key' required")
	}
	risk := stringArg(args, "risk_level")
	if risk == "" {
		risk = "medium"
	}
	if risk != "low" && risk != "medium" && risk != "high" {
		return toolError("risk_level must be low, medium, or high")
	}
	db := deps.DB()
	if db == nil {
		return toolError("database not configured")
	}
	owner, now, taskID := taskOwner(ctx), time.Now().UTC().Format(time.RFC3339Nano), uuid.NewString()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO tasks
		(id,idempotency_key,owner_id,collection_name,room,objective,authority_json,risk_level,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'draft',1,$9,$10)
		ON CONFLICT(owner_id,collection_name,idempotency_key) DO NOTHING`),
		taskID, idempotency, owner, collection, room, objective, jsonArg(args, "authority", map[string]any{}), risk, now, now)
	if err != nil {
		return toolError(err.Error())
	}
	var status string
	var version int
	err = tx.QueryRowContext(ctx, deps.Q(`SELECT id,status,version FROM tasks
		WHERE owner_id=$1 AND collection_name=$2 AND idempotency_key=$3`), owner, collection, idempotency).
		Scan(&taskID, &status, &version)
	if err != nil {
		return toolError(err.Error())
	}
	var criterionCount int
	_ = tx.QueryRowContext(ctx, deps.Q(`SELECT COUNT(*) FROM task_criteria WHERE task_id=$1`), taskID).Scan(&criterionCount)
	if criterionCount == 0 {
		criteria, _ := args["definition_of_done"].([]any)
		for i, raw := range criteria {
			item, ok := raw.(map[string]any)
			if !ok {
				return toolError(fmt.Sprintf("definition_of_done[%d] must be an object", i))
			}
			description := stringArg(item, "description")
			if description == "" {
				return toolError(fmt.Sprintf("definition_of_done[%d].description required", i))
			}
			criterionID := stringArg(item, "criterion_id")
			if criterionID == "" {
				criterionID = uuid.NewString()
			}
			_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO task_criteria
				(id,task_id,description,required,verification_json,created_at) VALUES($1,$2,$3,$4,$5,$6)`),
				criterionID, taskID, description, boolArg(item, "required", true), jsonArg(item, "verification", map[string]any{}), now)
			if err != nil {
				return toolError(err.Error())
			}
		}
		if len(criteria) > 0 {
			status = "planned"
			_, _ = tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET status='planned',version=version+1,updated_at=$1 WHERE id=$2`), now, taskID)
			version++
		}
		_ = taskEvent(ctx, tx, deps.Q, taskID, taskActor(ctx, args), "task_opened", map[string]any{"objective": objective, "risk_level": risk})
	}
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(map[string]any{"task_id": taskID, "status": status, "version": version, "collection": collection, "room": room})
}

// ToolTaskPlan replaces only pending plan rows and protects active history.
func ToolTaskPlan(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID := stringArg(args, "task_id")
	baseVersion := intArg(args, "base_version", 0)
	steps, ok := args["steps"].([]any)
	if taskID == "" || baseVersion < 1 || !ok || len(steps) == 0 {
		return toolError("'task_id', positive 'base_version', and non-empty 'steps' required")
	}
	db := deps.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	owner := taskOwner(ctx)
	var current int
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT version FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, owner).Scan(&current); err != nil {
		return toolError("task not found")
	}
	if current != baseVersion {
		return toolError(fmt.Sprintf("version conflict: current=%d", current))
	}
	var protected int
	_ = tx.QueryRowContext(ctx, deps.Q(`SELECT COUNT(*) FROM task_steps WHERE task_id=$1 AND status<>'pending'`), taskID).Scan(&protected)
	if protected > 0 {
		return toolError("cannot replace plan after a step has started")
	}
	_, _ = tx.ExecContext(ctx, deps.Q(`DELETE FROM task_steps WHERE task_id=$1`), taskID)
	ids := map[string]bool{}
	dependencyGraph := map[string][]string{}
	for i, raw := range steps {
		item, ok := raw.(map[string]any)
		if !ok {
			return toolError(fmt.Sprintf("steps[%d] must be an object", i))
		}
		id := stringArg(item, "step_id")
		if id == "" {
			id = uuid.NewString()
		}
		if ids[id] {
			return toolError("duplicate step_id: " + id)
		}
		ids[id] = true
		item["step_id"] = id
	}
	for _, raw := range steps {
		item := raw.(map[string]any)
		id := stringArg(item, "step_id")
		dependencyGraph[id] = stringSliceArg(item, "dependencies")
		for _, dep := range dependencyGraph[id] {
			if !ids[dep] || dep == id {
				return toolError(fmt.Sprintf("invalid dependency %q for step %q", dep, id))
			}
		}
	}
	if hasDependencyCycle(dependencyGraph) {
		return toolError("step dependencies must form an acyclic graph")
	}
	for i, raw := range steps {
		item := raw.(map[string]any)
		id, description := stringArg(item, "step_id"), stringArg(item, "description")
		if description == "" {
			return toolError(fmt.Sprintf("steps[%d].description required", i))
		}
		_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO task_steps
			(id,task_id,description,status,required,dependencies_json,criterion_ids_json,attempts,position,created_at,updated_at)
			VALUES($1,$2,$3,'pending',$4,$5,$6,0,$7,$8,$9)`), id, taskID, description,
			boolArg(item, "required", true), jsonArg(item, "dependencies", []string{}), jsonArg(item, "criterion_ids", []string{}),
			i, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return toolError(err.Error())
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET status='planned',version=version+1,updated_at=$1 WHERE id=$2 AND version=$3`), now, taskID, baseVersion)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("version conflict")
	}
	_ = taskEvent(ctx, tx, deps.Q, taskID, taskActor(ctx, args), "plan_updated", map[string]any{"steps": len(steps)})
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(map[string]any{"ok": true, "task_id": taskID, "status": "planned", "version": baseVersion + 1, "steps": len(steps)})
}

func hasDependencyCycle(graph map[string][]string) bool {
	const (
		visiting = 1
		visited  = 2
	)
	state := make(map[string]int, len(graph))
	var visit func(string) bool
	visit = func(node string) bool {
		if state[node] == visiting {
			return true
		}
		if state[node] == visited {
			return false
		}
		state[node] = visiting
		for _, dependency := range graph[node] {
			if visit(dependency) {
				return true
			}
		}
		state[node] = visited
		return false
	}
	for node := range graph {
		if visit(node) {
			return true
		}
	}
	return false
}

// ToolTaskStep performs atomic lease and step-state transitions.
func ToolTaskStep(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID, stepID, action := stringArg(args, "task_id"), stringArg(args, "step_id"), stringArg(args, "action")
	baseVersion, actor := intArg(args, "base_version", 0), taskActor(ctx, args)
	if taskID == "" || stepID == "" || baseVersion < 1 {
		return toolError("'task_id', 'step_id', and positive 'base_version' required")
	}
	validActions := map[string]bool{"claim": true, "renew": true, "release": true, "pass": true, "fail": true}
	if !validActions[action] {
		return toolError("action must be claim, renew, release, pass, or fail")
	}
	db := deps.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT version FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, taskOwner(ctx)).Scan(&version); err != nil {
		return toolError("task not found")
	}
	if version != baseVersion {
		return toolError(fmt.Sprintf("version conflict: current=%d", version))
	}
	now := time.Now().UTC()
	if action == "claim" {
		_, err = tx.ExecContext(ctx, deps.Q(`UPDATE task_steps SET status='pending',updated_at=$1
			WHERE task_id=$2 AND id=$3 AND status='active' AND EXISTS (
				SELECT 1 FROM task_leases l WHERE l.task_id=$4 AND l.step_id=$5 AND l.expires_at<=$6
			)`), now.Format(time.RFC3339Nano), taskID, stepID, taskID, stepID, now.Format(time.RFC3339Nano))
		if err != nil {
			return toolError(err.Error())
		}
	}
	_, _ = tx.ExecContext(ctx, deps.Q(`DELETE FROM task_leases WHERE task_id=$1 AND expires_at<=$2`), taskID, now.Format(time.RFC3339Nano))
	var stepStatus, depsJSON string
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT status,dependencies_json FROM task_steps WHERE id=$1 AND task_id=$2`), stepID, taskID).Scan(&stepStatus, &depsJSON); err != nil {
		return toolError("step not found")
	}
	leaseSeconds := intArg(args, "lease_seconds", 900)
	if leaseSeconds < 30 {
		leaseSeconds = 30
	}
	if leaseSeconds > taskMaxLeaseSeconds {
		leaseSeconds = taskMaxLeaseSeconds
	}
	switch action {
	case "claim":
		if stepStatus != "pending" && stepStatus != "failed" {
			return toolError("only pending or failed steps can be claimed")
		}
		var dependencies []string
		_ = json.Unmarshal([]byte(depsJSON), &dependencies)
		for _, dep := range dependencies {
			var status string
			if err := tx.QueryRowContext(ctx, deps.Q(`SELECT status FROM task_steps WHERE id=$1 AND task_id=$2`), dep, taskID).Scan(&status); err != nil || status != "passed" {
				return toolError("dependency not passed: " + dep)
			}
		}
		res, err := tx.ExecContext(ctx, deps.Q(`INSERT INTO task_leases(step_id,task_id,actor_id,expires_at,created_at)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(task_id,step_id) DO NOTHING`), stepID, taskID, actor, now.Add(time.Duration(leaseSeconds)*time.Second).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			return toolError(err.Error())
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return toolError("step already leased")
		}
		_, err = tx.ExecContext(ctx, deps.Q(`UPDATE task_steps SET status='active',attempts=attempts+1,updated_at=$1 WHERE id=$2`), now.Format(time.RFC3339Nano), stepID)
		if err != nil {
			return toolError(err.Error())
		}
	case "renew":
		res, err := tx.ExecContext(ctx, deps.Q(`UPDATE task_leases SET expires_at=$1 WHERE task_id=$2 AND step_id=$3 AND actor_id=$4`), now.Add(time.Duration(leaseSeconds)*time.Second).Format(time.RFC3339Nano), taskID, stepID, actor)
		if err != nil {
			return toolError(err.Error())
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return toolError("active lease not owned by actor")
		}
	case "release", "pass", "fail":
		res, err := tx.ExecContext(ctx, deps.Q(`DELETE FROM task_leases WHERE task_id=$1 AND step_id=$2 AND actor_id=$3`), taskID, stepID, actor)
		if err != nil {
			return toolError(err.Error())
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return toolError("active lease not owned by actor")
		}
		status := map[string]string{"release": "pending", "pass": "passed", "fail": "failed"}[action]
		_, err = tx.ExecContext(ctx, deps.Q(`UPDATE task_steps SET status=$1,updated_at=$2 WHERE id=$3`), status, now.Format(time.RFC3339Nano), stepID)
		if err != nil {
			return toolError(err.Error())
		}
	}
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET status='running',version=version+1,updated_at=$1 WHERE id=$2 AND version=$3`), now.Format(time.RFC3339Nano), taskID, baseVersion)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("version conflict")
	}
	_ = taskEvent(ctx, tx, deps.Q, taskID, actor, "step_"+action, map[string]any{"step_id": stepID})
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(map[string]any{"ok": true, "task_id": taskID, "step_id": stepID, "action": action, "version": baseVersion + 1})
}

// ToolTaskReceipt records immutable verification evidence.
func ToolTaskReceipt(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID, idem := stringArg(args, "task_id"), stringArg(args, "idempotency_key")
	receiptType, status := stringArg(args, "receipt_type"), stringArg(args, "status")
	baseVersion := intArg(args, "base_version", 0)
	validTypes := map[string]bool{"command": true, "artifact": true, "source": true, "observation": true, "reviewer": true}
	if taskID == "" || idem == "" || baseVersion < 1 || !validTypes[receiptType] || (status != "pass" && status != "fail") {
		return toolError("task_id, idempotency_key, base_version, valid receipt_type, and pass|fail status required")
	}
	criteria := stringSliceArg(args, "criterion_ids")
	if len(criteria) == 0 {
		return toolError("criterion_ids must not be empty")
	}
	revision := stringArg(args, "workspace_revision")
	if (receiptType == "command" || receiptType == "artifact" || receiptType == "reviewer") && revision == "" {
		return toolError("workspace_revision required for command, artifact, and reviewer receipts")
	}
	if receiptType == "artifact" && (stringArg(args, "evidence_uri") == "" || stringArg(args, "artifact_digest") == "") {
		return toolError("artifact receipts require evidence_uri and artifact_digest")
	}
	db := deps.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT version FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, taskOwner(ctx)).Scan(&current); err != nil {
		return toolError("task not found")
	}
	var existingReceiptID string
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM task_receipts WHERE task_id=$1 AND idempotency_key=$2`), taskID, idem).Scan(&existingReceiptID); err == nil {
		if err := tx.Commit(); err != nil {
			return toolError(err.Error())
		}
		return jsonResult(map[string]any{"ok": true, "task_id": taskID, "receipt_id": existingReceiptID, "version": current, "idempotent_replay": true})
	}
	if current != baseVersion {
		return toolError(fmt.Sprintf("version conflict: current=%d", current))
	}
	for _, criterionID := range criteria {
		var count int
		_ = tx.QueryRowContext(ctx, deps.Q(`SELECT COUNT(*) FROM task_criteria WHERE task_id=$1 AND id=$2`), taskID, criterionID).Scan(&count)
		if count != 1 {
			return toolError("unknown criterion_id: " + criterionID)
		}
	}
	now, receiptID := time.Now().UTC().Format(time.RFC3339Nano), uuid.NewString()
	criteriaJSON, _ := json.Marshal(criteria)
	var exitCode any
	if _, ok := args["exit_code"]; ok {
		exitCode = intArg(args, "exit_code", 0)
	}
	insertResult, err := tx.ExecContext(ctx, deps.Q(`INSERT INTO task_receipts
		(id,task_id,idempotency_key,owner_id,receipt_type,status,criterion_ids_json,observation,exit_code,
		 evidence_uri,artifact_digest,workspace_revision,metadata_json,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
		ON CONFLICT(task_id,idempotency_key) DO NOTHING`), receiptID, taskID, idem, taskOwner(ctx), receiptType, status,
		string(criteriaJSON), stringArg(args, "observation"), exitCode, stringArg(args, "evidence_uri"),
		stringArg(args, "artifact_digest"), revision, jsonArg(args, "metadata", map[string]any{}), now)
	if err != nil {
		return toolError(err.Error())
	}
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM task_receipts WHERE task_id=$1 AND idempotency_key=$2`), taskID, idem).Scan(&receiptID); err != nil {
		return toolError(err.Error())
	}
	if inserted, _ := insertResult.RowsAffected(); inserted == 0 {
		if err := tx.Commit(); err != nil {
			return toolError(err.Error())
		}
		return jsonResult(map[string]any{"ok": true, "task_id": taskID, "receipt_id": receiptID, "version": baseVersion, "idempotent_replay": true})
	}
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET current_workspace_revision=CASE WHEN $1<>'' THEN $2 ELSE current_workspace_revision END,
		version=version+1,updated_at=$3 WHERE id=$4 AND version=$5`), revision, revision, now, taskID, baseVersion)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("version conflict")
	}
	_ = taskEvent(ctx, tx, deps.Q, taskID, taskActor(ctx, args), "receipt_recorded", map[string]any{"receipt_id": receiptID, "type": receiptType, "status": status})
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(map[string]any{"ok": true, "task_id": taskID, "receipt_id": receiptID, "version": baseVersion + 1})
}

// ToolTaskCheckpoint stores compact recovery state, blockers, and memory candidates.
func ToolTaskCheckpoint(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID, idem, summary := stringArg(args, "task_id"), stringArg(args, "idempotency_key"), stringArg(args, "summary")
	baseVersion := intArg(args, "base_version", 0)
	if taskID == "" || idem == "" || summary == "" || baseVersion < 1 {
		return toolError("task_id, idempotency_key, summary, and positive base_version required")
	}
	db := deps.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT version FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, taskOwner(ctx)).Scan(&current); err != nil {
		return toolError("task not found")
	}
	var existingCheckpointID string
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM task_checkpoints WHERE task_id=$1 AND idempotency_key=$2`), taskID, idem).Scan(&existingCheckpointID); err == nil {
		if err := tx.Commit(); err != nil {
			return toolError(err.Error())
		}
		return jsonResult(map[string]any{"ok": true, "task_id": taskID, "checkpoint_id": existingCheckpointID, "version": current, "idempotent_replay": true})
	}
	if current != baseVersion {
		return toolError(fmt.Sprintf("version conflict: current=%d", current))
	}
	now, checkpointID := time.Now().UTC().Format(time.RFC3339Nano), uuid.NewString()
	revision := stringArg(args, "workspace_revision")
	insertResult, err := tx.ExecContext(ctx, deps.Q(`INSERT INTO task_checkpoints
		(id,task_id,idempotency_key,step_id,summary,verified_json,failed_json,next_action,workspace_revision,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(task_id,idempotency_key) DO NOTHING`),
		checkpointID, taskID, idem, stringArg(args, "step_id"), summary, jsonArg(args, "verified", []string{}),
		jsonArg(args, "failed", []string{}), stringArg(args, "next_action"), revision, now)
	if err != nil {
		return toolError(err.Error())
	}
	if err := tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM task_checkpoints WHERE task_id=$1 AND idempotency_key=$2`), taskID, idem).Scan(&checkpointID); err != nil {
		return toolError(err.Error())
	}
	if inserted, _ := insertResult.RowsAffected(); inserted == 0 {
		if err := tx.Commit(); err != nil {
			return toolError(err.Error())
		}
		return jsonResult(map[string]any{"ok": true, "task_id": taskID, "checkpoint_id": checkpointID, "version": baseVersion, "idempotent_replay": true})
	}
	resolvedBlockerIDs := stringSliceArg(args, "resolved_blocker_ids")
	for _, blockerID := range resolvedBlockerIDs {
		res, err := tx.ExecContext(ctx, deps.Q(`UPDATE task_blockers SET status='resolved',resolved_at=$1
			WHERE id=$2 AND task_id=$3 AND status='active'`), now, blockerID, taskID)
		if err != nil {
			return toolError(err.Error())
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return toolError("active blocker not found: " + blockerID)
		}
	}
	taskStatus := "running"
	if blocker, ok := args["blocker"].(map[string]any); ok && stringArg(blocker, "reason") != "" {
		taskStatus = "blocked"
		_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO task_blockers(id,task_id,reason,required_decision,status,created_at)
			VALUES($1,$2,$3,$4,'active',$5)`), uuid.NewString(), taskID, stringArg(blocker, "reason"), stringArg(blocker, "required_decision"), now)
		if err != nil {
			return toolError(err.Error())
		}
	}
	if candidates, ok := args["memory_candidates"].([]any); ok {
		for i, raw := range candidates {
			candidate, ok := raw.(map[string]any)
			if !ok {
				return toolError(fmt.Sprintf("memory_candidates[%d] must be an object", i))
			}
			key, value, room, hall := stringArg(candidate, "key"), stringArg(candidate, "value"), stringArg(candidate, "room"), stringArg(candidate, "hall")
			if key == "" || value == "" || room == "" || !IsValidHall(hall) {
				return toolError(fmt.Sprintf("memory_candidates[%d] requires key, value, room, and valid hall", i))
			}
			_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO task_memory_candidates
				(id,task_id,memory_key,value,room,hall,evidence_receipt_ids,status,created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8)
				ON CONFLICT(task_id,memory_key) DO UPDATE SET value=$9,room=$10,hall=$11,evidence_receipt_ids=$12,status='pending'`),
				uuid.NewString(), taskID, key, value, room, hall, jsonArg(candidate, "evidence_receipt_ids", []string{}), now,
				value, room, hall, jsonArg(candidate, "evidence_receipt_ids", []string{}))
			if err != nil {
				return toolError(err.Error())
			}
		}
	}
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET status=$1,current_workspace_revision=CASE WHEN $2<>'' THEN $3 ELSE current_workspace_revision END,
		version=version+1,updated_at=$4 WHERE id=$5 AND version=$6`), taskStatus, revision, revision, now, taskID, baseVersion)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("version conflict")
	}
	_ = taskEvent(ctx, tx, deps.Q, taskID, taskActor(ctx, args), "checkpoint_recorded", map[string]any{"checkpoint_id": checkpointID, "status": taskStatus, "resolved_blockers": resolvedBlockerIDs})
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(map[string]any{"ok": true, "task_id": taskID, "checkpoint_id": checkpointID, "status": taskStatus, "version": baseVersion + 1, "resolved_blockers": len(resolvedBlockerIDs)})
}

type taskValidation struct {
	Valid             bool     `json:"valid"`
	TaskID            string   `json:"task_id"`
	Mode              string   `json:"mode"`
	MissingReceipts   []string `json:"missing_receipts"`
	StaleReceipts     []string `json:"stale_receipts"`
	FailedReceipts    []string `json:"failed_receipts"`
	IncompleteSteps   []string `json:"incomplete_steps"`
	ActiveBlockers    []string `json:"active_blockers"`
	ActiveLeases      []string `json:"active_leases"`
	AuditRequired     bool     `json:"audit_required"`
	ReviewerSatisfied bool     `json:"reviewer_satisfied"`
}

func validateTask(ctx context.Context, deps Deps, taskID, owner, mode string) (taskValidation, int, error) {
	db, rewrite := deps.DB(), deps.Q
	v := taskValidation{TaskID: taskID, Mode: mode, MissingReceipts: []string{}, StaleReceipts: []string{}, FailedReceipts: []string{}, IncompleteSteps: []string{}, ActiveBlockers: []string{}, ActiveLeases: []string{}}
	var risk, revision string
	var version int
	if err := db.QueryRowContext(ctx, rewrite(`SELECT risk_level,current_workspace_revision,version FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, owner).Scan(&risk, &revision, &version); err != nil {
		return v, 0, err
	}
	v.AuditRequired = risk == "high"
	type receipt struct {
		id, typ, status, criteriaJSON, rev, evidence, digest string
		exit                                                 sql.NullInt64
	}
	var receipts []receipt
	rows, err := db.QueryContext(ctx, rewrite(`SELECT id,receipt_type,status,criterion_ids_json,workspace_revision,evidence_uri,artifact_digest,exit_code
		FROM task_receipts WHERE task_id=$1 ORDER BY created_at DESC`), taskID)
	if err != nil {
		return v, version, err
	}
	for rows.Next() {
		var r receipt
		if rows.Scan(&r.id, &r.typ, &r.status, &r.criteriaJSON, &r.rev, &r.evidence, &r.digest, &r.exit) == nil {
			receipts = append(receipts, r)
		}
	}
	rows.Close()
	criteriaRows, err := db.QueryContext(ctx, rewrite(`SELECT id FROM task_criteria WHERE task_id=$1 AND required=TRUE`), taskID)
	if err != nil {
		return v, version, err
	}
	var criteria []string
	for criteriaRows.Next() {
		var id string
		if criteriaRows.Scan(&id) == nil {
			criteria = append(criteria, id)
		}
	}
	criteriaRows.Close()
	for _, criterionID := range criteria {
		found, stale, failed := false, false, false
		for _, r := range receipts {
			var ids []string
			_ = json.Unmarshal([]byte(r.criteriaJSON), &ids)
			if !containsString(ids, criterionID) {
				continue
			}
			if r.status != "pass" || (r.typ == "command" && r.exit.Valid && r.exit.Int64 != 0) {
				failed = true
				continue
			}
			if revision != "" && r.rev != "" && r.rev != revision {
				stale = true
				continue
			}
			if r.typ == "artifact" {
				verifier, ok := deps.(ArtifactVerifier)
				if r.evidence == "" || r.digest == "" || !ok || verifier.VerifyArtifact(ctx, r.evidence, r.digest) != nil {
					failed = true
					continue
				}
			}
			found = true
			if r.typ == "reviewer" {
				v.ReviewerSatisfied = true
			}
			break
		}
		if !found {
			if stale {
				v.StaleReceipts = append(v.StaleReceipts, criterionID)
			} else if failed {
				v.FailedReceipts = append(v.FailedReceipts, criterionID)
			} else {
				v.MissingReceipts = append(v.MissingReceipts, criterionID)
			}
		}
	}
	stepRows, _ := db.QueryContext(ctx, rewrite(`SELECT id FROM task_steps WHERE task_id=$1 AND required=TRUE AND status<>'passed'`), taskID)
	if stepRows != nil {
		for stepRows.Next() {
			var id string
			if stepRows.Scan(&id) == nil {
				v.IncompleteSteps = append(v.IncompleteSteps, id)
			}
		}
		stepRows.Close()
	}
	blockRows, _ := db.QueryContext(ctx, rewrite(`SELECT id FROM task_blockers WHERE task_id=$1 AND status='active'`), taskID)
	if blockRows != nil {
		for blockRows.Next() {
			var id string
			if blockRows.Scan(&id) == nil {
				v.ActiveBlockers = append(v.ActiveBlockers, id)
			}
		}
		blockRows.Close()
	}
	leaseRows, _ := db.QueryContext(ctx, rewrite(`SELECT step_id FROM task_leases WHERE task_id=$1 AND expires_at>$2`), taskID, time.Now().UTC().Format(time.RFC3339Nano))
	if leaseRows != nil {
		for leaseRows.Next() {
			var id string
			if leaseRows.Scan(&id) == nil {
				v.ActiveLeases = append(v.ActiveLeases, id)
			}
		}
		leaseRows.Close()
	}
	if v.AuditRequired && !v.ReviewerSatisfied {
		for _, r := range receipts {
			if r.typ == "reviewer" && r.status == "pass" && (revision == "" || r.rev == revision) {
				v.ReviewerSatisfied = true
				break
			}
		}
	}
	v.Valid = len(v.MissingReceipts) == 0 && len(v.StaleReceipts) == 0 && len(v.FailedReceipts) == 0 && len(v.IncompleteSteps) == 0 && len(v.ActiveBlockers) == 0 && len(v.ActiveLeases) == 0 && (!v.AuditRequired || v.ReviewerSatisfied)
	return v, version, nil
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func ToolTaskValidate(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID, mode := stringArg(args, "task_id"), stringArg(args, "mode")
	if mode == "" {
		mode = "completion"
	}
	if taskID == "" || (mode != "checkpoint" && mode != "completion") {
		return toolError("task_id and checkpoint|completion mode required")
	}
	v, version, err := validateTask(ctx, deps, taskID, taskOwner(ctx), mode)
	if err != nil {
		return toolError("task not found")
	}
	out := map[string]any{}
	b, _ := json.Marshal(v)
	_ = json.Unmarshal(b, &out)
	out["version"] = version
	if !v.Valid {
		deps.LogHeartbeat("task_validation_failed", map[string]any{
			"task_id": taskID, "mode": mode, "missing_receipts": len(v.MissingReceipts),
			"stale_receipts": len(v.StaleReceipts), "failed_receipts": len(v.FailedReceipts),
			"incomplete_steps": len(v.IncompleteSteps), "active_blockers": len(v.ActiveBlockers),
			"active_leases": len(v.ActiveLeases), "at": time.Now().UTC().Format(time.RFC3339Nano),
		})
	}
	return jsonResult(out)
}

// ToolTaskBootstrap returns a bounded recovery snapshot scoped to one task.
func ToolTaskBootstrap(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID := stringArg(args, "task_id")
	if taskID == "" {
		return toolError("task_id required")
	}
	maxTokens := intArg(args, "max_tokens", taskDefaultBootstrapTokens)
	if maxTokens < 100 {
		maxTokens = 100
	}
	if maxTokens > 4000 {
		maxTokens = 4000
	}
	db := deps.DB()
	owner := taskOwner(ctx)
	var collection, room, objective, risk, status, revision string
	var version int
	if err := db.QueryRowContext(ctx, deps.Q(`SELECT collection_name,room,objective,risk_level,status,version,current_workspace_revision FROM tasks WHERE id=$1 AND (owner_id=$2 OR owner_id='')`), taskID, owner).Scan(&collection, &room, &objective, &risk, &status, &version, &revision); err != nil {
		return toolError("task not found")
	}
	criteria := queryMaps(ctx, db, deps.Q, `SELECT id,description,required,verification_json FROM task_criteria WHERE task_id=$1 ORDER BY created_at`, taskID)
	steps := queryMaps(ctx, db, deps.Q, `SELECT id,description,status,required,dependencies_json,criterion_ids_json,attempts,position FROM task_steps WHERE task_id=$1 ORDER BY position`, taskID)
	blockers := queryMaps(ctx, db, deps.Q, `SELECT id,reason,required_decision,status,created_at FROM task_blockers WHERE task_id=$1 AND status='active' ORDER BY created_at`, taskID)
	checkpoint := queryMaps(ctx, db, deps.Q, `SELECT id,step_id,summary,verified_json,failed_json,next_action,workspace_revision,created_at FROM task_checkpoints WHERE task_id=$1 ORDER BY created_at DESC LIMIT 1`, taskID)
	memories := queryMaps(ctx, db, deps.Q, `SELECT id,key,value,hall,room,is_pinned,pin_priority FROM memories WHERE collection_name=$1 AND (owner_id=$2 OR owner_id='') AND superseded_by='' AND (room=$3 OR room='') AND hall IN ('decision','discovery','fact') ORDER BY is_pinned DESC,pin_priority DESC,updated_at DESC LIMIT 12`, collection, owner, room)
	nextStep := map[string]any{}
	passed := map[string]bool{}
	for _, s := range steps {
		if s["status"] == "passed" {
			passed[fmt.Sprint(s["id"])] = true
		}
	}
	for _, s := range steps {
		if s["status"] != "pending" && s["status"] != "failed" {
			continue
		}
		var ds []string
		_ = json.Unmarshal([]byte(fmt.Sprint(s["dependencies_json"])), &ds)
		ok := true
		for _, d := range ds {
			if !passed[d] {
				ok = false
			}
		}
		if ok {
			nextStep = s
			break
		}
	}
	bundle := map[string]any{"task_id": taskID, "collection": collection, "room": room, "objective": objective, "risk_level": risk, "status": status, "version": version, "workspace_revision": revision, "criteria": criteria, "steps": steps, "active_blockers": blockers, "last_checkpoint": checkpoint, "next_step": nextStep, "memories": memories, "scope_status": "exact", "max_tokens": maxTokens}
	if !boundBootstrapBundle(bundle, maxTokens) {
		return toolError("token budget is too small to include safety-critical task state")
	}
	return jsonResult(bundle)
}

func boundBootstrapBundle(bundle map[string]any, maxTokens int) bool {
	measure := func() int {
		delete(bundle, "tokens_used")
		base, _ := json.Marshal(bundle)
		// Reserve enough bytes for the comma, key, and a conservatively wide
		// integer before measuring the final representation.
		bundle["tokens_used"] = (len(base) + 32 + 3) / 4
		encoded, _ := json.Marshal(bundle)
		actual := (len(encoded) + 3) / 4
		bundle["tokens_used"] = actual
		return actual
	}
	fits := func() bool { return measure() <= maxTokens }
	if fits() {
		return true
	}

	if memories, ok := bundle["memories"].([]map[string]any); ok {
		for len(memories) > 0 && !fits() {
			memories = memories[:len(memories)-1]
			bundle["memories"] = memories
		}
	}
	if fits() {
		return true
	}

	// next_step already carries the actionable recovery point, so the full
	// historical plan can be omitted before any safety-critical blocker data.
	bundle["steps"] = []map[string]any{}
	if next, ok := bundle["next_step"].(map[string]any); ok {
		bundle["next_step"] = compactBootstrapMap(next, []string{"id", "status", "description"}, 96)
	}
	if fits() {
		return true
	}

	if criteria, ok := bundle["criteria"].([]map[string]any); ok {
		compact := make([]map[string]any, 0, len(criteria))
		for _, criterion := range criteria {
			compact = append(compact, compactBootstrapMap(criterion, []string{"id", "required"}, 0))
		}
		bundle["criteria"] = compact
	}
	if fits() {
		return true
	}
	bundle["criteria"] = []map[string]any{}

	if checkpoints, ok := bundle["last_checkpoint"].([]map[string]any); ok && len(checkpoints) > 0 {
		bundle["last_checkpoint"] = []map[string]any{compactBootstrapMap(checkpoints[0], []string{"id", "summary", "next_action", "workspace_revision"}, 64)}
	}
	bundle["objective"] = truncateBootstrapString(fmt.Sprint(bundle["objective"]), 96)
	bundle["collection"] = truncateBootstrapString(fmt.Sprint(bundle["collection"]), 48)
	bundle["room"] = truncateBootstrapString(fmt.Sprint(bundle["room"]), 48)
	if fits() {
		return true
	}

	bundle["last_checkpoint"] = []map[string]any{}
	bundle["next_step"] = map[string]any{}
	bundle["objective"] = truncateBootstrapString(fmt.Sprint(bundle["objective"]), 32)
	// Active blockers are deliberately retained. Returning an explicit error is
	// safer than silently hiding blockers merely to satisfy a tiny budget.
	return fits()
}

func compactBootstrapMap(source map[string]any, keys []string, stringLimit int) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		value, ok := source[key]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok && stringLimit > 0 {
			value = truncateBootstrapString(text, stringLimit)
		}
		out[key] = value
	}
	return out
}

func truncateBootstrapString(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

// queryMaps is a small dynamic scanner used only for bounded bootstrap views.
func queryMaps(ctx context.Context, db *sql.DB, rewrite func(string) string, query string, args ...any) []map[string]any {
	rows, err := db.QueryContext(ctx, rewrite(query), args...)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		m := map[string]any{}
		for i, c := range cols {
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			m[c] = v
		}
		out = append(out, m)
	}
	return out
}

// ToolTaskComplete validates then atomically transitions the versioned task.
func ToolTaskComplete(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	taskID := stringArg(args, "task_id")
	expected := intArg(args, "expected_version", 0)
	if taskID == "" || expected < 1 {
		return toolError("task_id and positive expected_version required")
	}
	v, current, err := validateTask(ctx, deps, taskID, taskOwner(ctx), "completion")
	if err != nil {
		return toolError("task not found")
	}
	if current != expected {
		return toolError(fmt.Sprintf("version conflict: current=%d", current))
	}
	if !v.Valid {
		deps.LogHeartbeat("task_completion_rejected", map[string]any{"task_id": taskID, "version": current, "validation": v, "at": time.Now().UTC().Format(time.RFC3339Nano)})
		return jsonResult(map[string]any{"ok": false, "task_id": taskID, "validation": v, "version": current})
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := deps.DB().BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE tasks SET status='completed',version=version+1,updated_at=$1,completed_at=$2 WHERE id=$3 AND version=$4 AND status<>'completed'`), now, now, taskID, expected)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("version conflict or task already completed")
	}
	_ = taskEvent(ctx, tx, deps.Q, taskID, taskActor(ctx, args), "task_completed", map[string]any{"version": expected + 1})
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	promoted, rejected := promoteTaskMemories(ctx, deps, taskID)
	deps.LogHeartbeat("task_completed", map[string]any{"task_id": taskID, "promoted_memories": promoted, "rejected_memories": rejected, "at": now})
	return jsonResult(map[string]any{"ok": true, "task_id": taskID, "status": "completed", "version": expected + 1, "promoted_memories": promoted, "rejected_memories": rejected})
}

func promoteTaskMemories(ctx context.Context, deps Deps, taskID string) (int, int) {
	db := deps.DB()
	var collection, revision string
	_ = db.QueryRowContext(ctx, deps.Q(`SELECT collection_name,current_workspace_revision FROM tasks WHERE id=$1`), taskID).Scan(&collection, &revision)
	rows, err := db.QueryContext(ctx, deps.Q(`SELECT id,memory_key,value,room,hall,evidence_receipt_ids FROM task_memory_candidates WHERE task_id=$1 AND status='pending'`), taskID)
	if err != nil {
		return 0, 0
	}
	defer rows.Close()
	type candidate struct{ id, key, value, room, hall, evidence string }
	var items []candidate
	for rows.Next() {
		var c candidate
		if rows.Scan(&c.id, &c.key, &c.value, &c.room, &c.hall, &c.evidence) == nil {
			items = append(items, c)
		}
	}
	promoted, rejected := 0, 0
	for _, c := range items {
		var evidence []string
		_ = json.Unmarshal([]byte(c.evidence), &evidence)
		valid := len(evidence) > 0
		if c.hall == "event" && !absoluteDateRE.MatchString(c.value) {
			valid = false
		}
		for _, rid := range evidence {
			var status, receiptRevision string
			if db.QueryRowContext(ctx, deps.Q(`SELECT status,workspace_revision FROM task_receipts WHERE id=$1 AND task_id=$2`), rid, taskID).Scan(&status, &receiptRevision) != nil || status != "pass" || (revision != "" && receiptRevision != revision) {
				valid = false
			}
		}
		if !valid {
			rejected++
			_, _ = db.ExecContext(ctx, deps.Q(`UPDATE task_memory_candidates SET status='rejected' WHERE id=$1`), c.id)
			continue
		}
		var memoryID string
		var existingValue string
		existingErr := db.QueryRowContext(ctx, deps.Q(`SELECT id,value FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' ORDER BY owner_id DESC LIMIT 1`), c.key, collection, taskOwner(ctx)).Scan(&memoryID, &existingValue)
		if existingErr == nil && existingValue != c.value {
			result := ToolSupersedeMemory(ctx, deps, map[string]any{"old_memory_id": memoryID, "new_value": c.value, "reason": "verified long-horizon task outcome", "room": c.room, "hall": c.hall, "source_task_id": taskID, "source_receipt_ids": toAnyStrings(evidence), "verification_status": "verified"})
			if result.IsError {
				rejected++
				continue
			}
			_ = db.QueryRowContext(ctx, deps.Q(`SELECT id FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' ORDER BY owner_id DESC LIMIT 1`), c.key, collection, taskOwner(ctx)).Scan(&memoryID)
		} else if existingErr != nil {
			result := ToolSaveMemory(ctx, deps, map[string]any{"key": c.key, "value": c.value, "type": "project", "collection": collection, "room": c.room, "hall": c.hall, "source_task_id": taskID, "source_receipt_ids": toAnyStrings(evidence), "verification_status": "verified"})
			if result.IsError {
				rejected++
				continue
			}
			_ = db.QueryRowContext(ctx, deps.Q(`SELECT id FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' ORDER BY owner_id DESC LIMIT 1`), c.key, collection, taskOwner(ctx)).Scan(&memoryID)
		}
		_, _ = db.ExecContext(ctx, deps.Q(`UPDATE memories SET source_task_id=$1,source_receipt_ids=$2,verification_status='verified' WHERE id=$3`), taskID, c.evidence, memoryID)
		_, _ = db.ExecContext(ctx, deps.Q(`INSERT INTO task_memory_links(task_id,memory_id,relation,created_at) VALUES($1,$2,'produced',$3) ON CONFLICT DO NOTHING`), taskID, memoryID, time.Now().UTC().Format(time.RFC3339Nano))
		_, _ = db.ExecContext(ctx, deps.Q(`UPDATE task_memory_candidates SET status='promoted' WHERE id=$1`), c.id)
		promoted++
	}
	return promoted, rejected
}

func toAnyStrings(values []string) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = v
	}
	return out
}
