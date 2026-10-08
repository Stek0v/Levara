package chatimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ImportScope is verified by the calling transport. An empty tenant is a
// distinct personal scope; it does not imply trusted-local access.
type ImportScope struct {
	OwnerID      string
	TenantID     string
	TrustedLocal bool
}

// ChatIdentity connects physical message storage to its original selector.
// ProjectID is blank until the owner explicitly shares the conversation.
type ChatIdentity struct {
	ID              string
	OwnerID         string
	TenantID        string
	Platform        Platform
	SourceSessionID string
	ProjectID       string
}

func validImportText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (scope ImportScope) validate() error {
	if scope.TrustedLocal && scope.OwnerID == "" && scope.TenantID == "" {
		return nil
	}
	if scope.TrustedLocal || scope.OwnerID == "" || strings.TrimSpace(scope.OwnerID) != scope.OwnerID || strings.TrimSpace(scope.TenantID) != scope.TenantID || !validImportText(scope.OwnerID) || !validImportText(scope.TenantID) {
		return fmt.Errorf("chatimport: invalid import scope")
	}
	return nil
}

func importTupleID(kind string, fields ...string) string {
	// JSON string arrays are unambiguous even when source selectors contain
	// delimiters. The version and kind keep run/chat/message identities apart.
	data, _ := json.Marshal(append([]string{"levara:chat-import:v1", kind}, fields...))
	return uuid.NewSHA1(uuid.NameSpaceURL, data).String()
}

// Reserve the writer before inspecting unregistered legacy physical keys.
// The calling transport acquires its authority fence before these tables.
func lockImportWriter(ctx context.Context, tx *sql.Tx, q Q) error {
	if q("$1") == "?" {
		_, err := tx.ExecContext(ctx, `UPDATE chat_import_sessions SET id=id WHERE 1=0`)
		return err
	}
	_, err := tx.ExecContext(ctx, `LOCK TABLE chat_import_sessions,chat_import_run_scopes,chat_import_runs,chat_import_messages IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

type importRunScope struct {
	owner, tenant, source string
	local                 bool
	platform              Platform
}

func readImportRun(ctx context.Context, tx *sql.Tx, q Q, id string) (importRunScope, bool, error) {
	var row importRunScope
	err := tx.QueryRowContext(ctx, q(`SELECT s.owner_id,s.tenant_id,s.source_run_id,s.trusted_local,r.platform
		FROM chat_import_run_scopes s JOIN chat_import_runs r ON r.id=s.run_id WHERE s.run_id=$1`), id).Scan(&row.owner, &row.tenant, &row.source, &row.local, &row.platform)
	if errors.Is(err, sql.ErrNoRows) {
		return row, false, nil
	}
	return row, err == nil, err
}

func (scope ImportScope) matchesRun(row importRunScope, platform Platform) bool {
	return row.owner == scope.OwnerID && row.tenant == scope.TenantID && row.local == scope.TrustedLocal && row.platform == platform
}

// StartScopedRun owns its transaction. The supplied scope must already have
// been authenticated by the transport; this writer does not infer authority.
func StartScopedRun(ctx context.Context, db *sql.DB, q Q, scope ImportScope, run RunInfo) (string, error) {
	if db == nil || q == nil {
		return "", fmt.Errorf("chatimport: db and q are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := StartScopedRunTx(ctx, tx, q, scope, run)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// StartScopedRunTx participates in the caller's fenced transaction. The
// caller must roll back on error and recheck its authority before committing.
// A retry may supply the physical ID returned by a prior successful call.
func StartScopedRunTx(ctx context.Context, tx *sql.Tx, q Q, scope ImportScope, run RunInfo) (string, error) {
	if tx == nil || q == nil {
		return "", fmt.Errorf("chatimport: tx and q are required")
	}
	if err := scope.validate(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if run.Platform == "" || !validImportText(string(run.Platform)) || !validImportText(run.ID) {
		return "", fmt.Errorf("chatimport: invalid run selector")
	}
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	if err := lockImportWriter(ctx, tx, q); err != nil {
		return "", err
	}
	// Resolve a returned canonical ID before deriving a new one. Foreign IDs
	// are rejected rather than interpreted as an invitation to adopt a run.
	row, found, err := readImportRun(ctx, tx, q, run.ID)
	if err != nil {
		return "", err
	}
	if found && (!row.local || scope.TrustedLocal) {
		if !scope.matchesRun(row, run.Platform) {
			return "", fmt.Errorf("chatimport: run scope or platform mismatch")
		}
		return run.ID, ctx.Err()
	}
	id := run.ID
	if !scope.TrustedLocal {
		id = importTupleID("run", scope.OwnerID, scope.TenantID, run.ID)
	}
	row, found, err = readImportRun(ctx, tx, q, id)
	if err != nil {
		return "", err
	}
	if found {
		if !scope.matchesRun(row, run.Platform) || row.source != run.ID {
			return "", fmt.Errorf("chatimport: run identity collision")
		}
		return id, ctx.Err()
	}
	var existingPlatform Platform
	err = tx.QueryRowContext(ctx, q(`SELECT platform FROM chat_import_runs WHERE id=$1`), id).Scan(&existingPlatform)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil && (!scope.TrustedLocal || existingPlatform != run.Platform) {
		return "", fmt.Errorf("chatimport: unclaimed run identity collision")
	}
	res, err := tx.ExecContext(ctx, q(`INSERT INTO chat_import_runs(id,platform,source_path,source_sha256,status,started_at)
		VALUES($1,$2,$3,$4,'running',$5) ON CONFLICT(id) DO NOTHING`), id, string(run.Platform), run.SourcePath, run.SourceSHA256, run.StartedAt)
	if err != nil {
		return "", fmt.Errorf("chatimport start run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if n == 0 && !scope.TrustedLocal {
		// A concurrent retry is safe only if it also committed this registry.
		row, found, err = readImportRun(ctx, tx, q, id)
		if err != nil {
			return "", err
		}
		if !found || !scope.matchesRun(row, run.Platform) || row.source != run.ID {
			return "", fmt.Errorf("chatimport: unclaimed run identity collision")
		}
	}
	if _, err := tx.ExecContext(ctx, q(`INSERT INTO chat_import_run_scopes(run_id,owner_id,tenant_id,source_run_id,trusted_local)
		VALUES($1,$2,$3,$4,$5) ON CONFLICT(run_id) DO NOTHING`), id, scope.OwnerID, scope.TenantID, run.ID, scope.TrustedLocal); err != nil {
		return "", fmt.Errorf("chatimport register run: %w", err)
	}
	row, found, err = readImportRun(ctx, tx, q, id)
	if err != nil {
		return "", err
	}
	if !found || !scope.matchesRun(row, run.Platform) || row.source != run.ID {
		return "", fmt.Errorf("chatimport: run identity collision")
	}
	return id, ctx.Err()
}

// InsertScopedConversation owns a transaction covering identity registration
// and every message. Errors return no partial count or appended warnings.
func InsertScopedConversation(ctx context.Context, db *sql.DB, q Q, scope ImportScope, runID string, conv *Conversation, warnings *[]string, now time.Time) (ChatIdentity, int, error) {
	if db == nil || q == nil {
		return ChatIdentity{}, 0, fmt.Errorf("chatimport: db and q are required")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ChatIdentity{}, 0, err
	}
	defer tx.Rollback()
	var pending []string
	chat, count, err := InsertScopedConversationTx(ctx, tx, q, scope, runID, conv, &pending, now)
	if err != nil {
		return ChatIdentity{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return ChatIdentity{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return ChatIdentity{}, 0, err
	}
	if warnings != nil {
		*warnings = append(*warnings, pending...)
	}
	return chat, count, nil
}

// InsertScopedConversationTx uses the caller's transaction and canonical run
// ID. Its count/warnings are provisional until that transaction commits; any
// error requires rollback of the caller's transaction.
func InsertScopedConversationTx(ctx context.Context, tx *sql.Tx, q Q, scope ImportScope, runID string, conv *Conversation, warnings *[]string, now time.Time) (ChatIdentity, int, error) {
	if tx == nil || q == nil {
		return ChatIdentity{}, 0, fmt.Errorf("chatimport: tx and q are required")
	}
	if err := scope.validate(); err != nil {
		return ChatIdentity{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return ChatIdentity{}, 0, err
	}
	if conv == nil || conv.SessionID == "" || conv.Platform == "" || !validImportText(conv.SessionID) || !validImportText(string(conv.Platform)) {
		return ChatIdentity{}, 0, fmt.Errorf("chatimport: invalid conversation selector")
	}
	if err := lockImportWriter(ctx, tx, q); err != nil {
		return ChatIdentity{}, 0, err
	}
	row, found, err := readImportRun(ctx, tx, q, runID)
	if err != nil {
		return ChatIdentity{}, 0, err
	}
	if !found && scope.TrustedLocal {
		// Explicit local callers may continue a pre-registry ledger, without
		// assigning its contents to an authenticated identity.
		var exists bool
		if err := tx.QueryRowContext(ctx, q(`SELECT EXISTS(SELECT 1 FROM chat_import_runs WHERE id=$1)`), runID).Scan(&exists); err != nil {
			return ChatIdentity{}, 0, err
		}
		if !exists {
			return ChatIdentity{}, 0, fmt.Errorf("chatimport: run not found")
		}
		_, err = StartScopedRunTx(ctx, tx, q, scope, RunInfo{ID: runID, Platform: conv.Platform})
		if err != nil {
			return ChatIdentity{}, 0, err
		}
		row, found, err = readImportRun(ctx, tx, q, runID)
		if err != nil {
			return ChatIdentity{}, 0, err
		}
	}
	if !found || !scope.matchesRun(row, conv.Platform) {
		return ChatIdentity{}, 0, fmt.Errorf("chatimport: run scope or platform mismatch")
	}
	chat := ChatIdentity{ID: conv.SessionID, OwnerID: scope.OwnerID, TenantID: scope.TenantID, Platform: conv.Platform, SourceSessionID: conv.SessionID}
	if !scope.TrustedLocal {
		chat.ID = importTupleID("session", scope.OwnerID, scope.TenantID, string(conv.Platform), conv.SessionID)
	}
	var stored ChatIdentity
	var local bool
	read := func() error {
		return tx.QueryRowContext(ctx, q(`SELECT id,owner_id,tenant_id,platform,source_session_id,project_id,trusted_local
			FROM chat_import_sessions WHERE id=$1 AND platform=$2`), chat.ID, string(chat.Platform)).Scan(&stored.ID, &stored.OwnerID, &stored.TenantID, &stored.Platform, &stored.SourceSessionID, &stored.ProjectID, &local)
	}
	err = read()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ChatIdentity{}, 0, err
	}
	if errors.Is(err, sql.ErrNoRows) {
		if !scope.TrustedLocal {
			var occupied bool
			if err := tx.QueryRowContext(ctx, q(`SELECT EXISTS(SELECT 1 FROM chat_import_messages WHERE session_id=$1 AND platform=$2)`), chat.ID, string(chat.Platform)).Scan(&occupied); err != nil {
				return ChatIdentity{}, 0, err
			}
			if occupied {
				return ChatIdentity{}, 0, fmt.Errorf("chatimport: unclaimed session identity collision")
			}
		}
		if _, err := tx.ExecContext(ctx, q(`INSERT INTO chat_import_sessions(id,owner_id,tenant_id,platform,source_session_id,project_id,trusted_local)
			VALUES($1,$2,$3,$4,$5,'',$6) ON CONFLICT(id,platform) DO NOTHING`), chat.ID, chat.OwnerID, chat.TenantID, string(chat.Platform), chat.SourceSessionID, scope.TrustedLocal); err != nil {
			return ChatIdentity{}, 0, fmt.Errorf("chatimport register session: %w", err)
		}
		if err := read(); err != nil {
			return ChatIdentity{}, 0, err
		}
	}
	if stored.OwnerID != scope.OwnerID || stored.TenantID != scope.TenantID || stored.SourceSessionID != conv.SessionID || local != scope.TrustedLocal {
		return ChatIdentity{}, 0, fmt.Errorf("chatimport: session identity collision")
	}
	var pending []string
	count, err := insertConversation(ctx, tx, q, runID, chat.ID, scope.TrustedLocal, conv, &pending, now)
	if err != nil {
		return ChatIdentity{}, 0, err
	}
	if err := ctx.Err(); err != nil {
		return ChatIdentity{}, 0, err
	}
	if warnings != nil {
		*warnings = append(*warnings, pending...)
	}
	return stored, count, nil
}

// FinishRunTx closes a ledger inside the caller's authority fence. The caller
// must first validate its run registry scope and roll back on any error.
func FinishRunTx(ctx context.Context, tx *sql.Tx, q Q, runID, status string, imported, skipped, warned int, warnings []string, finishedAt string) error {
	if tx == nil || q == nil {
		return fmt.Errorf("chatimport: tx and q are required")
	}
	return finishRun(ctx, tx, q, runID, status, imported, skipped, warned, warnings, finishedAt)
}
