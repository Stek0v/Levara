// render_version.go — P3: versioned RAG derivatives.
//
// RenderConversationMarkdown changes over time (e.g. v2 dropped system
// boilerplate). The raw layer is the source of truth; rendered documents
// are derivatives that must migrate to the current render version
// incrementally — like schema migrations, not overnight rebuild scripts.
// chat_import_rag tracks the version each session's searchable document
// was rendered with; the daemon skips unchanged renders (no duplicates)
// and a janitor refreshes stale ones on a budget.
package chatimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// RenderVersion is the current conversation-render revision.
// v1: initial renderer (system messages collapsed into <details>).
// v2: system boilerplate dropped entirely (search-noise fix).
const RenderVersion = 2

// RagSchemaStatements creates the rag-derivative registry (dialect-neutral).
var RagSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS chat_import_rag (
		platform TEXT NOT NULL,
		session_id TEXT NOT NULL,
		render_version INTEGER NOT NULL DEFAULT 0,
		data_id TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (platform, session_id)
	)`,
}

// EnsureRagSchema applies the registry DDL.
func EnsureRagSchema(ctx context.Context, db *sql.DB, q Q) error {
	for _, stmt := range RagSchemaStatements {
		if _, err := db.ExecContext(ctx, q(stmt)); err != nil {
			return fmt.Errorf("chatimport rag schema: %w", err)
		}
	}
	return nil
}

// RecordRagVersion upserts the render version (and optional data item id)
// for one session's searchable document.
func RecordRagVersion(ctx context.Context, db *sql.DB, q Q, platform Platform, sessionID string, version int, dataID string) error {
	_, err := db.ExecContext(ctx, q(`
		INSERT INTO chat_import_rag (platform, session_id, render_version, data_id, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT(platform, session_id) DO UPDATE SET
			render_version = $6, data_id = $7, updated_at = $8
	`), string(platform), sessionID, version, dataID, nowRFC3339(),
		version, dataID, nowRFC3339())
	return err
}

// RagVersion returns the recorded render version for a session (0 = none).
func RagVersion(ctx context.Context, db *sql.DB, q Q, platform Platform, sessionID string) int {
	var v int
	_ = db.QueryRowContext(ctx, q(`SELECT render_version FROM chat_import_rag WHERE platform = $1 AND session_id = $2`),
		string(platform), sessionID).Scan(&v)
	return v
}

// RagRef identifies a stale derivative for the janitor.
type RagRef struct {
	Platform  Platform
	SessionID string
}

// StaleRagSessions returns up to limit sessions whose recorded render
// version is below the current one (or not recorded at all, when the
// session exists in the raw layer).
func StaleRagSessions(ctx context.Context, db *sql.DB, q Q, currentVersion, limit int) ([]RagRef, error) {
	rows, err := db.QueryContext(ctx, q(`
		SELECT DISTINCT m.platform, m.session_id
		FROM chat_import_messages m
		LEFT JOIN chat_import_rag r
			ON r.platform = m.platform AND r.session_id = m.session_id
		WHERE COALESCE(r.render_version, 0) < $1
		ORDER BY m.session_id
		LIMIT $2
	`), currentVersion, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RagRef
	for rows.Next() {
		var ref RagRef
		var platform string
		if err := rows.Scan(&platform, &ref.SessionID); err != nil {
			return nil, err
		}
		ref.Platform = Platform(platform)
		out = append(out, ref)
	}
	return out, rows.Err()
}

// LoadConversation reconstructs a Conversation from the raw layer in
// storage order — the janitor's render source when source files are gone.
//
// A5 note: for conversations with >10k messages, this materializes all
// content in memory. The rag janitor mitigates by rendering segments
// (A4), but distill and future callers should be aware: prefer
// LoadConversationRange for incremental processing of huge sessions.
func LoadConversation(ctx context.Context, db *sql.DB, q Q, platform Platform, sessionID string) (*Conversation, error) {
	rows, err := db.QueryContext(ctx, q(`
		SELECT external_id, ordinal, role, kind, model, content, source_created_at, metadata, session_title
		FROM chat_import_messages
		WHERE platform = $1 AND session_id = $2
		ORDER BY ordinal, source_created_at, external_id
	`), string(platform), sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conv := &Conversation{Platform: platform, SessionID: sessionID}
	for rows.Next() {
		var m Message
		var title, metaRaw string
		if err := rows.Scan(&m.ExternalID, &m.Ordinal, &m.Role, &m.Kind, &m.Model, &m.Content, &m.CreatedAt, &metaRaw, &title); err != nil {
			return nil, err
		}
		if conv.Title == "" {
			conv.Title = title
		}
		if m.Model != "" && conv.Model == "" {
			conv.Model = m.Model
		}
		if conv.CreatedAt == "" {
			conv.CreatedAt = m.CreatedAt
		}
		if metaRaw != "" && metaRaw != "{}" {
			_ = json.Unmarshal([]byte(metaRaw), &m.Metadata)
		}
		conv.Messages = append(conv.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(conv.Messages) == 0 {
		return nil, fmt.Errorf("chatimport: no messages for %s/%s", platform, sessionID)
	}
	return conv, nil
}
