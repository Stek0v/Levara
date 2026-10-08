package mcp

// Per-agent diary tools: diary_write, diary_read.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
)

const (
	diaryReadLimit = 100
	diaryTimeout   = 10 * time.Second
)

// diaryIdentity uses verified transport authority. Only explicitly trusted,
// anonymous local callers retain the historical global agent namespace.
func diaryIdentity(ctx context.Context, deps Deps, agent string) (string, access.MetadataActor, bool, error) {
	actor := deps.MetadataActor(ctx)
	legacy := actor.TrustedLocal && actor.UserID == "" && actor.TenantID == ""
	if legacy {
		return DiaryOwner(agent), actor, true, nil
	}
	actor.TrustedLocal = false
	if actor.UserID == "" || actor.Credential.Kind == "" ||
		actor.Credential.Kind == "api_key" && strings.TrimSpace(actor.APIKeyPermissions) == "" {
		return "", actor, false, access.ErrRevokedCredential
	}
	identity, err := json.Marshal([3]string{actor.UserID, actor.TenantID, strings.TrimSpace(agent)})
	if err != nil {
		return "", actor, false, err
	}
	return "diary:v1:" + base64.RawURLEncoding.EncodeToString(identity), actor, false, nil
}

func diaryRecheck(ctx context.Context, policy access.SQLPolicy, actor access.MetadataActor) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c := actor.Credential
	if err := policy.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
		return err
	}
	if actor.TenantID != "" {
		member, err := policy.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return err
		}
		if !member {
			return access.ErrDocumentForbidden
		}
	}
	return ctx.Err()
}

// ToolDiaryWrite upserts an entry for the verified caller, selected tenant,
// normalized agent and collection, preserving its canonical ID.
func ToolDiaryWrite(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return errorResult("database not configured")
	}
	agent, _ := args["agent"].(string)
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if strings.TrimSpace(agent) == "" || key == "" || value == "" {
		return errorResult("'agent', 'key', 'value' required")
	}
	ctx, cancel := context.WithTimeout(ctx, diaryTimeout)
	defer cancel()
	owner, actor, legacy, err := diaryIdentity(ctx, deps, agent)
	if err != nil {
		return errorResult(err.Error())
	}
	collectionName, _ := args["collection"].(string)
	var tx *sql.Tx
	var policy access.SQLPolicy
	if !legacy {
		policy = access.SQLPolicy{DB: db, Q: deps.Q}
		tx, policy, err = policy.BeginMetadataWrite(ctx, actor, memoryCommitSQLite(deps))
		if err != nil {
			return errorResult(err.Error())
		}
		defer tx.Rollback()
	} else {
		tx, err = db.BeginTx(ctx, nil)
		if err != nil {
			return errorResult(err.Error())
		}
		defer tx.Rollback()
	}
	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := archiveRetiredMemoryKey(ctx, tx, deps, key, owner, collectionName, now); err != nil {
		return errorResult(err.Error())
	}

	// Unique placeholders keep SQLite rewriting equivalent to PostgreSQL.
	_, err = tx.ExecContext(ctx, deps.Q(`
		INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		VALUES ($1, $2, $3, 'diary', $4, $5, '', '', FALSE, 0, $6, $7)
		ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET value = $8, updated_at = $9
	`),
		id, key, value, owner, collectionName, now, now,
		value, now)
	if err != nil {
		return errorResult(err.Error())
	}
	if !legacy {
		if err := diaryRecheck(ctx, policy, actor); err != nil {
			return errorResult(err.Error())
		}
	}
	if err := tx.Commit(); err != nil {
		return errorResult(err.Error())
	}
	return statusResult(true, fmt.Sprintf("Diary[%s] wrote %s", agent, key))
}

// ToolDiaryRead returns complete entries in the caller's diary namespace,
// optionally filtered by query substring and collection.
func ToolDiaryRead(ctx context.Context, deps Deps, args map[string]any) ToolResult {
	db := deps.DB()
	if db == nil {
		return jsonResult(map[string]any{"entries": []any{}})
	}
	agent, _ := args["agent"].(string)
	if strings.TrimSpace(agent) == "" {
		return errorResult("'agent' required")
	}
	ctx, cancel := context.WithTimeout(ctx, diaryTimeout)
	defer cancel()
	owner, actor, legacy, err := diaryIdentity(ctx, deps, agent)
	if err != nil {
		return errorResult(err.Error())
	}
	query := db.QueryContext
	var policy access.SQLPolicy
	if !legacy {
		if !access.APIKeyAllows(actor.APIKeyPermissions, access.ActionRead) {
			return errorResult(access.ErrDocumentForbidden.Error())
		}
		var tx *sql.Tx
		var release func()
		policy = access.SQLPolicy{DB: db, Q: deps.Q}
		tx, policy, release, err = policy.BeginTransferFenceTx(ctx, memoryCommitSQLite(deps))
		if err != nil {
			return errorResult(err.Error())
		}
		defer release()
		if err := diaryRecheck(ctx, policy, actor); err != nil {
			return errorResult(err.Error())
		}
		query = tx.QueryContext
	}
	queryStr, _ := args["query"].(string)
	collectionName, _ := args["collection"].(string)

	var conds []string
	var qargs []any
	pos := 1
	conds = append(conds, fmt.Sprintf("owner_id = $%d", pos))
	qargs = append(qargs, owner)
	pos++
	conds = append(conds, "superseded_by = ''", "valid_until IS NULL")
	if queryStr != "" {
		pat := "%" + queryStr + "%"
		conds = append(conds, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", pos, pos+1))
		qargs = append(qargs, pat, pat)
		pos += 2
	}
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
	}
	sqlStr := fmt.Sprintf(`
		SELECT key, value, created_at, updated_at FROM memories
		WHERE %s ORDER BY updated_at DESC LIMIT %d
	`, strings.Join(conds, " AND "), diaryReadLimit)
	rows, err := query(ctx, deps.Q(sqlStr), qargs...)
	if err != nil {
		return errorResult(err.Error())
	}
	defer rows.Close()

	var entries []map[string]any
	for rows.Next() {
		var k, v, ca, ua string
		if err := rows.Scan(&k, &v, &ca, &ua); err != nil {
			return errorResult(err.Error())
		}
		entries = append(entries, map[string]any{
			"key": k, "value": v, "created_at": ca, "updated_at": ua,
		})
	}
	if err := rows.Err(); err != nil {
		return errorResult(err.Error())
	}
	if err := rows.Close(); err != nil {
		return errorResult(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return errorResult(err.Error())
	}
	if !legacy {
		if err := diaryRecheck(ctx, policy, actor); err != nil {
			return errorResult(err.Error())
		}
	}
	if entries == nil {
		return jsonResult(map[string]any{
			"entries": []any{},
			"message": fmt.Sprintf("Diary[%s] is empty", agent),
		})
	}
	return jsonResult(map[string]any{"entries": entries})
}
