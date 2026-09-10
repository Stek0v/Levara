package access

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PruneData removes SQL data in one bounded transaction. It preserves durable
// document tombstones and audit records. Physical/vector/provider cleanup is a
// separate operation and is not proved by a successful SQL prune.
func (p SQLPolicy) PruneData(ctx context.Context, actor MetadataActor, includeGraph bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if p.DB == nil {
		return ErrDocumentInvalid
	}
	// This is an instance-wide operation. A tenant-scoped token cannot expand
	// its scope through an administrator role or an explicit tool argument.
	if actor.TenantID != "" || !APIKeyAllows(actor.APIKeyPermissions, ActionDelete) || actor.Credential.Kind == "api_key" && strings.TrimSpace(actor.APIKeyPermissions) == "" {
		return ErrDocumentForbidden
	}
	sqlite := strings.Contains(strings.ToLower(fmt.Sprintf("%T", p.DB.Driver())), "sqlite")
	tx, locked, err := p.BeginMetadataWrite(ctx, actor, sqlite)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !actor.TrustedLocal || actor.UserID != "" {
		active, admin, err := locked.documentIdentity(ctx, tx, actor.UserID)
		if err != nil {
			return err
		}
		if !active || !admin {
			return ErrDocumentForbidden
		}
	}
	var held bool
	// Never join live datasets/data: orphaned and tombstoned registrations
	// retain their legal hold after source links disappear.
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM document_resources WHERE hold=TRUE)").Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrDocumentForbidden
	}
	// Registrations have no FK to the source tables and must survive deletion.
	// Existing tombstones keep their content revision. Clearing any retained
	// grants advances the ACL revision even for an already tombstoned resource.
	if _, err := tx.ExecContext(ctx, `UPDATE document_resources SET
 content_revision=CASE WHEN tombstoned=FALSE THEN content_revision+1 ELSE content_revision END,
 acl_revision=acl_revision+1,tombstoned=TRUE
 WHERE tombstoned=FALSE OR EXISTS(SELECT 1 FROM document_grants g WHERE g.dataset_id=document_resources.dataset_id AND g.data_id=document_resources.data_id)`); err != nil {
		return err
	}
	if includeGraph {
		for _, stmt := range []string{"DELETE FROM graph_edges", "DELETE FROM graph_nodes"} {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE document_structured_artifacts SET state='retired',updated_at=CURRENT_TIMESTAMP WHERE state='active'`); err != nil {
		return err
	}
	for _, stmt := range []string{"DELETE FROM document_grants", "DELETE FROM dataset_shares", "DELETE FROM dataset_data", "DELETE FROM data", "DELETE FROM datasets"} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	// Time-based expiry can change while SQL locks prevent credential mutation.
	if !actor.TrustedLocal {
		c := actor.Credential
		if err := locked.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
