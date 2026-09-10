package access

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	ErrSCIMInvalid         = errors.New("scim: invalid managed resource")
	ErrSCIMConflict        = errors.New("scim: resource already exists")
	ErrSCIMNotFound        = errors.New("scim: managed resource not found")
	ErrSCIMVersionConflict = errors.New("scim: resource version conflict")
)

// EnsureDirectoryGroupSchema follows the base tenant/group schema, even when the
// directory HTTP surface is disabled: local group mutations check ownership.
func EnsureDirectoryGroupSchema(ctx context.Context, db *sql.DB, q QueryRewriter) error {
	if db == nil {
		return ErrSCIMDisabled
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS scim_directories (issuer TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id), UNIQUE(issuer,tenant_id))`,
		`CREATE TABLE IF NOT EXISTS scim_groups (
   issuer TEXT NOT NULL, external_id TEXT NOT NULL, resource_id TEXT NOT NULL UNIQUE,
   group_id TEXT UNIQUE, tenant_id TEXT NOT NULL,
   PRIMARY KEY(issuer,external_id),
   FOREIGN KEY(issuer,tenant_id) REFERENCES scim_directories(issuer,tenant_id),
   FOREIGN KEY(group_id,tenant_id) REFERENCES access_groups(id,tenant_id))`,
	} {
		if q != nil {
			statement = q(statement)
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s SCIMStore) BindDirectory(ctx context.Context, issuer, tenantID string) error {
	if s.DB == nil || !scimExactID(issuer) || tenantID == "" {
		return ErrSCIMInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.rewrite(`INSERT INTO scim_directories(issuer,tenant_id) VALUES($1,$2) ON CONFLICT(issuer) DO NOTHING`), issuer, tenantID); err != nil {
		return err
	}
	var bound string
	if err = tx.QueryRowContext(ctx, s.rewrite(`SELECT tenant_id FROM scim_directories WHERE issuer=$1`), issuer).Scan(&bound); err != nil {
		return err
	}
	if bound != tenantID {
		return ErrSCIMConflict
	}
	return tx.Commit()
}

func scimExactID(v string) bool {
	return v != "" && len(v) <= 1024 && strings.TrimSpace(v) == v && utf8.ValidString(v) && strings.IndexFunc(v, unicode.IsControl) < 0
}

// The first write serializes changes for this fixed directory before reading
// state. This also avoids SQLite read-to-write transaction upgrades.
func (s SCIMStore) lockDirectory(ctx context.Context, tx *sql.Tx, issuer string) error {
	if s.TenantID == "" {
		return nil
	}
	result, err := tx.ExecContext(ctx, s.rewrite(`UPDATE scim_directories SET tenant_id=tenant_id WHERE issuer=$1 AND tenant_id=$2`), issuer, s.TenantID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrSCIMInvalid
	}
	return nil
}

func (s SCIMStore) ensureUserSchema(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS scim_enterprise_users (
   issuer TEXT NOT NULL, external_id TEXT NOT NULL,
   employee_number TEXT NOT NULL DEFAULT '', cost_center TEXT NOT NULL DEFAULT '',
   organization TEXT NOT NULL DEFAULT '', division TEXT NOT NULL DEFAULT '', department TEXT NOT NULL DEFAULT '', manager_id TEXT NOT NULL DEFAULT '',
   PRIMARY KEY(issuer,external_id), FOREIGN KEY(issuer,external_id) REFERENCES scim_identities(issuer,external_id))`,
		`CREATE TABLE IF NOT EXISTS scim_provisioning_events (
   id TEXT PRIMARY KEY, issuer TEXT NOT NULL, tenant_id TEXT NOT NULL DEFAULT '',
   action TEXT NOT NULL, resource_kind TEXT NOT NULL, resource_id TEXT NOT NULL,
   revision BIGINT NOT NULL DEFAULT 0, member_count INTEGER NOT NULL DEFAULT 0,
   created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
		`CREATE INDEX IF NOT EXISTS idx_scim_provisioning_events_scope ON scim_provisioning_events(issuer,tenant_id,created_at,id)`,
	} {
		if _, err := s.DB.ExecContext(ctx, s.rewrite(statement)); err != nil {
			return err
		}
	}
	return nil
}

func (s SCIMStore) auditMutation(ctx context.Context, tx *sql.Tx, issuer, action, kind, id string, revision int64, members int) error {
	_, err := tx.ExecContext(ctx, s.rewrite(`INSERT INTO scim_provisioning_events(id,issuer,tenant_id,action,resource_kind,resource_id,revision,member_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`), uuid.NewString(), issuer, s.TenantID, action, kind, id, revision, members)
	return err
}

type SCIMEnterpriseUser struct {
	EmployeeNumber string `json:"employeeNumber,omitempty"`
	CostCenter     string `json:"costCenter,omitempty"`
	Organization   string `json:"organization,omitempty"`
	Division       string `json:"division,omitempty"`
	Department     string `json:"department,omitempty"`
	ManagerID      string `json:"-"`
}

func (s SCIMStore) enterpriseUser(ctx context.Context, q documentQuerier, issuer, external string) (SCIMEnterpriseUser, error) {
	var u SCIMEnterpriseUser
	err := q.QueryRowContext(ctx, s.rewrite(`SELECT employee_number,cost_center,organization,division,department,manager_id FROM scim_enterprise_users WHERE issuer=$1 AND external_id=$2`), issuer, external).Scan(&u.EmployeeNumber, &u.CostCenter, &u.Organization, &u.Division, &u.Department, &u.ManagerID)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return u, err
}
func (s SCIMStore) EnterpriseUser(ctx context.Context, issuer, external string) (SCIMEnterpriseUser, error) {
	if s.DB == nil {
		return SCIMEnterpriseUser{}, ErrSCIMDisabled
	}
	return s.enterpriseUser(ctx, s.DB, issuer, external)
}
func (s SCIMStore) putEnterprise(ctx context.Context, tx *sql.Tx, u SCIMUser) error {
	if u.Enterprise == nil && u.EnterprisePatch == nil {
		return nil
	}
	if s.TenantID == "" {
		return ErrSCIMInvalid
	}
	profile, err := s.enterpriseUser(ctx, tx, u.Issuer, u.ExternalID)
	if err != nil {
		return err
	}
	if u.Enterprise != nil {
		profile = *u.Enterprise
	}
	fields := map[string]*string{"employeeNumber": &profile.EmployeeNumber, "costCenter": &profile.CostCenter, "organization": &profile.Organization, "division": &profile.Division, "department": &profile.Department, "manager": &profile.ManagerID}
	for name, value := range u.EnterprisePatch {
		dst, ok := fields[name]
		if !ok {
			return ErrSCIMInvalid
		}
		*dst = ""
		if value != nil {
			*dst = *value
		}
	}
	for _, v := range fields {
		if len(*v) > 1024 || !utf8.ValidString(*v) || strings.IndexFunc(*v, unicode.IsControl) >= 0 {
			return ErrSCIMInvalid
		}
	}
	if profile.ManagerID != "" {
		var found string
		err := tx.QueryRowContext(ctx, s.rewrite(`SELECT u.id FROM users u JOIN scim_identities i ON i.user_id=u.id JOIN user_tenant t ON t.user_id=u.id WHERE i.issuer=$1 AND u.id=$2 AND t.tenant_id=$3 AND u.is_active=true`), u.Issuer, profile.ManagerID, s.TenantID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSCIMInvalid
		}
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, s.rewrite(`INSERT INTO scim_enterprise_users(issuer,external_id,employee_number,cost_center,organization,division,department,manager_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
  ON CONFLICT(issuer,external_id) DO UPDATE SET employee_number=EXCLUDED.employee_number,cost_center=EXCLUDED.cost_center,organization=EXCLUDED.organization,division=EXCLUDED.division,department=EXCLUDED.department,manager_id=EXCLUDED.manager_id`), u.Issuer, u.ExternalID, profile.EmployeeNumber, profile.CostCenter, profile.Organization, profile.Division, profile.Department, profile.ManagerID)
	return err
}

type SCIMGroup struct {
	ID, ExternalID, TenantID, DisplayName string
	Revision                              int64
	Members                               []string
}
type SCIMGroupOperation struct {
	Op, Field, Value string
	Members          []string
}

func (s SCIMStore) managedGroup(ctx context.Context, q documentQuerier, issuer, id string) (SCIMGroup, error) {
	g := SCIMGroup{Members: []string{}}
	rows, err := q.QueryContext(ctx, s.rewrite(`SELECT x.resource_id,x.external_id,x.tenant_id,g.name,g.revision,COALESCE(m.user_id,'') FROM scim_groups x JOIN access_groups g ON g.id=x.group_id AND g.tenant_id=x.tenant_id LEFT JOIN access_group_members m ON m.group_id=g.id WHERE x.issuer=$1 AND x.resource_id=$2 AND x.tenant_id=$3 ORDER BY m.user_id`), issuer, id, s.TenantID)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var member string
		if err := rows.Scan(&g.ID, &g.ExternalID, &g.TenantID, &g.DisplayName, &g.Revision, &member); err != nil {
			return g, err
		}
		if member != "" {
			g.Members = append(g.Members, member)
		}
	}
	if err := rows.Err(); err != nil {
		return g, err
	}
	if g.ID == "" {
		return g, ErrSCIMNotFound
	}
	return g, nil
}
func (s SCIMStore) GetSCIMGroup(ctx context.Context, issuer, id string) (SCIMGroup, error) {
	if s.DB == nil || s.TenantID == "" {
		return SCIMGroup{}, ErrSCIMDisabled
	}
	return s.managedGroup(ctx, s.DB, issuer, id)
}
func (s SCIMStore) ListSCIMGroups(ctx context.Context, issuer, field, value string, start, count int) ([]SCIMGroup, int, error) {
	if s.DB == nil || s.TenantID == "" {
		return nil, 0, ErrSCIMDisabled
	}
	if start < 1 || count < 1 || count > 200 {
		return nil, 0, ErrSCIMInvalid
	}
	filter := ""
	args := []any{issuer, s.TenantID}
	if field != "" {
		switch field {
		case "externalId":
			filter = " AND x.external_id=$3"
		case "displayName":
			filter = " AND g.name=$3"
		default:
			return nil, 0, ErrSCIMInvalid
		}
		args = append(args, value)
	}
	base := ` FROM scim_groups x JOIN access_groups g ON g.id=x.group_id WHERE x.issuer=$1 AND x.tenant_id=$2` + filter
	var total int
	if err := s.DB.QueryRowContext(ctx, s.rewrite("SELECT COUNT(*)"+base), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	// Only a bounded page of group IDs is retained; close rows before a second
	// query so SQLite deployments with one SQL connection do not deadlock.
	pageArgs := append(append([]any(nil), args...), count, start-1)
	rows, err := s.DB.QueryContext(ctx, s.rewrite("SELECT x.resource_id"+base+fmt.Sprintf(" ORDER BY x.resource_id LIMIT $%d OFFSET $%d", len(pageArgs)-1, len(pageArgs))), pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, 0, err
	}
	groups := []SCIMGroup{}
	for _, id := range ids {
		g, err := s.GetSCIMGroup(ctx, issuer, id)
		if errors.Is(err, ErrSCIMNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		groups = append(groups, g)
	}
	return groups, total, nil
}

func (s SCIMStore) setSCIMGroupMembers(ctx context.Context, tx *sql.Tx, issuer, id string, members []string) error {
	if len(members) > 1000 {
		return ErrSCIMInvalid
	}
	unique := map[string]bool{}
	for _, uid := range members {
		if !scimExactID(uid) {
			return ErrSCIMInvalid
		}
		unique[uid] = true
		var found string
		err := tx.QueryRowContext(ctx, s.rewrite(`SELECT u.id FROM users u JOIN scim_identities i ON i.user_id=u.id JOIN user_tenant t ON t.user_id=u.id WHERE i.issuer=$1 AND u.id=$2 AND t.tenant_id=$3 AND u.is_active=true`), issuer, uid, s.TenantID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSCIMInvalid
		}
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.rewrite(`DELETE FROM access_group_members WHERE group_id=$1`), id); err != nil {
		return err
	}
	ids := make([]string, 0, len(unique))
	for id := range unique {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, uid := range ids {
		if _, err := tx.ExecContext(ctx, s.rewrite(`INSERT INTO access_group_members(group_id,tenant_id,user_id) VALUES($1,$2,$3)`), id, s.TenantID, uid); err != nil {
			return err
		}
	}
	return nil
}

func (s SCIMStore) CreateSCIMGroup(ctx context.Context, issuer, external, name string, members []string) (SCIMGroup, bool, error) {
	if s.DB == nil || s.TenantID == "" || !scimExactID(issuer) || !scimExactID(external) || !scimExactID(name) || len(name) > 256 {
		return SCIMGroup{}, false, ErrSCIMInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SCIMGroup{}, false, err
	}
	defer tx.Rollback()
	if err = s.lockDirectory(ctx, tx, issuer); err != nil {
		return SCIMGroup{}, false, err
	}
	var current, oldResource string
	err = tx.QueryRowContext(ctx, s.rewrite(`SELECT COALESCE(group_id,''),resource_id FROM scim_groups WHERE issuer=$1 AND external_id=$2 AND tenant_id=$3`), issuer, external, s.TenantID).Scan(&current, &oldResource)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return SCIMGroup{}, false, err
	}
	if current != "" {
		g, err := s.managedGroup(ctx, tx, issuer, current)
		if err != nil {
			return g, false, err
		}
		return g, false, tx.Commit()
	}
	var collision string
	err = tx.QueryRowContext(ctx, s.rewrite(`SELECT id FROM access_groups WHERE tenant_id=$1 AND name=$2`), s.TenantID, name).Scan(&collision)
	if err == nil {
		return SCIMGroup{}, false, ErrSCIMConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SCIMGroup{}, false, err
	}
	id := uuid.NewString()
	if _, err = tx.ExecContext(ctx, s.rewrite(`INSERT INTO principals(id,type) VALUES($1,'group')`), id); err != nil {
		return SCIMGroup{}, false, err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`INSERT INTO access_groups(id,tenant_id,name) VALUES($1,$2,$3)`), id, s.TenantID, name); err != nil {
		return SCIMGroup{}, false, err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`INSERT INTO scim_groups(issuer,external_id,resource_id,group_id,tenant_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(issuer,external_id) DO UPDATE SET resource_id=EXCLUDED.resource_id,group_id=EXCLUDED.group_id`), issuer, external, id, id, s.TenantID); err != nil {
		return SCIMGroup{}, false, err
	}
	if err = s.setSCIMGroupMembers(ctx, tx, issuer, id, members); err != nil {
		return SCIMGroup{}, false, err
	}
	g, err := s.managedGroup(ctx, tx, issuer, id)
	if err != nil {
		return g, false, err
	}
	if err = s.auditMutation(ctx, tx, issuer, "create", "Group", id, g.Revision, len(g.Members)); err != nil {
		return g, false, err
	}
	return g, true, tx.Commit()
}

func (s SCIMStore) UpdateSCIMGroup(ctx context.Context, issuer, id string, expected int64, ops []SCIMGroupOperation) (SCIMGroup, error) {
	if s.DB == nil || s.TenantID == "" || expected < 0 || len(ops) == 0 || len(ops) > 100 {
		return SCIMGroup{}, ErrSCIMInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SCIMGroup{}, err
	}
	defer tx.Rollback()
	if err = s.lockDirectory(ctx, tx, issuer); err != nil {
		return SCIMGroup{}, err
	}
	g, err := s.managedGroup(ctx, tx, issuer, id)
	if err != nil {
		return g, err
	}
	if expected != 0 && g.Revision != expected {
		return g, ErrSCIMVersionConflict
	}
	members := map[string]bool{}
	for _, id := range g.Members {
		members[id] = true
	}
	for _, op := range ops {
		switch op.Field {
		case "displayName":
			if op.Op != "add" && op.Op != "replace" {
				return g, ErrSCIMInvalid
			}
			if !scimExactID(op.Value) || len(op.Value) > 256 {
				return g, ErrSCIMInvalid
			}
			g.DisplayName = op.Value
		case "members":
			switch op.Op {
			case "replace":
				members = map[string]bool{}
			case "remove":
				members = map[string]bool{}
			case "add":
			default:
				return g, ErrSCIMInvalid
			}
			if op.Op != "remove" {
				for _, id := range op.Members {
					members[id] = true
				}
			}
		case "member":
			if op.Op != "remove" || !scimExactID(op.Value) {
				return g, ErrSCIMInvalid
			}
			delete(members, op.Value)
		default:
			return g, ErrSCIMInvalid
		}
	}
	var collision string
	err = tx.QueryRowContext(ctx, s.rewrite(`SELECT id FROM access_groups WHERE tenant_id=$1 AND name=$2 AND id<>$3`), s.TenantID, g.DisplayName, g.ID).Scan(&collision)
	if err == nil {
		return g, ErrSCIMConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return g, err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`UPDATE access_groups SET name=$1,revision=revision+1 WHERE id=$2`), g.DisplayName, g.ID); err != nil {
		return g, err
	}
	g.Members = []string{}
	for id := range members {
		g.Members = append(g.Members, id)
	}
	if err = s.setSCIMGroupMembers(ctx, tx, issuer, g.ID, g.Members); err != nil {
		return g, err
	}
	g, err = s.managedGroup(ctx, tx, issuer, id)
	if err != nil {
		return g, err
	}
	if err = s.auditMutation(ctx, tx, issuer, "update", "Group", id, g.Revision, len(g.Members)); err != nil {
		return g, err
	}
	return g, tx.Commit()
}

func (s SCIMStore) DeleteSCIMGroup(ctx context.Context, issuer, id string, expected int64) error {
	if s.DB == nil || s.TenantID == "" || expected < 0 {
		return ErrSCIMInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.lockDirectory(ctx, tx, issuer); err != nil {
		return err
	}
	var live string
	err = tx.QueryRowContext(ctx, s.rewrite(`SELECT COALESCE(group_id,'') FROM scim_groups WHERE issuer=$1 AND resource_id=$2 AND tenant_id=$3`), issuer, id, s.TenantID).Scan(&live)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSCIMNotFound
	}
	if err != nil {
		return err
	}
	if live == "" {
		return tx.Commit()
	}
	g, err := s.managedGroup(ctx, tx, issuer, id)
	if err != nil {
		return err
	}
	if expected != 0 && expected != g.Revision {
		return ErrSCIMVersionConflict
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`UPDATE access_groups SET revision=revision+1 WHERE id=$1`), id); err != nil {
		return err
	}
	// Retire the directory mapping before group/principal deletion. A recreate
	// gets a new principal ID; its old grants cannot become effective again.
	if _, err = tx.ExecContext(ctx, s.rewrite(`UPDATE scim_groups SET group_id=NULL WHERE issuer=$1 AND resource_id=$2`), issuer, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`UPDATE document_resources SET acl_revision=acl_revision+1 WHERE EXISTS(SELECT 1 FROM document_grants g WHERE g.dataset_id=document_resources.dataset_id AND g.data_id=document_resources.data_id AND g.principal_kind='group' AND g.principal_id=$1)`), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`DELETE FROM document_grants WHERE principal_kind='group' AND principal_id=$1`), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`DELETE FROM access_groups WHERE id=$1`), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.rewrite(`DELETE FROM principals WHERE id=$1`), id); err != nil {
		return err
	}
	if err = s.auditMutation(ctx, tx, issuer, "delete", "Group", id, g.Revision+1, 0); err != nil {
		return err
	}
	return tx.Commit()
}

// SCIMProvisioningEvent is a durable success record written with its mutation.
// It contains no credential, email, request body or descriptive directory name.
type SCIMProvisioningEvent struct {
	ID, Issuer, TenantID, Action, ResourceKind, ResourceID string
	Revision                                               int64
	MemberCount                                            int
}

func (s SCIMStore) ProvisioningEvents(ctx context.Context, issuer string, limit int) ([]SCIMProvisioningEvent, error) {
	if s.DB == nil || !scimExactID(issuer) || limit < 1 || limit > 200 {
		return nil, ErrSCIMInvalid
	}
	rows, err := s.DB.QueryContext(ctx, s.rewrite(`SELECT id,issuer,tenant_id,action,resource_kind,resource_id,revision,member_count FROM scim_provisioning_events WHERE issuer=$1 AND tenant_id=$2 ORDER BY created_at DESC,id DESC LIMIT $3`), issuer, s.TenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []SCIMProvisioningEvent{}
	for rows.Next() {
		var e SCIMProvisioningEvent
		if err := rows.Scan(&e.ID, &e.Issuer, &e.TenantID, &e.Action, &e.ResourceKind, &e.ResourceID, &e.Revision, &e.MemberCount); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
