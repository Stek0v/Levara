package access

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

func (p SQLPolicy) reader() documentQuerier {
	if p.readTx != nil {
		return p.readTx
	}
	return p.DB
}

// WithReadTransaction uses the caller's transaction for policy reads. The
// caller owns locking, commit and rollback; this method acquires no fence.
func (p SQLPolicy) WithReadTransaction(tx *sql.Tx) SQLPolicy {
	p.readTx = tx
	return p
}

// AuthorizeWorkspaceFenced stabilizes the policy rows used by a bounded
// workspace effect inside the caller-owned transaction.
func (p SQLPolicy) AuthorizeWorkspaceFenced(ctx context.Context, tx *sql.Tx, sqlite bool, req WorkspaceRequest) (Decision, error) {
	if tx == nil {
		return Decision{}, errors.New("access: workspace fence requires transaction")
	}
	if !sqlite {
		if _, err := tx.ExecContext(ctx, `LOCK TABLE users,datasets,dataset_shares IN SHARE MODE`); err != nil {
			return Decision{}, err
		}
	}
	return p.WithReadTransaction(tx).AuthorizeWorkspace(ctx, req)
}

// RecheckCredential uses already-verified token facts, never untrusted claims.
// It runs again under the read fence to close revocation after middleware.
func (p SQLPolicy) RecheckCredential(ctx context.Context, userID, kind, keyID, permissions, sessionID string, epoch, issuedAt, expiresAt int64) error {
	var current, watermark int64
	err := p.reader().QueryRowContext(ctx, p.rewrite(`SELECT COALESCE(e.epoch,0), COALESCE(e.revoked_before,0)
	 FROM users u LEFT JOIN credential_epochs e ON e.user_id=u.id
	 WHERE u.id=$1 AND u.is_active=true`), userID).Scan(&current, &watermark)
	if err != nil {
		return ErrRevokedCredential
	}
	switch kind {
	case "jwt":
		if epoch < 0 || epoch != current || expiresAt <= time.Now().Unix() {
			return ErrRevokedCredential
		}
		if sessionID != "" {
			var id string
			err = p.reader().QueryRowContext(ctx, p.rewrite("SELECT id FROM auth_sessions WHERE id=$1 AND user_id=$2 AND revoked=false AND expires_at>$3"), sessionID, userID, time.Now().Unix()).Scan(&id)
		}
	case "api_key":
		var livePermissions string
		err = p.reader().QueryRowContext(ctx, p.rewrite("SELECT permissions FROM api_keys WHERE id=$1 AND user_id=$2 AND revoked=false"), keyID, userID).Scan(&livePermissions)
		if livePermissions != permissions {
			return ErrRevokedCredential
		}
	case "external":
		if expiresAt <= time.Now().Unix() || (watermark > 0 && issuedAt <= watermark) {
			return ErrRevokedCredential
		}
	default:
		return ErrRevokedCredential
	}
	if err != nil {
		return ErrRevokedCredential
	}
	return nil
}

// BeginReadFence stabilizes SQL authorization while a bounded caller transfers
// protected bytes. The returned policy reads through the same transaction, so
// a single-connection pool works. Release must run after the transfer drains.
func (p SQLPolicy) BeginReadFence(ctx context.Context, sqlite bool) (SQLPolicy, func(), error) {
	if p.DB == nil {
		return p, nil, errors.New("access: read fence requires database")
	}
	if _, ok := ctx.Deadline(); !ok {
		return p, nil, errors.New("access: read fence requires deadline")
	}
	tx, err := p.DB.BeginTx(ctx, nil)
	if err != nil {
		return p, nil, err
	}
	if err := lockReadFence(ctx, tx, sqlite); err != nil {
		_ = tx.Rollback()
		return p, nil, err
	}
	p.readTx = tx
	return p, func() { _ = tx.Rollback() }, nil
}

// BeginTransferFence keeps authorization stable until the transfer actually
// drains, even if its observer context expires. The transport must interrupt
// blocked transfers on cancellation, and the caller must then release.
func (p SQLPolicy) BeginTransferFence(ctx context.Context, sqlite bool) (SQLPolicy, func(), error) {
	if p.DB == nil {
		return p, nil, errors.New("access: transfer fence requires database")
	}
	if _, ok := ctx.Deadline(); !ok {
		return p, nil, errors.New("access: transfer fence requires deadline")
	}
	conn, err := p.DB.Conn(ctx)
	if err != nil {
		return p, nil, err
	}
	// Cancel BEGIN/acquisition with the observer; after handoff, lifetime follows
	// actual transfer drain rather than the observer deadline.
	txCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	tx, err := conn.BeginTx(txCtx, nil)
	if err != nil {
		stop()
		cancel()
		_ = conn.Close()
		return p, nil, err
	}
	release := func() { stop(); _ = tx.Rollback(); cancel(); _ = conn.Close() }
	if err := lockReadFence(ctx, tx, sqlite); err != nil {
		release()
		return p, nil, err
	}
	if !stop() || ctx.Err() != nil {
		release()
		return p, nil, ctx.Err()
	}
	p.readTx = tx
	return p, release, nil
}

func lockReadFence(ctx context.Context, tx *sql.Tx, sqlite bool) error {
	var err error
	// ponytail: global SQL write exclusion during bounded egress; replace with
	// resource locks only when measurements justify the added revoker protocol.
	if sqlite {
		_, err = tx.ExecContext(ctx, "UPDATE users SET id=id WHERE 1=0")
	} else {
		_, err = tx.ExecContext(ctx, `LOCK TABLE users, tenants, user_tenant,
		 datasets, data, dataset_data, dataset_shares, document_resources,
		 document_grants, access_groups, access_group_members,
		 api_keys, credential_epochs, auth_sessions IN SHARE MODE`)
		if err == nil {
			// ponytail: serialize publication with bounded transfers; a per-document
			// revoker protocol can replace this lock if measured throughput needs it.
			_, err = tx.ExecContext(ctx, "LOCK TABLE document_index_publications, document_pipeline_statuses IN SHARE ROW EXCLUSIVE MODE")
		}
	}
	return err
}

func (p SQLPolicy) HasRegisteredDocuments(ctx context.Context, datasetID string) (bool, error) {
	var count int
	err := p.reader().QueryRowContext(ctx, p.rewrite("SELECT COUNT(*) FROM document_resources WHERE dataset_id=$1"), datasetID).Scan(&count)
	return count > 0, err
}

// DocumentIndexPublished excludes partial, superseded and never-published derivatives.
func (p SQLPolicy) DocumentIndexPublished(ctx context.Context, ref DocumentRef, revision int64, collection, generation string) (bool, error) {
	_, present, err := p.DocumentIndexLineage(ctx, ref, revision, collection, generation)
	return present, err
}

type DocumentPublicationLineage struct {
	SourcesJSON        string
	RequiresAdmin      bool
	SourceRevision     int64
	RawContentHash     string
	AttemptID          string
	PipelineStatusJSON string
}

// AdvanceSourceVersion allocates a database-wide monotonic source version and
// invalidates every publication of the changed data ID in the same transaction.
// The caller must have changed/inserted source metadata under its write fence;
// exact repeats must not call this. The counter survives source deletion.
func (p SQLPolicy) AdvanceSourceVersion(ctx context.Context, dataID string) (int64, error) {
	if p.readTx == nil || dataID == "" {
		return 0, ErrDocumentInvalid
	}
	var version int64
	err := p.readTx.QueryRowContext(ctx, p.rewrite(`UPDATE source_revision_counter SET value=value+1
 WHERE id=1 AND value>=1 AND value<9223372036854775807 RETURNING value`)).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("source revision counter unavailable or exhausted")
	}
	if err != nil {
		return 0, err
	}
	result, err := p.readTx.ExecContext(ctx, p.rewrite("UPDATE data SET source_revision=$1 WHERE id=$2"), version, dataID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrDocumentNotFound
	}
	if _, err := p.readTx.ExecContext(ctx, p.rewrite("DELETE FROM document_index_publications WHERE data_id=$1"), dataID); err != nil {
		return 0, err
	}
	if _, err := p.readTx.ExecContext(ctx, p.rewrite("UPDATE document_structured_artifacts SET state='retired',updated_at=CURRENT_TIMESTAMP WHERE data_id=$1 AND state='active'"), dataID); err != nil {
		return 0, err
	}
	return version, nil
}

// SourceVersion reads the current source behind an actual dataset association.
// Authorization is separate: publication verifies write permission under its fence.
func (p SQLPolicy) SourceVersion(ctx context.Context, ref DocumentRef) (int64, string, error) {
	if p.DB == nil && p.readTx == nil {
		return 0, "", ErrDocumentInvalid
	}
	var revision int64
	var hash string
	err := p.reader().QueryRowContext(ctx, p.rewrite(`SELECT d.source_revision,d.raw_content_hash FROM data d
 JOIN dataset_data dd ON dd.data_id=d.id WHERE dd.dataset_id=$1 AND dd.data_id=$2`), ref.DatasetID, ref.DataID).Scan(&revision, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrDocumentNotFound
	}
	if err != nil {
		return 0, "", err
	}
	canonical, ok := sourceSHA256(hash)
	if revision <= 0 || !ok {
		return 0, "", ErrDocumentVersionConflict
	}
	return revision, canonical, nil
}

// ValidateDatasetUploadTarget rejects an ID/name conflict before upload bytes
// leave the process. The ingest transaction repeats this check before commit.
func (p SQLPolicy) ValidateDatasetUploadTarget(ctx context.Context, datasetID, datasetName string) error {
	if p.readTx == nil || datasetID == "" || datasetName == "" {
		return ErrDocumentInvalid
	}
	var currentName string
	err := p.reader().QueryRowContext(ctx, p.rewrite("SELECT name FROM datasets WHERE id=$1"), datasetID).Scan(&currentName)
	if err == nil {
		if currentName != datasetName {
			return ErrDocumentVersionConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var reservedID string
	err = p.reader().QueryRowContext(ctx, p.rewrite("SELECT id FROM datasets WHERE name=$1"), datasetName).Scan(&reservedID)
	if err == nil {
		return ErrDocumentVersionConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

// ValidateSourceReplacement rejects known CAS and association conflicts while
// the caller holds a read fence. Publication repeats the same checks under its
// write transaction.
func (p SQLPolicy) ValidateSourceReplacement(ctx context.Context, ref DocumentRef, datasetName string, revision int64, hash string) error {
	if p.readTx == nil || ref.DatasetID == "" || ref.DataID == "" || datasetName == "" || revision <= 0 || len(hash) != 64 {
		return ErrDocumentInvalid
	}
	var currentName, currentHash string
	var currentRevision int64
	var inclusions int
	err := p.reader().QueryRowContext(ctx, p.rewrite(`SELECT ds.name,d.source_revision,d.raw_content_hash,
 (SELECT COUNT(*) FROM dataset_data all_dd WHERE all_dd.data_id=d.id)
 FROM datasets ds JOIN dataset_data dd ON dd.dataset_id=ds.id JOIN data d ON d.id=dd.data_id
 WHERE ds.id=$1 AND d.id=$2`), ref.DatasetID, ref.DataID).Scan(&currentName, &currentRevision, &currentHash, &inclusions)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDocumentNotFound
	}
	if err != nil {
		return err
	}
	if currentName != datasetName || currentRevision != revision || !strings.EqualFold(currentHash, hash) {
		return ErrDocumentVersionConflict
	}
	if inclusions != 1 {
		return ErrDocumentSharedMetadata
	}
	return nil
}
func sourceSHA256(hash string) (string, bool) {
	if len(hash) != 64 {
		return "", false
	}
	if _, err := hex.DecodeString(hash); err != nil {
		return "", false
	}
	return strings.ToLower(hash), true
}
func (p SQLPolicy) DocumentIndexLineage(ctx context.Context, ref DocumentRef, revision int64, collection, generation string) (DocumentPublicationLineage, bool, error) {
	var lineage DocumentPublicationLineage
	if p.DB == nil && p.readTx == nil {
		return lineage, false, ErrDocumentInvalid
	}
	if revision < 0 || collection == "" || generation == "" {
		return lineage, false, nil
	}
	var admin int
	err := p.reader().QueryRowContext(ctx, p.rewrite(`SELECT ip.sources_json,ip.requires_admin,ip.source_revision,ip.raw_content_hash
 FROM document_index_publications ip
 JOIN dataset_data dd ON dd.dataset_id=ip.dataset_id AND dd.data_id=ip.data_id
 JOIN data d ON d.id=dd.data_id
 LEFT JOIN document_resources r ON r.dataset_id=dd.dataset_id AND r.data_id=dd.data_id
 WHERE ip.dataset_id=$1 AND ip.data_id=$2 AND ip.content_revision=$3 AND ip.collection_name=$4 AND ip.generation=$5 AND ip.lineage_verified=1
 AND ip.source_revision>0 AND ip.source_revision=d.source_revision AND ip.raw_content_hash=LOWER(d.raw_content_hash)
 AND ((ip.content_revision=0 AND r.dataset_id IS NULL) OR (ip.content_revision>0 AND r.content_revision=ip.content_revision AND r.tombstoned=false))`), ref.DatasetID, ref.DataID, revision, collection, generation).Scan(&lineage.SourcesJSON, &admin, &lineage.SourceRevision, &lineage.RawContentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return lineage, false, nil
	}
	if err != nil {
		return lineage, false, err
	}
	canonical, ok := sourceSHA256(lineage.RawContentHash)
	if !ok || canonical != lineage.RawContentHash {
		return DocumentPublicationLineage{}, false, nil
	}
	lineage.RequiresAdmin = admin != 0
	return lineage, true, nil
}

// CommitDocumentIndex publishes only after every derivative write succeeds.
// The caller must hold a read fence and recheck its credential and all sources
// through that same transaction. This method consumes the fence transaction.
func (p SQLPolicy) CommitDocumentIndex(ctx context.Context, actor Actor, ref DocumentRef, revision int64, collection, generation string) error {
	return p.CommitDocumentIndexWithLineage(ctx, actor, ref, revision, collection, generation, DocumentPublicationLineage{SourcesJSON: "[]"})
}

func (p SQLPolicy) CommitDocumentIndexWithLineage(ctx context.Context, actor Actor, ref DocumentRef, revision int64, collection, generation string, lineage DocumentPublicationLineage) error {
	if p.readTx == nil {
		return ErrDocumentInvalid
	}
	// Compatibility is restricted to registered documents. Legacy processing
	// must carry the captured version/hash explicitly, never a late wildcard.
	if revision > 0 && lineage.SourceRevision == 0 && lineage.RawContentHash == "" {
		var err error
		lineage.SourceRevision, lineage.RawContentHash, err = p.SourceVersion(ctx, ref)
		if err != nil {
			return err
		}
	}
	return p.CommitDocumentIndexVersioned(ctx, actor, ref, revision, collection, generation, lineage)
}

// CommitDocumentIndexVersioned binds publication to the exact SQL source captured
// before processing. A content revision of zero denotes an unregistered legacy
// association, never a wildcard for a current registration.
func (p SQLPolicy) CommitDocumentIndexVersioned(ctx context.Context, actor Actor, ref DocumentRef, revision int64, collection, generation string, lineage DocumentPublicationLineage) error {
	hash, validHash := sourceSHA256(lineage.RawContentHash)
	if p.DB == nil || p.readTx == nil || collection == "" || generation == "" || revision < 0 || lineage.SourceRevision <= 0 || !validHash {
		return ErrDocumentInvalid
	}
	lineage.RawContentHash = hash
	var sources []json.RawMessage
	if len(lineage.SourcesJSON) > 128<<10 || json.Unmarshal([]byte(lineage.SourcesJSON), &sources) != nil || sources == nil || len(sources) > 256 {
		return ErrDocumentInvalid
	}
	admin := 0
	if lineage.RequiresAdmin {
		ok, err := p.IsSuperuser(ctx, actor.UserID)
		if err != nil {
			return err
		}
		if !ok || actor.TenantID != "" {
			return ErrDocumentForbidden
		}
		admin = 1
	}
	r, err := p.GetDocumentResource(ctx, ref)
	if revision == 0 {
		if err == nil {
			return ErrDocumentVersionConflict
		}
		if !errors.Is(err, ErrDocumentNotFound) {
			return err
		}
	} else {
		if err != nil {
			return err
		}
		if r.ContentRevision != revision {
			return ErrDocumentVersionConflict
		}
	}
	d, err := p.AuthorizeDocument(ctx, actor, ref, ActionWrite)
	if err != nil {
		return err
	}
	if !d.Allowed {
		return ErrDocumentForbidden
	}
	currentRevision, currentHash, err := p.SourceVersion(ctx, ref)
	if err != nil {
		return err
	}
	if currentRevision != lineage.SourceRevision || currentHash != lineage.RawContentHash {
		return ErrDocumentVersionConflict
	}
	if lineage.AttemptID != "" {
		var terminal struct {
			Status string `json:"status"`
		}
		if len(lineage.PipelineStatusJSON) > 16<<10 || json.Unmarshal([]byte(lineage.PipelineStatusJSON), &terminal) != nil || terminal.Status != "COMPLETED" {
			return ErrDocumentInvalid
		}
		result, updateErr := p.readTx.ExecContext(ctx, p.rewrite(`UPDATE document_pipeline_statuses SET
	 pipeline_state='COMPLETED',status_json=$1,updated_at=CURRENT_TIMESTAMP
	 WHERE dataset_id=$2 AND data_id=$3 AND collection_name=$4 AND source_revision=$5
	 AND LOWER(raw_content_hash)=$6 AND attempt_id=$7`), lineage.PipelineStatusJSON, ref.DatasetID, ref.DataID,
			collection, lineage.SourceRevision, lineage.RawContentHash, lineage.AttemptID)
		if updateErr != nil {
			return updateErr
		}
		if n, rowsErr := result.RowsAffected(); rowsErr != nil {
			return rowsErr
		} else if n != 1 {
			return ErrDocumentVersionConflict
		}
	}
	_, err = p.readTx.ExecContext(ctx, p.rewrite(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,sources_json,requires_admin,lineage_verified,source_revision,raw_content_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$9) ON CONFLICT(dataset_id,data_id,collection_name)
 DO UPDATE SET content_revision=EXCLUDED.content_revision,generation=EXCLUDED.generation,sources_json=EXCLUDED.sources_json,requires_admin=EXCLUDED.requires_admin,lineage_verified=1,source_revision=EXCLUDED.source_revision,raw_content_hash=EXCLUDED.raw_content_hash`), ref.DatasetID, ref.DataID, collection, revision, generation, lineage.SourcesJSON, admin, lineage.SourceRevision, lineage.RawContentHash)
	if err != nil {
		return err
	}
	return p.readTx.Commit()
}
