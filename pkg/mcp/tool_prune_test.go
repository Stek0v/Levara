package mcp

import (
	"database/sql"
	"testing"
)

// Legacy no-auth fixtures are explicit TrustedLocal dependencies. They still
// install the real policy tables rather than treating missing schema as no hold.
func installPrunePolicyFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY,is_active BOOLEAN,is_superuser BOOLEAN)`,
		`CREATE TABLE IF NOT EXISTS document_resources(dataset_id TEXT,data_id TEXT,tenant_id TEXT DEFAULT '',mode TEXT DEFAULT 'inherit',acl_revision BIGINT DEFAULT 1,content_revision BIGINT DEFAULT 1,tombstoned BOOLEAN DEFAULT FALSE,hold BOOLEAN DEFAULT FALSE,PRIMARY KEY(dataset_id,data_id))`,
		`CREATE TABLE IF NOT EXISTS document_grants(dataset_id TEXT,data_id TEXT)`,
		`CREATE TABLE IF NOT EXISTS dataset_shares(id TEXT,dataset_id TEXT,user_id TEXT,role TEXT)`,
		`CREATE TABLE IF NOT EXISTS document_structured_artifacts(id TEXT PRIMARY KEY,data_id TEXT,storage_location TEXT,destination TEXT DEFAULT '',state TEXT,updated_at TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// These are observable data tables, not the implementation's statement list.
var pruneTables = []string{"dataset_data", "data", "datasets", "graph_nodes", "graph_edges"}
