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

	log.Printf("PostgreSQL schema migration complete (11 tables)")
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

	// User settings (per-user JSON config)
	`CREATE TABLE IF NOT EXISTS user_settings (
		user_id TEXT PRIMARY KEY,
		settings JSONB NOT NULL DEFAULT '{}',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Dataset sharing / RBAC
	`CREATE TABLE IF NOT EXISTS dataset_shares (
		id TEXT PRIMARY KEY,
		dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		granted_by TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE(dataset_id, user_id)
	)`,

	// Notebooks
	`CREATE TABLE IF NOT EXISTS notebooks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT 'Untitled',
		owner_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Notebook cells
	`CREATE TABLE IF NOT EXISTS notebook_cells (
		id TEXT PRIMARY KEY,
		notebook_id TEXT NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
		cell_type TEXT NOT NULL DEFAULT 'code',
		source TEXT NOT NULL DEFAULT '',
		output TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Graph nodes (PostgreSQL mirror of Neo4j for SQL queries)
	`CREATE TABLE IF NOT EXISTS graph_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		properties JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Graph edges (PostgreSQL mirror of Neo4j)
	`CREATE TABLE IF NOT EXISTS graph_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',
		properties JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Indexes for common queries
	`CREATE INDEX IF NOT EXISTS idx_users_email ON users(email)`,
	`CREATE INDEX IF NOT EXISTS idx_datasets_name ON datasets(name)`,
	`CREATE INDEX IF NOT EXISTS idx_datasets_owner ON datasets(owner_id)`,
	`CREATE INDEX IF NOT EXISTS idx_data_content_hash ON data(content_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_dataset_data_dataset ON dataset_data(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_type ON graph_nodes(type)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_name ON graph_nodes(name)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_source ON graph_edges(source_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_target ON graph_edges(target_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_rel ON graph_edges(relationship_name)`,
	`CREATE INDEX IF NOT EXISTS idx_notebooks_owner ON notebooks(owner_id)`,
	`CREATE INDEX IF NOT EXISTS idx_notebook_cells_notebook ON notebook_cells(notebook_id)`,
}
