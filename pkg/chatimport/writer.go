package chatimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Q translates PostgreSQL-style $N placeholders to the active dialect.
// It mirrors internal/http.Q and pkg/ingest.q; the writer stays transportable
// between the server (Postgres or SQLite) and tests.
type Q func(string) string

// SQLiteQ rewrites $N placeholders to ? and NOW() to CURRENT_TIMESTAMP.
func SQLiteQ(query string) string {
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] == '$' && i+1 < len(query) && query[i+1] >= '1' && query[i+1] <= '9' {
			b.WriteByte('?')
			i++
			for i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				i++
			}
			continue
		}
		b.WriteByte(query[i])
	}
	s := b.String()
	s = strings.ReplaceAll(s, "NOW()", "CURRENT_TIMESTAMP")
	return strings.ReplaceAll(s, "now()", "CURRENT_TIMESTAMP")
}

// NoopQ leaves the query untouched (PostgreSQL dialect).
func NoopQ(query string) string { return query }

// SchemaStatements creates the chat_import_* tables. The DDL is deliberately
// dialect-neutral (TEXT timestamps set by the writer, no dialect-specific
// defaults), so the same list applies to PostgreSQL and SQLite.
// internal/http.MigrateSchema appends these on startup.
var SchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS chat_import_runs (
		id TEXT PRIMARY KEY,
		platform TEXT NOT NULL,
		source_path TEXT NOT NULL DEFAULT '',
		source_sha256 TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		imported_count INTEGER NOT NULL DEFAULT 0,
		skipped_count INTEGER NOT NULL DEFAULT 0,
		warned_count INTEGER NOT NULL DEFAULT 0,
		warnings TEXT NOT NULL DEFAULT '[]',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_import_runs_platform ON chat_import_runs(platform, started_at)`,
	`CREATE TABLE IF NOT EXISTS chat_import_messages (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES chat_import_runs(id) ON DELETE CASCADE,
		platform TEXT NOT NULL,
		session_id TEXT NOT NULL,
		session_title TEXT NOT NULL DEFAULT '',
		external_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		role TEXT NOT NULL,
		kind TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		source_created_at TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		imported_at TEXT NOT NULL,
		UNIQUE(external_id, session_id, platform)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_import_messages_session ON chat_import_messages(platform, session_id, ordinal)`,
}

// EnsureSchema creates the chat_import_* tables if they do not exist.
func EnsureSchema(ctx context.Context, db *sql.DB, q Q) error {
	if db == nil || q == nil {
		return fmt.Errorf("chatimport: db and q are required")
	}
	for _, stmt := range SchemaStatements {
		if _, err := db.ExecContext(ctx, q(stmt)); err != nil {
			return fmt.Errorf("chatimport schema: %w", err)
		}
	}
	return nil
}

// sanitizePGText neutralizes NUL bytes and invalid UTF-8 sequences —
// PostgreSQL TEXT rejects both.
func sanitizePGText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", " "), "\uFFFD")
}

// RunInfo identifies one importer invocation recorded in the ledger.
type RunInfo struct {
	ID           string
	Platform     Platform
	SourcePath   string
	SourceSHA256 string
	StartedAt    string
}

// StartRun inserts a ledger row with status 'running'. Idempotent on the
// run id so a retried POST recreates nothing.
func StartRun(ctx context.Context, db *sql.DB, q Q, run RunInfo) error {
	if run.ID == "" {
		run.ID = uuid.NewString()
	}
	_, err := db.ExecContext(ctx, q(`
		INSERT INTO chat_import_runs (id, platform, source_path, source_sha256, status, started_at)
		VALUES ($1, $2, $3, $4, 'running', $5)
		ON CONFLICT(id) DO NOTHING
	`), run.ID, string(run.Platform), run.SourcePath, run.SourceSHA256, run.StartedAt)
	if err != nil {
		return fmt.Errorf("chatimport start run: %w", err)
	}
	return nil
}

// FinishRun closes a ledger row with final counters. Only a 'running' row is
// closable: a retried POST over an already-finished run must not rewrite the
// ledger history. Warnings are capped to keep the row small; the count stays
// exact.
func FinishRun(ctx context.Context, db *sql.DB, q Q, runID, status string, imported, skipped, warned int, warnings []string, finishedAt string) error {
	const maxWarnings = 50
	if len(warnings) > maxWarnings {
		warnings = append(append([]string{}, warnings[:maxWarnings]...),
			fmt.Sprintf("…%d more warnings suppressed", len(warnings)-maxWarnings))
	}
	raw, err := json.Marshal(warnings)
	if err != nil {
		return fmt.Errorf("chatimport finish run: %w", err)
	}
	res, err := db.ExecContext(ctx, q(`
		UPDATE chat_import_runs
		SET status = $1, imported_count = $2, skipped_count = $3, warned_count = $4, warnings = $5, finished_at = $6
		WHERE id = $7 AND status = 'running'
	`), status, imported, skipped, warned, string(raw), finishedAt, runID)
	if err != nil {
		return fmt.Errorf("chatimport finish run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists string
		err := db.QueryRowContext(ctx, q(`SELECT status FROM chat_import_runs WHERE id = $1`), runID).Scan(&exists)
		if err == sql.ErrNoRows {
			return fmt.Errorf("chatimport finish run: run %s not found", runID)
		}
		if err != nil {
			return fmt.Errorf("chatimport finish run: %w", err)
		}
		// Already finished by a retried request — keep the original counters.
	}
	return nil
}

// messageID is deterministic: the same source item always maps to the same
// primary key, which makes re-imports no-ops at the DB level.
func messageID(platform Platform, sessionID, externalID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(string(platform)+"/"+sessionID+"/"+externalID)).String()
}

// InsertConversation writes all conversation messages with
// ON CONFLICT DO NOTHING on (external_id, session_id, platform) and returns
// how many rows were actually inserted. Secrets scanning is warn-only: rows
// are stored as-is and findings are appended to warnings.
func InsertConversation(ctx context.Context, db *sql.DB, q Q, runID string, conv *Conversation, warnings *[]string, now time.Time) (int, error) {
	if conv == nil || conv.SessionID == "" {
		return 0, fmt.Errorf("chatimport: conversation without session_id")
	}
	importedAt := now.UTC().Format(time.RFC3339)
	title := sanitizePGText(conv.Title)
	inserted := 0
	for _, m := range conv.Messages {
		// PostgreSQL TEXT rejects NUL bytes and invalid UTF-8; tool outputs
		// occasionally carry raw binary, and byte-boundary truncation in
		// helpers can split runes. Sanitize every text column so the row
		// survives on both dialects (SQLite masks all of this in tests).
		m.Content = sanitizePGText(m.Content)
		for k, v := range m.Metadata {
			m.Metadata[k] = sanitizePGText(v)
		}
		meta := "{}"
		if len(m.Metadata) > 0 {
			raw, err := json.Marshal(m.Metadata)
			if err != nil {
				return inserted, fmt.Errorf("chatimport: metadata marshal: %w", err)
			}
			meta = string(raw)
		}
		for _, finding := range ScanSecrets(m.Content) {
			*warnings = append(*warnings, fmt.Sprintf("session %s message %s: possible %s (stored as-is)", conv.SessionID, m.ExternalID, finding))
		}
		res, err := db.ExecContext(ctx, q(`
			INSERT INTO chat_import_messages
				(id, run_id, platform, session_id, session_title, external_id, ordinal, role, kind, model, content, source_created_at, metadata, imported_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			ON CONFLICT(external_id, session_id, platform) DO NOTHING
		`),
			messageID(conv.Platform, conv.SessionID, m.ExternalID), runID, string(conv.Platform),
			conv.SessionID, title, m.ExternalID, m.Ordinal, m.Role, string(m.Kind),
			m.Model, m.Content, m.CreatedAt, meta, importedAt)
		if err != nil {
			return inserted, fmt.Errorf("chatimport insert message %s: %w", m.ExternalID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted++
		}
	}
	return inserted, nil
}
