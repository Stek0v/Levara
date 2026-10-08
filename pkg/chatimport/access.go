package chatimport

import (
	"context"
	"database/sql"
	"errors"

	"github.com/stek0v/levara/pkg/access"
)

var (
	ErrChatForbidden = errors.New("imported chat access denied")
	ErrChatAmbiguous = errors.New("ambiguous imported chat; specify chat_id and platform")
)

func localChatActor(actor access.MetadataActor) bool {
	return actor.TrustedLocal && actor.UserID == "" && actor.TenantID == ""
}

// RecheckChatActor accepts only verified transport facts. A configured local
// mode never exempts a nonempty identity from live credential checks.
func RecheckChatActor(ctx context.Context, policy access.SQLPolicy, actor access.MetadataActor, action string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !access.APIKeyAllows(actor.APIKeyPermissions, action) {
		return ErrChatForbidden
	}
	if localChatActor(actor) {
		return nil
	}
	if actor.UserID == "" || actor.Credential.Kind == "" || actor.Credential.Kind == "api_key" && actor.APIKeyPermissions == "" {
		return access.ErrRevokedCredential
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
			return ErrChatForbidden
		}
	}
	return ctx.Err()
}

// LockChatRegistry follows the existing access fence lock order. All callers
// acquire their credential/project fence before this registry lock.
func LockChatRegistry(ctx context.Context, tx *sql.Tx, sqlite, write bool) error {
	if tx == nil {
		return errors.New("chat access requires transaction")
	}
	if sqlite {
		return nil // existing access fence has acquired SQLite's write exclusion
	}
	query := "LOCK TABLE chat_import_sessions IN SHARE MODE"
	if write {
		query = "LOCK TABLE chat_import_sessions IN SHARE ROW EXCLUSIVE MODE"
	}
	_, err := tx.ExecContext(ctx, query)
	return err
}

// chatProjectRole deliberately excludes public-project inheritance and global
// superuser bypass: neither represents the owner's explicit project audience.
func chatProjectRole(ctx context.Context, tx *sql.Tx, q Q, project, user string) (string, error) {
	if project == "" || user == "" {
		return "", nil
	}
	var owner, role string
	err := tx.QueryRowContext(ctx, q(`SELECT COALESCE(d.owner_id,''),COALESCE(s.role,'')
 FROM datasets d LEFT JOIN dataset_shares s ON s.dataset_id=d.id AND s.user_id=$1
 WHERE d.id=$2`), user, project).Scan(&owner, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", nil
	}
	if owner == user {
		return access.RoleAdmin, nil
	}
	return role, nil
}

// CanReadChat runs inside the caller's fenced transaction. Identity rows must
// have been read from that same transaction, never client-provided metadata.
func CanReadChat(ctx context.Context, tx *sql.Tx, q Q, policy access.SQLPolicy, actor access.MetadataActor, chat ChatIdentity) (bool, error) {
	if err := RecheckChatActor(ctx, policy, actor, access.ActionRead); err != nil {
		return false, err
	}
	if localChatActor(actor) {
		return chat.OwnerID == "" && chat.TenantID == "", nil
	}
	if chat.OwnerID == "" || chat.TenantID != actor.TenantID {
		return false, nil
	}
	if chat.OwnerID == actor.UserID {
		return true, nil
	}
	role, err := chatProjectRole(ctx, tx, q, chat.ProjectID, actor.UserID)
	return access.RoleAllows(role, access.ActionRead), err
}

// ResolveChat prefers the caller's own source identity, otherwise requires a
// unique readable project match. Rows close before policy SQL on pool size one.
func ResolveChat(ctx context.Context, tx *sql.Tx, q Q, policy access.SQLPolicy, actor access.MetadataActor, chatID, platform, sourceSession string) (ChatIdentity, error) {
	if err := RecheckChatActor(ctx, policy, actor, access.ActionRead); err != nil {
		return ChatIdentity{}, err
	}
	if chatID == "" && (platform == "" || sourceSession == "") {
		return ChatIdentity{}, errors.New("chat_id or platform/session_id required")
	}
	query := `SELECT id,owner_id,tenant_id,platform,source_session_id,project_id FROM chat_import_sessions WHERE `
	args := []any{}
	if chatID != "" {
		query += "id=$1"
		args = append(args, chatID)
		if platform != "" {
			query += " AND platform=$2"
			args = append(args, platform)
		}
	} else {
		query += "platform=$1 AND source_session_id=$2"
		args = append(args, platform, sourceSession)
	}
	rows, err := tx.QueryContext(ctx, q(query), args...)
	if err != nil {
		return ChatIdentity{}, err
	}
	var candidates []ChatIdentity
	for rows.Next() {
		var chat ChatIdentity
		if err := rows.Scan(&chat.ID, &chat.OwnerID, &chat.TenantID, &chat.Platform, &chat.SourceSessionID, &chat.ProjectID); err != nil {
			rows.Close()
			return ChatIdentity{}, err
		}
		candidates = append(candidates, chat)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return ChatIdentity{}, err
	}
	if closeErr != nil {
		return ChatIdentity{}, closeErr
	}
	var matches []ChatIdentity
	for _, chat := range candidates {
		allowed, err := CanReadChat(ctx, tx, q, policy, actor, chat)
		if err != nil {
			return ChatIdentity{}, err
		}
		if !allowed {
			continue
		}
		if chatID == "" && actor.UserID != "" && chat.OwnerID == actor.UserID {
			return chat, nil
		}
		matches = append(matches, chat)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return ChatIdentity{}, ErrChatAmbiguous
	}
	if localChatActor(actor) && len(candidates) == 0 && platform != "" {
		physical := sourceSession
		if chatID != "" {
			physical = chatID
		}
		var occupied int
		if err := tx.QueryRowContext(ctx, q("SELECT COUNT(*) FROM chat_import_sessions WHERE id=$1 AND platform=$2"), physical, platform).Scan(&occupied); err != nil {
			return ChatIdentity{}, err
		}
		if occupied != 0 {
			return ChatIdentity{}, ErrChatForbidden
		}
		// Unknown legacy rows are available only to explicit local mode.
		return ChatIdentity{ID: physical, Platform: Platform(platform), SourceSessionID: physical}, nil
	}
	return ChatIdentity{}, ErrChatForbidden
}

// SetChatProject changes only a chat loaded under the write fence. Owners
// consent to attachment; project administrators can detach an existing share.
func SetChatProject(ctx context.Context, tx *sql.Tx, q Q, policy access.SQLPolicy, actor access.MetadataActor, chat ChatIdentity, project string) error {
	if err := RecheckChatActor(ctx, policy, actor, access.ActionWrite); err != nil {
		return err
	}
	if localChatActor(actor) || chat.OwnerID == "" || chat.TenantID != actor.TenantID {
		return ErrChatForbidden
	}
	owner := actor.UserID == chat.OwnerID
	if project != "" {
		if !owner {
			return ErrChatForbidden
		}
		role, err := chatProjectRole(ctx, tx, q, project, actor.UserID)
		if err != nil {
			return err
		}
		if !access.RoleAllows(role, access.ActionWrite) {
			return ErrChatForbidden
		}
	} else if !owner {
		role, err := chatProjectRole(ctx, tx, q, chat.ProjectID, actor.UserID)
		if err != nil {
			return err
		}
		if role != access.RoleAdmin {
			return ErrChatForbidden
		}
	}
	res, err := tx.ExecContext(ctx, q(`UPDATE chat_import_sessions SET project_id=$1
 WHERE id=$2 AND platform=$3 AND owner_id=$4 AND tenant_id=$5 AND project_id=$6`), project, chat.ID, string(chat.Platform), chat.OwnerID, chat.TenantID, chat.ProjectID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrChatForbidden
	}
	return RecheckChatActor(ctx, policy, actor, access.ActionWrite)
}
