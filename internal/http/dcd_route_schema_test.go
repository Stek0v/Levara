package http

import (
	"database/sql"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestDCDRouteSchemaMigrationIsIdempotent(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()

	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	for _, table := range []string{"knowledge_domains", "knowledge_collections", "knowledge_documents"} {
		if !sqliteTableExists(t, db, table) {
			t.Fatalf("missing route table %s", table)
		}
	}
	for _, tc := range []struct {
		table  string
		column string
	}{
		{"graph_nodes", "domain_id"},
		{"graph_nodes", "collection_id"},
		{"graph_nodes", "document_id"},
		{"graph_nodes", "section_path"},
		{"graph_nodes", "chunk_index"},
		{"graph_nodes", "prev_chunk_id"},
		{"graph_nodes", "next_chunk_id"},
		{"graph_edges", "domain_id"},
		{"graph_edges", "collection_id"},
		{"graph_edges", "document_id"},
		{"graph_edges", "section_path"},
	} {
		if !sqliteColumnExists(t, db, tc.table, tc.column) {
			t.Fatalf("missing %s.%s", tc.table, tc.column)
		}
	}
	for _, index := range []string{
		"idx_knowledge_domains_scope",
		"idx_knowledge_collections_scope",
		"idx_knowledge_documents_scope",
		"idx_graph_nodes_route",
		"idx_graph_edges_route",
	} {
		if !sqliteIndexExists(t, db, index) {
			t.Fatalf("missing route index %s", index)
		}
	}
}

func TestDCDRouteRowsAreScopedByOwnerTeamAndDataset(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()

	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description)
		VALUES ('alice-domain', 'alice', 'team-a', 'ds-a', 'Billing', 'Alice billing docs')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description)
		VALUES ('bob-domain', 'bob', 'team-b', 'ds-b', 'Billing', 'Bob billing docs')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name)
		VALUES ('alice-collection', 'alice-domain', 'alice', 'team-a', 'ds-a', 'Runbooks')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name)
		VALUES ('bob-collection', 'bob-domain', 'bob', 'team-b', 'ds-b', 'Runbooks')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title)
		VALUES ('alice-doc', 'alice-collection', 'alice-domain', 'alice', 'team-a', 'ds-a', 'Incident Playbook')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title)
		VALUES ('bob-doc', 'bob-collection', 'bob-domain', 'bob', 'team-b', 'ds-b', 'Incident Playbook')`)

	var domainID, collectionID, documentID string
	if err := db.QueryRow(`
		SELECT d.id, c.id, doc.id
		FROM knowledge_domains d
		JOIN knowledge_collections c ON c.domain_id = d.id
		JOIN knowledge_documents doc ON doc.collection_id = c.id
		WHERE d.owner_id = ? AND d.team_id = ? AND d.dataset_id = ? AND d.name = ?
		  AND c.owner_id = d.owner_id AND c.team_id = d.team_id AND c.dataset_id = d.dataset_id
		  AND doc.owner_id = d.owner_id AND doc.team_id = d.team_id AND doc.dataset_id = d.dataset_id`,
		"alice", "team-a", "ds-a", "Billing").Scan(&domainID, &collectionID, &documentID); err != nil {
		t.Fatalf("select alice route: %v", err)
	}
	if domainID != "alice-domain" || collectionID != "alice-collection" || documentID != "alice-doc" {
		t.Fatalf("alice route = %s/%s/%s", domainID, collectionID, documentID)
	}

	var count int
	if err := db.QueryRow(`
		SELECT COUNT(*)
		FROM knowledge_domains
		WHERE owner_id = ? AND team_id = ? AND dataset_id = ? AND name = ?`,
		"alice", "team-a", "ds-b", "Billing").Scan(&count); err != nil {
		t.Fatalf("count cross-dataset route: %v", err)
	}
	if count != 0 {
		t.Fatalf("cross-dataset route count = %d, want 0", count)
	}
}

func newDCDRouteSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/dcd-route-schema.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func withSQLiteProvider(t *testing.T) {
	t.Helper()
	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	t.Cleanup(func() { SetDBProvider(prev) })
}

func mustExecDCDRouteSchema(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func sqliteTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&count); err != nil {
		t.Fatalf("check table %s: %v", table, err)
	}
	return count > 0
}

func sqliteColumnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}

func sqliteIndexExists(t *testing.T, db *sql.DB, index string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil {
		t.Fatalf("check index %s: %v", index, err)
	}
	return count > 0
}
