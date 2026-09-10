package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/storage"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx via database/sql
)

// sqlQ translates PostgreSQL placeholders to SQLite when needed.
// Mirrors internal/http/sqlcompat.go Q() but avoids circular import.
var sqliteMode bool

func SetSQLiteMode(v bool) { sqliteMode = v }

func q(query string) string {
	if !sqliteMode {
		return query
	}
	// Replace $N with ?
	result := make([]byte, 0, len(query))
	for i := 0; i < len(query); i++ {
		if query[i] == '$' && i+1 < len(query) && query[i+1] >= '1' && query[i+1] <= '9' {
			result = append(result, '?')
			i++ // skip digit
			for i+1 < len(query) && query[i+1] >= '0' && query[i+1] <= '9' {
				i++ // skip multi-digit
			}
		} else {
			result = append(result, query[i])
		}
	}
	s := string(result)
	s = strings.ReplaceAll(s, "NOW()", "CURRENT_TIMESTAMP")
	s = strings.ReplaceAll(s, "now()", "CURRENT_TIMESTAMP")
	return s
}

// MetadataWriter writes ingestion metadata to PostgreSQL.
// Replaces Python's 6 separate SQLAlchemy round-trips with 1-2 batch INSERTs.
type MetadataWriter struct {
	db              *sql.DB
	owned           bool   // true if we created the connection and should close it
	storageIdentity string // configured stable non-local destination; set before serving requests
}

// NewMetadataWriter connects to PostgreSQL.
func NewMetadataWriter(dsn string) (*MetadataWriter, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres connect: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(60 * time.Second)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres ping: %w", err)
	}
	return &MetadataWriter{db: db, owned: true}, nil
}

// NewMetadataWriterFromDB wraps an existing connection pool (no Close needed).
func NewMetadataWriterFromDB(db *sql.DB) *MetadataWriter {
	return &MetadataWriter{db: db, owned: false}
}

// NewMetadataWriterForStorage binds authorized uploads and offline recovery to
// the configured destination. Plain local uploads derive their root from storagePath.
func NewMetadataWriterForStorage(db *sql.DB, backend storage.Storage) (*MetadataWriter, error) {
	w := NewMetadataWriterFromDB(db)
	if backend == nil {
		return w, nil
	}
	if _, local := backend.(*storage.LocalStorage); local {
		return w, nil
	}
	identity, err := storage.DestinationIdentity(backend)
	if err != nil {
		return nil, err
	}
	w.SetStorageIdentity(identity)
	return w, nil
}

// Close the connection (only if we own it).
func (w *MetadataWriter) Close() error {
	if w.owned {
		return w.db.Close()
	}
	return nil
}

// WriteMetadata is the compatibility entry point for trusted internal imports.
// New request uploads must use IngestAuthorized. Metadata-only callers use
// WriteMetadataAuthorized with verified credentials and already safe blob refs.
func (w *MetadataWriter) WriteMetadata(ctx context.Context, results []Result, ownerID, datasetID, datasetName string) (int, error) {
	if len(results) == 0 {
		return 0, nil
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	written, err := w.writeMetadataTx(ctx, tx, results, ownerID, datasetID, datasetName, nil, nil)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return written, nil
}

// WriteMetadataAuthorized rechecks live credentials and document policy in the
// publication transaction. Registered content/metadata changes require an
// explicit source-version/CAS operation; this path only preserves exact repeats.
func (w *MetadataWriter) WriteMetadataAuthorized(ctx context.Context, results []Result, actor access.MetadataActor, datasetID, datasetName string) (int, error) {
	if len(results) == 0 {
		return 0, nil
	}
	if w == nil || w.db == nil || datasetID == "" {
		return 0, access.ErrDocumentInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	policy := access.SQLPolicy{DB: w.db, Q: q}
	tx, locked, err := policy.BeginMetadataWrite(ctx, actor, sqliteMode)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	guard := metadataAccess{policy: locked, actor: actor}
	written, err := w.writeMetadataTx(ctx, tx, results, actor.UserID, datasetID, datasetName, &guard, nil)
	if err != nil {
		return 0, err
	}
	// SQL locks keep revocation state stable; wall-clock expiry can still pass
	// during a large batch. Recheck it immediately before publication commits.
	if !actor.TrustedLocal {
		c := actor.Credential
		if err := locked.RecheckCredential(ctx, actor.UserID, c.Kind, c.KeyID, actor.APIKeyPermissions, c.SessionID, c.Epoch, c.IssuedAt, c.ExpiresAt); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return written, nil
}

type metadataAccess struct {
	policy access.SQLPolicy
	actor  access.MetadataActor
}

func (g metadataAccess) checkDataset(ctx context.Context, tx *sql.Tx, datasetID, datasetName string) error {
	if datasetID == "" {
		return access.ErrDocumentInvalid
	}
	var name string
	err := tx.QueryRowContext(ctx, q("SELECT name FROM datasets WHERE id=$1"), datasetID).Scan(&name)
	switch {
	case err == nil:
		if datasetName != "" && datasetName != name {
			return fmt.Errorf("dataset ID/name conflict: %w", access.ErrDocumentVersionConflict)
		}
		if !g.actor.TrustedLocal {
			decision, err := g.policy.AuthorizeDataset(ctx, g.actor.Actor, datasetID, access.ActionWrite)
			if err != nil {
				return err
			}
			if !decision.Allowed {
				return access.ErrDocumentForbidden
			}
		}
	case errors.Is(err, sql.ErrNoRows):
		if strings.TrimSpace(datasetName) == "" {
			return access.ErrDocumentInvalid
		}
		var reserved string
		err = tx.QueryRowContext(ctx, q("SELECT id FROM datasets WHERE name=$1"), datasetName).Scan(&reserved)
		if err == nil {
			return fmt.Errorf("dataset name reserved: %w", access.ErrDocumentVersionConflict)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	default:
		return err
	}

	return nil
}

func (w *MetadataWriter) writeMetadataTx(ctx context.Context, tx *sql.Tx, results []Result, ownerID, datasetID, datasetName string, guard *metadataAccess, expected *SourceCAS) (int, error) {
	if guard != nil {
		if err := guard.checkDataset(ctx, tx, datasetID, datasetName); err != nil {
			return 0, err
		}
	}

	// Ignore only an existing target ID. A conflicting name must not create
	// a successful-looking association to a dataset that was never inserted.
	if datasetID != "" && datasetName != "" {
		_, err := tx.ExecContext(ctx, q(`
			INSERT INTO datasets (id, name, owner_id, created_at, updated_at)
			VALUES ($1, $2, $3, NOW(), NOW())
			ON CONFLICT (id) DO NOTHING
		`), datasetID, datasetName, ownerID)
		if err != nil {
			return 0, fmt.Errorf("create dataset: %w", err)
		}
	}

	// Insert each record and association inside the same transaction.
	now := time.Now().UTC()
	written := 0

	for _, r := range results {
		originalPath, originalHash, originalSize := r.OriginalFilePath, r.OriginalContentHash, r.OriginalFileSize
		if originalPath == "" {
			originalPath, originalHash, originalSize = r.FilePath, r.ContentHash, r.FileSize
		}
		// Filesystem dedup does not prove a committed SQL row exists.
		tags := r.Tags
		if tags == "" {
			tags = "[]"
		}
		skipUpdate := false
		sourceChanged := false
		if guard != nil {
			if r.ID == "" {
				return 0, access.ErrDocumentInvalid
			}
			registered, err := guard.policy.AuthorizeMetadataAliases(ctx, guard.actor.Actor, r.ID)
			if err != nil {
				return 0, err
			}
			var current metadataRow
			var sourceRevision int64
			err = tx.QueryRowContext(ctx, q(`SELECT COALESCE(owner_id,''),name,extension,mime_type,raw_data_location,original_data_location,content_hash,raw_content_hash,tags,room,data_size,source_revision FROM data WHERE id=$1`), r.ID).Scan(&current.owner, &current.name, &current.extension, &current.mime, &current.rawLocation, &current.originalLocation, &current.originalHash, &current.rawHash, &current.tags, &current.room, &current.size, &sourceRevision)
			if err == nil {
				if current.owner != ownerID {
					return 0, access.ErrDocumentForbidden
				}
				if expected != nil && (sourceRevision != expected.Revision || !strings.EqualFold(current.rawHash, expected.RawContentHash)) {
					return 0, access.ErrDocumentVersionConflict
				}
				desired := metadataRow{ownerID, r.Name, r.Extension, r.MimeType, r.FilePath, originalPath, originalHash, r.ContentHash, tags, r.Room, originalSize}
				skipUpdate = current == desired
				if registered && !skipUpdate && expected == nil {
					return 0, fmt.Errorf("registered metadata requires source-version CAS: %w", access.ErrDocumentVersionConflict)
				}
			} else if expected != nil && errors.Is(err, sql.ErrNoRows) {
				return 0, access.ErrDocumentNotFound
			} else if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			} else if registered {
				return 0, access.ErrDocumentNotFound
			}
		}
		if !skipUpdate {
			res, err := tx.ExecContext(ctx, q(`
			INSERT INTO data (id, name, extension, mime_type, raw_data_location,
				original_data_location, content_hash, raw_content_hash, owner_id,
				loader_engine, pipeline_status, tags, room, token_count, data_size, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
			ON CONFLICT (id) DO UPDATE SET
                name = EXCLUDED.name,
                extension = EXCLUDED.extension,
                mime_type = EXCLUDED.mime_type,
                content_hash = EXCLUDED.content_hash,
                raw_data_location = EXCLUDED.raw_data_location,
                original_data_location = EXCLUDED.original_data_location,
                raw_content_hash = EXCLUDED.raw_content_hash,
                pipeline_status = '{}', token_count = -1,
                data_size = EXCLUDED.data_size, tags = EXCLUDED.tags,
                room = EXCLUDED.room, updated_at = EXCLUDED.updated_at
            WHERE data.owner_id = EXCLUDED.owner_id AND (
                data.name IS DISTINCT FROM EXCLUDED.name
                OR data.extension IS DISTINCT FROM EXCLUDED.extension
                OR data.mime_type IS DISTINCT FROM EXCLUDED.mime_type
                OR data.raw_data_location IS DISTINCT FROM EXCLUDED.raw_data_location
                OR data.original_data_location IS DISTINCT FROM EXCLUDED.original_data_location
                OR data.content_hash IS DISTINCT FROM EXCLUDED.content_hash
                OR data.raw_content_hash IS DISTINCT FROM EXCLUDED.raw_content_hash
                OR data.data_size IS DISTINCT FROM EXCLUDED.data_size
                OR data.tags IS DISTINCT FROM EXCLUDED.tags
                OR data.room IS DISTINCT FROM EXCLUDED.room)
	`), r.ID, r.Name, r.Extension, r.MimeType, r.FilePath,
				originalPath, originalHash, r.ContentHash, ownerID,
				"go_ingest", "{}", tags, r.Room, -1, originalSize, now, now)
			if err != nil {
				return 0, fmt.Errorf("insert data %s: %w", r.ID, err)
			}
			n, err := res.RowsAffected()
			if err != nil {
				return 0, fmt.Errorf("count data %s: %w", r.ID, err)
			}
			if n == 0 {
				// Conditional UPSERT locks but does not rewrite an exact repeat.
				// Distinguish that success from the immutable-owner rejection.
				var currentOwner string
				if err := tx.QueryRowContext(ctx, q("SELECT owner_id FROM data WHERE id=$1"), r.ID).Scan(&currentOwner); err != nil {
					return 0, err
				}
				if currentOwner != ownerID {
					return 0, fmt.Errorf("data %s belongs to a different owner", r.ID)
				}
			} else {
				sourceChanged = true
			}
		}
		if skipUpdate && r.structuredArtifactChanged {
			sourceChanged = true
		}
		if sourceChanged {
			// Source and structured bytes share one version domain. The persistent
			// counter prevents stale CAS or an old data ID from becoming current.
			policy := access.SQLPolicy{DB: w.db, Q: q}
			if _, err := policy.WithReadTransaction(tx).AdvanceSourceVersion(ctx, r.ID); err != nil {
				return 0, err
			}
			if expected != nil {
				if _, err := tx.ExecContext(ctx, q("UPDATE document_resources SET content_revision=content_revision+1 WHERE data_id=$1"), r.ID); err != nil {
					return 0, err
				}
			}
		}
		written++

		// Link to dataset — always, even for duplicates (same file in multiple datasets)
		if datasetID != "" {
			_, err := tx.ExecContext(ctx, q(`
				INSERT INTO dataset_data (dataset_id, data_id)
				VALUES ($1, $2)
				ON CONFLICT DO NOTHING
			`), datasetID, r.ID)
			if err != nil {
				return 0, fmt.Errorf("link data %s: %w", r.ID, err)
			}
		}
	}

	return written, nil
}

// Only fields overwritten by metadata publication participate in idempotency;
// pipeline status, token count and timestamps must survive an exact reimport.
type metadataRow struct {
	owner, name, extension, mime, rawLocation, originalLocation, originalHash, rawHash, tags, room string
	size                                                                                           int64
}
