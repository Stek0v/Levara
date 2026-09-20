// schema.go — Database auto-migration for Levara tables.
// Supports both PostgreSQL and SQLite via DBProvider switch.
// Creates tables if they don't exist on server startup.
package http

import (
	"context"
	"database/sql"
	"fmt"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/ingest"
	"log"
	"strings"
)

// MigrateSchema creates all required tables if they don't exist.
// Safe to call multiple times (idempotent via IF NOT EXISTS).
// Automatically selects PostgreSQL or SQLite DDL based on activeDBProvider.
func MigrateSchema(db *sql.DB) error {
	if db == nil {
		return nil
	}

	// Use background context (no timeout) — canceling a context closes the
	// DB connection, which kills the entire pool for SQLite (MaxOpenConns=1).
	ctx := context.Background()

	stmts := schemaStatements
	if activeDBProvider == DBSQLite {
		stmts = schemaSQLiteStatements
	}
	// Chat-import tables are dialect-neutral and live in their package as
	// the single DDL source (also used by standalone EnsureSchema).
	stmts = append(stmts, chatimport.SchemaStatements...)

	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			// ALTER TABLE may fail on SQLite if column already exists — safe to ignore
			if strings.Contains(stmt, "ALTER TABLE") {
				continue
			}
			return fmt.Errorf("migrate: %w\nSQL: %s", err, stmt[:min(len(stmt), 80)])
		}
	}

	if err := accesspkg.EnsureDirectoryGroupSchema(ctx, db, Q); err != nil {
		return fmt.Errorf("migrate managed directory groups: %w", err)
	}
	if err := ingest.EnsureIngestJournalSchema(ctx, db); err != nil {
		return fmt.Errorf("migrate ingest journal: %w", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS source_revision_counter(id INTEGER PRIMARY KEY CHECK(id=1),value BIGINT NOT NULL CHECK(value>=1))`); err != nil {
		return fmt.Errorf("migrate source revision counter: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO source_revision_counter(id,value) VALUES(1,COALESCE((SELECT MAX(source_revision) FROM data),1))
 ON CONFLICT(id) DO UPDATE SET value=CASE WHEN source_revision_counter.value<EXCLUDED.value THEN EXCLUDED.value ELSE source_revision_counter.value END`); err != nil {
		return fmt.Errorf("initialize source revision counter: %w", err)
	}
	label := "PostgreSQL"
	if activeDBProvider == DBSQLite {
		label = "SQLite"
	}
	log.Printf("%s schema migration complete", label)
	return nil
}

var schemaStatements = []string{
	// Principals: base entity for users/groups (Levara FK requirement)
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
		source_revision BIGINT NOT NULL DEFAULT 1 CHECK(source_revision>0),
		owner_id TEXT NOT NULL DEFAULT '',
		loader_engine TEXT NOT NULL DEFAULT 'go_ingest',
		pipeline_status TEXT NOT NULL DEFAULT '{}',
		tags TEXT NOT NULL DEFAULT '[]',
		room TEXT NOT NULL DEFAULT '',
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
	// dataset_id scopes nodes to a tenant/project; '' means legacy/global.
	`CREATE TABLE IF NOT EXISTS graph_nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		properties JSONB NOT NULL DEFAULT '{}',
		dataset_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		collection_id TEXT NOT NULL DEFAULT '',
		document_id TEXT NOT NULL DEFAULT '',
		section_path TEXT NOT NULL DEFAULT '',
		chunk_index INTEGER NOT NULL DEFAULT -1,
		prev_chunk_id TEXT NOT NULL DEFAULT '',
		next_chunk_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Graph edges (PostgreSQL mirror of Neo4j)
	// Temporal validity: valid_from/valid_until track when an edge was true.
	// superseded_by stores edge ID that replaced this one (for non-overlapping
	// exclusive relationships like assigned_to, role_is, status_is).
	// dataset_id scopes edges to a tenant/project; '' means legacy/global.
	`CREATE TABLE IF NOT EXISTS graph_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',
		properties JSONB NOT NULL DEFAULT '{}',
		valid_from TIMESTAMPTZ,
		valid_until TIMESTAMPTZ,
		superseded_by TEXT NOT NULL DEFAULT '',
		confidence REAL NOT NULL DEFAULT 1.0,
		dataset_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		collection_id TEXT NOT NULL DEFAULT '',
		document_id TEXT NOT NULL DEFAULT '',
		section_path TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// DCD route taxonomy. These tables prepare the Domain -> Collection ->
	// Document hierarchy used by route narrowing before VSA retrieval.
	`CREATE TABLE IF NOT EXISTS knowledge_domains (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json JSONB NOT NULL DEFAULT '[]',
		embedding_ref TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS knowledge_collections (
		id TEXT PRIMARY KEY,
		domain_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json JSONB NOT NULL DEFAULT '[]',
		embedding_ref TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS knowledge_documents (
		id TEXT PRIMARY KEY,
		collection_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		source_document_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json JSONB NOT NULL DEFAULT '[]',
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
	`CREATE INDEX IF NOT EXISTS idx_knowledge_domains_scope ON knowledge_domains(owner_id, team_id, dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_domains_name ON knowledge_domains(dataset_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_collections_scope ON knowledge_collections(owner_id, team_id, dataset_id, domain_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_collections_name ON knowledge_collections(domain_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_scope ON knowledge_documents(owner_id, team_id, dataset_id, domain_id, collection_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_title ON knowledge_documents(collection_id, title)`,
	`CREATE TABLE IF NOT EXISTS vsa_fact_shards (
		id TEXT PRIMARY KEY,
		dataset_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		shard_index INTEGER NOT NULL DEFAULT 0,
		dim INTEGER NOT NULL DEFAULT 1024,
		fact_count INTEGER NOT NULL DEFAULT 0,
		vector_json JSONB NOT NULL DEFAULT '[]',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_vsa_shards_predicate ON vsa_fact_shards(dataset_id, predicate)`,
	`CREATE TABLE IF NOT EXISTS vsa_fact_members (
		shard_id TEXT NOT NULL,
		edge_id TEXT NOT NULL,
		source_id TEXT NOT NULL DEFAULT '',
		target_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (shard_id, edge_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_vsa_members_lookup ON vsa_fact_members(dataset_id, predicate, source_id)`,
	`CREATE TABLE IF NOT EXISTS graph_predicate_synonyms (
		dataset_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		synonym TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'generated',
		weight REAL NOT NULL DEFAULT 50,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (dataset_id, predicate, synonym, source)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_predicate_synonyms_lookup ON graph_predicate_synonyms(dataset_id, synonym)`,
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

	// A session anchor fixes owner/tenant even after turns are deleted. Each
	// turn's provenance is committed atomically with its interaction row.
	`CREATE TABLE IF NOT EXISTS interaction_provenance (
		id TEXT PRIMARY KEY,
		interaction_id TEXT UNIQUE REFERENCES interactions(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL CHECK (session_id <> ''),
		owner_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL CHECK (kind IN ('session', 'server', 'client')),
		sources TEXT NOT NULL DEFAULT '[]',
		requires_admin INTEGER NOT NULL DEFAULT 0 CHECK (requires_admin IN (0,1)),
		created_at TEXT NOT NULL,
		CHECK ((kind = 'session' AND interaction_id IS NULL) OR (kind IN ('server', 'client') AND interaction_id IS NOT NULL))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_interaction_provenance_session ON interaction_provenance(session_id) WHERE kind = 'session'`,
	`CREATE INDEX IF NOT EXISTS idx_interaction_provenance_owner ON interaction_provenance(owner_id, tenant_id, created_at)`,

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
	`ALTER TABLE data ADD COLUMN IF NOT EXISTS tags TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE datasets ADD COLUMN IF NOT EXISTS github_repo TEXT NOT NULL DEFAULT ''`,

	// Memories (project/user memory store)
	// room    — narrow topic within collection (auth, deploy, mcp, ocr-bench)
	// hall    — controlled memory genre: fact|event|decision|preference|advice|discovery
	// is_pinned/pin_priority — wake_up critical-facts mechanism
	// superseded_by      — id of the record that replaced this one (''=active)
	// valid_until        — when this record stopped being current (NULL=open-ended)
	// consolidated_from  — JSON array of source ids for a generated semantic record (''=otherwise)
	// consolidation_run_id — run id stamped on generated records and superseded sources
	// tier               — raw|consolidated|semantic
	`CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'project',
		owner_id TEXT NOT NULL DEFAULT '',
		collection_name TEXT NOT NULL DEFAULT '',
		room TEXT NOT NULL DEFAULT '',
		hall TEXT NOT NULL DEFAULT '',
		is_pinned BOOLEAN NOT NULL DEFAULT FALSE,
		pin_priority INTEGER NOT NULL DEFAULT 0,
		superseded_by TEXT NOT NULL DEFAULT '',
		valid_until TIMESTAMPTZ,
		consolidated_from TEXT NOT NULL DEFAULT '',
		consolidation_run_id TEXT NOT NULL DEFAULT '',
		tier TEXT NOT NULL DEFAULT 'raw',
		source_task_id TEXT NOT NULL DEFAULT '',
		source_receipt_ids TEXT NOT NULL DEFAULT '[]',
		verification_status TEXT NOT NULL DEFAULT '',
		supersedes_memory_id TEXT NOT NULL DEFAULT '',
		supersession_reason TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	// Upsert identity is (key, owner_id, collection_name) so the same key
	// can exist in different pinned contexts without clobbering (P1 isolation).
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_key_owner_coll ON memories(key, owner_id, collection_name)`,
	`CREATE TABLE IF NOT EXISTS memory_commits (
		id TEXT PRIMARY KEY, owner_id TEXT NOT NULL DEFAULT '', collection_name TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT NOT NULL, status TEXT NOT NULL, request_digest TEXT NOT NULL,
		plan_digest TEXT NOT NULL, plan_json TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), expires_at TIMESTAMPTZ NOT NULL, applied_at TIMESTAMPTZ,
		UNIQUE(owner_id, collection_name, idempotency_key)
	)`,
	// Drop legacy index from deployments that predated collection-scoped upsert.
	`DROP INDEX IF EXISTS idx_memories_key_owner`,
	// Migrations for existing PG databases. ALTER TABLE must run BEFORE the
	// dependent CREATE INDEX statements below — otherwise on an old DB the
	// indexes reference columns that haven't been added yet, the migration
	// errors out, and the ALTERs are never reached.
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS room TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS hall TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS is_pinned BOOLEAN NOT NULL DEFAULT FALSE`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS pin_priority INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS superseded_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS consolidated_from TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS consolidation_run_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'raw'`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS source_task_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS source_receipt_ids TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS verification_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS supersedes_memory_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN IF NOT EXISTS supersession_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE data ADD COLUMN IF NOT EXISTS room TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE data ADD COLUMN IF NOT EXISTS source_revision BIGINT NOT NULL DEFAULT 1 CHECK(source_revision>0)`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS valid_from TIMESTAMPTZ`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS valid_until TIMESTAMPTZ`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS superseded_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS confidence REAL NOT NULL DEFAULT 1.0`,
	// Tenant/project scoping — promotes dataset_id from JSON property to a
	// first-class column so query_entity and graph reads can filter without
	// JSON extraction. Empty string preserves legacy/global rows.
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS dataset_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS dataset_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS collection_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS document_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS section_path TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS chunk_index INTEGER NOT NULL DEFAULT -1`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS prev_chunk_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN IF NOT EXISTS next_chunk_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS domain_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS collection_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS document_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN IF NOT EXISTS section_path TEXT NOT NULL DEFAULT ''`,
	// Indexes that depend on the columns added by the ALTERs above.
	`CREATE INDEX IF NOT EXISTS idx_memories_room ON memories(collection_name, room)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_hall ON memories(hall)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_pinned ON memories(is_pinned, pin_priority DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_source_task ON memories(source_task_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_validity ON graph_edges(source_id, relationship_name, valid_until)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_dataset ON graph_nodes(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_dataset ON graph_edges(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_route ON graph_nodes(dataset_id, domain_id, collection_id, document_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_route ON graph_edges(dataset_id, domain_id, collection_id, document_id)`,

	// Long-horizon task runtime. PostgreSQL is the canonical operational
	// state; semantic memory remains a separate durable knowledge surface.
	`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL,
		owner_id TEXT NOT NULL DEFAULT '',
		collection_name TEXT NOT NULL,
		room TEXT NOT NULL,
		objective TEXT NOT NULL,
		authority_json TEXT NOT NULL DEFAULT '{}',
		risk_level TEXT NOT NULL DEFAULT 'medium',
		status TEXT NOT NULL DEFAULT 'draft',
		version INTEGER NOT NULL DEFAULT 1,
		current_workspace_revision TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		completed_at TIMESTAMPTZ,
		UNIQUE(owner_id, collection_name, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_criteria (
		id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		description TEXT NOT NULL, required BOOLEAN NOT NULL DEFAULT TRUE,
		verification_json TEXT NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY(task_id, id)
	)`,
	`CREATE TABLE IF NOT EXISTS task_steps (
		id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		description TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', required BOOLEAN NOT NULL DEFAULT TRUE,
		dependencies_json TEXT NOT NULL DEFAULT '[]', criterion_ids_json TEXT NOT NULL DEFAULT '[]', action_json TEXT NOT NULL DEFAULT '{}',
		attempts INTEGER NOT NULL DEFAULT 0, position INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY(task_id, id)
	)`,
	`CREATE TABLE IF NOT EXISTS task_leases (
		step_id TEXT NOT NULL,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		actor_id TEXT NOT NULL, expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY(task_id, step_id),
		CONSTRAINT task_leases_task_step_fkey FOREIGN KEY(task_id, step_id) REFERENCES task_steps(task_id, id) ON DELETE CASCADE
	)`,
	`ALTER TABLE task_leases DROP CONSTRAINT IF EXISTS task_leases_step_id_fkey`,
	`ALTER TABLE task_leases DROP CONSTRAINT IF EXISTS task_leases_task_step_fkey`,
	`ALTER TABLE task_leases DROP CONSTRAINT IF EXISTS task_leases_pkey`,
	`ALTER TABLE task_criteria DROP CONSTRAINT IF EXISTS task_criteria_pkey`,
	`ALTER TABLE task_steps DROP CONSTRAINT IF EXISTS task_steps_pkey`,
	`ALTER TABLE task_criteria ADD CONSTRAINT task_criteria_pkey PRIMARY KEY(task_id, id)`,
	`ALTER TABLE task_steps ADD CONSTRAINT task_steps_pkey PRIMARY KEY(task_id, id)`,
	`ALTER TABLE task_leases ADD CONSTRAINT task_leases_pkey PRIMARY KEY(task_id, step_id)`,
	`ALTER TABLE task_leases ADD CONSTRAINT task_leases_task_step_fkey FOREIGN KEY(task_id, step_id) REFERENCES task_steps(task_id, id) ON DELETE CASCADE`,
	`CREATE TABLE IF NOT EXISTS task_receipts (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		idempotency_key TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '', receipt_type TEXT NOT NULL,
		status TEXT NOT NULL, criterion_ids_json TEXT NOT NULL DEFAULT '[]', observation TEXT NOT NULL DEFAULT '',
		exit_code INTEGER, evidence_uri TEXT NOT NULL DEFAULT '', artifact_digest TEXT NOT NULL DEFAULT '',
		workspace_revision TEXT NOT NULL DEFAULT '', metadata_json TEXT NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(task_id, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_checkpoints (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		idempotency_key TEXT NOT NULL, step_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL,
		verified_json TEXT NOT NULL DEFAULT '[]', failed_json TEXT NOT NULL DEFAULT '[]',
		next_action TEXT NOT NULL DEFAULT '', workspace_revision TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(task_id, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_blockers (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		reason TEXT NOT NULL, required_decision TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), resolved_at TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS task_events (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		actor_id TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE TABLE IF NOT EXISTS task_memory_candidates (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		memory_key TEXT NOT NULL, value TEXT NOT NULL, room TEXT NOT NULL, hall TEXT NOT NULL,
		evidence_receipt_ids TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'pending',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(task_id, memory_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_memory_links (
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, memory_id TEXT NOT NULL,
		relation TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(task_id, memory_id, relation)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_scope_status ON tasks(owner_id, collection_name, status)`,
	`ALTER TABLE task_steps ADD COLUMN action_json TEXT NOT NULL DEFAULT '{}'`,
	`CREATE INDEX IF NOT EXISTS idx_task_steps_task_status ON task_steps(task_id, status, position)`,
	`CREATE INDEX IF NOT EXISTS idx_task_receipts_task ON task_receipts(task_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_events_task ON task_events(task_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_blockers_active ON task_blockers(task_id, status)`,

	`CREATE INDEX IF NOT EXISTS idx_acl_principal ON acl(principal_id)`,
	`CREATE INDEX IF NOT EXISTS idx_acl_dataset ON acl(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_interactions_session ON interactions(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_user_tenant_user ON user_tenant(user_id)`,

	// Search feedback (user ratings on search results)
	`CREATE TABLE IF NOT EXISTS search_feedback (
		id TEXT PRIMARY KEY,
		query TEXT NOT NULL,
		result_id TEXT NOT NULL DEFAULT '',
		collection TEXT NOT NULL DEFAULT '',
		search_type TEXT NOT NULL DEFAULT '',
		rating INTEGER NOT NULL DEFAULT 0,
		comment TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_feedback_collection ON search_feedback(collection)`,

	// API Keys (programmatic access without JWT login)
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		key_hash TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		permissions TEXT NOT NULL DEFAULT 'read-write',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_used TIMESTAMPTZ,
		revoked BOOLEAN NOT NULL DEFAULT FALSE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id)`,

	// Graph communities (Louvain multi-level)
	`CREATE TABLE IF NOT EXISTS graph_communities (
		id TEXT PRIMARY KEY,
		level INTEGER NOT NULL DEFAULT 0,
		parent_id TEXT NOT NULL DEFAULT '',
		member_node_ids JSONB NOT NULL DEFAULT '[]',
		member_count INTEGER NOT NULL DEFAULT 0,
		internal_weight REAL NOT NULL DEFAULT 0,
		summary TEXT NOT NULL DEFAULT '',
		summary_embedding_id TEXT NOT NULL DEFAULT '',
		modularity REAL NOT NULL DEFAULT 0,
		resolution REAL NOT NULL DEFAULT 1.0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_level ON graph_communities(level)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_parent ON graph_communities(parent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_member_count ON graph_communities(member_count DESC)`,

	// Community members join table (fast node→community lookup)
	`CREATE TABLE IF NOT EXISTS community_members (
		community_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		level INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (community_id, node_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_community_members_node ON community_members(node_id, level)`,

	// Adaptive routing weights (feedback-driven)
	`CREATE TABLE IF NOT EXISTS routing_weights (
		search_type TEXT PRIMARY KEY,
		weight REAL NOT NULL DEFAULT 1.0,
		success_count INTEGER NOT NULL DEFAULT 0,
		total_count INTEGER NOT NULL DEFAULT 0,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,

	// Heartbeat event log (doctor, sync, cognify, prune)
	`CREATE TABLE IF NOT EXISTS heartbeats (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		payload JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_heartbeats_type_time ON heartbeats(event_type, created_at DESC)`,

	// Explicit document registrations survive dataset_data unlink/data deletion.
	// No cascade to source rows: a retained tombstone must never become legacy
	// inheritance again. API registration verifies the live association first.
	`CREATE TABLE IF NOT EXISTS document_resources (
		dataset_id TEXT NOT NULL,
		data_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id),
		mode TEXT NOT NULL CHECK (mode IN ('inherit', 'restricted')),
		acl_revision BIGINT NOT NULL DEFAULT 1 CHECK (acl_revision > 0),
		content_revision BIGINT NOT NULL DEFAULT 1 CHECK (content_revision > 0),
		tombstoned BOOLEAN NOT NULL DEFAULT FALSE,
		hold BOOLEAN NOT NULL DEFAULT FALSE,
		PRIMARY KEY (dataset_id, data_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_resources_tenant ON document_resources(tenant_id, dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_document_resources_data ON document_resources(data_id)`,
	`CREATE TABLE IF NOT EXISTS document_structured_artifacts (
		id TEXT PRIMARY KEY,
		data_id TEXT NOT NULL,
		source_revision BIGINT NOT NULL CHECK(source_revision > 0),
		raw_content_hash TEXT NOT NULL CHECK(raw_content_hash <> ''),
		artifact_sha256 TEXT NOT NULL CHECK(artifact_sha256 <> ''),
		byte_size BIGINT NOT NULL CHECK(byte_size >= 0),
		storage_location TEXT NOT NULL UNIQUE,
		destination TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','retired')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_structured_artifacts_source ON document_structured_artifacts(data_id, source_revision, state)`,
	`CREATE TABLE IF NOT EXISTS document_index_publications (
        dataset_id TEXT NOT NULL, data_id TEXT NOT NULL, collection_name TEXT NOT NULL,
        content_revision BIGINT NOT NULL CHECK(content_revision >= 0), generation TEXT NOT NULL,
        source_revision BIGINT NOT NULL DEFAULT 0, raw_content_hash TEXT NOT NULL DEFAULT '',
        sources_json TEXT NOT NULL DEFAULT '[]', requires_admin INTEGER NOT NULL DEFAULT 0 CHECK(requires_admin IN (0,1)),
        lineage_verified INTEGER NOT NULL DEFAULT 0 CHECK(lineage_verified IN (0,1)),
        PRIMARY KEY(dataset_id,data_id,collection_name),
        FOREIGN KEY(dataset_id,data_id) REFERENCES dataset_data(dataset_id,data_id) ON DELETE CASCADE
    )`,
	`CREATE INDEX IF NOT EXISTS idx_document_index_publications_data ON document_index_publications(data_id)`,
	`CREATE TABLE IF NOT EXISTS document_pipeline_statuses (
        dataset_id TEXT NOT NULL, data_id TEXT NOT NULL, collection_name TEXT NOT NULL,
        source_revision BIGINT NOT NULL CHECK(source_revision > 0), raw_content_hash TEXT NOT NULL,
        attempt_id TEXT NOT NULL DEFAULT '', pipeline_state TEXT NOT NULL, status_json TEXT NOT NULL,
        updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
        PRIMARY KEY(dataset_id,data_id,collection_name),
        FOREIGN KEY(dataset_id,data_id) REFERENCES dataset_data(dataset_id,data_id) ON DELETE CASCADE
    )`,
	`ALTER TABLE document_pipeline_statuses ADD COLUMN IF NOT EXISTS attempt_id TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS access_groups (
		id TEXT PRIMARY KEY REFERENCES principals(id),
		tenant_id TEXT NOT NULL REFERENCES tenants(id),
		name TEXT NOT NULL CHECK (name <> ''),
		revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
		UNIQUE (tenant_id, name),
		UNIQUE (id, tenant_id)
	)`,
	`CREATE TABLE IF NOT EXISTS access_group_members (
		group_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		user_id TEXT NOT NULL REFERENCES users(id),
		PRIMARY KEY (group_id, user_id),
		FOREIGN KEY (group_id, tenant_id) REFERENCES access_groups(id, tenant_id) ON DELETE CASCADE,
		FOREIGN KEY (user_id, tenant_id) REFERENCES user_tenant(user_id, tenant_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_access_group_members_user ON access_group_members(tenant_id, user_id, group_id)`,
	`CREATE TABLE IF NOT EXISTS document_grants (
		dataset_id TEXT NOT NULL,
		data_id TEXT NOT NULL,
		principal_kind TEXT NOT NULL CHECK (principal_kind IN ('user', 'group')),
		principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
		role TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
		granted_by TEXT NOT NULL,
		PRIMARY KEY (dataset_id, data_id, principal_kind, principal_id),
		FOREIGN KEY (dataset_id, data_id) REFERENCES document_resources(dataset_id, data_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_grants_principal ON document_grants(principal_kind, principal_id, dataset_id, data_id)`,
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
		source_revision BIGINT NOT NULL DEFAULT 1 CHECK(source_revision>0),
		owner_id TEXT NOT NULL DEFAULT '',
		loader_engine TEXT NOT NULL DEFAULT 'go_ingest',
		pipeline_status TEXT NOT NULL DEFAULT '{}',
		tags TEXT NOT NULL DEFAULT '[]',
		room TEXT NOT NULL DEFAULT '',
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
		dataset_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		collection_id TEXT NOT NULL DEFAULT '',
		document_id TEXT NOT NULL DEFAULT '',
		section_path TEXT NOT NULL DEFAULT '',
		chunk_index INTEGER NOT NULL DEFAULT -1,
		prev_chunk_id TEXT NOT NULL DEFAULT '',
		next_chunk_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS graph_edges (
		id TEXT PRIMARY KEY,
		source_id TEXT NOT NULL,
		target_id TEXT NOT NULL,
		relationship_name TEXT NOT NULL DEFAULT '',
		properties TEXT NOT NULL DEFAULT '{}',
		valid_from TEXT,
		valid_until TEXT,
		superseded_by TEXT NOT NULL DEFAULT '',
		confidence REAL NOT NULL DEFAULT 1.0,
		dataset_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		collection_id TEXT NOT NULL DEFAULT '',
		document_id TEXT NOT NULL DEFAULT '',
		section_path TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	`CREATE TABLE IF NOT EXISTS knowledge_domains (
		id TEXT PRIMARY KEY,
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json TEXT NOT NULL DEFAULT '[]',
		embedding_ref TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS knowledge_collections (
		id TEXT PRIMARY KEY,
		domain_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json TEXT NOT NULL DEFAULT '[]',
		embedding_ref TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS knowledge_documents (
		id TEXT PRIMARY KEY,
		collection_id TEXT NOT NULL DEFAULT '',
		domain_id TEXT NOT NULL DEFAULT '',
		owner_id TEXT NOT NULL DEFAULT '',
		team_id TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		source_document_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL DEFAULT '',
		description TEXT NOT NULL DEFAULT '',
		aliases_json TEXT NOT NULL DEFAULT '[]',
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
	`CREATE INDEX IF NOT EXISTS idx_knowledge_domains_scope ON knowledge_domains(owner_id, team_id, dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_domains_name ON knowledge_domains(dataset_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_collections_scope ON knowledge_collections(owner_id, team_id, dataset_id, domain_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_collections_name ON knowledge_collections(domain_id, name)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_scope ON knowledge_documents(owner_id, team_id, dataset_id, domain_id, collection_id)`,
	`CREATE INDEX IF NOT EXISTS idx_knowledge_documents_title ON knowledge_documents(collection_id, title)`,
	`CREATE TABLE IF NOT EXISTS vsa_fact_shards (
		id TEXT PRIMARY KEY,
		dataset_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		shard_index INTEGER NOT NULL DEFAULT 0,
		dim INTEGER NOT NULL DEFAULT 1024,
		fact_count INTEGER NOT NULL DEFAULT 0,
		vector_json TEXT NOT NULL DEFAULT '[]',
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_vsa_shards_predicate ON vsa_fact_shards(dataset_id, predicate)`,
	`CREATE TABLE IF NOT EXISTS vsa_fact_members (
		shard_id TEXT NOT NULL,
		edge_id TEXT NOT NULL,
		source_id TEXT NOT NULL DEFAULT '',
		target_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		dataset_id TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (shard_id, edge_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_vsa_members_lookup ON vsa_fact_members(dataset_id, predicate, source_id)`,
	`CREATE TABLE IF NOT EXISTS graph_predicate_synonyms (
		dataset_id TEXT NOT NULL DEFAULT '',
		predicate TEXT NOT NULL DEFAULT '',
		synonym TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL DEFAULT 'generated',
		weight REAL NOT NULL DEFAULT 50,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (dataset_id, predicate, synonym, source)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_predicate_synonyms_lookup ON graph_predicate_synonyms(dataset_id, synonym)`,
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

	`CREATE TABLE IF NOT EXISTS interaction_provenance (
		id TEXT PRIMARY KEY,
		interaction_id TEXT UNIQUE REFERENCES interactions(id) ON DELETE CASCADE,
		session_id TEXT NOT NULL CHECK (session_id <> ''),
		owner_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL CHECK (kind IN ('session', 'server', 'client')),
		sources TEXT NOT NULL DEFAULT '[]',
		requires_admin INTEGER NOT NULL DEFAULT 0 CHECK (requires_admin IN (0,1)),
		created_at TEXT NOT NULL,
		CHECK ((kind = 'session' AND interaction_id IS NULL) OR (kind IN ('server', 'client') AND interaction_id IS NOT NULL))
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_interaction_provenance_session ON interaction_provenance(session_id) WHERE kind = 'session'`,
	`CREATE INDEX IF NOT EXISTS idx_interaction_provenance_owner ON interaction_provenance(owner_id, tenant_id, created_at)`,

	`CREATE TABLE IF NOT EXISTS chat_import_runs (
		id TEXT PRIMARY KEY,
		platform TEXT NOT NULL,
		source_path TEXT NOT NULL DEFAULT '',
		source_sha256 TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		imported_count INTEGER NOT NULL DEFAULT 0,
		skipped_count INTEGER NOT NULL DEFAULT 0,
		warned_count INTEGER NOT NULL DEFAULT 0,
		warnings TEXT NOT NULL DEFAULT '[]',
		started_at TEXT NOT NULL,
		finished_at TEXT NOT NULL DEFAULT ''
	)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_import_runs_platform ON chat_import_runs(platform, started_at)`,

	`CREATE TABLE IF NOT EXISTS chat_import_messages (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL REFERENCES chat_import_runs(id) ON DELETE CASCADE,
		platform TEXT NOT NULL,
		session_id TEXT NOT NULL,
		session_title TEXT NOT NULL DEFAULT '',
		external_id TEXT NOT NULL,
		ordinal INTEGER NOT NULL,
		role TEXT NOT NULL,
		kind TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		content TEXT NOT NULL DEFAULT '',
		source_created_at TEXT NOT NULL DEFAULT '',
		metadata TEXT NOT NULL DEFAULT '{}',
		imported_at TEXT NOT NULL,
		UNIQUE(external_id, session_id, platform)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_chat_import_messages_session ON chat_import_messages(platform, session_id, ordinal)`,

	`CREATE TABLE IF NOT EXISTS ontologies (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		file_path TEXT NOT NULL DEFAULT '',
		format TEXT NOT NULL DEFAULT 'rdf/xml',
		classes_count INTEGER NOT NULL DEFAULT 0,
		individuals_count INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	// Memories (project/user memory store)
	`CREATE TABLE IF NOT EXISTS memories (
		id TEXT PRIMARY KEY,
		key TEXT NOT NULL,
		value TEXT NOT NULL,
		type TEXT NOT NULL DEFAULT 'project',
		owner_id TEXT NOT NULL DEFAULT '',
		collection_name TEXT NOT NULL DEFAULT '',
		room TEXT NOT NULL DEFAULT '',
		hall TEXT NOT NULL DEFAULT '',
		is_pinned INTEGER NOT NULL DEFAULT 0,
		pin_priority INTEGER NOT NULL DEFAULT 0,
		superseded_by TEXT NOT NULL DEFAULT '',
		valid_until TEXT,
		consolidated_from TEXT NOT NULL DEFAULT '',
		consolidation_run_id TEXT NOT NULL DEFAULT '',
		tier TEXT NOT NULL DEFAULT 'raw',
		source_task_id TEXT NOT NULL DEFAULT '',
		source_receipt_ids TEXT NOT NULL DEFAULT '[]',
		verification_status TEXT NOT NULL DEFAULT '',
		supersedes_memory_id TEXT NOT NULL DEFAULT '',
		supersession_reason TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	// Upsert identity is (key, owner_id, collection_name) so the same key
	// can exist in different pinned contexts without clobbering (P1 isolation).
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_key_owner_coll ON memories(key, owner_id, collection_name)`,
	`CREATE TABLE IF NOT EXISTS memory_commits (
		id TEXT PRIMARY KEY, owner_id TEXT NOT NULL DEFAULT '', collection_name TEXT NOT NULL DEFAULT '',
		idempotency_key TEXT NOT NULL, status TEXT NOT NULL, request_digest TEXT NOT NULL,
		plan_digest TEXT NOT NULL, plan_json TEXT NOT NULL, result_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, expires_at TEXT NOT NULL, applied_at TEXT,
		UNIQUE(owner_id, collection_name, idempotency_key)
	)`,
	// Drop legacy index from deployments that predated collection-scoped upsert.
	`DROP INDEX IF EXISTS idx_memories_key_owner`,
	// idx_memories_room/hall/pinned are created at the end of the
	// schemaSQLiteStatements list, after the ALTER TABLE migrations that
	// add the columns those indexes depend on.

	`CREATE INDEX IF NOT EXISTS idx_acl_principal ON acl(principal_id)`,
	`CREATE INDEX IF NOT EXISTS idx_acl_dataset ON acl(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_interactions_session ON interactions(session_id)`,
	`CREATE INDEX IF NOT EXISTS idx_user_tenant_user ON user_tenant(user_id)`,

	// Search feedback
	`CREATE TABLE IF NOT EXISTS search_feedback (
		id TEXT PRIMARY KEY,
		query TEXT NOT NULL,
		result_id TEXT NOT NULL DEFAULT '',
		collection TEXT NOT NULL DEFAULT '',
		search_type TEXT NOT NULL DEFAULT '',
		rating INTEGER NOT NULL DEFAULT 0,
		comment TEXT NOT NULL DEFAULT '',
		user_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_feedback_collection ON search_feedback(collection)`,

	// API Keys
	`CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT PRIMARY KEY,
		key_hash TEXT NOT NULL,
		user_id TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		permissions TEXT NOT NULL DEFAULT 'read-write',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_used TEXT,
		revoked INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash)`,
	`CREATE INDEX IF NOT EXISTS idx_api_keys_user ON api_keys(user_id)`,

	// Migrations for existing SQLite databases (ALTER errors ignored).
	// Order matters: ALTERs come BEFORE the dependent CREATE INDEX statements
	// so old DBs receive the new columns first.
	`ALTER TABLE data ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE data ADD COLUMN room TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE data ADD COLUMN source_revision BIGINT NOT NULL DEFAULT 1 CHECK(source_revision>0)`,
	`ALTER TABLE memories ADD COLUMN room TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN hall TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN is_pinned INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE memories ADD COLUMN pin_priority INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE memories ADD COLUMN superseded_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN valid_until TEXT`,
	`ALTER TABLE memories ADD COLUMN consolidated_from TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN consolidation_run_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN tier TEXT NOT NULL DEFAULT 'raw'`,
	`ALTER TABLE memories ADD COLUMN source_task_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN source_receipt_ids TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE memories ADD COLUMN verification_status TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN supersedes_memory_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE memories ADD COLUMN supersession_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN valid_from TEXT`,
	`ALTER TABLE graph_edges ADD COLUMN valid_until TEXT`,
	`ALTER TABLE graph_edges ADD COLUMN superseded_by TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0`,
	`ALTER TABLE graph_nodes ADD COLUMN dataset_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN dataset_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN domain_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN collection_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN document_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN section_path TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN chunk_index INTEGER NOT NULL DEFAULT -1`,
	`ALTER TABLE graph_nodes ADD COLUMN prev_chunk_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_nodes ADD COLUMN next_chunk_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN domain_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN collection_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN document_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE graph_edges ADD COLUMN section_path TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE datasets ADD COLUMN github_repo TEXT NOT NULL DEFAULT ''`,
	// Indexes that depend on the columns added above.
	`CREATE INDEX IF NOT EXISTS idx_memories_room ON memories(collection_name, room)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_hall ON memories(hall)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_pinned ON memories(is_pinned, pin_priority DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_memories_source_task ON memories(source_task_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_validity ON graph_edges(source_id, relationship_name, valid_until)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_dataset ON graph_nodes(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_dataset ON graph_edges(dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_nodes_route ON graph_nodes(dataset_id, domain_id, collection_id, document_id)`,
	`CREATE INDEX IF NOT EXISTS idx_graph_edges_route ON graph_edges(dataset_id, domain_id, collection_id, document_id)`,

	// Long-horizon task runtime — SQLite-compatible mirror of PostgreSQL DDL.
	`CREATE TABLE IF NOT EXISTS tasks (
		id TEXT PRIMARY KEY, idempotency_key TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '',
		collection_name TEXT NOT NULL, room TEXT NOT NULL, objective TEXT NOT NULL,
		authority_json TEXT NOT NULL DEFAULT '{}', risk_level TEXT NOT NULL DEFAULT 'medium',
		status TEXT NOT NULL DEFAULT 'draft', version INTEGER NOT NULL DEFAULT 1,
		current_workspace_revision TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, completed_at TEXT,
		UNIQUE(owner_id, collection_name, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_criteria (
		id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		description TEXT NOT NULL, required INTEGER NOT NULL DEFAULT 1,
		verification_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(task_id, id)
	)`,
	`CREATE TABLE IF NOT EXISTS task_steps (
		id TEXT NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		description TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', required INTEGER NOT NULL DEFAULT 1,
		dependencies_json TEXT NOT NULL DEFAULT '[]', criterion_ids_json TEXT NOT NULL DEFAULT '[]', action_json TEXT NOT NULL DEFAULT '{}',
		attempts INTEGER NOT NULL DEFAULT 0, position INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(task_id, id)
	)`,
	`CREATE TABLE IF NOT EXISTS task_leases (
		step_id TEXT NOT NULL,
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		actor_id TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(task_id, step_id),
		FOREIGN KEY(task_id, step_id) REFERENCES task_steps(task_id, id) ON DELETE CASCADE
	)`,
	`CREATE TABLE IF NOT EXISTS task_receipts (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		idempotency_key TEXT NOT NULL, owner_id TEXT NOT NULL DEFAULT '', receipt_type TEXT NOT NULL,
		status TEXT NOT NULL, criterion_ids_json TEXT NOT NULL DEFAULT '[]', observation TEXT NOT NULL DEFAULT '',
		exit_code INTEGER, evidence_uri TEXT NOT NULL DEFAULT '', artifact_digest TEXT NOT NULL DEFAULT '',
		workspace_revision TEXT NOT NULL DEFAULT '', metadata_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(task_id, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_checkpoints (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		idempotency_key TEXT NOT NULL, step_id TEXT NOT NULL DEFAULT '', summary TEXT NOT NULL,
		verified_json TEXT NOT NULL DEFAULT '[]', failed_json TEXT NOT NULL DEFAULT '[]',
		next_action TEXT NOT NULL DEFAULT '', workspace_revision TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(task_id, idempotency_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_blockers (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		reason TEXT NOT NULL, required_decision TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, resolved_at TEXT
	)`,
	`CREATE TABLE IF NOT EXISTS task_events (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		actor_id TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL, payload_json TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS task_memory_candidates (
		id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
		memory_key TEXT NOT NULL, value TEXT NOT NULL, room TEXT NOT NULL, hall TEXT NOT NULL,
		evidence_receipt_ids TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'pending',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP, UNIQUE(task_id, memory_key)
	)`,
	`CREATE TABLE IF NOT EXISTS task_memory_links (
		task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE, memory_id TEXT NOT NULL,
		relation TEXT NOT NULL, created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY(task_id, memory_id, relation)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_scope_status ON tasks(owner_id, collection_name, status)`,
	`ALTER TABLE task_steps ADD COLUMN action_json TEXT NOT NULL DEFAULT '{}'`,
	`CREATE INDEX IF NOT EXISTS idx_task_steps_task_status ON task_steps(task_id, status, position)`,
	`CREATE INDEX IF NOT EXISTS idx_task_receipts_task ON task_receipts(task_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_events_task ON task_events(task_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_task_blockers_active ON task_blockers(task_id, status)`,

	// Graph communities (Louvain multi-level) — SQLite
	`CREATE TABLE IF NOT EXISTS graph_communities (
		id TEXT PRIMARY KEY,
		level INTEGER NOT NULL DEFAULT 0,
		parent_id TEXT NOT NULL DEFAULT '',
		member_node_ids TEXT NOT NULL DEFAULT '[]',
		member_count INTEGER NOT NULL DEFAULT 0,
		internal_weight REAL NOT NULL DEFAULT 0,
		summary TEXT NOT NULL DEFAULT '',
		summary_embedding_id TEXT NOT NULL DEFAULT '',
		modularity REAL NOT NULL DEFAULT 0,
		resolution REAL NOT NULL DEFAULT 1.0,
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_level ON graph_communities(level)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_parent ON graph_communities(parent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_communities_member_count ON graph_communities(member_count DESC)`,

	// Community members join table — SQLite
	`CREATE TABLE IF NOT EXISTS community_members (
		community_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		level INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (community_id, node_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_community_members_node ON community_members(node_id, level)`,

	// Adaptive routing weights — SQLite
	`CREATE TABLE IF NOT EXISTS routing_weights (
		search_type TEXT PRIMARY KEY,
		weight REAL NOT NULL DEFAULT 1.0,
		success_count INTEGER NOT NULL DEFAULT 0,
		total_count INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,

	// Heartbeat event log — SQLite
	`CREATE TABLE IF NOT EXISTS heartbeats (
		id TEXT PRIMARY KEY,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_heartbeats_type_time ON heartbeats(event_type, created_at DESC)`,

	// Explicit document registrations survive dataset_data unlink/data deletion.
	// No cascade to source rows: a retained tombstone must never become legacy
	// inheritance again. API registration verifies the live association first.
	`CREATE TABLE IF NOT EXISTS document_resources (
		dataset_id TEXT NOT NULL,
		data_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL REFERENCES tenants(id),
		mode TEXT NOT NULL CHECK (mode IN ('inherit', 'restricted')),
		acl_revision BIGINT NOT NULL DEFAULT 1 CHECK (acl_revision > 0),
		content_revision BIGINT NOT NULL DEFAULT 1 CHECK (content_revision > 0),
		tombstoned BOOLEAN NOT NULL DEFAULT FALSE,
		hold BOOLEAN NOT NULL DEFAULT FALSE,
		PRIMARY KEY (dataset_id, data_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_resources_tenant ON document_resources(tenant_id, dataset_id)`,
	`CREATE INDEX IF NOT EXISTS idx_document_resources_data ON document_resources(data_id)`,
	`CREATE TABLE IF NOT EXISTS document_structured_artifacts (
		id TEXT PRIMARY KEY,
		data_id TEXT NOT NULL,
		source_revision INTEGER NOT NULL CHECK(source_revision > 0),
		raw_content_hash TEXT NOT NULL CHECK(raw_content_hash <> ''),
		artifact_sha256 TEXT NOT NULL CHECK(artifact_sha256 <> ''),
		byte_size INTEGER NOT NULL CHECK(byte_size >= 0),
		storage_location TEXT NOT NULL UNIQUE,
		destination TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','retired')),
		created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_structured_artifacts_source ON document_structured_artifacts(data_id, source_revision, state)`,
	`CREATE TABLE IF NOT EXISTS document_index_publications (
        dataset_id TEXT NOT NULL, data_id TEXT NOT NULL, collection_name TEXT NOT NULL,
        content_revision BIGINT NOT NULL CHECK(content_revision >= 0), generation TEXT NOT NULL,
        source_revision BIGINT NOT NULL DEFAULT 0, raw_content_hash TEXT NOT NULL DEFAULT '',
        sources_json TEXT NOT NULL DEFAULT '[]', requires_admin INTEGER NOT NULL DEFAULT 0 CHECK(requires_admin IN (0,1)),
        lineage_verified INTEGER NOT NULL DEFAULT 0 CHECK(lineage_verified IN (0,1)),
        PRIMARY KEY(dataset_id,data_id,collection_name),
        FOREIGN KEY(dataset_id,data_id) REFERENCES dataset_data(dataset_id,data_id) ON DELETE CASCADE
    )`,
	`CREATE INDEX IF NOT EXISTS idx_document_index_publications_data ON document_index_publications(data_id)`,
	`CREATE TABLE IF NOT EXISTS document_pipeline_statuses (
        dataset_id TEXT NOT NULL, data_id TEXT NOT NULL, collection_name TEXT NOT NULL,
        source_revision BIGINT NOT NULL CHECK(source_revision > 0), raw_content_hash TEXT NOT NULL,
        attempt_id TEXT NOT NULL DEFAULT '', pipeline_state TEXT NOT NULL, status_json TEXT NOT NULL,
        updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
        PRIMARY KEY(dataset_id,data_id,collection_name),
        FOREIGN KEY(dataset_id,data_id) REFERENCES dataset_data(dataset_id,data_id) ON DELETE CASCADE
    )`,
	`ALTER TABLE document_pipeline_statuses ADD COLUMN attempt_id TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS access_groups (
		id TEXT PRIMARY KEY REFERENCES principals(id),
		tenant_id TEXT NOT NULL REFERENCES tenants(id),
		name TEXT NOT NULL CHECK (name <> ''),
		revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
		UNIQUE (tenant_id, name),
		UNIQUE (id, tenant_id)
	)`,
	`CREATE TABLE IF NOT EXISTS access_group_members (
		group_id TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		user_id TEXT NOT NULL REFERENCES users(id),
		PRIMARY KEY (group_id, user_id),
		FOREIGN KEY (group_id, tenant_id) REFERENCES access_groups(id, tenant_id) ON DELETE CASCADE,
		FOREIGN KEY (user_id, tenant_id) REFERENCES user_tenant(user_id, tenant_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_access_group_members_user ON access_group_members(tenant_id, user_id, group_id)`,
	`CREATE TABLE IF NOT EXISTS document_grants (
		dataset_id TEXT NOT NULL,
		data_id TEXT NOT NULL,
		principal_kind TEXT NOT NULL CHECK (principal_kind IN ('user', 'group')),
		principal_id TEXT NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
		role TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
		granted_by TEXT NOT NULL,
		PRIMARY KEY (dataset_id, data_id, principal_kind, principal_id),
		FOREIGN KEY (dataset_id, data_id) REFERENCES document_resources(dataset_id, data_id) ON DELETE CASCADE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_document_grants_principal ON document_grants(principal_kind, principal_id, dataset_id, data_id)`,
}
