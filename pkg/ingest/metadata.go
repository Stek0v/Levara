package ingest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

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
	db    *sql.DB
	owned bool // true if we created the connection and should close it
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

// Close the connection (only if we own it).
func (w *MetadataWriter) Close() error {
	if w.owned {
		return w.db.Close()
	}
	return nil
}

// WriteMetadata writes Data records + dataset association in a single transaction.
// Replaces: get_dataset_data + identify + data lookup + ORM build + session.commit
func (w *MetadataWriter) WriteMetadata(ctx context.Context, results []Result, ownerID, datasetID, datasetName string) (int, error) {
	if len(results) == 0 {
		return 0, nil
	}

	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Ignore only an existing target ID. A conflicting name must not create
	// a successful-looking association to a dataset that was never inserted.
	if datasetID != "" && datasetName != "" {
		_, err = tx.ExecContext(ctx, q(`
			INSERT INTO datasets (id, name, owner_id, created_at, updated_at)
			VALUES ($1, $2, $3, NOW(), NOW())
			ON CONFLICT (id) DO NOTHING
		`), datasetID, datasetName, ownerID)
		if err != nil {
			return 0, fmt.Errorf("create dataset: %w", err)
		}
	}

	// Batch insert Data records
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
					pipeline_status = CASE WHEN data.raw_content_hash = EXCLUDED.raw_content_hash THEN data.pipeline_status ELSE '{}' END,
					token_count = CASE WHEN data.raw_content_hash = EXCLUDED.raw_content_hash THEN data.token_count ELSE -1 END,
				data_size = EXCLUDED.data_size,
				tags = EXCLUDED.tags,
				room = EXCLUDED.room,
			updated_at = EXCLUDED.updated_at
			WHERE data.owner_id = EXCLUDED.owner_id
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
			return 0, fmt.Errorf("data %s belongs to a different owner", r.ID)
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

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return written, nil
}
