package access

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const (
	DocumentInherit    = "inherit"
	DocumentRestricted = "restricted"
	DocumentUser       = "user"
	DocumentGroup      = "group"
)

var (
	ErrDocumentNotFound        = errors.New("document resource not found")
	ErrDocumentForbidden       = errors.New("document access denied")
	ErrDocumentInvalid         = errors.New("invalid document policy request")
	ErrDocumentVersionConflict = errors.New("document policy version conflict")
)

// DocumentRef identifies one inclusion of a blob, not every use of data.id.
type DocumentRef struct{ DatasetID, DataID string }

type DocumentResource struct {
	DocumentRef
	TenantID, Mode               string
	ACLRevision, ContentRevision int64
	Tombstoned, Hold             bool
}

type DocumentPrincipal struct{ Kind, ID string }

type documentQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (p SQLPolicy) GetDocumentResource(ctx context.Context, ref DocumentRef) (DocumentResource, error) {
	if p.DB == nil {
		return DocumentResource{}, ErrDocumentNotFound
	}
	return p.documentResource(ctx, p.reader(), ref)
}

// GraphDatasetIDs returns dataset candidates for a document-aware graph read.
// The caller must still authorize every graph assertion against its exact
// document publication; this list only keeps the SQL scan bounded.
func (p SQLPolicy) GraphDatasetIDs(ctx context.Context, actor Actor) ([]string, error) {
	if p.DB == nil || actor.UserID == "" {
		return nil, nil
	}
	admin, err := p.IsSuperuser(ctx, actor.UserID)
	if err != nil || admin {
		return nil, err
	}
	ids, err := p.VisibleDatasetIDs(ctx, actor.UserID)
	if err != nil {
		return nil, err
	}
	rows, err := p.reader().QueryContext(ctx, p.rewrite(`SELECT DISTINCT dr.dataset_id
		FROM document_resources dr JOIN user_tenant ut ON ut.tenant_id=dr.tenant_id
		WHERE ut.user_id=$1 ORDER BY dr.dataset_id`), actor.UserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, rows.Err()
}

func (p SQLPolicy) documentResource(ctx context.Context, q documentQuerier, ref DocumentRef) (DocumentResource, error) {
	r := DocumentResource{DocumentRef: ref}
	err := q.QueryRowContext(ctx, p.rewrite(`SELECT tenant_id, mode, acl_revision, content_revision, tombstoned, hold
		FROM document_resources WHERE dataset_id=$1 AND data_id=$2`), ref.DatasetID, ref.DataID).
		Scan(&r.TenantID, &r.Mode, &r.ACLRevision, &r.ContentRevision, &r.Tombstoned, &r.Hold)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDocumentNotFound
	}
	return r, err
}

// documentIdentity deliberately requires an existing active user. It never
// accepts Actor.Superuser or the legacy missing-user activation fallback.
func (p SQLPolicy) documentIdentity(ctx context.Context, q documentQuerier, userID string) (bool, bool, error) {
	if userID == "" {
		return false, false, nil
	}
	var active, admin bool
	err := q.QueryRowContext(ctx, p.rewrite("SELECT is_active, is_superuser FROM users WHERE id=$1"), userID).Scan(&active, &admin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return active, admin, err
}

func (p SQLPolicy) documentMember(ctx context.Context, q documentQuerier, userID, tenantID string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, p.rewrite("SELECT COUNT(*) FROM user_tenant WHERE user_id=$1 AND tenant_id=$2"), userID, tenantID).Scan(&count)
	return count > 0, err
}

func (p SQLPolicy) documentOwner(ctx context.Context, q documentQuerier, ref DocumentRef) (string, error) {
	var owner string
	err := q.QueryRowContext(ctx, p.rewrite(`SELECT COALESCE(d.owner_id,'') FROM datasets d
		JOIN dataset_data dd ON dd.dataset_id=d.id WHERE dd.dataset_id=$1 AND dd.data_id=$2`), ref.DatasetID, ref.DataID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrDocumentNotFound
	}
	return owner, err
}

// AuthorizeDocument preserves dataset inheritance only for unregistered, live
// associations. Registered resources, including tombstones and missing links,
// never fall back. This checks current SQL state; it is not an egress lease.
func (p SQLPolicy) AuthorizeDocument(ctx context.Context, actor Actor, ref DocumentRef, action string) (Decision, error) {
	if ref.DatasetID == "" || ref.DataID == "" || !ValidPermissionType(action) {
		return Decision{}, ErrDocumentInvalid
	}
	if p.DB == nil {
		return p.AuthorizeDataset(ctx, actor, ref.DatasetID, action)
	}
	r, err := p.documentResource(ctx, p.reader(), ref)
	if errors.Is(err, ErrDocumentNotFound) {
		if _, err := p.documentOwner(ctx, p.reader(), ref); errors.Is(err, ErrDocumentNotFound) {
			return Decision{Reason: "document_not_found"}, nil
		} else if err != nil {
			return Decision{}, err
		}
		return p.AuthorizeDataset(ctx, actor, ref.DatasetID, action)
	}
	if err != nil {
		return Decision{}, err
	}
	return p.authorizeRegisteredDocument(ctx, p.reader(), actor, r, action)
}

func (p SQLPolicy) authorizeRegisteredDocument(ctx context.Context, q documentQuerier, actor Actor, r DocumentResource, action string) (Decision, error) {
	d := Decision{Authenticated: actor.UserID != "", APIKeyAllowed: APIKeyAllows(actor.APIKeyPermissions, action)}
	if !d.APIKeyAllowed {
		d.Reason = "api_key_permissions_denied"
		return d, nil
	}
	active, admin, err := p.documentIdentity(ctx, q, actor.UserID)
	if err != nil {
		return d, err
	}
	if !active {
		d.Reason = "user_inactive_or_missing"
		return d, nil
	}
	if r.Tombstoned {
		d.Reason = "document_tombstoned"
		return d, nil
	}
	owner, err := p.documentOwner(ctx, q, r.DocumentRef)
	if errors.Is(err, ErrDocumentNotFound) {
		d.Reason = "document_not_found"
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if r.Hold && (action == ActionWrite || action == ActionDelete) {
		d.Reason = "document_hold"
		return d, nil
	}
	if admin {
		d.Allowed = true
		d.Role = RoleAdmin
		d.Reason = "superuser"
		return d, nil
	}
	if actor.TenantID != r.TenantID {
		d.Reason = "tenant_mismatch"
		return d, nil
	}
	member, err := p.documentMember(ctx, q, actor.UserID, r.TenantID)
	if err != nil {
		return d, err
	}
	if !member {
		d.Reason = "tenant_membership_required"
		return d, nil
	}
	if owner == actor.UserID {
		d.Allowed = true
		d.Role = RoleAdmin
		d.Reason = "owner"
		return d, nil
	}
	role := ""
	if r.Mode == DocumentInherit {
		if owner == "" {
			role = RoleViewer
		}
		var inherited string
		err := q.QueryRowContext(ctx, p.rewrite("SELECT role FROM dataset_shares WHERE dataset_id=$1 AND user_id=$2"), r.DatasetID, actor.UserID).Scan(&inherited)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return d, err
		}
		role = documentMaxRole(role, inherited)
	}
	rows, err := q.QueryContext(ctx, p.rewrite(`SELECT role FROM document_grants
		WHERE dataset_id=$1 AND data_id=$2 AND
		((principal_kind='user' AND principal_id=$3) OR
		(principal_kind='group' AND principal_id IN
		 (SELECT g.id FROM access_groups g JOIN access_group_members m ON m.group_id=g.id AND m.tenant_id=g.tenant_id
		  WHERE g.tenant_id=$4 AND m.user_id=$5)))`), r.DatasetID, r.DataID, actor.UserID, r.TenantID, actor.UserID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var granted string
		if err := rows.Scan(&granted); err != nil {
			return d, err
		}
		role = documentMaxRole(role, granted)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	d.Role, d.Allowed = role, documentRoleAllows(role, action)
	d.Reason = "document_denied"
	if d.Allowed {
		d.Reason = "document_" + role
	}
	return d, nil
}

// Document management is distinct from content editing. The dataset helper
// normalizes share/delete to write for legacy roles, which would let an editor
// grant itself admin when applied to explicit document policies.
func documentRoleAllows(role, action string) bool {
	switch action {
	case ActionRead:
		return role == RoleViewer || role == RoleEditor || role == RoleAdmin
	case ActionWrite:
		return role == RoleEditor || role == RoleAdmin
	case ActionShare, ActionDelete:
		return role == RoleAdmin
	default:
		return false
	}
}

func documentMaxRole(a, b string) string {
	rank := map[string]int{RoleViewer: 1, RoleEditor: 2, RoleAdmin: 3}
	b = strings.ToLower(b)
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// RegisterDocument fixes the tenant explicitly. Repetition with identical
// registration is idempotent; it cannot restore a tombstone or change tenant.
func (p SQLPolicy) RegisterDocument(ctx context.Context, actor Actor, ref DocumentRef, tenantID, mode string) (DocumentResource, error) {
	if p.DB == nil || ref.DatasetID == "" || ref.DataID == "" || tenantID == "" || !documentModeValid(mode) {
		return DocumentResource{}, ErrDocumentInvalid
	}
	tx, owned, err := p.documentMutationTransaction(ctx)
	if err != nil {
		return DocumentResource{}, err
	}
	if owned {
		defer tx.Rollback()
	}
	if err := p.lockDocumentDataset(ctx, tx, ref.DatasetID); err != nil {
		return DocumentResource{}, err
	}
	if !APIKeyAllows(actor.APIKeyPermissions, ActionShare) {
		return DocumentResource{}, ErrDocumentForbidden
	}
	active, admin, err := p.documentIdentity(ctx, tx, actor.UserID)
	if err != nil {
		return DocumentResource{}, err
	}
	if !active {
		return DocumentResource{}, ErrDocumentForbidden
	}
	owner, err := p.documentOwner(ctx, tx, ref)
	if err != nil {
		return DocumentResource{}, err
	}
	if !admin {
		member, err := p.documentMember(ctx, tx, actor.UserID, tenantID)
		if err != nil {
			return DocumentResource{}, err
		}
		if !member || actor.TenantID != tenantID || owner != actor.UserID {
			return DocumentResource{}, ErrDocumentForbidden
		}
	}
	if err := p.lockDocumentData(ctx, tx, ref.DataID); err != nil {
		return DocumentResource{}, err
	}
	if _, err := tx.ExecContext(ctx, p.rewrite(`INSERT INTO document_resources(dataset_id,data_id,tenant_id,mode)
		VALUES($1,$2,$3,$4) ON CONFLICT(dataset_id,data_id) DO NOTHING`), ref.DatasetID, ref.DataID, tenantID, mode); err != nil {
		return DocumentResource{}, err
	}
	r, err := p.documentResource(ctx, tx, ref)
	if err != nil {
		return DocumentResource{}, err
	}
	if r.TenantID != tenantID || r.Mode != mode || r.Tombstoned {
		return DocumentResource{}, ErrDocumentVersionConflict
	}
	if err := finishDocumentMutation(tx, owned); err != nil {
		return DocumentResource{}, err
	}
	return r, nil
}

func documentModeValid(mode string) bool {
	return mode == DocumentInherit || mode == DocumentRestricted
}

func (p SQLPolicy) documentMutationTransaction(ctx context.Context) (*sql.Tx, bool, error) {
	if p.readTx != nil {
		return p.readTx, false, nil
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	return tx, true, err
}

func finishDocumentMutation(tx *sql.Tx, owned bool) error {
	if !owned {
		return nil
	}
	return tx.Commit()
}

// mutateDocument locks the revision with CAS before dependent SQL mutations.
// Errors roll back the revision and every grant/state change together.
func (p SQLPolicy) mutateDocument(ctx context.Context, actor Actor, ref DocumentRef, aclRevision int64, contentRevision int64, action string, mutate func(*sql.Tx, DocumentResource) error) (DocumentResource, error) {
	if p.DB == nil || aclRevision <= 0 {
		return DocumentResource{}, ErrDocumentInvalid
	}
	tx, owned, err := p.documentMutationTransaction(ctx)
	if err != nil {
		return DocumentResource{}, err
	}
	if owned {
		defer tx.Rollback()
	}
	r, err := p.mutateDocumentTx(ctx, tx, actor, ref, aclRevision, contentRevision, action, mutate)
	if err != nil {
		return DocumentResource{}, err
	}
	if err := finishDocumentMutation(tx, owned); err != nil {
		return DocumentResource{}, err
	}
	return r, nil
}

func (p SQLPolicy) mutateDocumentTx(ctx context.Context, tx *sql.Tx, actor Actor, ref DocumentRef, aclRevision, contentRevision int64, action string, mutate func(*sql.Tx, DocumentResource) error) (DocumentResource, error) {
	r, err := p.documentResource(ctx, tx, ref)
	if err != nil {
		return DocumentResource{}, err
	}
	d, err := p.authorizeRegisteredDocument(ctx, tx, actor, r, action)
	if err != nil {
		return DocumentResource{}, err
	}
	if !d.Allowed {
		return DocumentResource{}, ErrDocumentForbidden
	}
	if r.ACLRevision != aclRevision || (contentRevision > 0 && r.ContentRevision != contentRevision) {
		return DocumentResource{}, ErrDocumentVersionConflict
	}
	result, err := tx.ExecContext(ctx, p.rewrite(`UPDATE document_resources SET acl_revision=acl_revision+1
		WHERE dataset_id=$1 AND data_id=$2 AND acl_revision=$3 AND content_revision=$4 AND tombstoned=FALSE`), ref.DatasetID, ref.DataID, aclRevision, r.ContentRevision)
	if err != nil {
		return DocumentResource{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return DocumentResource{}, err
	}
	if n != 1 {
		return DocumentResource{}, ErrDocumentVersionConflict
	}
	if err := mutate(tx, r); err != nil {
		return DocumentResource{}, err
	}
	r, err = p.documentResource(ctx, tx, ref)
	if err != nil {
		return DocumentResource{}, err
	}
	return r, nil
}

func (p SQLPolicy) GrantDocument(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision int64, principal DocumentPrincipal, role string) (DocumentResource, error) {
	role = strings.ToLower(role)
	if !ValidRole(role) {
		return DocumentResource{}, ErrDocumentInvalid
	}
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, 0, ActionShare, func(tx *sql.Tx, r DocumentResource) error {
		if err := p.validateDocumentPrincipal(ctx, tx, principal, r.TenantID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, p.rewrite(`INSERT INTO document_grants(dataset_id,data_id,principal_kind,principal_id,role,granted_by)
			VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(dataset_id,data_id,principal_kind,principal_id)
			DO UPDATE SET role=EXCLUDED.role, granted_by=EXCLUDED.granted_by`), ref.DatasetID, ref.DataID, principal.Kind, principal.ID, role, actor.UserID)
		return err
	})
}

func (p SQLPolicy) RevokeDocument(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision int64, principal DocumentPrincipal) (DocumentResource, error) {
	if principal.ID == "" || (principal.Kind != DocumentUser && principal.Kind != DocumentGroup) {
		return DocumentResource{}, ErrDocumentInvalid
	}
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, 0, ActionShare, func(tx *sql.Tx, _ DocumentResource) error {
		_, err := tx.ExecContext(ctx, p.rewrite("DELETE FROM document_grants WHERE dataset_id=$1 AND data_id=$2 AND principal_kind=$3 AND principal_id=$4"), ref.DatasetID, ref.DataID, principal.Kind, principal.ID)
		return err
	})
}

func (p SQLPolicy) SetDocumentMode(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision int64, mode string) (DocumentResource, error) {
	if !documentModeValid(mode) {
		return DocumentResource{}, ErrDocumentInvalid
	}
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, 0, ActionShare, func(tx *sql.Tx, _ DocumentResource) error {
		_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET mode=$1 WHERE dataset_id=$2 AND data_id=$3"), mode, ref.DatasetID, ref.DataID)
		return err
	})
}

func (p SQLPolicy) SetDocumentHold(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision int64, hold bool) (DocumentResource, error) {
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, 0, ActionShare, func(tx *sql.Tx, _ DocumentResource) error {
		_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET hold=$1 WHERE dataset_id=$2 AND data_id=$3"), hold, ref.DatasetID, ref.DataID)
		return err
	})
}

func (p SQLPolicy) TombstoneDocument(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision, expectedContentRevision int64) (DocumentResource, error) {
	if expectedContentRevision <= 0 {
		return DocumentResource{}, ErrDocumentInvalid
	}
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, expectedContentRevision, ActionDelete, func(tx *sql.Tx, _ DocumentResource) error {
		_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET tombstoned=TRUE, content_revision=content_revision+1 WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID)
		return err
	})
}

// AdvanceDocumentContent invalidates prior source revisions; publication must
// separately compare the returned revision. This does not publish derivatives.
func (p SQLPolicy) AdvanceDocumentContent(ctx context.Context, actor Actor, ref DocumentRef, expectedACLRevision, expectedContentRevision int64) (DocumentResource, error) {
	if expectedContentRevision <= 0 {
		return DocumentResource{}, ErrDocumentInvalid
	}
	return p.mutateDocument(ctx, actor, ref, expectedACLRevision, expectedContentRevision, ActionWrite, func(tx *sql.Tx, _ DocumentResource) error {
		_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET content_revision=content_revision+1 WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID)
		return err
	})
}

func (p SQLPolicy) validateDocumentPrincipal(ctx context.Context, q documentQuerier, principal DocumentPrincipal, tenantID string) error {
	if principal.ID == "" {
		return ErrDocumentInvalid
	}
	switch principal.Kind {
	case DocumentUser:
		active, _, err := p.documentIdentity(ctx, q, principal.ID)
		if err != nil {
			return err
		}
		member, err := p.documentMember(ctx, q, principal.ID, tenantID)
		if err != nil {
			return err
		}
		if !active || !member {
			return ErrDocumentForbidden
		}
	case DocumentGroup:
		var tenant string
		err := q.QueryRowContext(ctx, p.rewrite("SELECT tenant_id FROM access_groups WHERE id=$1"), principal.ID).Scan(&tenant)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDocumentForbidden
		}
		if err != nil {
			return err
		}
		if tenant != tenantID {
			return ErrDocumentForbidden
		}
	default:
		return ErrDocumentInvalid
	}
	return nil
}

type DocumentGrant struct {
	PrincipalKind string `json:"principal_kind"`
	PrincipalID   string `json:"principal_id"`
	Role          string `json:"role"`
}

// ReadDocumentPolicy exposes grants only to document managers. A read-only API
// key may inspect administration metadata but cannot mutate it.
func (p SQLPolicy) ReadDocumentPolicy(ctx context.Context, actor Actor, ref DocumentRef) (DocumentResource, []DocumentGrant, error) {
	if p.DB == nil {
		return DocumentResource{}, nil, ErrDocumentNotFound
	}
	if !APIKeyAllows(actor.APIKeyPermissions, ActionRead) {
		return DocumentResource{}, nil, ErrDocumentForbidden
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return DocumentResource{}, nil, err
	}
	defer tx.Rollback()
	r, err := p.documentResource(ctx, tx, ref)
	if err != nil {
		return DocumentResource{}, nil, err
	}
	actor.APIKeyPermissions = ""
	d, err := p.authorizeRegisteredDocument(ctx, tx, actor, r, ActionShare)
	if err != nil {
		return DocumentResource{}, nil, err
	}
	if !d.Allowed {
		return DocumentResource{}, nil, ErrDocumentForbidden
	}
	rows, err := tx.QueryContext(ctx, p.rewrite("SELECT principal_kind,principal_id,role FROM document_grants WHERE dataset_id=$1 AND data_id=$2 ORDER BY principal_kind,principal_id"), ref.DatasetID, ref.DataID)
	if err != nil {
		return DocumentResource{}, nil, err
	}
	grants := []DocumentGrant{}
	for rows.Next() {
		var g DocumentGrant
		if err := rows.Scan(&g.PrincipalKind, &g.PrincipalID, &g.Role); err != nil {
			rows.Close()
			return DocumentResource{}, nil, err
		}
		grants = append(grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DocumentResource{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentResource{}, nil, err
	}
	return r, grants, nil
}

// Registration and dataset deletion acquire this row before reading document
// registrations. This serializes their scan/insert/delete boundary on both SQL
// dialects; it is not a model/output egress lease.
func (p SQLPolicy) lockDocumentDataset(ctx context.Context, tx *sql.Tx, datasetID string) error {
	result, err := tx.ExecContext(ctx, p.rewrite("UPDATE datasets SET id=id WHERE id=$1"), datasetID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDocumentNotFound
	}
	return nil
}

func (p SQLPolicy) lockDocumentData(ctx context.Context, tx *sql.Tx, dataID string) error {
	result, err := tx.ExecContext(ctx, p.rewrite("UPDATE data SET id=id WHERE id=$1"), dataID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDocumentNotFound
	}
	return nil
}

func (p SQLPolicy) unlinkDocumentTx(ctx context.Context, tx *sql.Tx, ref DocumentRef) error {
	result, err := tx.ExecContext(ctx, p.rewrite("DELETE FROM dataset_data WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDocumentNotFound
	}
	_, err = tx.ExecContext(ctx, p.rewrite(`UPDATE document_structured_artifacts SET state='retired',updated_at=CURRENT_TIMESTAMP
		WHERE data_id=$1 AND state='active' AND NOT EXISTS (SELECT 1 FROM dataset_data WHERE data_id=$2)`), ref.DataID, ref.DataID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, p.rewrite("DELETE FROM data WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM dataset_data WHERE data_id=$2)"), ref.DataID, ref.DataID)
	return err
}

// DeleteDocumentAssociation atomically tombstones a registered inclusion and
// removes its SQL source link. It does not erase graph/vector/storage artifacts.
// Legacy live links retain dataset authorization; a registration appearing while
// the transaction starts requires explicit revisions and never falls back.
func (p SQLPolicy) DeleteDocumentAssociation(ctx context.Context, actor Actor, ref DocumentRef, expectedACL, expectedContent int64) error {
	if p.DB == nil {
		return ErrDocumentNotFound
	}
	_, preflightErr := p.GetDocumentResource(ctx, ref)
	if preflightErr != nil && !errors.Is(preflightErr, ErrDocumentNotFound) {
		return preflightErr
	}
	if errors.Is(preflightErr, ErrDocumentNotFound) {
		d, err := p.AuthorizeDataset(ctx, actor, ref.DatasetID, ActionWrite)
		if err != nil {
			return err
		}
		if !d.Allowed {
			return ErrDocumentForbidden
		}
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.lockDocumentDataset(ctx, tx, ref.DatasetID); err != nil {
		return err
	}
	_, err = p.documentResource(ctx, tx, ref)
	if errors.Is(err, ErrDocumentNotFound) {
		if preflightErr == nil {
			return ErrDocumentVersionConflict
		}
		if err := p.unlinkDocumentTx(ctx, tx, ref); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if expectedACL <= 0 || expectedContent <= 0 {
			return ErrDocumentInvalid
		}
		_, err = p.mutateDocumentTx(ctx, tx, actor, ref, expectedACL, expectedContent, ActionDelete, func(tx *sql.Tx, _ DocumentResource) error {
			if _, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET tombstoned=TRUE,content_revision=content_revision+1 WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID); err != nil {
				return err
			}
			return p.unlinkDocumentTx(ctx, tx, ref)
		})
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteDatasetWithDocuments checks every registered inclusion and hold before
// deleting the dataset. All tombstones and SQL removals commit or roll back
// together. Durable registrations survive the dataset's foreign-key cascades.
func (p SQLPolicy) DeleteDatasetWithDocuments(ctx context.Context, actor Actor, datasetID string) error {
	if p.DB == nil {
		return ErrDocumentNotFound
	}
	d, err := p.AuthorizeDataset(ctx, actor, datasetID, ActionWrite)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return ErrDocumentForbidden
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.lockDocumentDataset(ctx, tx, datasetID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, p.rewrite("SELECT data_id FROM document_resources WHERE dataset_id=$1 ORDER BY data_id"), datasetID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		ref := DocumentRef{DatasetID: datasetID, DataID: id}
		r, err := p.documentResource(ctx, tx, ref)
		if err != nil {
			return err
		}
		if r.Hold {
			return ErrDocumentForbidden
		}
		if r.Tombstoned {
			continue
		}
		_, err = p.mutateDocumentTx(ctx, tx, actor, ref, r.ACLRevision, r.ContentRevision, ActionDelete, func(tx *sql.Tx, _ DocumentResource) error {
			_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET tombstoned=TRUE,content_revision=content_revision+1 WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID)
			return err
		})
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, p.rewrite("DELETE FROM datasets WHERE id=$1"), datasetID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, p.rewrite(`UPDATE document_structured_artifacts SET state='retired',updated_at=CURRENT_TIMESTAMP
		WHERE state='active' AND NOT EXISTS (SELECT 1 FROM dataset_data WHERE data_id=document_structured_artifacts.data_id)`)); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrDocumentSharedMetadata = errors.New("document metadata is shared by multiple datasets")

// RenameDocument changes shared source metadata only for a single registered
// inclusion. Per-inclusion names require a separate schema; changing another
// dataset's view implicitly is forbidden. The content and ACL revisions advance.
func (p SQLPolicy) RenameDocument(ctx context.Context, actor Actor, ref DocumentRef, expectedACL, expectedContent int64, name string) (DocumentResource, error) {
	if p.DB == nil || expectedACL <= 0 || expectedContent <= 0 || name == "" {
		return DocumentResource{}, ErrDocumentInvalid
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return DocumentResource{}, err
	}
	defer tx.Rollback()
	if err := p.lockDocumentDataset(ctx, tx, ref.DatasetID); err != nil {
		return DocumentResource{}, err
	}
	// Source-key locking serializes registration and FK-backed new inclusions
	// with the check that a physical name belongs to exactly one inclusion.
	if err := p.lockDocumentData(ctx, tx, ref.DataID); err != nil {
		return DocumentResource{}, err
	}
	r, err := p.mutateDocumentTx(ctx, tx, actor, ref, expectedACL, expectedContent, ActionWrite, func(tx *sql.Tx, _ DocumentResource) error {
		var count int
		if err := tx.QueryRowContext(ctx, p.rewrite("SELECT COUNT(*) FROM dataset_data WHERE data_id=$1"), ref.DataID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrDocumentSharedMetadata
		}
		if err := p.renameDocumentSourceTx(ctx, tx, ref.DataID, name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, p.rewrite("UPDATE document_resources SET content_revision=content_revision+1 WHERE dataset_id=$1 AND data_id=$2"), ref.DatasetID, ref.DataID)
		return err
	})
	if err != nil {
		return DocumentResource{}, err
	}
	if err := tx.Commit(); err != nil {
		return DocumentResource{}, err
	}
	return r, nil
}

// RenameLegacyDocument preserves legacy dataset writes only while no registered
// inclusion references these shared bytes. An unregistered alias cannot bypass
// another inclusion's hold or content/ACL revision checks.
func (p SQLPolicy) RenameLegacyDocument(ctx context.Context, actor Actor, ref DocumentRef, name string) error {
	if p.DB == nil || name == "" {
		return ErrDocumentInvalid
	}
	d, err := p.AuthorizeDataset(ctx, actor, ref.DatasetID, ActionWrite)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return ErrDocumentForbidden
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := p.lockDocumentDataset(ctx, tx, ref.DatasetID); err != nil {
		return err
	}
	if err := p.lockDocumentData(ctx, tx, ref.DataID); err != nil {
		return err
	}
	if _, err := p.documentOwner(ctx, tx, ref); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, p.rewrite("SELECT COUNT(*) FROM document_resources WHERE data_id=$1"), ref.DataID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrDocumentSharedMetadata
	}
	if err := p.renameDocumentSourceTx(ctx, tx, ref.DataID, name); err != nil {
		return err
	}
	return tx.Commit()
}

// Source processing metadata is shared by data.id, even for legacy inclusions.
func (p SQLPolicy) renameDocumentSourceTx(ctx context.Context, tx *sql.Tx, dataID, name string) error {
	result, err := tx.ExecContext(ctx, p.rewrite("UPDATE data SET name=$1,pipeline_status='{}',token_count=-1,updated_at=CURRENT_TIMESTAMP WHERE id=$2 AND name IS DISTINCT FROM $3"), name, dataID, name)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n > 0 {
		_, err = p.WithReadTransaction(tx).AdvanceSourceVersion(ctx, dataID)
	}
	return err
}
