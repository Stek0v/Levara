package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DefaultTenantForUser resolves the tenant to auto-select for a request that did
// not carry an explicit X-Tenant-Id header: the user's tenant membership. A nil
// DB or empty userID yields ("", nil), preserving the dev-mode / single-user
// path where tenant isolation is not enforced; a user with no membership also
// yields ("", nil). Only a real query failure returns an error.
//
// The LIMIT 1 mirrors the prior HTTP behavior: a user belonging to several
// tenants gets one auto-selected rather than failing — callers that need strict
// single-tenant semantics check membership explicitly via IsTenantMember.
func (p SQLPolicy) DefaultTenantForUser(ctx context.Context, userID string) (string, error) {
	if p.DB == nil || userID == "" {
		return "", nil
	}
	var tenantID string
	err := p.DB.QueryRowContext(ctx,
		p.rewrite("SELECT tenant_id FROM user_tenant WHERE user_id = $1 LIMIT 1"),
		userID,
	).Scan(&tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return tenantID, nil
}

// CanManageTenant reports whether userID may change tenant membership. Tenant
// owners and global superusers are managers; missing users/tenants and empty
// identities are denied. Database failures are returned so transports fail
// closed instead of silently granting administrative access.
func (p SQLPolicy) CanManageTenant(ctx context.Context, userID, tenantID string) (bool, error) {
	if p.DB == nil || userID == "" || tenantID == "" {
		return false, nil
	}
	if superuser, err := p.IsSuperuser(ctx, userID); err != nil {
		return false, err
	} else if superuser {
		return true, nil
	}

	var ownerID string
	err := p.DB.QueryRowContext(ctx,
		p.rewrite("SELECT owner_id FROM tenants WHERE id = $1"),
		tenantID,
	).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ownerID == userID, nil
}

// TenantOwnerFilterSQL returns the SQL fragment that restricts a query to rows
// whose owner_id belongs to tenantID, plus its bind args. An empty tenantID
// yields ("", nil) — the no-isolation (dev / single-user) path. startIdx is the
// 1-based positional placeholder index for the tenant argument; values <= 0 are
// clamped to 1. sqlite selects "?" placeholders instead of "$N". The fragment is
// prefixed with " AND " so it appends directly onto an existing WHERE clause.
func TenantOwnerFilterSQL(tenantID string, startIdx int, sqlite bool) (string, []any) {
	if tenantID == "" {
		return "", nil
	}
	if startIdx <= 0 {
		startIdx = 1
	}
	placeholder := fmt.Sprintf("$%d", startIdx)
	if sqlite {
		placeholder = "?"
	}
	return " AND owner_id IN (SELECT user_id FROM user_tenant WHERE tenant_id = " + placeholder + ")", []any{tenantID}
}
