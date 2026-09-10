package access

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/google/uuid"
)

var (
	ErrGroupNotFound        = errors.New("access group not found")
	ErrGroupForbidden       = errors.New("access group access denied")
	ErrGroupInvalid         = errors.New("invalid access group request")
	ErrGroupVersionConflict = errors.New("access group version conflict")
)

// AccessGroup is a local organizational group, not an unverified IdP claim.
type AccessGroup struct {
	ID, TenantID, Name string
	Revision           int64
	Members            []string
}

func (p SQLPolicy) groupManager(ctx context.Context, q documentQuerier, actor Actor, tenantID string) error {
	if !APIKeyAllows(actor.APIKeyPermissions, ActionShare) {
		return ErrGroupForbidden
	}
	active, admin, err := p.documentIdentity(ctx, q, actor.UserID)
	if err != nil {
		return err
	}
	if !active {
		return ErrGroupForbidden
	}
	var owner string
	err = q.QueryRowContext(ctx, p.rewrite("SELECT owner_id FROM tenants WHERE id=$1"), tenantID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrGroupNotFound
	}
	if err != nil {
		return err
	}
	if admin {
		return nil
	}
	member, err := p.documentMember(ctx, q, actor.UserID, tenantID)
	if err != nil {
		return err
	}
	if actor.TenantID != tenantID || !member || owner != actor.UserID {
		return ErrGroupForbidden
	}
	return nil
}

func (p SQLPolicy) accessGroup(ctx context.Context, q documentQuerier, groupID string) (AccessGroup, error) {
	g := AccessGroup{ID: groupID, Members: []string{}}
	err := q.QueryRowContext(ctx, p.rewrite("SELECT tenant_id,name,revision FROM access_groups WHERE id=$1"), groupID).Scan(&g.TenantID, &g.Name, &g.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrGroupNotFound
	}
	return g, err
}

// GetGroup exposes membership only to the tenant manager/active instance admin.
// Authorization uses live membership; this result is not an authorization cache.
func (p SQLPolicy) GetGroup(ctx context.Context, actor Actor, groupID string) (AccessGroup, error) {
	if p.DB == nil {
		return AccessGroup{}, ErrGroupNotFound
	}
	q := p.reader()
	g, err := p.accessGroup(ctx, q, groupID)
	if err != nil {
		return AccessGroup{}, err
	}
	// Reading group administration metadata needs read permission, not write.
	if !APIKeyAllows(actor.APIKeyPermissions, ActionRead) {
		return AccessGroup{}, ErrGroupForbidden
	}
	manager := actor
	manager.APIKeyPermissions = ""
	if err := p.groupManager(ctx, q, manager, g.TenantID); err != nil {
		return AccessGroup{}, err
	}
	return p.groupMembers(ctx, q, g)
}

func (p SQLPolicy) groupMembers(ctx context.Context, q documentQuerier, g AccessGroup) (AccessGroup, error) {
	rows, err := q.QueryContext(ctx, p.rewrite("SELECT user_id FROM access_group_members WHERE group_id=$1 ORDER BY user_id"), g.ID)
	if err != nil {
		return AccessGroup{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return AccessGroup{}, err
		}
		g.Members = append(g.Members, id)
	}
	if err := rows.Err(); err != nil {
		return AccessGroup{}, err
	}
	return g, nil
}

// CreateGroup is idempotent by exact (tenant,name). Names are display labels;
// grants bind the generated stable ID and never derive privileges from a name.
func (p SQLPolicy) CreateGroup(ctx context.Context, actor Actor, tenantID, name string) (AccessGroup, error) {
	name = strings.TrimSpace(name)
	if p.DB == nil || tenantID == "" || name == "" {
		return AccessGroup{}, ErrGroupInvalid
	}
	tx, owned, err := p.documentMutationTransaction(ctx)
	if err != nil {
		return AccessGroup{}, err
	}
	if owned {
		defer tx.Rollback()
	}
	if err := p.groupManager(ctx, tx, actor, tenantID); err != nil {
		return AccessGroup{}, err
	}
	id := uuid.NewString()
	if _, err := tx.ExecContext(ctx, p.rewrite("INSERT INTO principals(id,type) VALUES($1,'group')"), id); err != nil {
		return AccessGroup{}, err
	}
	var groupID string
	err = tx.QueryRowContext(ctx, p.rewrite(`INSERT INTO access_groups(id,tenant_id,name) VALUES($1,$2,$3)
		ON CONFLICT(tenant_id,name) DO UPDATE SET name=EXCLUDED.name RETURNING id`), id, tenantID, name).Scan(&groupID)
	if err != nil {
		return AccessGroup{}, err
	}
	if groupID != id {
		if _, err := tx.ExecContext(ctx, p.rewrite("DELETE FROM principals WHERE id=$1"), id); err != nil {
			return AccessGroup{}, err
		}
	}
	g, err := p.accessGroup(ctx, tx, groupID)
	if err != nil {
		return AccessGroup{}, err
	}
	if err := p.localGroupMutable(ctx, tx, groupID); err != nil {
		return AccessGroup{}, err
	}
	g, err = p.groupMembers(ctx, tx, g)
	if err != nil {
		return AccessGroup{}, err
	}
	if err := finishDocumentMutation(tx, owned); err != nil {
		return AccessGroup{}, err
	}
	return g, nil
}

// ReplaceGroupMembers atomically replaces the direct membership set. It does
// not implement nested groups or SCIM synchronization. Empty means remove all;
// every submitted user must exist, be active, and belong to this tenant now.
func (p SQLPolicy) ReplaceGroupMembers(ctx context.Context, actor Actor, groupID string, expectedRevision int64, userIDs []string) (AccessGroup, error) {
	if p.DB == nil || groupID == "" || expectedRevision <= 0 {
		return AccessGroup{}, ErrGroupInvalid
	}
	members := make(map[string]bool)
	for _, id := range userIDs {
		if strings.TrimSpace(id) == "" {
			return AccessGroup{}, ErrGroupInvalid
		}
		members[id] = true
	}
	tx, owned, err := p.documentMutationTransaction(ctx)
	if err != nil {
		return AccessGroup{}, err
	}
	if owned {
		defer tx.Rollback()
	}
	g, err := p.accessGroup(ctx, tx, groupID)
	if err != nil {
		return AccessGroup{}, err
	}
	if err := p.groupManager(ctx, tx, actor, g.TenantID); err != nil {
		return AccessGroup{}, err
	}
	result, err := tx.ExecContext(ctx, p.rewrite("UPDATE access_groups SET revision=revision+1 WHERE id=$1 AND revision=$2"), groupID, expectedRevision)
	if err != nil {
		return AccessGroup{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return AccessGroup{}, err
	}
	if n != 1 {
		return AccessGroup{}, ErrGroupVersionConflict
	}
	if err := p.localGroupMutable(ctx, tx, groupID); err != nil {
		return AccessGroup{}, err
	}
	for id := range members {
		if err := p.validateDocumentPrincipal(ctx, tx, DocumentPrincipal{Kind: DocumentUser, ID: id}, g.TenantID); err != nil {
			return AccessGroup{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, p.rewrite("DELETE FROM access_group_members WHERE group_id=$1"), groupID); err != nil {
		return AccessGroup{}, err
	}
	g.Members = []string{}
	for id := range members {
		g.Members = append(g.Members, id)
	}
	sort.Strings(g.Members)
	for _, id := range g.Members {
		if _, err := tx.ExecContext(ctx, p.rewrite("INSERT INTO access_group_members(group_id,tenant_id,user_id) VALUES($1,$2,$3)"), groupID, g.TenantID, id); err != nil {
			return AccessGroup{}, err
		}
	}
	g.Revision = expectedRevision + 1
	if err := finishDocumentMutation(tx, owned); err != nil {
		return AccessGroup{}, err
	}
	return g, nil
}

// Directory membership has one authoritative writer. Schema absence/errors
// are failures, not permission to overwrite a managed group locally.
func (p SQLPolicy) localGroupMutable(ctx context.Context, q documentQuerier, groupID string) error {
	var count int
	if err := q.QueryRowContext(ctx, p.rewrite("SELECT COUNT(*) FROM scim_groups WHERE group_id=$1"), groupID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrGroupForbidden
	}
	return nil
}
