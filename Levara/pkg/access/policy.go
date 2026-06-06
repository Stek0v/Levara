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

type VisibleDataset struct {
	ID          string
	Name        string
	CreatedAt   string
	OwnerID     string
	RecordCount int
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

	// Deactivated accounts lose access here, in the shared policy layer: a user
	// deprovisioned by SCIM (users.is_active = false) is denied regardless of
	// ownership, share role, or superuser status. This runs only for
	// authenticated requests against a real DB — dev-mode and anonymous paths
	// already returned above.
	if active, err := p.IsActive(ctx, req.UserID); err != nil {
		return decision, err
	} else if !active {
		decision.Reason = "user_inactive"
		return decision, nil
	}

	q := p.rewrite
	if super, err := p.IsSuperuser(ctx, req.UserID); err != nil {
		return decision, err
	} else if super {
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

// IsSuperuser reports whether userID carries the global superuser flag. A nil
// DB, an empty user, or a missing user row are all "not a superuser" — only a
// real query failure returns an error. This is the single canonical superuser
// lookup; AuthorizeWorkspace, AllowedDatasetIDs, and HTTP handlers route
// through it instead of re-issuing the query.
func (p SQLPolicy) IsSuperuser(ctx context.Context, userID string) (bool, error) {
	if p.DB == nil || userID == "" {
		return false, nil
	}
	var isSuperuser bool
	err := p.DB.QueryRowContext(ctx, p.rewrite("SELECT COALESCE(is_superuser, false) FROM users WHERE id = $1"), userID).Scan(&isSuperuser)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return isSuperuser, nil
}

// IsActive reports whether userID's account is active. It is the canonical
// activation lookup the policy facades route through so a SCIM-deprovisioned
// user (users.is_active = false) is denied everywhere. A nil DB or empty user
// is "active" (dev-mode/anonymous never reach the gate), and a missing user row
// is fail-open active (COALESCE default true) — the deny path is an explicit
// is_active = false, not the mere absence of a row, matching IsSuperuser's
// ErrNoRows handling. Only a real query failure returns an error.
func (p SQLPolicy) IsActive(ctx context.Context, userID string) (bool, error) {
	if p.DB == nil || userID == "" {
		return true, nil
	}
	var active bool
	err := p.DB.QueryRowContext(ctx, p.rewrite("SELECT COALESCE(is_active, true) FROM users WHERE id = $1"), userID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return active, nil
}

// AllowedDatasetIDs returns dataset IDs the user owns or has been granted.
// Nil means no filtering, matching the existing Levara search contract for
// dev mode, anonymous requests, superusers, and SQL fallback errors.
func (p SQLPolicy) AllowedDatasetIDs(ctx context.Context, userID string) []string {
	if p.DB == nil || userID == "" {
		return nil
	}

	if super, _ := p.IsSuperuser(ctx, userID); super {
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

// VisibleDatasetIDs returns the concrete dataset IDs visible to userID,
// ordered by id. Unlike AllowedDatasetIDs, this method is not a search
// fail-open filter helper: SQL errors are returned to the caller. Anonymous,
// nil-DB, and superuser callers preserve existing workspace-context semantics
// by returning every dataset id.
func (p SQLPolicy) VisibleDatasetIDs(ctx context.Context, userID string) ([]string, error) {
	if p.DB == nil {
		return nil, nil
	}
	showAll := userID == ""
	if !showAll {
		super, err := p.IsSuperuser(ctx, userID)
		if err != nil {
			return nil, err
		}
		showAll = super
	}

	var (
		rows *sql.Rows
		err  error
	)
	if showAll {
		rows, err = p.DB.QueryContext(ctx, p.rewrite("SELECT id FROM datasets ORDER BY id"))
	} else {
		query, args := p.rewriteArgs(`SELECT DISTINCT d.id FROM datasets d
			LEFT JOIN dataset_shares s ON s.dataset_id = d.id AND s.user_id = $1
			WHERE d.owner_id = $1 OR d.owner_id = '' OR d.owner_id IS NULL OR s.id IS NOT NULL
			ORDER BY d.id`, userID)
		rows, err = p.DB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListVisibleDatasets returns dataset-list rows visible to userID, preserving
// the existing /datasets semantics: anonymous and superuser callers see every
// dataset; regular users see owned, public, and shared datasets. The returned
// shape is transport-neutral so HTTP can map it into its DTO without owning the
// visibility policy SQL.
func (p SQLPolicy) ListVisibleDatasets(ctx context.Context, userID string) ([]VisibleDataset, error) {
	if p.DB == nil {
		return nil, nil
	}
	showAll := userID == ""
	if !showAll {
		super, err := p.IsSuperuser(ctx, userID)
		if err != nil {
			return nil, err
		}
		showAll = super
	}

	var (
		rows *sql.Rows
		err  error
	)
	if showAll {
		rows, err = p.DB.QueryContext(ctx,
			p.rewrite(`SELECT d.id, d.name, d.created_at, COALESCE(d.owner_id,''), COUNT(dd.data_id)
			 FROM datasets d LEFT JOIN dataset_data dd ON dd.dataset_id = d.id
			 GROUP BY d.id ORDER BY d.created_at DESC`))
	} else {
		query, args := p.rewriteArgs(`SELECT DISTINCT d.id, d.name, d.created_at, COALESCE(d.owner_id,''), COUNT(dd.data_id)
			 FROM datasets d
			 LEFT JOIN dataset_shares s ON s.dataset_id = d.id AND s.user_id = $1
			 LEFT JOIN dataset_data dd ON dd.dataset_id = d.id
			 WHERE d.owner_id = $1 OR d.owner_id = '' OR d.owner_id IS NULL OR s.id IS NOT NULL
			 GROUP BY d.id ORDER BY d.created_at DESC`, userID)
		rows, err = p.DB.QueryContext(ctx, query, args...)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []VisibleDataset
	for rows.Next() {
		var d VisibleDataset
		if err := rows.Scan(&d.ID, &d.Name, &d.CreatedAt, &d.OwnerID, &d.RecordCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
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

// CanManageDatasetShares reports whether granterID may grant or revoke shares
// on datasetID: the dataset owner, or a user holding an admin share. Mirrors
// the prior inline HTTP checks exactly — a missing dataset row leaves owner
// empty (so a non-owner falls through to the share-role check), and only an
// exact admin share grants management rights. A nil DB yields false; the share
// handlers already special-case the no-DB path before calling.
func (p SQLPolicy) CanManageDatasetShares(ctx context.Context, datasetID, granterID string) bool {
	if p.DB == nil {
		return false
	}
	var ownerID string
	_ = p.DB.QueryRowContext(ctx, p.rewrite("SELECT owner_id FROM datasets WHERE id = $1"), datasetID).Scan(&ownerID)
	if ownerID == granterID {
		return true
	}
	var role string
	_ = p.DB.QueryRowContext(ctx, p.rewrite("SELECT role FROM dataset_shares WHERE dataset_id = $1 AND user_id = $2"), datasetID, granterID).Scan(&role)
	return role == RoleAdmin
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
