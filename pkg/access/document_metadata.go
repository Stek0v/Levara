package access

import (
	"context"
	"database/sql"
)

// MetadataCredential contains verified authentication facts, never raw tokens.
// A transport must populate these only after its cryptographic/key validation.
type MetadataCredential struct {
	Kind, KeyID, SessionID     string
	Epoch, IssuedAt, ExpiresAt int64
}

type MetadataActor struct {
	Actor
	Credential   MetadataCredential
	TrustedLocal bool // explicit configured no-auth mode; never inferred from missing proof
}

// BeginMetadataWrite stabilizes authorization and every data alias during a
// bounded publication. The caller owns commit/rollback. Extraction and provider
// calls finish before acquiring this fence. Authorized ingest may hold it across
// cooperative immutable blob writes (30s ceiling); offline recovery, not this
// transaction, guards against a backend that ignores cancellation.
func (p SQLPolicy) BeginMetadataWrite(ctx context.Context, actor MetadataActor, sqlite bool) (*sql.Tx, SQLPolicy, error) {
	if p.DB == nil {
		return nil, p, ErrDocumentInvalid
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, p, ErrDocumentInvalid
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, p, err
	}
	fail := func(err error) (*sql.Tx, SQLPolicy, error) { _ = tx.Rollback(); return nil, p, err }
	// The order follows BeginReadFence. This deliberately serializes a short
	// metadata transaction with revocations/publication until narrower locking is
	// justified; SQL deadlocks/timeouts are errors and never partial success.
	if sqlite {
		_, err = tx.ExecContext(ctx, "UPDATE users SET id=id WHERE 1=0")
	} else {
		_, err = tx.ExecContext(ctx, `LOCK TABLE users, tenants, user_tenant,
   datasets, data, dataset_data, dataset_shares, document_resources,
   document_grants, access_groups, access_group_members,
   api_keys, credential_epochs, auth_sessions IN SHARE ROW EXCLUSIVE MODE`)
	}
	if err != nil {
		return fail(err)
	}
	p = p.WithReadTransaction(tx)
	if !APIKeyAllows(actor.APIKeyPermissions, ActionWrite) {
		return fail(ErrDocumentForbidden)
	}
	if !actor.TrustedLocal {
		if actor.UserID == "" {
			return fail(ErrRevokedCredential)
		}
		c := actor.Credential
		if err := p.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return fail(err)
		}
	}
	if actor.TenantID != "" {
		member, err := p.IsTenantMember(ctx, actor.UserID, actor.TenantID)
		if err != nil {
			return fail(err)
		}
		if !member {
			return fail(ErrDocumentForbidden)
		}
	}
	return tx, p, nil
}

// AuthorizeMetadataAliases includes legacy associations and tombstoned or unlinked registrations:
// metadata is shared by data.id, so a different inclusion cannot bypass a hold,
// revocation or tenant boundary. It never falls back to legacy inheritance for
// a registered alias and must run inside BeginMetadataWrite's transaction.
func (p SQLPolicy) AuthorizeMetadataAliases(ctx context.Context, actor Actor, dataID string) (bool, error) {
	if p.readTx == nil || dataID == "" {
		return false, ErrDocumentInvalid
	}
	rows, err := p.readTx.QueryContext(ctx, p.rewrite(`SELECT dataset_id,data_id,tenant_id,mode,acl_revision,content_revision,tombstoned,hold FROM document_resources WHERE data_id=$1 ORDER BY dataset_id`), dataID)
	if err != nil {
		return false, err
	}
	var aliases []DocumentResource
	for rows.Next() {
		var r DocumentResource
		if err := rows.Scan(&r.DatasetID, &r.DataID, &r.TenantID, &r.Mode, &r.ACLRevision, &r.ContentRevision, &r.Tombstoned, &r.Hold); err != nil {
			rows.Close()
			return false, err
		}
		aliases = append(aliases, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, r := range aliases {
		if r.ACLRevision <= 0 || r.ContentRevision <= 0 {
			return true, ErrDocumentVersionConflict
		}
		d, err := p.authorizeRegisteredDocument(ctx, p.readTx, actor, r, ActionWrite)
		if err != nil {
			return true, err
		}
		if !d.Allowed {
			return true, ErrDocumentForbidden
		}
	}
	// A legacy inclusion still shares the data row. Its dataset permissions
	// cannot be bypassed by uploading through another, writable inclusion.
	rows, err = p.readTx.QueryContext(ctx, p.rewrite(`SELECT dd.dataset_id
 FROM dataset_data dd LEFT JOIN document_resources r
 ON r.dataset_id=dd.dataset_id AND r.data_id=dd.data_id
 WHERE dd.data_id=$1 AND r.data_id IS NULL ORDER BY dd.dataset_id`), dataID)
	if err != nil {
		return len(aliases) > 0, err
	}
	var legacy []string
	for rows.Next() {
		var datasetID string
		if err := rows.Scan(&datasetID); err != nil {
			rows.Close()
			return len(aliases) > 0, err
		}
		legacy = append(legacy, datasetID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return len(aliases) > 0, err
	}
	for _, datasetID := range legacy {
		d, err := p.AuthorizeDocument(ctx, actor, DocumentRef{DatasetID: datasetID, DataID: dataID}, ActionWrite)
		if err != nil {
			return len(aliases) > 0, err
		}
		if !d.Allowed {
			return len(aliases) > 0, ErrDocumentForbidden
		}
	}
	return len(aliases) > 0, nil
}
