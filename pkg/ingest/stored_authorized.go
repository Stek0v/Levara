package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/storage"
)

const ingestAttemptPrefix = "ingest-authorized/"
const ingestPublicationLimit = 30 * time.Second

// SourceCAS identifies the exact stored source a replacement is allowed to
// supersede. Replacement is intentionally limited to one dataset inclusion.
type SourceCAS struct {
	Revision       int64
	RawContentHash string
}

// SetStorageIdentity binds non-local attempts to a stable destination fingerprint
// supplied by composition (backend, bucket/root and encryption identity, no secrets).
// Call once before serving requests; changing destination requires offline recovery
// with the old backend and identity first.
func (w *MetadataWriter) SetStorageIdentity(identity string) { w.storageIdentity = identity }

// EnsureIngestJournalSchema is additive and safe to run before accepting requests.
func EnsureIngestJournalSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ingest_pending_uploads (
 attempt_id TEXT PRIMARY KEY, destination TEXT NOT NULL, keys_json TEXT NOT NULL, expires_at BIGINT NOT NULL)`)
	return err
}

type authorizedPrep struct {
	result                    Result
	raw, original, structured []byte
}

func prepareAuthorized(items, originals []Item, actor access.MetadataActor, attempt string) ([]authorizedPrep, error) {
	if originals != nil && len(items) != len(originals) {
		return nil, access.ErrDocumentInvalid
	}
	out := make([]authorizedPrep, 0, len(items))
	ids := map[string]authorizedPrep{}
	for i, item := range items {
		item.OwnerID = actor.UserID
		raw, err := prepareItem(item, ".", map[string]bool{})
		if err != nil {
			return nil, err
		}
		original := raw
		if originals != nil {
			source := originals[i]
			source.OwnerID = actor.UserID
			original, err = prepareItem(source, ".", map[string]bool{})
			if err != nil {
				return nil, err
			}
		}
		if original.id == "" || filepath.Base(original.id) != original.id || original.id == "." || original.id == ".." || strings.ContainsAny(original.id, "/\\\x00") {
			return nil, access.ErrDocumentInvalid
		}
		p := authorizedPrep{raw: bytes.Clone(raw.content), original: bytes.Clone(original.content), structured: bytes.Clone(item.StructuredArtifact)}
		// Names, MIME and stable source identity come from the original; semantic
		// room/tags belong to the prepared input, as in the existing upload pipeline.
		p.result = Result{ID: original.id, Name: original.name, Extension: original.ext, MimeType: original.mimeType, Tags: raw.tagsJSON, Room: raw.room, ContentHash: raw.contentHash, FileSize: int64(len(raw.content)), OriginalContentHash: original.contentHash, OriginalFileSize: int64(len(original.content))}
		if prior, exists := ids[p.result.ID]; exists {
			priorResult := prior.result
			priorResult.FilePath = ""
			priorResult.OriginalFilePath = ""
			priorResult.StructuredArtifactID = ""
			priorResult.StructuredArtifactPath = ""
			priorResult.StructuredArtifactHash = ""
			priorResult.StructuredArtifactSize = 0
			if priorResult != p.result || !bytes.Equal(prior.raw, p.raw) || !bytes.Equal(prior.original, p.original) || !bytes.Equal(prior.structured, p.structured) {
				return nil, fmt.Errorf("conflicting document ID in batch: %w", access.ErrDocumentVersionConflict)
			}
			out = append(out, prior)
			continue
		}
		// Object paths never contain a supplied identifier, name or content hash.
		base := ingestAttemptPrefix + attempt + "/" + fmt.Sprint(i)
		p.result.FilePath = base + "-text"
		p.result.OriginalFilePath = p.result.FilePath
		if p.result.ContentHash != p.result.OriginalContentHash {
			p.result.OriginalFilePath = base + "-original"
		}
		if len(p.structured) > 0 {
			hash := sha256.Sum256(p.structured)
			p.result.StructuredArtifactID = uuid.NewString()
			p.result.StructuredArtifactPath = base + "-structured"
			p.result.StructuredArtifactHash = hex.EncodeToString(hash[:])
			p.result.StructuredArtifactSize = int64(len(p.structured))
		}
		ids[p.result.ID] = p
		out = append(out, p)
	}
	return out, nil
}

// plan checks the complete batch without modifying SQL. Exact repeats reuse
// committed locations; otherwise registered rows require the explicit CAS API.
func (g metadataAccess) plan(ctx context.Context, tx *sql.Tx, results []Result, datasetID, datasetName string, expected *SourceCAS) ([]Result, error) {
	if err := g.checkDataset(ctx, tx, datasetID, datasetName); err != nil {
		return nil, err
	}
	if expected != nil && len(results) != 1 {
		return nil, access.ErrDocumentInvalid
	}
	planned := append([]Result(nil), results...)
	for i, r := range planned {
		registered, err := g.policy.AuthorizeMetadataAliases(ctx, g.actor.Actor, r.ID)
		if err != nil {
			return nil, err
		}
		var current metadataRow
		var sourceRevision int64
		err = tx.QueryRowContext(ctx, q(`SELECT COALESCE(owner_id,''),name,extension,mime_type,raw_data_location,original_data_location,content_hash,raw_content_hash,tags,room,data_size,source_revision FROM data WHERE id=$1`), r.ID).Scan(&current.owner, &current.name, &current.extension, &current.mime, &current.rawLocation, &current.originalLocation, &current.originalHash, &current.rawHash, &current.tags, &current.room, &current.size, &sourceRevision)
		if errors.Is(err, sql.ErrNoRows) {
			if expected != nil {
				return nil, access.ErrDocumentNotFound
			}
			if registered {
				return nil, access.ErrDocumentNotFound
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if current.owner != g.actor.UserID {
			return nil, access.ErrDocumentForbidden
		}
		if expected != nil {
			if sourceRevision != expected.Revision || !strings.EqualFold(current.rawHash, expected.RawContentHash) {
				return nil, access.ErrDocumentVersionConflict
			}
			var aliases, target int
			if err := tx.QueryRowContext(ctx, q("SELECT COUNT(*) FROM dataset_data WHERE data_id=$1"), r.ID).Scan(&aliases); err != nil {
				return nil, err
			}
			if err := tx.QueryRowContext(ctx, q("SELECT COUNT(*) FROM dataset_data WHERE data_id=$1 AND dataset_id=$2"), r.ID, datasetID).Scan(&target); err != nil {
				return nil, err
			}
			if aliases != 1 {
				return nil, access.ErrDocumentSharedMetadata
			}
			if target != 1 {
				return nil, access.ErrDocumentNotFound
			}
		}
		desired := metadataRow{g.actor.UserID, r.Name, r.Extension, r.MimeType, current.rawLocation, current.originalLocation, r.OriginalContentHash, r.ContentHash, r.Tags, r.Room, r.OriginalFileSize}
		if current == desired && current.rawLocation != "" && current.originalLocation != "" {
			planned[i].FilePath = current.rawLocation
			planned[i].OriginalFilePath = current.originalLocation
			planned[i].AlreadyExists = true
			if r.StructuredArtifactPath != "" {
				err := tx.QueryRowContext(ctx, q(`SELECT id,storage_location,artifact_sha256,byte_size
					FROM document_structured_artifacts WHERE data_id=$1 AND source_revision=$2
					AND LOWER(raw_content_hash)=LOWER($3) AND artifact_sha256=$4 AND state='active'
					ORDER BY created_at,id LIMIT 1`), r.ID, sourceRevision, current.rawHash, r.StructuredArtifactHash).
					Scan(&planned[i].StructuredArtifactID, &planned[i].StructuredArtifactPath, &planned[i].StructuredArtifactHash, &planned[i].StructuredArtifactSize)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return nil, err
				}
				if errors.Is(err, sql.ErrNoRows) {
					planned[i].structuredArtifactChanged = true
					if registered && expected == nil {
						return nil, fmt.Errorf("registered structured artifact requires source-version CAS: %w", access.ErrDocumentVersionConflict)
					}
				}
			}
		} else if registered && expected == nil {
			return nil, fmt.Errorf("registered metadata requires source-version CAS: %w", access.ErrDocumentVersionConflict)
		}
	}
	return planned, nil
}

// IngestAuthorized validates the entire batch and live policy before any Save.
// A coarse SQL authorization fence covers cooperative storage calls and final
// metadata publication, with a 30s ceiling. A Storage ignoring cancellation may
// outlive that ceiling: database/sql can roll back the fence, but immutable
// private attempt keys remain unpublished. We wait for Save to actually return;
// crash/uncertain outcomes are recovered only offline under the server writer
// lease, never by an online expiration worker.
func (w *MetadataWriter) IngestAuthorized(ctx context.Context, items, originals []Item, storagePath string, backend storage.Storage, actor access.MetadataActor, datasetID, datasetName string) ([]Result, int, error) {
	return w.ingestAuthorized(ctx, items, originals, storagePath, backend, actor, datasetID, datasetName, nil)
}

// ReplaceAuthorized replaces one existing source only when revision and hash
// still match. It reuses the normal private-attempt journal and cleanup path.
func (w *MetadataWriter) ReplaceAuthorized(ctx context.Context, items, originals []Item, storagePath string, backend storage.Storage, actor access.MetadataActor, datasetID, datasetName string, expected SourceCAS) ([]Result, int, error) {
	if len(items) != 1 || (originals != nil && len(originals) != 1) || expected.Revision <= 0 || len(expected.RawContentHash) != 64 {
		return nil, 0, access.ErrDocumentInvalid
	}
	if _, err := hex.DecodeString(expected.RawContentHash); err != nil {
		return nil, 0, access.ErrDocumentInvalid
	}
	expected.RawContentHash = strings.ToLower(expected.RawContentHash)
	return w.ingestAuthorized(ctx, items, originals, storagePath, backend, actor, datasetID, datasetName, &expected)
}

func (w *MetadataWriter) ingestAuthorized(ctx context.Context, items, originals []Item, storagePath string, backend storage.Storage, actor access.MetadataActor, datasetID, datasetName string, expected *SourceCAS) ([]Result, int, error) {
	if w == nil || w.db == nil || datasetID == "" {
		return nil, 0, access.ErrDocumentInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, ingestPublicationLimit)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	attempt := uuid.NewString()
	preps, err := prepareAuthorized(items, originals, actor, attempt)
	if err != nil {
		return nil, 0, err
	}
	if len(preps) == 0 {
		return []Result{}, 0, nil
	}
	dest, err := w.ingestDestination(storagePath, backend)
	if err != nil {
		return nil, 0, err
	}
	results := make([]Result, len(preps))
	for i, p := range preps {
		results[i] = p.result
		results[i].FilePath = dest.location(p.result.FilePath)
		results[i].OriginalFilePath = dest.location(p.result.OriginalFilePath)
		if p.result.StructuredArtifactPath != "" {
			results[i].StructuredArtifactPath = dest.location(p.result.StructuredArtifactPath)
		}
	}
	policy := access.SQLPolicy{DB: w.db, Q: q}
	tx, locked, err := policy.BeginMetadataWrite(ctx, actor, sqliteMode)
	if err != nil {
		return nil, 0, err
	}
	guard := metadataAccess{policy: locked, actor: actor}
	_, err = guard.plan(ctx, tx, results, datasetID, datasetName, expected)
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	keys := attemptLocations(results)
	encoded, _ := json.Marshal(keys)
	deadline, _ := ctx.Deadline()
	_, err = tx.ExecContext(ctx, q(`INSERT INTO ingest_pending_uploads(attempt_id,destination,keys_json,expires_at) VALUES($1,$2,$3,$4)`), attempt, dest.identity, string(encoded), deadline.UnixNano())
	if err != nil {
		tx.Rollback()
		return nil, 0, err
	}
	if err = tx.Commit(); err != nil {
		return nil, 0, fmt.Errorf("reserve ingest attempt (outcome uncertain): %w", err)
	}
	// Reservation is durable before bytes. Failure cleanup only follows returned
	// backend operations; an uncertain COMMIT deliberately keeps the journal.
	published, writeErr, uncertain := w.publishAttempt(ctx, attempt, dest, preps, results, actor, datasetID, datasetName, expected)
	if writeErr == nil {
		return published, len(published), nil
	}
	if uncertain {
		return nil, 0, writeErr
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), ingestPublicationLimit)
	defer cleanupCancel()
	if err := w.cleanAttempt(cleanupCtx, attempt, dest, keys); err != nil {
		return nil, 0, errors.Join(writeErr, fmt.Errorf("ingest cleanup pending: %w", err))
	}
	return nil, 0, writeErr
}

func attemptLocations(results []Result) []string {
	seen := map[string]bool{}
	var keys []string
	for _, r := range results {
		for _, location := range []string{r.FilePath, r.OriginalFilePath, r.StructuredArtifactPath} {
			if location != "" && !seen[location] {
				seen[location] = true
				keys = append(keys, location)
			}
		}
	}
	return keys
}

func (w *MetadataWriter) publishAttempt(ctx context.Context, attempt string, dest ingestDestination, preps []authorizedPrep, templates []Result, actor access.MetadataActor, datasetID, datasetName string, expected *SourceCAS) ([]Result, error, bool) {
	policy := access.SQLPolicy{DB: w.db, Q: q}
	tx, locked, err := policy.BeginMetadataWrite(ctx, actor, sqliteMode)
	if err != nil {
		return nil, err, false
	}
	defer tx.Rollback()
	var expiry int64
	var identity string
	if err := tx.QueryRowContext(ctx, q(`SELECT expires_at,destination FROM ingest_pending_uploads WHERE attempt_id=$1`), attempt).Scan(&expiry, &identity); err != nil {
		return nil, err, false
	}
	if identity != dest.identity || time.Now().UnixNano() >= expiry {
		return nil, errors.New("ingest attempt expired or destination changed"), false
	}
	guard := metadataAccess{policy: locked, actor: actor}
	results, err := guard.plan(ctx, tx, templates, datasetID, datasetName, expected)
	if err != nil {
		return nil, err, false
	}
	saved := map[string]bool{}
	for i, r := range results {
		paths := []string{r.FilePath, r.OriginalFilePath, r.StructuredArtifactPath}
		ownPaths := []string{templates[i].FilePath, templates[i].OriginalFilePath, templates[i].StructuredArtifactPath}
		payloads := [][]byte{preps[i].raw, preps[i].original, preps[i].structured}
		for j, path := range paths {
			own := ownPaths[j]
			if path == "" {
				continue
			}
			if path != own || saved[path] {
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err, false
			}
			if time.Now().UnixNano() >= expiry {
				return nil, errors.New("ingest attempt expired"), false
			}
			if err := dest.save(ctx, path, payloads[j]); err != nil {
				return nil, fmt.Errorf("save ingest object: %w", err), false
			}
			saved[path] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err, false
	}
	if _, err := w.writeMetadataTx(ctx, tx, results, actor.UserID, datasetID, datasetName, &guard, expected); err != nil {
		return nil, err, false
	}
	publishedArtifacts := map[string]bool{}
	for i := range results {
		var rawHash string
		if err := tx.QueryRowContext(ctx, q("SELECT source_revision,LOWER(raw_content_hash) FROM data WHERE id=$1"), results[i].ID).Scan(&results[i].SourceRevision, &rawHash); err != nil {
			return nil, err, false
		}
		if results[i].StructuredArtifactPath == "" || results[i].StructuredArtifactPath != templates[i].StructuredArtifactPath || publishedArtifacts[results[i].StructuredArtifactPath] {
			continue
		}
		publishedArtifacts[results[i].StructuredArtifactPath] = true
		if _, err := tx.ExecContext(ctx, q("UPDATE document_structured_artifacts SET state='retired',updated_at=CURRENT_TIMESTAMP WHERE data_id=$1 AND state='active'"), results[i].ID); err != nil {
			return nil, err, false
		}
		if _, err := tx.ExecContext(ctx, q(`INSERT INTO document_structured_artifacts
			(id,data_id,source_revision,raw_content_hash,artifact_sha256,byte_size,storage_location,destination,state,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'active',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`),
			results[i].StructuredArtifactID, results[i].ID, results[i].SourceRevision, rawHash,
			results[i].StructuredArtifactHash, results[i].StructuredArtifactSize, results[i].StructuredArtifactPath, dest.identity); err != nil {
			return nil, err, false
		}
	}
	if !actor.TrustedLocal {
		c := actor.Credential
		if err := locked.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return nil, err, false
		}
	}
	if _, err := tx.ExecContext(ctx, q(`DELETE FROM ingest_pending_uploads WHERE attempt_id=$1`), attempt); err != nil {
		return nil, err, false
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("publish ingest attempt (outcome uncertain): %w", err), true
	}
	return results, nil, false
}

// cleanAttempt is called only after this process's Save has returned, or during
// offline recovery with the exclusive writer lease. It never deletes SQL refs.
func (w *MetadataWriter) cleanAttempt(ctx context.Context, attempt string, dest ingestDestination, keys []string) error {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, path := range keys {
		if !dest.owns(attempt, path) {
			return errors.New("journal contains foreign path")
		}
		var referenced bool
		if err := tx.QueryRowContext(ctx, q(`SELECT EXISTS(SELECT 1 FROM data WHERE raw_data_location=$1 OR original_data_location=$2)`), path, path).Scan(&referenced); err != nil {
			return err
		}
		if !referenced && strings.HasSuffix(path, "-structured") {
			if err := tx.QueryRowContext(ctx, q(`SELECT EXISTS(SELECT 1 FROM document_structured_artifacts WHERE storage_location=$1)`), path).Scan(&referenced); err != nil {
				return err
			}
		}
		if referenced {
			return errors.New("pending object is referenced by committed metadata")
		}
	}
	for _, path := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := dest.remove(ctx, path); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, q(`DELETE FROM ingest_pending_uploads WHERE attempt_id=$1 AND destination=$2`), attempt, dest.identity); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupRetiredStructuredArtifacts removes only rows whose SQL state already
// closed access. Failures leave the row for an idempotent retry.
func (w *MetadataWriter) CleanupRetiredStructuredArtifacts(ctx context.Context, dataID, storagePath string, backend storage.Storage) error {
	if w == nil || w.db == nil {
		return access.ErrDocumentInvalid
	}
	dest, err := w.ingestDestination(storagePath, backend)
	if err != nil {
		return err
	}
	query := "SELECT id,storage_location,destination FROM document_structured_artifacts WHERE state='retired'"
	args := []any{}
	if dataID != "" {
		query += " AND data_id=$1"
		args = append(args, dataID)
	}
	rows, err := w.db.QueryContext(ctx, q(query), args...)
	if err != nil {
		return err
	}
	type retiredArtifact struct{ id, location, destination string }
	var artifacts []retiredArtifact
	for rows.Next() {
		var a retiredArtifact
		if err := rows.Scan(&a.id, &a.location, &a.destination); err != nil {
			rows.Close()
			return err
		}
		artifacts = append(artifacts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var cleanupErr error
	for _, a := range artifacts {
		if err := ctx.Err(); err != nil {
			return errors.Join(cleanupErr, err)
		}
		if a.destination != dest.identity || !dest.ownsStructured(a.location) {
			cleanupErr = errors.Join(cleanupErr, errors.New("structured artifact inventory contains foreign path"))
			continue
		}
		if err := dest.remove(ctx, a.location); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if _, err := w.db.ExecContext(ctx, q("DELETE FROM document_structured_artifacts WHERE id=$1 AND state='retired'"), a.id); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

// RecoverPendingIngestOffline MUST run before serving traffic, while holding the
// exclusive server writer lease (also excluding another server process). Expiry
// is not proof that a non-cooperative Storage has stopped: do not call online.
// Use the same backend destination/encryption identity as the original attempt.
func (w *MetadataWriter) RecoverPendingIngestOffline(ctx context.Context, storagePath string, backend storage.Storage) (int, error) {
	if w == nil || w.db == nil {
		return 0, access.ErrDocumentInvalid
	}
	dest, err := w.ingestDestination(storagePath, backend)
	if err != nil {
		return 0, err
	}
	rows, err := w.db.QueryContext(ctx, q(`SELECT attempt_id,destination,keys_json FROM ingest_pending_uploads WHERE expires_at<=$1 ORDER BY attempt_id`), time.Now().UnixNano())
	if err != nil {
		return 0, err
	}
	type pending struct{ id, identity, keys string }
	var attempts []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.identity, &p.keys); err != nil {
			rows.Close()
			return 0, err
		}
		attempts = append(attempts, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	// Validate every destination before deleting anything, even when an old
	// configuration left several pending attempts in this database.
	decoded := make([][]string, len(attempts))
	for i, p := range attempts {
		if p.identity != dest.identity {
			return 0, errors.New("pending ingest destination mismatch; recover with original configuration")
		}
		id, err := uuid.Parse(p.id)
		if err != nil || id.String() != p.id {
			return 0, errors.New("invalid pending ingest attempt")
		}
		if err := json.Unmarshal([]byte(p.keys), &decoded[i]); err != nil {
			return 0, err
		}
		if len(decoded[i]) == 0 {
			return 0, errors.New("pending ingest keys missing")
		}
		for _, path := range decoded[i] {
			if !dest.owns(p.id, path) {
				return 0, errors.New("journal contains foreign path")
			}
		}
	}
	n := 0
	for i, p := range attempts {
		if err := w.cleanAttempt(ctx, p.id, dest, decoded[i]); err != nil {
			return n, err
		}
		n++
	}

	return n, nil
}
