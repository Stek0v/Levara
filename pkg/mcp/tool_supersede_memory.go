package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ToolSupersedeMemory replaces one active memory while preserving its audit
// history. The old row is archived under a synthetic key so the replacement
// can retain the stable user-facing key without violating the scoped unique
// index. SQL is committed before vector side effects, matching save_memory.
func ToolSupersedeMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	oldID, _ := args["old_memory_id"].(string)
	newValue, _ := args["new_value"].(string)
	reason, _ := args["reason"].(string)
	if strings.TrimSpace(oldID) == "" || strings.TrimSpace(newValue) == "" || strings.TrimSpace(reason) == "" {
		return toolError("'old_memory_id', 'new_value', and 'reason' required")
	}
	db := deps.DB()
	if db == nil {
		return toolError("database not configured")
	}
	ownerID := extractOwnerID(ctx)
	tx, actor, policy, err := memoryCommitBegin(ctx, deps)
	if err != nil {
		return toolError(err.Error())
	}
	defer tx.Rollback()
	var key, oldValue, memType, oldOwner, collection, room, hall string
	err = tx.QueryRowContext(ctx, deps.Q(`SELECT key,value,type,owner_id,collection_name,room,hall
		FROM memories WHERE id=$1 AND (owner_id=$2 OR owner_id='') AND superseded_by=''`), oldID, ownerID).
		Scan(&key, &oldValue, &memType, &oldOwner, &collection, &room, &hall)
	if err != nil {
		return toolError("active memory not found")
	}
	if oldOwner == "" && !memoryCommitCanMutateShared(ctx, policy, actor) {
		return toolError("shared memory requires a current administrator or trusted local caller")
	}
	if v, ok := args["key"].(string); ok && strings.TrimSpace(v) != "" {
		key = strings.TrimSpace(v)
	}
	if v, ok := args["room"].(string); ok && strings.TrimSpace(v) != "" {
		room = strings.TrimSpace(v)
	}
	if v, ok := args["hall"].(string); ok && strings.TrimSpace(v) != "" {
		hall = strings.TrimSpace(v)
	}
	if !IsValidHall(hall) {
		return toolError(fmt.Sprintf("invalid hall %q", hall))
	}

	newID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	archiveKey := fmt.Sprintf("%s#superseded:%s", key, oldID)
	evidence, err := memorySourceEvidence(args)
	if err != nil {
		return toolError(err.Error())
	}

	verification, err := memoryCommitValidateEvidence(ctx, tx, deps, ownerID, collection, evidence)
	if err != nil {
		return toolError(err.Error())
	}
	res, err := tx.ExecContext(ctx, deps.Q(`UPDATE memories SET key=$1,superseded_by=$2,supersession_reason=$3,valid_until=$4,updated_at=$5
		WHERE id=$6 AND superseded_by=''`), archiveKey, newID, reason, now, now, oldID)
	if err != nil {
		return toolError(err.Error())
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return toolError("memory was superseded concurrently")
	}
	_, err = tx.ExecContext(ctx, deps.Q(`INSERT INTO memories
		(id,key,value,type,owner_id,collection_name,room,hall,is_pinned,pin_priority,superseded_by,
		 source_task_id,source_receipt_ids,verification_status,supersedes_memory_id,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,FALSE,0,'',$9,$10,$11,$12,$13,$14)`),
		newID, key, newValue, memType, oldOwner, collection, room, hall,
		evidence.SourceTaskID, memoryReceiptJSON(evidence.SourceReceiptIDs), verification, oldID, now, now)
	if err != nil {
		return toolError(err.Error())
	}
	if err := memoryCommitRecheck(ctx, policy, actor); err != nil {
		return toolError("memory supersession authorization changed")
	}
	if err := tx.Commit(); err != nil {
		return toolError(err.Error())
	}

	if deps.HasCollections() {
		_ = deps.CollectionDelete(memoryCollectionName(collection), oldID)
	}
	if deps.EmbedAvailable() {
		indexMemorySync(deps, collection, newID, key, newValue, memType)
	}
	deps.LogHeartbeat("memory_superseded", map[string]any{
		"old_memory_id": oldID, "new_memory_id": newID, "collection": collection,
		"reason": Truncate(reason, memoryValueLogMaxLen), "at": now,
	})
	return jsonResult(map[string]any{
		"ok": true, "old_memory_id": oldID, "new_memory_id": newID,
		"key": key, "reason": reason,
	})
}

func toolError(message string) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: "Error: " + message}}, IsError: true}
}
