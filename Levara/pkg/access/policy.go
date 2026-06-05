// Package access contains transport-independent authorization decisions.
package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const (
	ActionRead  = "read"
	ActionWrite = "write"

	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

type QueryRewriter func(string) string

type SQLPolicy struct {
	DB *sql.DB
	Q  QueryRewriter
	QA QueryArgsRewriter
}

type QueryArgsRewriter func(string, ...any) (string, []any)

type WorkspaceRequest struct {
	UserID            string
	ProjectID         string
	Action            string
	APIKeyPermissions string
}

type Decision struct {
	Allowed       bool
	Role          string
	Reason        string
	DevMode       bool
	Authenticated bool
	APIKeyAllowed bool
}

func (p SQLPolicy) AuthorizeWorkspace(ctx context.Context, req WorkspaceRequest) (Decision, error) {
	action := normalizeAction(req.Action)
	decision := Decision{
		Authenticated: req.UserID != "",
		APIKeyAllowed: APIKeyAllows(req.APIKeyPermissions, action),
	}
	if req.ProjectID == "" {
		decision.Allowed = true
		decision.Reason = "project_id_not_required"
		return decision, nil
	}
	if !decision.APIKeyAllowed {
		decision.Reason = "api_key_permissions_denied"
		return decision, nil
	}
	if p.DB == nil || req.UserID == "" {
		decision.Allowed = true
		decision.Role = RoleAdmin
		decision.Reason = "dev_mode"
		decision.DevMode = true
		return decision, nil
	}

	q := p.rewrite
	var isSuperuser bool
	if err := p.DB.QueryRowContext(ctx, q("SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1"), req.UserID).Scan(&isSuperuser); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return decision, err
	}
	if isSuperuser {
		decision.Allowed = true
		decision.Role = RoleAdmin
		decision.Reason = "superuser"
		return decision, nil
	}

	var ownerID string
	err := p.DB.QueryRowContext(ctx, q("SELECT COALESCE(owner_id, '') FROM datasets WHERE id = $1"), req.ProjectID).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		decision.Reason = "denied"
		return decision, nil
	}
	if err != nil {
		return decision, err
	}
	if ownerID == "" || ownerID == req.UserID {
		decision.Allowed = true
		decision.Role = RoleAdmin
		decision.Reason = "owner"
		return decision, nil
	}

	var role string
	err = p.DB.QueryRowContext(ctx, q("SELECT role FROM dataset_shares WHERE dataset_id = $1 AND user_id = $2"), req.ProjectID, req.UserID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		decision.Reason = "denied"
		return decision, nil
	}
	if err != nil {
		return decision, err
	}
	decision.Role = strings.ToLower(role)
	decision.Allowed = RoleAllows(role, action)
	if decision.Allowed {
		decision.Reason = "shared_" + decision.Role
	} else {
		decision.Reason = "role_insufficient"
	}
	return decision, nil
}

func (p SQLPolicy) rewrite(query string) string {
	if p.Q == nil {
		return query
	}
	return p.Q(query)
}

func (p SQLPolicy) rewriteArgs(query string, args ...any) (string, []any) {
	if p.QA != nil {
		return p.QA(query, args...)
	}
	return p.rewrite(query), args
}

// AllowedDatasetIDs returns dataset IDs the user owns or has been granted.
// Nil means no filtering, matching the existing Levara search contract for
// dev mode, anonymous requests, superusers, and SQL fallback errors.
func (p SQLPolicy) AllowedDatasetIDs(ctx context.Context, userID string) []string {
	if p.DB == nil || userID == "" {
		return nil
	}

	var isSuperuser bool
	_ = p.DB.QueryRowContext(ctx, p.rewrite("SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1"), userID).Scan(&isSuperuser)
	if isSuperuser {
		return nil
	}

	query, args := p.rewriteArgs(`SELECT DISTINCT d.id FROM datasets d
		 LEFT JOIN dataset_shares s ON s.dataset_id = d.id AND s.user_id = $1
		 WHERE d.owner_id = $1 OR d.owner_id = '' OR d.owner_id IS NULL OR s.id IS NOT NULL`, userID)
	rows, err := p.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// CanAccessDataset preserves the existing Levara dataset access semantics:
// no DB or no user means dev-mode allow; an empty owner is public; any share
// row grants access regardless of role.
func (p SQLPolicy) CanAccessDataset(ctx context.Context, datasetID, userID string) bool {
	if p.DB == nil || userID == "" {
		return true
	}

	var ownerID string
	_ = p.DB.QueryRowContext(ctx, p.rewrite("SELECT owner_id FROM datasets WHERE id = $1"), datasetID).Scan(&ownerID)
	if ownerID == "" || ownerID == userID {
		return true
	}

	var shareID string
	_ = p.DB.QueryRowContext(ctx, p.rewrite("SELECT id FROM dataset_shares WHERE dataset_id = $1 AND user_id = $2"), datasetID, userID).Scan(&shareID)
	return shareID != ""
}

func APIKeyAllows(perms, action string) bool {
	if perms == "" {
		return true
	}
	perms = strings.ToLower(perms)
	if normalizeAction(action) == ActionRead {
		return strings.Contains(perms, "read") || strings.Contains(perms, "write") || strings.Contains(perms, "admin")
	}
	return strings.Contains(perms, "write") || strings.Contains(perms, "admin")
}

func RoleAllows(role, action string) bool {
	switch strings.ToLower(role) {
	case RoleAdmin:
		return true
	case RoleEditor:
		return normalizeAction(action) == ActionRead || normalizeAction(action) == ActionWrite
	case RoleViewer:
		return normalizeAction(action) == ActionRead
	default:
		return false
	}
}

func normalizeAction(action string) string {
	if strings.ToLower(action) == ActionWrite {
		return ActionWrite
	}
	return ActionRead
}
