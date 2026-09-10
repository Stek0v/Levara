package mcp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/memoryindex"
)

var (
	ErrMemoryDeleteNotFound  = errors.New("memory delete target not found")
	ErrMemoryDeleteAmbiguous = errors.New("memory delete key is ambiguous")
	ErrMemoryDeleteSelectors = errors.New("memory delete selectors conflict")
)

// DeleteMemoryRequest identifies one memory either by its durable row ID or by
// a legacy key. MemoryID cannot be combined with Key or Collection. Key-only
// deletion is limited to one personal row; Collection may narrow that legacy
// lookup to one shard.
type DeleteMemoryRequest struct {
	MemoryID   string
	Key        string
	Collection string
}

// DeletedMemory is the exact row identity committed by DeleteMemory.
type DeletedMemory struct {
	ID, Key, Collection, OwnerID string
}

// DeleteMemory resolves authorization, deletes one concrete SQL row, and
// enqueues its exact vector retirement in the same transaction. Shared rows
// require an explicit MemoryID and a live administrator credential.
func DeleteMemory(ctx context.Context, deps Deps, req DeleteMemoryRequest) (DeletedMemory, error) {
	if req.MemoryID != "" && (req.Key != "" || req.Collection != "") {
		return DeletedMemory{}, ErrMemoryDeleteSelectors
	}
	if req.MemoryID == "" && req.Key == "" {
		return DeletedMemory{}, ErrMemoryDeleteSelectors
	}

	tx, actor, policy, err := beginMemoryDelete(ctx, deps)
	if err != nil {
		return DeletedMemory{}, err
	}
	defer tx.Rollback()

	target, err := resolveMemoryDeleteTarget(ctx, tx, deps, actor, req)
	if err != nil {
		return DeletedMemory{}, err
	}
	if target.OwnerID == "" && actor.UserID != "" && !memoryCommitCanMutateShared(ctx, policy, actor) {
		return DeletedMemory{}, access.ErrDocumentForbidden
	}

	result, err := tx.ExecContext(ctx, deps.Q(`DELETE FROM memories
		WHERE id=$1 AND key=$2 AND owner_id=$3 AND collection_name=$4 AND superseded_by=''`),
		target.ID, target.Key, target.OwnerID, target.Collection)
	if err != nil {
		return DeletedMemory{}, err
	}
	if rowsAffected(result) != 1 {
		return DeletedMemory{}, ErrMemoryDeleteNotFound
	}

	provider, hasOutbox := deps.(interface{ MemoryIndexOutbox() *memoryindex.Store })
	var outbox *memoryindex.Store
	if hasOutbox {
		outbox = provider.MemoryIndexOutbox()
	}
	if outbox != nil {
		if _, err = outbox.EnqueueTx(ctx, tx, memoryindex.Job{
			MemoryID: target.ID, Operation: "delete_vector", Collection: target.Collection,
			OwnerID: target.OwnerID, Digest: "delete:" + target.ID,
		}); err != nil {
			return DeletedMemory{}, err
		}
	} else if deps.HasCollections() {
		return DeletedMemory{}, errors.New("memory index outbox not configured")
	}
	if err := tx.Commit(); err != nil {
		return DeletedMemory{}, err
	}
	return target, nil
}

func beginMemoryDelete(ctx context.Context, deps Deps) (*sql.Tx, access.MetadataActor, access.SQLPolicy, error) {
	actor := deps.MetadataActor(ctx)
	policy := access.SQLPolicy{DB: deps.DB(), Q: deps.Q}
	if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionWrite) || !actor.TrustedLocal && actor.UserID == "" {
		return nil, actor, policy, access.ErrDocumentForbidden
	}
	var tx *sql.Tx
	var err error
	if actor.TrustedLocal {
		tx, err = deps.DB().BeginTx(ctx, nil)
	} else {
		tx, policy, err = policy.BeginMetadataWrite(ctx, actor, memoryCommitSQLite(deps))
	}
	if err != nil {
		return nil, actor, policy, err
	}
	fail := func(err error) (*sql.Tx, access.MetadataActor, access.SQLPolicy, error) {
		_ = tx.Rollback()
		return nil, actor, policy, err
	}
	if memoryCommitSQLite(deps) {
		_, err = tx.ExecContext(ctx, "UPDATE memories SET id=id WHERE 1=0")
	} else {
		_, err = tx.ExecContext(ctx, "LOCK TABLE memories IN SHARE ROW EXCLUSIVE MODE")
	}
	if err != nil {
		return fail(err)
	}
	return tx, actor, policy.WithReadTransaction(tx), nil
}

func resolveMemoryDeleteTarget(ctx context.Context, tx *sql.Tx, deps Deps, actor access.MetadataActor, req DeleteMemoryRequest) (DeletedMemory, error) {
	if req.MemoryID != "" {
		row := tx.QueryRowContext(ctx, deps.Q(`SELECT id,key,collection_name,owner_id FROM memories
			WHERE id=$1 AND (owner_id=$2 OR owner_id='') AND superseded_by=''`), req.MemoryID, actor.UserID)
		var target DeletedMemory
		if err := row.Scan(&target.ID, &target.Key, &target.Collection, &target.OwnerID); errors.Is(err, sql.ErrNoRows) {
			return DeletedMemory{}, ErrMemoryDeleteNotFound
		} else if err != nil {
			return DeletedMemory{}, err
		}
		return target, nil
	}

	query := `SELECT id,key,collection_name,owner_id FROM memories
		WHERE key=$1 AND owner_id=$2 AND superseded_by=''`
	args := []any{req.Key, actor.UserID}
	if req.Collection != "" {
		query += ` AND collection_name=$3`
		args = append(args, req.Collection)
	}
	query += ` ORDER BY id LIMIT 2`
	rows, err := tx.QueryContext(ctx, deps.Q(query), args...)
	if err != nil {
		return DeletedMemory{}, err
	}
	defer rows.Close()
	targets := make([]DeletedMemory, 0, 2)
	for rows.Next() {
		var target DeletedMemory
		if err := rows.Scan(&target.ID, &target.Key, &target.Collection, &target.OwnerID); err != nil {
			return DeletedMemory{}, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return DeletedMemory{}, err
	}
	switch len(targets) {
	case 0:
		return DeletedMemory{}, ErrMemoryDeleteNotFound
	case 1:
		return targets[0], nil
	default:
		return DeletedMemory{}, ErrMemoryDeleteAmbiguous
	}
}

// ToolDeleteMemory permanently removes exactly one memory. memory_id is the
// canonical selector. The legacy key selector remains available for a unique
// personal match and returns an error when it is ambiguous or absent.
func ToolDeleteMemory(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	if deps == nil || deps.DB() == nil {
		return toolError("database not configured")
	}
	memoryID, memoryIDPresent, err := deleteMemoryStringArg(args, "memory_id")
	if err != nil {
		return toolError(err.Error())
	}
	key, keyPresent, err := deleteMemoryStringArg(args, "key")
	if err != nil {
		return toolError(err.Error())
	}
	collection, collectionPresent, err := deleteMemoryStringArg(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}
	if memoryIDPresent && (keyPresent || collectionPresent) {
		return toolError("memory_id cannot be combined with key or collection")
	}
	if (!memoryIDPresent || memoryID == "") && (!keyPresent || key == "") {
		return toolError("exactly one of 'memory_id' or 'key' is required")
	}

	target, err := DeleteMemory(ctx, deps, DeleteMemoryRequest{MemoryID: memoryID, Key: key, Collection: collection})
	if err != nil {
		switch {
		case errors.Is(err, ErrMemoryDeleteNotFound):
			if memoryID != "" {
				return toolError("No memory matched memory_id " + memoryID)
			}
			return toolError("No memory matched key " + key)
		case errors.Is(err, ErrMemoryDeleteAmbiguous):
			return toolError("Memory key " + key + " is ambiguous; pass memory_id or collection")
		case errors.Is(err, ErrMemoryDeleteSelectors):
			return toolError("memory delete selectors conflict")
		default:
			return toolError(err.Error())
		}
	}
	return statusResult(true, "Deleted "+target.Key+" ("+strconv.Itoa(1)+" record(s))")
}

func deleteMemoryStringArg(args map[string]any, name string) (string, bool, error) {
	value, present := args[name]
	if !present {
		return "", false, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("'%s' must be a string", name)
	}
	return text, true, nil
}
