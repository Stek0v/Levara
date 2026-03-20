// schema.go — PostgreSQL auto-migration for Cognevra tables.
// Creates tables if they don't exist on server startup.
package http

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"
)

// MigrateSchema creates all required tables if they don't exist.
// Safe to call multiple times (idempotent via IF NOT EXISTS).
func MigrateSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	for _, stmt := range schemaStatements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt[:min(len(stmt), 80)])
		}
	}

	log.Printf("PostgreSQL schema migration complete (5 tables)")
	return nil
}

var schemaStatements = []string{
	// Principals: base entity for users/groups (Cognee FK requirement)
	`CREATE TABLE IF NOT EXISTS principals (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT 'user',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Users: authentication
	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY REFERENCES principals(id),
		email TEXT NOT NULL UNIQUE,
		hashed_password TEXT NOT NULL,
		is_active BOOLEAN NOT NULL DEFAULT true,
		is_superuser BOOLEAN NOT NULL DEFAULT false,
		is_verified BOOLEAN NOT NULL DEFAULT false,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Datasets: groups of uploaded data
	`CREATE TABLE IF NOT EXISTS datasets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		owner_id TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE(name)
	)`,

	// Data: individual uploaded files/texts
	`CREATE TABLE IF NOT EXISTS data (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		extension TEXT NOT NULL DEFAULT '',
		mime_type TEXT NOT NULL DEFAULT '',
		raw_data_location TEXT NOT NULL DEFAULT '',
		original_data_location TEXT NOT NULL DEFAULT '',
		content_hash TEXT NOT NULL DEFAULT '',
		raw_content_hash TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		loader_engine TEXT NOT NULL DEFAULT 'go_ingest',
		pipeline_status TEXT NOT NULL DEFAULT '{}',
		token_count INTEGER NOT NULL DEFAULT -1,
		data_size BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Dataset-Data junction table
	`CREATE TABLE IF NOT EXISTS dataset_data (
		dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
		data_id TEXT NOT NULL REFERENCES data(id) ON DELETE CASCADE,
		PRIMARY KEY (dataset_id, data_id)
	)`,

	// Indexes for common queries
	`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,
	`CREATE INDEX IF NOT EXISTS idx_datasets_name ON datasets(name)`,
	`CREATE INDEX IF NOT EXISTS idx_datasets_owner ON datasets(owner_id)`,
	`CREATE INDEX IF NOT EXISTS idx_data_content_hash ON data(content_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_dataset_data_dataset ON dataset_data(dataset_id)`,
}
