package access

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
)

// SCIM provisioning store (backlog A3, per ADR-003).
//
// Identity matching: externalId is primary (an IdP-owned immutable mapping
// stored in scim_identities); userName (email) collisions reject with
// ErrSCIMEmailConflict — no silent personality merges. Deletes are soft
// (users.is_active=false), never row removal, so audit and receipts keep
// referencing a stable principal.

var (
	ErrSCIMEmailConflict   = errors.New("scim: userName already in use by another identity")
	ErrSCIMExternalIDBound = errors.New("scim: externalId is immutable once set")
	ErrSCIMDisabled        = errors.New("scim: provisioning store not configured")
)

// SCIMStore persists SCIM-provisioned identities on top of the users /
// principals tables the local auth flow already owns. SQL matches the
// existing schema contract (Postgres $N rewritten for sqlite via Q).
type SCIMStore struct {
	DB       *sql.DB
	Q        QueryRewriter
	TenantID string // optional fixed managed-directory tenant, validated against scim_directories
}

func (s SCIMStore) rewrite(q string) string {
	if s.Q == nil {
		return q
	}
	return s.Q(q)
}

// scimUserID derives the stable Levara user id for an SCIM external identity:
// "scim-" + sha256(issuer \n externalId)[:32] — injective across directories.
func SCIMUserID(issuer, externalID string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(issuer)) + "\n" + strings.TrimSpace(externalID)))
	return "scim-" + hex.EncodeToString(sum[:])[:32]
}

// EnsureSchema creates the scim_identities mapping table if absent.
func (s SCIMStore) EnsureSchema(ctx context.Context) error {
	if s.DB == nil {
		return ErrSCIMDisabled
	}
	_, err := s.DB.ExecContext(ctx, s.rewrite(`CREATE TABLE IF NOT EXISTS scim_identities (
		issuer TEXT NOT NULL,
		external_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (issuer, external_id)
	)`))
	if err != nil {
		return err
	}
	if err := EnsureIdentitySchema(ctx, s.DB, s.Q); err != nil {
		return err
	}
	return s.ensureUserSchema(ctx)
}

// SCIMUser is the desired state a directory pushed for one identity.
type SCIMUser struct {
	Issuer          string // directory tenant identifier (e.g. the IdP entityID)
	ExternalID      string // IdP-owned immutable user identifier
	Email           string // userName claim
	Active          bool
	ActiveUnchanged bool // PATCH omission preserves the locked current value
	Enterprise      *SCIMEnterpriseUser
	EnterprisePatch map[string]*string
}

// ProvisionCreate implements the ADR-003 create semantics: match on
// (issuer, externalId) → idempotent update; userName (email) collision with
// a different identity → ErrSCIMEmailConflict; otherwise create a locked
// user (random password no one holds — logins happen via SSO/JWT only) and
// record the external mapping.
func (s SCIMStore) ProvisionCreate(ctx context.Context, u SCIMUser) (userID string, created bool, err error) {
	if s.DB == nil {
		return "", false, ErrSCIMDisabled
	}
	if strings.TrimSpace(u.Issuer) == "" || strings.TrimSpace(u.ExternalID) == "" || strings.TrimSpace(u.Email) == "" {
		return "", false, errors.New("scim: issuer, externalId and userName are required")
	}
	if s.TenantID != "" && (!scimExactID(u.Issuer) || !scimExactID(u.ExternalID)) {
		return "", false, ErrSCIMInvalid
	}
	uid := SCIMUserID(u.Issuer, u.ExternalID)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()
	if err := s.lockDirectory(ctx, tx, u.Issuer); err != nil {
		return "", false, err
	}
	// The first write serializes concurrent creates of this deterministic ID.
	// Using a write before reads also avoids SQLite read-to-write upgrades.
	if _, err := tx.ExecContext(ctx, s.rewrite(
		"INSERT INTO principals (id, type) VALUES ($1, 'user') ON CONFLICT DO NOTHING"), uid); err != nil {
		return "", false, err
	}
	var existing string
	err = tx.QueryRowContext(ctx, s.rewrite("SELECT user_id FROM scim_identities WHERE issuer = $1 AND external_id = $2"), u.Issuer, u.ExternalID).Scan(&existing)
	if err == nil {
		if err := s.updateActive(ctx, tx, existing, u.Active); err != nil {
			return "", false, err
		}
		if err := s.putEnterprise(ctx, tx, u); err != nil {
			return "", false, err
		}
		if err := s.auditMutation(ctx, tx, u.Issuer, "create_retry", "User", existing, 0, 0); err != nil {
			return "", false, err
		}
		return existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	// Never attach an existing user merely because a derived ID or email matches.
	var other string
	err = tx.QueryRowContext(ctx, s.rewrite("SELECT id FROM users WHERE id = $1"), uid).Scan(&other)
	if err == nil {
		return "", false, ErrSCIMExternalIDBound
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}

	// Locked password: 32 random bytes hashed — nobody can log in with it.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	lockedHash := hex.EncodeToString(buf)

	if result, err := tx.ExecContext(ctx, s.rewrite(
		`INSERT INTO users (id, email, hashed_password, is_active, is_superuser, is_verified)
		 VALUES ($1, $2, $3, $4, false, true) ON CONFLICT (email) DO NOTHING`), uid, u.Email, lockedHash, u.Active); err != nil {
		return "", false, err
	} else if n, err := result.RowsAffected(); err != nil {
		return "", false, err
	} else if n != 1 {
		return "", false, ErrSCIMEmailConflict
	}
	if _, err := tx.ExecContext(ctx, s.rewrite(
		"INSERT INTO scim_identities (issuer, external_id, user_id) VALUES ($1, $2, $3)"),
		u.Issuer, u.ExternalID, uid); err != nil {
		return "", false, err
	}
	if err := s.putEnterprise(ctx, tx, u); err != nil {
		return "", false, err
	}
	if err := s.auditMutation(ctx, tx, u.Issuer, "create", "User", uid, 0, 0); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return uid, true, nil
}

// ProvisionUpdate atomically patches activation, email and enterprise metadata.
// Email renames are honored when the target address is free. externalId
// immutability is enforced by the caller (HTTP
// layer), which rejects any attempt to change the mapping key.
func (s SCIMStore) ProvisionUpdate(ctx context.Context, u SCIMUser, newEmail string) error {
	if s.DB == nil {
		return ErrSCIMDisabled
	}
	uid, err := s.lookup(ctx, u.Issuer, u.ExternalID)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.lockDirectory(ctx, tx, u.Issuer); err != nil {
		return err
	}
	// Acquire the user write lock even when active was omitted, so concurrent
	// metadata/email patches cannot overwrite an intervening deactivation.
	if _, err := tx.ExecContext(ctx, s.rewrite("UPDATE users SET id=id WHERE id=$1"), uid); err != nil {
		return err
	}
	if !u.ActiveUnchanged {
		if err := s.updateActive(ctx, tx, uid, u.Active); err != nil {
			return err
		}
	}
	if newEmail != "" {
		var other string
		err = tx.QueryRowContext(ctx,
			s.rewrite("SELECT id FROM users WHERE email = $1"), newEmail).Scan(&other)
		if err == nil && other != uid {
			return ErrSCIMEmailConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			s.rewrite("UPDATE users SET email = $1 WHERE id = $2"), newEmail, uid); err != nil {
			return err
		}
	}
	if err := s.putEnterprise(ctx, tx, u); err != nil {
		return err
	}
	if err := s.auditMutation(ctx, tx, u.Issuer, "update", "User", uid, 0, 0); err != nil {
		return err
	}
	return tx.Commit()
}

// ProvisionDeactivate implements soft delete: is_active=false so the shared
// policy layer denies the principal everywhere. The mapping row stays, so a
// re-provision of the same externalId reactivates cleanly.
func (s SCIMStore) ProvisionDeactivate(ctx context.Context, issuer, externalID string) error {
	if s.DB == nil {
		return ErrSCIMDisabled
	}
	uid, err := s.lookup(ctx, issuer, externalID)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockDirectory(ctx, tx, issuer); err != nil {
		return err
	}
	if err := s.updateActive(ctx, tx, uid, false); err != nil {
		return err
	}
	if err := s.auditMutation(ctx, tx, issuer, "deactivate", "User", uid, 0, 0); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SCIMStore) updateActive(ctx context.Context, tx *sql.Tx, uid string, active bool) error {
	result, err := tx.ExecContext(ctx, s.rewrite("UPDATE users SET is_active = $1, is_verified = true WHERE id = $2"), active, uid)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrUserNotFound
	}
	if !active {
		return revokeUserCredentials(ctx, tx, s.Q, uid)
	}
	return nil
}

// Lookup maps (issuer, externalId) to the local user id, or ErrUserNotFound.
func (s SCIMStore) Lookup(ctx context.Context, issuer, externalID string) (string, error) {
	if s.DB == nil {
		return "", ErrSCIMDisabled
	}
	return s.lookup(ctx, issuer, externalID)
}

func (s SCIMStore) lookup(ctx context.Context, issuer, externalID string) (string, error) {
	var uid string
	err := s.DB.QueryRowContext(ctx,
		s.rewrite("SELECT user_id FROM scim_identities WHERE issuer = $1 AND external_id = $2"),
		issuer, externalID).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUserNotFound
	}
	return uid, err
}

// TokenCheck is constant-time comparison for the static SCIM bearer token.
func TokenCheck(presented, configured string) bool {
	return len(configured) > 0 &&
		subtle.ConstantTimeCompare([]byte(presented), []byte(configured)) == 1
}
