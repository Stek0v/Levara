package mcp

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/memoryindex"
)

const memoryCommitTTL = 30 * time.Minute

var memoryCommitSecret = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:password|api[_-]?key|authorization)\s*[:=]|\bbearer\s+[a-z0-9._-]{12,}|\bsk-[a-z0-9]{16,})`)

type memoryCommitCandidate struct {
	CandidateID        string   `json:"candidate_id"`
	Key                string   `json:"key"`
	Value              string   `json:"value"`
	Room               string   `json:"room"`
	Hall               string   `json:"hall"`
	SupersedesMemoryID string   `json:"supersedes_memory_id,omitempty"`
	VerificationStatus string   `json:"verification_status,omitempty"`
	SourceTaskID       string   `json:"source_task_id,omitempty"`
	SourceReceiptIDs   []string `json:"source_receipt_ids,omitempty"`
}

type memoryCommitItem struct {
	CandidateID    string `json:"candidate_id"`
	Action         string `json:"action"`
	ReasonCode     string `json:"reason_code"`
	TargetMemoryID string `json:"target_memory_id"`
	TargetDigest   string `json:"target_digest,omitempty"`
}

type memoryCommitPlan struct {
	Collection string                  `json:"collection"`
	OwnerID    string                  `json:"owner_id"`
	Candidates []memoryCommitCandidate `json:"candidates"`
	Items      []memoryCommitItem      `json:"items"`
}

// ToolMemoryCommitPreview stores a short-lived, idempotent plan. It does not
// mutate memories; only prepared-plan state is written so apply can detect a
// stale comparison instead of silently recalculating it.
func ToolMemoryCommitPreview(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	collection, _ := args["collection"].(string)
	idempotencyKey, _ := args["idempotency_key"].(string)
	candidates, ok := memoryCommitCandidates(args["candidates"])
	if strings.TrimSpace(collection) == "" || strings.TrimSpace(idempotencyKey) == "" || !ok || len(candidates) == 0 || len(candidates) > 20 {
		return toolError("'collection', 'idempotency_key', and 1-20 'candidates' required")
	}
	db := deps.DB()
	if db == nil {
		return toolError("database not configured")
	}
	ownerID := extractOwnerID(ctx)
	requestDigest := memoryCommitDigest(map[string]any{"collection": collection, "idempotency_key": idempotencyKey, "candidates": candidates})

	var commitID, status, existingRequestDigest, storedPlan, expiresAt string
	err := db.QueryRowContext(ctx, deps.Q(`SELECT id,status,request_digest,plan_json,expires_at FROM memory_commits WHERE owner_id=$1 AND collection_name=$2 AND idempotency_key=$3`), ownerID, collection, idempotencyKey).
		Scan(&commitID, &status, &existingRequestDigest, &storedPlan, &expiresAt)
	if err == nil {
		if existingRequestDigest != requestDigest {
			return toolError("idempotency key reused with different request")
		}
		if status == "applied" || !memoryCommitExpired(expiresAt) {
			return memoryCommitPreviewResult(commitID, status, collection, storedPlan, expiresAt)
		}
	} else if err != sql.ErrNoRows {
		return toolError(err.Error())
	}

	plan := memoryCommitPlan{Collection: collection, OwnerID: ownerID, Candidates: candidates, Items: make([]memoryCommitItem, 0, len(candidates))}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		item := memoryCommitItem{CandidateID: candidate.CandidateID}
		switch {
		case strings.TrimSpace(candidate.CandidateID) == "" || seen[candidate.CandidateID] || strings.TrimSpace(candidate.Key) == "" || strings.TrimSpace(candidate.Value) == "" || strings.TrimSpace(candidate.Room) == "" || !IsValidHall(candidate.Hall):
			item.Action, item.ReasonCode = "reject", "schema_invalid"
		case memoryCommitSecret.MatchString(candidate.Value):
			item.Action, item.ReasonCode = "reject", "secret_rejected"
		case candidate.SupersedesMemoryID != "":
			var id, key, value string
			err := db.QueryRowContext(ctx, deps.Q(`SELECT id,key,value FROM memories WHERE id=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by=''`), candidate.SupersedesMemoryID, collection, ownerID).Scan(&id, &key, &value)
			if err != nil {
				item.Action, item.ReasonCode = "reject", "stale_target"
			} else {
				item.Action, item.ReasonCode, item.TargetMemoryID, item.TargetDigest = "supersede", "explicit_supersede", id, memoryCommitTargetDigest(key, value)
			}
		default:
			var id, value string
			err := db.QueryRowContext(ctx, deps.Q(`SELECT id,value FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' ORDER BY owner_id DESC LIMIT 1`), candidate.Key, collection, ownerID).Scan(&id, &value)
			switch {
			case err == sql.ErrNoRows:
				item.Action, item.ReasonCode = "add", "no_equivalent"
			case err != nil:
				item.Action, item.ReasonCode = "reject", "lookup_failed"
			case normalizeMemoryCommitText(value) == normalizeMemoryCommitText(candidate.Value):
				item.Action, item.ReasonCode, item.TargetMemoryID, item.TargetDigest = "skip", "exact_duplicate", id, memoryCommitTargetDigest(candidate.Key, value)
			default:
				item.Action, item.ReasonCode, item.TargetMemoryID, item.TargetDigest = "conflict", "key_conflict", id, memoryCommitTargetDigest(candidate.Key, value)
			}
		}
		seen[candidate.CandidateID] = true
		plan.Items = append(plan.Items, item)
	}
	planJSON, _ := json.Marshal(plan)
	planDigest := memoryCommitDigest(plan)
	if commitID == "" {
		commitID = "mc_" + uuid.NewString()
	}
	expiresAt = time.Now().UTC().Add(memoryCommitTTL).Format(time.RFC3339Nano)
	_, err = db.ExecContext(ctx, deps.Q(`INSERT INTO memory_commits(id,owner_id,collection_name,idempotency_key,status,request_digest,plan_digest,plan_json,result_json,created_at,expires_at,applied_at)
		VALUES($1,$2,$3,$4,'prepared',$5,$6,$7,'',$8,$9,NULL)
		ON CONFLICT(owner_id,collection_name,idempotency_key) DO UPDATE SET status='prepared',request_digest=$10,plan_digest=$11,plan_json=$12,result_json='',created_at=$13,expires_at=$14,applied_at=NULL`),
		commitID, ownerID, collection, idempotencyKey, requestDigest, planDigest, string(planJSON), time.Now().UTC().Format(time.RFC3339Nano), expiresAt,
		requestDigest, planDigest, string(planJSON), time.Now().UTC().Format(time.RFC3339Nano), expiresAt)
	if err != nil {
		return toolError(err.Error())
	}
	return memoryCommitPreviewResult(commitID, "prepared", collection, string(planJSON), expiresAt)
}

// ToolMemoryCommitApply applies selected add/supersede actions from a stored
// plan. A status update claims the plan inside the transaction, making retry
// idempotent and preventing concurrent applies from duplicating mutations.
func ToolMemoryCommitApply(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	commitID, _ := args["commit_id"].(string)
	planDigest, _ := args["plan_digest"].(string)
	if strings.TrimSpace(commitID) == "" || strings.TrimSpace(planDigest) == "" {
		return toolError("'commit_id' and 'plan_digest' required")
	}
	db := deps.DB()
	if db == nil {
		return toolError("database not configured")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	ownerID := extractOwnerID(ctx)
	var status, storedDigest, storedPlan, storedResult, expiresAt string
	err = tx.QueryRowContext(ctx, deps.Q(`SELECT status,plan_digest,plan_json,result_json,expires_at FROM memory_commits WHERE id=$1 AND owner_id=$2`), commitID, ownerID).
		Scan(&status, &storedDigest, &storedPlan, &storedResult, &expiresAt)
	if err == sql.ErrNoRows {
		return toolError("prepared commit not found")
	}
	if err != nil {
		return toolError(err.Error())
	}
	if storedDigest != planDigest {
		return toolError("plan digest mismatch")
	}
	if status == "applied" {
		return memoryCommitStoredResult(storedResult)
	}
	if status != "prepared" || memoryCommitExpired(expiresAt) {
		return toolError("prepared commit is no longer applicable")
	}
	var plan memoryCommitPlan
	if err := json.Unmarshal([]byte(storedPlan), &plan); err != nil {
		return toolError("stored plan is invalid")
	}
	if memoryCommitDigest(plan) != storedDigest {
		return toolError("stored plan digest is invalid")
	}
	claimed, err := tx.ExecContext(ctx, deps.Q(`UPDATE memory_commits SET status='applying' WHERE id=$1 AND owner_id=$2 AND status='prepared'`), commitID, ownerID)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := claimed.RowsAffected(); n != 1 {
		return toolError("prepared commit is already applying")
	}
	accepted := memoryCommitAccepted(args["accepted_candidate_ids"], plan.Items)
	result := map[string]any{"commit_id": commitID, "status": "applied", "added": 0, "superseded": 0, "skipped": 0, "index_jobs": []map[string]any{}}
	indexJobs := result["index_jobs"].([]map[string]any)
	for i, item := range plan.Items {
		if !accepted[item.CandidateID] {
			continue
		}
		if item.Action == "skip" {
			var key, value string
			err := tx.QueryRowContext(ctx, deps.Q(`SELECT key,value FROM memories WHERE id=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by=''`), item.TargetMemoryID, plan.Collection, ownerID).Scan(&key, &value)
			if err != nil || memoryCommitTargetDigest(key, value) != item.TargetDigest {
				return toolError("prepared plan is stale")
			}
			result["skipped"] = result["skipped"].(int) + 1
			continue
		}
		if item.Action != "add" && item.Action != "supersede" {
			continue
		}
		candidate := plan.Candidates[i]
		var newID, oldID string
		if item.Action == "add" {
			var existing string
			err := tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' ORDER BY owner_id DESC LIMIT 1`), candidate.Key, plan.Collection, ownerID).Scan(&existing)
			if err != sql.ErrNoRows {
				return toolError("prepared plan is stale")
			}
			newID = uuid.NewString()
			if err := memoryCommitInsert(ctx, tx, deps, newID, candidate, ownerID, plan.Collection, "", ""); err != nil {
				return toolError(err.Error())
			}
			result["added"] = result["added"].(int) + 1
		} else {
			var oldKey, oldValue, oldType, oldOwner string
			err := tx.QueryRowContext(ctx, deps.Q(`SELECT key,value,type,owner_id FROM memories WHERE id=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by=''`), item.TargetMemoryID, plan.Collection, ownerID).
				Scan(&oldKey, &oldValue, &oldType, &oldOwner)
			if err != nil || memoryCommitTargetDigest(oldKey, oldValue) != item.TargetDigest {
				return toolError("prepared plan is stale")
			}
			var collision string
			err = tx.QueryRowContext(ctx, deps.Q(`SELECT id FROM memories WHERE key=$1 AND collection_name=$2 AND (owner_id=$3 OR owner_id='') AND superseded_by='' AND id<>$4`), candidate.Key, plan.Collection, ownerID, item.TargetMemoryID).Scan(&collision)
			if err != sql.ErrNoRows {
				return toolError("prepared plan is stale")
			}
			newID, oldID = uuid.NewString(), item.TargetMemoryID
			now := time.Now().UTC().Format(time.RFC3339Nano)
			archiveKey := fmt.Sprintf("%s#superseded:%s", oldKey, oldID)
			updated, err := tx.ExecContext(ctx, deps.Q(`UPDATE memories SET key=$1,superseded_by=$2,supersession_reason=$3,valid_until=$4,updated_at=$5 WHERE id=$6 AND superseded_by=''`), archiveKey, newID, "memory commit", now, now, oldID)
			if err != nil || rowsAffected(updated) != 1 {
				return toolError("prepared plan is stale")
			}
			if err := memoryCommitInsert(ctx, tx, deps, newID, candidate, oldOwner, plan.Collection, oldType, oldID); err != nil {
				return toolError(err.Error())
			}
			result["superseded"] = result["superseded"].(int) + 1
		}
		if provider, ok := deps.(interface{ MemoryIndexOutbox() *memoryindex.Store }); ok && provider.MemoryIndexOutbox() != nil {
			if oldID != "" && deps.HasCollections() {
				job, err := provider.MemoryIndexOutbox().EnqueueTx(ctx, tx, memoryindex.Job{MemoryID: oldID, Operation: "delete_vector", Collection: plan.Collection, OwnerID: ownerID, Digest: item.TargetDigest})
				if err != nil {
					return toolError(err.Error())
				}
				indexJobs = append(indexJobs, map[string]any{"memory_id": oldID, "job_id": job.ID, "status": job.Status})
			}
			if deps.EmbedAvailable() {
				job, err := provider.MemoryIndexOutbox().EnqueueTx(ctx, tx, memoryindex.Job{MemoryID: newID, Operation: "upsert_vector", Collection: plan.Collection, OwnerID: ownerID, Digest: memoryCommitTargetDigest(candidate.Key, candidate.Value), Model: deps.EmbedModel()})
				if err != nil {
					return toolError(err.Error())
				}
				indexJobs = append(indexJobs, map[string]any{"memory_id": newID, "job_id": job.ID, "status": job.Status})
			}
		}
	}
	result["index_jobs"] = indexJobs
	resultJSON, _ := json.Marshal(result)
	_, err = tx.ExecContext(ctx, deps.Q(`UPDATE memory_commits SET status='applied',result_json=$1,applied_at=$2 WHERE id=$3 AND status='applying'`), string(resultJSON), time.Now().UTC().Format(time.RFC3339Nano), commitID)
	if err != nil {
		return toolError(err.Error())
	}
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}
	return jsonResult(result)
}

func memoryCommitInsert(ctx context.Context, tx *sql.Tx, deps Deps, id string, candidate memoryCommitCandidate, ownerID, collection, memType, supersedes string) error {
	if memType == "" {
		memType = "project"
	}
	receipts := memoryReceiptJSON(candidate.SourceReceiptIDs)
	verification := candidate.VerificationStatus
	if verification == "" {
		verification = "verified"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, deps.Q(`INSERT INTO memories(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,superseded_by,source_task_id,source_receipt_ids,verification_status,supersedes_memory_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,FALSE,0,'',$9,$10,$11,$12,$13,$14)`), id, candidate.Key, candidate.Value, memType, ownerID, collection, candidate.Room, candidate.Hall, candidate.SourceTaskID, receipts, verification, supersedes, now, now)
	return err
}

func memoryCommitCandidates(value any) ([]memoryCommitCandidate, bool) {
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]memoryCommitCandidate, 0, len(raw))
	for _, value := range raw {
		candidate, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		out = append(out, memoryCommitCandidate{CandidateID: stringArg(candidate, "candidate_id"), Key: stringArg(candidate, "key"), Value: stringArg(candidate, "value"), Room: stringArg(candidate, "room"), Hall: stringArg(candidate, "hall"), SupersedesMemoryID: stringArg(candidate, "supersedes_memory_id"), VerificationStatus: stringArg(candidate, "verification_status"), SourceTaskID: stringArg(candidate, "source_task_id"), SourceReceiptIDs: stringSliceArg(candidate, "source_receipt_ids")})
	}
	return out, true
}

func memoryCommitPreviewResult(commitID, status, collection, storedPlan, expiresAt string) ToolResult {
	var plan memoryCommitPlan
	if err := json.Unmarshal([]byte(storedPlan), &plan); err != nil {
		return toolError("stored plan is invalid")
	}
	summary := map[string]int{"add": 0, "supersede": 0, "skip": 0, "conflict": 0, "reject": 0}
	items := make([]map[string]any, 0, len(plan.Items))
	for _, item := range plan.Items {
		summary[item.Action]++
		items = append(items, map[string]any{"candidate_id": item.CandidateID, "action": item.Action, "reason_code": item.ReasonCode, "target_memory_id": item.TargetMemoryID, "warnings": []any{}})
	}
	return jsonResult(map[string]any{"commit_id": commitID, "status": status, "collection": collection, "plan_digest": memoryCommitDigest(plan), "expires_at": expiresAt, "summary": summary, "items": items})
}

func memoryCommitStoredResult(value string) ToolResult {
	var result map[string]any
	if json.Unmarshal([]byte(value), &result) != nil {
		return toolError("stored commit result is invalid")
	}
	return jsonResult(result)
}

func memoryCommitAccepted(value any, items []memoryCommitItem) map[string]bool {
	selected := stringSliceArg(map[string]any{"ids": value}, "ids")
	accepted := map[string]bool{}
	if len(selected) == 0 {
		for _, item := range items {
			if item.Action == "add" || item.Action == "supersede" || item.Action == "skip" {
				accepted[item.CandidateID] = true
			}
		}
		return accepted
	}
	for _, id := range selected {
		accepted[id] = true
	}
	return accepted
}

func memoryCommitDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func memoryCommitTargetDigest(key, value string) string {
	return memoryCommitDigest(key + "\x00" + value)
}

func memoryCommitExpired(expiresAt string) bool {
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	return err != nil || !time.Now().Before(expires)
}

func normalizeMemoryCommitText(value string) string { return strings.Join(strings.Fields(value), " ") }

func rowsAffected(result sql.Result) int64 {
	n, _ := result.RowsAffected()
	return n
}
