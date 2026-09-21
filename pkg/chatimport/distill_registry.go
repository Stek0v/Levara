// distill_registry.go — P5: auto-distillation registry.
//
// Tracks which imported sessions have been distilled into memories so a
// janitor can process new ones on a budget. Idempotent by (platform,
// session_id, hall): a re-distill upserts the same memory keys.
package chatimport

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// DistillSchemaStatements creates the distillation registry (dialect-neutral).
var DistillSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS chat_import_distill (
		platform TEXT NOT NULL,
		session_id TEXT NOT NULL,
		hall TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		memory_keys TEXT NOT NULL DEFAULT '[]',
		last_error TEXT NOT NULL DEFAULT '',
		distilled_at TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (platform, session_id, hall)
	)`,
}

// EnsureDistillSchema applies the registry DDL.
func EnsureDistillSchema(ctx context.Context, db *sql.DB, q Q) error {
	for _, stmt := range DistillSchemaStatements {
		if _, err := db.ExecContext(ctx, q(stmt)); err != nil {
			return fmt.Errorf("chatimport distill schema: %w", err)
		}
	}
	return nil
}

// DistillCandidate identifies a session ready for distillation.
type DistillCandidate struct {
	Platform  Platform
	SessionID string
	Title     string
	Messages  int
}

// DistillCandidates returns up to limit sessions from the raw layer with
// at least minMessages messages that have no recorded distillation for the
// given hall. Ordered oldest-first so early sessions distill first.
func DistillCandidates(ctx context.Context, db *sql.DB, q Q, hall string, minMessages, limit int) ([]DistillCandidate, error) {
	rows, err := db.QueryContext(ctx, q(`
		SELECT m.platform, m.session_id, MAX(m.session_title), COUNT(*) AS msgs
		FROM chat_import_messages m
		WHERE NOT EXISTS (
			SELECT 1 FROM chat_import_distill d
			WHERE d.platform = m.platform AND d.session_id = m.session_id AND d.hall = $1
			  AND d.status = 'ok'
		)
		GROUP BY m.platform, m.session_id
		HAVING COUNT(*) >= $2
		   AND SUM(CASE WHEN m.role = 'user' THEN 1 ELSE 0 END) >= 2
		ORDER BY MIN(m.source_created_at)
		LIMIT $3
	`), hall, minMessages, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DistillCandidate
	for rows.Next() {
		var c DistillCandidate
		var platform string
		if err := rows.Scan(&platform, &c.SessionID, &c.Title, &c.Messages); err != nil {
			return nil, err
		}
		c.Platform = Platform(platform)
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordDistillOutcome upserts the distillation result for one session+hall.
func RecordDistillOutcome(ctx context.Context, db *sql.DB, q Q, platform Platform, sessionID, hall, status string, memoryKeys []string, errMsg string) error {
	keys := "[]"
	if raw, err := json.Marshal(memoryKeys); err == nil {
		keys = string(raw)
	}
	_, err := db.ExecContext(ctx, q(`
		INSERT INTO chat_import_distill (platform, session_id, hall, status, memory_keys, last_error, distilled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT(platform, session_id, hall) DO UPDATE SET
			status = $8, memory_keys = $9, last_error = $10, distilled_at = $11
	`), string(platform), sessionID, hall, status, keys, errMsg, nowRFC3339(),
		status, keys, errMsg, nowRFC3339())
	return err
}

// DistillStats summarizes the registry for observability.
type DistillStats struct {
	OK      int
	Failed  int
	Pending int
}

// DistillStatsFor returns per-status counts for one hall.
func DistillStatsFor(ctx context.Context, db *sql.DB, q Q, hall string) (DistillStats, error) {
	var s DistillStats
	rows, err := db.QueryContext(ctx, q(`SELECT status, COUNT(*) FROM chat_import_distill WHERE hall = $1 GROUP BY status`), hall)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return s, err
		}
		switch status {
		case "ok":
			s.OK = n
		case "failed":
			s.Failed = n
		default:
			s.Pending = n
		}
	}
	return s, rows.Err()
}
