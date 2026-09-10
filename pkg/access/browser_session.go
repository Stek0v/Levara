package access

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"time"
)

// CreateBrowserSession records one independently revocable browser credential.
func CreateBrowserSession(ctx context.Context, db *sql.DB, q QueryRewriter, userID string, expires int64) (string, error) {
	if db == nil {
		return "", ErrProvisioningNoDB
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(random)
	query := `INSERT INTO auth_sessions(id,user_id,expires_at) VALUES($1,$2,$3)`
	if q != nil {
		query = q(query)
	}
	_, err := db.ExecContext(ctx, query, id, userID, expires)
	return id, err
}

func RevokeBrowserSession(ctx context.Context, db *sql.DB, q QueryRewriter, userID, sessionID string) error {
	if db == nil {
		return ErrProvisioningNoDB
	}
	query := `UPDATE auth_sessions SET revoked=true WHERE id=$1 AND user_id=$2`
	if q != nil {
		query = q(query)
	}
	_, err := db.ExecContext(ctx, query, sessionID, userID)
	return err
}

// EnsureBrowserSessionSchema is idempotent on SQLite and PostgreSQL. Session
// IDs are random bearer claims; no provider token or browser secret is stored.
func EnsureBrowserSessionSchema(ctx context.Context, db *sql.DB, q QueryRewriter) error {
	if db == nil {
		return ErrProvisioningNoDB
	}
	query := `CREATE TABLE IF NOT EXISTS auth_sessions (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, expires_at BIGINT NOT NULL,
		revoked BOOLEAN NOT NULL DEFAULT false
	)`
	if q != nil {
		query = q(query)
	}
	_, err := db.ExecContext(ctx, query)
	return err
}

// ValidateBrowserSession leaves legacy and programmatic JWTs without a session
// claim unchanged. Callers must also validate the live user and credential epoch.
func ValidateBrowserSession(ctx context.Context, db *sql.DB, q QueryRewriter, userID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	if db == nil {
		return ErrProvisioningNoDB
	}
	query := `SELECT id FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked=false AND expires_at>$3`
	if q != nil {
		query = q(query)
	}
	var id string
	if err := db.QueryRowContext(ctx, query, sessionID, userID, time.Now().Unix()).Scan(&id); err != nil {
		return ErrRevokedCredential
	}
	return nil
}
