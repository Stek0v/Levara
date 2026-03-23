// schema.go — Database auto-migration for Cognevra tables.
// Supports both PostgreSQL and SQLite via DBProvider switch.
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
// Automatically selects PostgreSQL or SQLite DDL based on activeDBProvider.
func MigrateSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	stmts := schemaStatements
	if activeDBProvider == DBSQLite {
		stmts = schemaSQLiteStatements
	}

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt[:min(len(stmt), 80)])
		}
	}

	label := "PostgreSQL"
	if activeDBProvider == DBSQLite {
		label = "SQLite"
	}
	log.Printf("%s schema migration complete", label)
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

	// Tenants
	`CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		owner_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// User-Tenant junction
	`CREATE TABLE IF NOT EXISTS user_tenant (
		user_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, tenant_id)
	)`,

	// Roles
	`CREATE TABLE IF NOT EXISTS roles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// User-Role junction
	`CREATE TABLE IF NOT EXISTS user_role (
		user_id TEXT NOT NULL,
		role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, role_id)
	)`,

	// ACL (Access Control List)
	`CREATE TABLE IF NOT EXISTS acl (
		id TEXT PRIMARY KEY,
		principal_id TEXT NOT NULL,
		dataset_id TEXT NOT NULL,
		permission_type TEXT NOT NULL DEFAULT 'read',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE(principal_id, dataset_id, permission_type)
	)`,

	// Interactions (session tracking)
	`CREATE TABLE IF NOT EXISTS interactions (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		query TEXT NOT NULL DEFAULT '',
		response TEXT NOT NULL DEFAULT '',
		search_type TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Ontologies
	`CREATE TABLE IF NOT EXISTS ontologies (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		file_path TEXT NOT NULL DEFAULT '',
		format TEXT NOT NULL DEFAULT 'rdf/xml',
		classes_count INTEGER NOT NULL DEFAULT 0,
		individuals_count INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Migration: add columns if table already exists without them
	`ALTER TABLE ontologies ADD COLUMN IF NOT EXISTS classes_count INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE ontologies ADD COLUMN IF NOT EXISTS individuals_count INTEGER NOT NULL DEFAULT 0`,

	`CREATE INDEX IF NOT EXISTS idx_acl_principal ON acl(principal_id)`,
	`CREATE INDEX IF NOT EXISTS idx_acl_dataset ON acl(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_interactions_session ON interactions(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_user_tenant_user ON user_tenant(user_id)`,
}

// schemaSQLiteStatements — SQLite-compatible DDL.
// Differences from PostgreSQL:
//   - TIMESTAMPTZ -> TEXT
//   - DEFAULT NOW() -> DEFAULT CURRENT_TIMESTAMP
//   - JSONB -> TEXT
//   - BOOLEAN -> INTEGER (0/1)
//   - No REFERENCES (SQLite supports them but needs PRAGMA foreign_keys=ON)
//   - No ALTER TABLE ADD COLUMN IF NOT EXISTS (handled by ignoring errors)
var schemaSQLiteStatements = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA foreign_keys=ON`,

	`CREATE TABLE IF NOT EXISTS principals (
		id TEXT PRIMARY KEY,
		type TEXT NOT NULL DEFAULT 'user',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY REFERENCES principals(id),
		email TEXT NOT NULL UNIQUE,
		hashed_password TEXT NOT NULL,
		is_active INTEGER NOT NULL DEFAULT 1,
		is_superuser INTEGER NOT NULL DEFAULT 0,
		is_verified INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS datasets (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		owner_id TEXT,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(name)
	)`,

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
		data_size INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS dataset_data (
		dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
		data_id TEXT NOT NULL REFERENCES data(id) ON DELETE CASCADE,
		PRIMARY KEY (dataset_id, data_id)
	)`,

	`CREATE TABLE IF NOT EXISTS user_settings (
		user_id TEXT PRIMARY KEY,
		settings TEXT NOT NULL DEFAULT '{}',
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS dataset_shares (
		id TEXT PRIMARY KEY,
		dataset_id TEXT NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
		user_id TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		granted_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(dataset_id, user_id)
	)`,

	`CREATE TABLE IF NOT EXISTS notebooks (
		id TEXT PRIMARY KEY,
		title TEXT NOT NULL DEFAULT 'Untitled',
		owner_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS notebook_cells (
		id TEXT PRIMARY KEY,
		notebook_id TEXT NOT NULL REFERENCES notebooks(id) ON DELETE CASCADE,
		cell_type TEXT NOT NULL DEFAULT 'code',
		source TEXT NOT NULL DEFAULT '',
		output TEXT NOT NULL DEFAULT '',
		position INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS graph_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		properties TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS graph_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',
		properties TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

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

	`CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		owner_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS user_tenant (
		user_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, tenant_id)
	)`,

	`CREATE TABLE IF NOT EXISTS roles (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS user_role (
		user_id TEXT NOT NULL,
		role_id TEXT NOT NULL REFERENCES roles(id) ON DELETE CASCADE,
		PRIMARY KEY (user_id, role_id)
	)`,

	`CREATE TABLE IF NOT EXISTS acl (
		id TEXT PRIMARY KEY,
		principal_id TEXT NOT NULL,
		dataset_id TEXT NOT NULL,
		permission_type TEXT NOT NULL DEFAULT 'read',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(principal_id, dataset_id, permission_type)
	)`,

	`CREATE TABLE IF NOT EXISTS interactions (
		id TEXT PRIMARY KEY,
		session_id TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		query TEXT NOT NULL DEFAULT '',
		response TEXT NOT NULL DEFAULT '',
		search_type TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS ontologies (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		file_path TEXT NOT NULL DEFAULT '',
		format TEXT NOT NULL DEFAULT 'rdf/xml',
		classes_count INTEGER NOT NULL DEFAULT 0,
		individuals_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE INDEX IF NOT EXISTS idx_acl_principal ON acl(principal_id)`,
	`CREATE INDEX IF NOT EXISTS idx_acl_dataset ON acl(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_interactions_session ON interactions(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_user_tenant_user ON user_tenant(user_id)`,
}
