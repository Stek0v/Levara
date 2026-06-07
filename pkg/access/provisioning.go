package access

import (
	"context"
	"database/sql"
	"errors"
)

// Phase 4B: enterprise provisioning (SCIM) boundary.
//
// A Provisioner is the seam an external directory drives to create, update, and
// deactivate Levara users and their tenant memberships — the write-side mirror
// of IdentityBridge's read-side mapping. Like the bridge it owns no policy
// decisions: it only mutates the users / user_tenant tables that SQLPolicy
// already consults, so deactivating a user here removes their access everywhere
// through the shared policy layer, with no handler-specific branch. This also
// covers the Phase 2B leftover — per-agent credentials can be modelled as
// provisioned principals without adding per-handler code.
//
// The default deployment wires NoopProvisioner: provisioning is rejected, and
// users are managed only through the local auth flows.

var (
	// ErrProvisioningDisabled is returned by the default NoopProvisioner: no
	// external directory is wired, so provisioning calls are rejected.
	ErrProvisioningDisabled = errors.New("access: external provisioning is not enabled")
	// ErrUserNotFound is returned when a provisioning call targets a user id that
	// does not exist locally (the SQL reference impl provisions existing users; it
	// does not mint principals).
	ErrUserNotFound = errors.New("access: user not found")
	// ErrProvisioningNoDB is returned when a SQLProvisioner is used without a DB.
	ErrProvisioningNoDB = errors.New("access: provisioner has no database handle")
)

// ProvisionedUser is the desired state an external directory pushes for a user.
// It is intentionally small: the activation/superuser flags policy code reads,
// plus the full set of tenant memberships to reconcile. Email is carried for
// audit/correlation but the reference impl does not rewrite it (the local email
// is unique and owned by the local auth flow).
type ProvisionedUser struct {
	UserID    string
	Email     string
	Active    bool
	Superuser bool
	// TenantIDs is the authoritative membership set. A nil slice means "leave
	// memberships untouched"; a non-nil (possibly empty) slice is reconciled
	// exactly — extra memberships are removed, missing ones added.
	TenantIDs []string
}

// Provisioner is the write-side enterprise seam (SCIM-shaped): users and group/
// tenant memberships flow in from an external directory.
type Provisioner interface {
	// ProvisionUser applies the desired activation/superuser state (and, when
	// TenantIDs is non-nil, the membership set) to an existing user. Returns
	// ErrUserNotFound when the user does not exist locally.
	ProvisionUser(ctx context.Context, u ProvisionedUser) error
	// DeactivateUser flips users.is_active to false so the shared policy layer
	// denies the principal everywhere. Returns ErrUserNotFound on an unknown id.
	DeactivateUser(ctx context.Context, userID string) error
	// SyncTenantMembership reconciles user_tenant to exactly tenantIDs (add the
	// missing, remove the extra) in a single transaction.
	SyncTenantMembership(ctx context.Context, userID string, tenantIDs []string) error
}

// NoopProvisioner is the default: every call is rejected with
// ErrProvisioningDisabled, so users are managed only via local auth.
type NoopProvisioner struct{}

// ProvisionUser always reports that provisioning is disabled.
func (NoopProvisioner) ProvisionUser(context.Context, ProvisionedUser) error {
	return ErrProvisioningDisabled
}

// DeactivateUser always reports that provisioning is disabled.
func (NoopProvisioner) DeactivateUser(context.Context, string) error {
	return ErrProvisioningDisabled
}

// SyncTenantMembership always reports that provisioning is disabled.
func (NoopProvisioner) SyncTenantMembership(context.Context, string, []string) error {
	return ErrProvisioningDisabled
}

// SQLProvisioner is the reference provisioner: it writes directly to the same
// users / user_tenant tables SQLPolicy reads, so its effects are visible to
// every access decision without any other code change. Q rewrites Postgres
// `$N` placeholders to positional `?` for sqlite (nil = no rewrite); each
// placeholder index is emitted once with its own argument — never reused —
// per the sqlite positional-rewrite contract.
type SQLProvisioner struct {
	DB *sql.DB
	Q  QueryRewriter
}

func (p SQLProvisioner) rewrite(q string) string {
	if p.Q == nil {
		return q
	}
	return p.Q(q)
}

// ProvisionUser updates the activation and superuser flags of an existing user
// and, when TenantIDs is non-nil, reconciles their tenant memberships.
func (p SQLProvisioner) ProvisionUser(ctx context.Context, u ProvisionedUser) error {
	if p.DB == nil {
		return ErrProvisioningNoDB
	}
	if u.UserID == "" {
		return ErrUserNotFound
	}
	res, err := p.DB.ExecContext(ctx,
		p.rewrite("UPDATE users SET is_active = $1, is_superuser = $2 WHERE id = $3"),
		u.Active, u.Superuser, u.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	if u.TenantIDs != nil {
		return p.SyncTenantMembership(ctx, u.UserID, u.TenantIDs)
	}
	return nil
}

// DeactivateUser sets users.is_active = false; the shared policy layer then
// denies the principal everywhere (search, memory, workspace).
func (p SQLProvisioner) DeactivateUser(ctx context.Context, userID string) error {
	if p.DB == nil {
		return ErrProvisioningNoDB
	}
	if userID == "" {
		return ErrUserNotFound
	}
	res, err := p.DB.ExecContext(ctx,
		p.rewrite("UPDATE users SET is_active = $1 WHERE id = $2"),
		false, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SyncTenantMembership reconciles user_tenant to exactly tenantIDs in one
// transaction: rows present in tenantIDs but not in the table are inserted,
// rows present in the table but not in tenantIDs are deleted. An empty (non-nil)
// tenantIDs clears all memberships.
func (p SQLProvisioner) SyncTenantMembership(ctx context.Context, userID string, tenantIDs []string) error {
	if p.DB == nil {
		return ErrProvisioningNoDB
	}
	if userID == "" {
		return ErrUserNotFound
	}
	desired := make(map[string]bool, len(tenantIDs))
	for _, t := range tenantIDs {
		if t != "" {
			desired[t] = true
		}
	}

	rows, err := p.DB.QueryContext(ctx,
		p.rewrite("SELECT tenant_id FROM user_tenant WHERE user_id = $1"), userID)
	if err != nil {
		return err
	}
	current := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		current[t] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for t := range desired {
		if current[t] {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			p.rewrite("INSERT INTO user_tenant(user_id, tenant_id) VALUES ($1, $2)"),
			userID, t); err != nil {
			tx.Rollback()
			return err
		}
	}
	for t := range current {
		if desired[t] {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			p.rewrite("DELETE FROM user_tenant WHERE user_id = $1 AND tenant_id = $2"),
			userID, t); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Compile-time guarantees that both provisioners satisfy the interface.
var (
	_ Provisioner = NoopProvisioner{}
	_ Provisioner = SQLProvisioner{}
)
