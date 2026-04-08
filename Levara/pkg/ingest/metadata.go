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

	// Ensure dataset exists (ignore conflict on id OR name)
	if datasetID != "" && datasetName != "" {
		_, _ = tx.ExecContext(ctx, q(`
			INSERT INTO datasets (id, name, owner_id, created_at, updated_at)
			VALUES ($1, $2, $3, NOW(), NOW())
			ON CONFLICT DO NOTHING
		`), datasetID, datasetName, ownerID)
		// Ignore error — dataset may already exist by name or id
	}

	// Batch insert Data records
	now := time.Now().UTC()
	written := 0

	for _, r := range results {
		if !r.AlreadyExists {
			tags := r.Tags
			if tags == "" {
				tags = "[]"
			}
			_, err = tx.ExecContext(ctx, q(`
				INSERT INTO data (id, name, extension, mime_type, raw_data_location,
					original_data_location, content_hash, raw_content_hash, owner_id,
					loader_engine, pipeline_status, tags, room, token_count, data_size, created_at, updated_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
				ON CONFLICT (id) DO UPDATE SET
					name = EXCLUDED.name,
					content_hash = EXCLUDED.content_hash,
					raw_data_location = EXCLUDED.raw_data_location,
					data_size = EXCLUDED.data_size,
					tags = EXCLUDED.tags,
					room = EXCLUDED.room,
				updated_at = EXCLUDED.updated_at
		`), r.ID, r.Name, r.Extension, r.MimeType, r.FilePath,
				r.FilePath, r.ContentHash, r.ContentHash, ownerID,
				"go_ingest", "{}", tags, r.Room, -1, r.FileSize, now, now)
			if err != nil {
				return written, fmt.Errorf("insert data %s: %w", r.ID, err)
			}
			written++
		}

		// Link to dataset — always, even for duplicates (same file in multiple datasets)
		if datasetID != "" {
			_, _ = tx.ExecContext(ctx, q(`
				INSERT INTO dataset_data (dataset_id, data_id)
				VALUES ($1, $2)
				ON CONFLICT DO NOTHING
			`), datasetID, r.ID)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}

	return written, nil
}

// BatchInsertValues builds a single INSERT with multiple VALUES for efficiency.
// For very large batches (1000+), this is faster than individual inserts.
func buildBatchInsert(table string, columns []string, count int) string {
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(table)
	b.WriteString(" (")
	b.WriteString(strings.Join(columns, ", "))
	b.WriteString(") VALUES ")

	numCols := len(columns)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("(")
		for j := 0; j < numCols; j++ {
			if j > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fmt.Sprintf("$%d", i*numCols+j+1))
		}
		b.WriteString(")")
	}
	return b.String()
}
