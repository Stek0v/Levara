package access

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrInactiveIdentity = errors.New("access: user is missing or inactive")
var ErrRevokedCredential = errors.New("access: credential revoked")

// EnsureIdentitySchema adds credential versions without altering the users
// schema. A missing version row means epoch zero for existing credentials.
// Call this during startup, before accepting authenticated traffic.
func EnsureIdentitySchema(ctx context.Context, db *sql.DB, q QueryRewriter) error {
	if db == nil {
		return ErrProvisioningNoDB
	}
	query := `CREATE TABLE IF NOT EXISTS credential_epochs (
		user_id TEXT PRIMARY KEY,
		epoch BIGINT NOT NULL DEFAULT 0 CHECK (epoch >= 0),
 revoked_before BIGINT NOT NULL DEFAULT 0
	)`
	if q != nil {
		query = q(query)
	}
	_, err := db.ExecContext(ctx, query)
	return err
}

// CurrentCredentialEpoch only resolves existing active users. Missing schema,
// database failures and anonymous identities are errors, never dev-mode grants.
func CurrentCredentialEpoch(ctx context.Context, db *sql.DB, q QueryRewriter, userID string) (int64, error) {
	epoch, _, err := currentCredentialState(ctx, db, q, userID)
	return epoch, err
}

func currentCredentialState(ctx context.Context, db *sql.DB, q QueryRewriter, userID string) (int64, int64, error) {
	if db == nil {
		return 0, 0, ErrProvisioningNoDB
	}
	if userID == "" {
		return 0, 0, ErrInactiveIdentity
	}
	query := `SELECT COALESCE(e.epoch, 0), COALESCE(e.revoked_before, 0) FROM users u
 LEFT JOIN credential_epochs e ON e.user_id = u.id
 WHERE u.id = $1 AND u.is_active = true`
	if q != nil {
		query = q(query)
	}
	var epoch, revokedBefore int64
	err := db.QueryRowContext(ctx, query, userID).Scan(&epoch, &revokedBefore)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrInactiveIdentity
	}
	return epoch, revokedBefore, err
}

// ValidateExternalCredential rejects external bearer tokens issued at or before
// the latest deactivation. An absent iat is acceptable only before any revoke.
// The second containing the revoke is denied conservatively: providers must
// issue a new token in a later second after reactivation.
func ValidateExternalCredential(ctx context.Context, db *sql.DB, q QueryRewriter, userID string, issuedAt int64) error {
	_, err := ExternalCredentialEpoch(ctx, db, q, userID, issuedAt)
	return err
}

// ExternalCredentialEpoch validates the external issuance watermark and captures
// the epoch from the same SQL snapshot. Session issuance must preserve this
// epoch, so a concurrent deactivate/reactivate cannot upgrade an old assertion.
func ExternalCredentialEpoch(ctx context.Context, db *sql.DB, q QueryRewriter, userID string, issuedAt int64) (int64, error) {
	epoch, revokedBefore, err := currentCredentialState(ctx, db, q, userID)
	if err != nil {
		return 0, err
	}
	if revokedBefore > 0 && issuedAt <= revokedBefore {
		return 0, ErrRevokedCredential
	}
	return epoch, nil
}

func ValidateCredential(ctx context.Context, db *sql.DB, q QueryRewriter, userID string, epoch int64) error {
	current, err := CurrentCredentialEpoch(ctx, db, q, userID)
	if err != nil {
		return err
	}
	if epoch < 0 || current != epoch {
		return ErrRevokedCredential
	}
	return nil
}

// revokeUserCredentials runs after locking/updating the users row. Key issuance
// takes the same lock, so no key can escape a concurrent deactivation. This and
// the active-state mutation must commit or roll back together.
func revokeUserCredentials(ctx context.Context, tx *sql.Tx, q QueryRewriter, userID string) error {
	rewrite := func(query string) string {
		if q != nil {
			return q(query)
		}
		return query
	}
	if _, err := tx.ExecContext(ctx, rewrite(`INSERT INTO credential_epochs (user_id, epoch, revoked_before) VALUES ($1, 1, $2)
		ON CONFLICT (user_id) DO UPDATE SET epoch = credential_epochs.epoch + 1,
 revoked_before = CASE WHEN credential_epochs.revoked_before > excluded.revoked_before
 THEN credential_epochs.revoked_before ELSE excluded.revoked_before END`), userID, time.Now().Unix()); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, rewrite(`UPDATE api_keys SET revoked = true WHERE user_id = $1`), userID)
	return err
}
