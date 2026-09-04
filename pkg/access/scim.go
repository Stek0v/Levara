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
	DB *sql.DB
	Q  QueryRewriter
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
	return err
}

// SCIMUser is the desired state a directory pushed for one identity.
type SCIMUser struct {
	Issuer     string // directory tenant identifier (e.g. the IdP entityID)
	ExternalID string // IdP-owned immutable user identifier
	Email      string // userName claim
	Active     bool
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
	uid := SCIMUserID(u.Issuer, u.ExternalID)

	// Existing mapping for this identity?
	var existing string
	err = s.DB.QueryRowContext(ctx,
		s.rewrite("SELECT user_id FROM scim_identities WHERE issuer = $1 AND external_id = $2"),
		u.Issuer, u.ExternalID).Scan(&existing)
	switch {
	case err == nil:
		// Idempotent re-provision: refresh active state, never touch email
		// (directory renames come through PATCH, handled in ProvisionUpdate).
		if _, err := s.DB.ExecContext(ctx,
			s.rewrite("UPDATE users SET is_active = $1, is_verified = true WHERE id = $2"),
			u.Active, existing); err != nil {
			return "", false, err
		}
		return existing, false, nil
	case errors.Is(err, sql.ErrNoRows):
		// fall through to create
	default:
		return "", false, err
	}

	// Email collision guard: another identity already owns the address.
	var other string
	err = s.DB.QueryRowContext(ctx,
		s.rewrite("SELECT id FROM users WHERE email = $1"), u.Email).Scan(&other)
	switch {
	case err == nil && other != uid:
		return "", false, ErrSCIMEmailConflict
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return "", false, err
	}

	// Locked password: 32 random bytes hashed — nobody can log in with it.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	lockedHash := hex.EncodeToString(buf)

	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.rewrite(
		"INSERT INTO principals (id, type) VALUES ($1, 'user') ON CONFLICT DO NOTHING"), uid); err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, s.rewrite(
		`INSERT INTO users (id, email, hashed_password, is_active, is_superuser, is_verified)
		 VALUES ($1, $2, $3, $4, false, true)`), uid, u.Email, lockedHash, u.Active); err != nil {
		return "", false, err
	}
	if _, err := tx.ExecContext(ctx, s.rewrite(
		"INSERT INTO scim_identities (issuer, external_id, user_id) VALUES ($1, $2, $3)"),
		u.Issuer, u.ExternalID, uid); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return uid, true, nil
}

// ProvisionUpdate applies a desired-state patch: activation flips are the
// only directory-driven mutation; email renames are honored when the target
// address is free. externalId immutability is enforced by the caller (HTTP
// layer), which rejects any attempt to change the mapping key.
func (s SCIMStore) ProvisionUpdate(ctx context.Context, u SCIMUser, newEmail string) error {
	if s.DB == nil {
		return ErrSCIMDisabled
	}
	uid, err := s.lookup(ctx, u.Issuer, u.ExternalID)
	if err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx,
		s.rewrite("UPDATE users SET is_active = $1, is_verified = true WHERE id = $2"),
		u.Active, uid); err != nil {
		return err
	}
	if newEmail != "" && newEmail != u.Email {
		var other string
		err = s.DB.QueryRowContext(ctx,
			s.rewrite("SELECT id FROM users WHERE email = $1"), newEmail).Scan(&other)
		if err == nil && other != uid {
			return ErrSCIMEmailConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := s.DB.ExecContext(ctx,
			s.rewrite("UPDATE users SET email = $1 WHERE id = $2"), newEmail, uid); err != nil {
			return err
		}
	}
	return nil
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
	_, err = s.DB.ExecContext(ctx,
		s.rewrite("UPDATE users SET is_active = false WHERE id = $1"), uid)
	return err
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
