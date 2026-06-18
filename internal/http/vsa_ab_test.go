package http

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func TestVSAGraphContextABShowsRecallLift(t *testing.T) {
	ctx := context.Background()
	db := newVSAABTestDB(t)
	defer db.Close()

	prev := GetDBProvider()
	SetDBProvider(DBSQLite)
	defer SetDBProvider(prev)

	cfg := APIConfig{DB: db}
	entityNames := []string{"Checkout"}
	allowed := []string{"ds-vsa"}
	expected := []string{
		"Checkout is related to Orders via CALLS",
		"Checkout is related to Payments via CALLS",
		"Checkout is related to Ledger via CALLS",
		"Checkout is related to Oncall via OWNED_BY",
	}

	withoutVSA := vsaGraphContext(ctx, cfg, entityNames, allowed, 20)
	withoutRecall := graphFactRecall(withoutVSA, expected)
	if withoutRecall != 0 {
		t.Fatalf("without VSA recall=%.2f context=%v, want 0 before rebuild", withoutRecall, withoutVSA)
	}

	if err := vsaStoreForDB(db, 256, 16).RebuildFromGraph(ctx, "ds-vsa"); err != nil {
		t.Fatalf("rebuild VSA: %v", err)
	}
	withVSA := vsaGraphContext(ctx, cfg, entityNames, allowed, 20)
	withRecall := graphFactRecall(withVSA, expected)
	t.Logf("VSA A/B fact_recall without=%.2f with=%.2f context_without=%d context_with=%d", withoutRecall, withRecall, len(withoutVSA), len(withVSA))
	if withRecall < 1.0 {
		t.Fatalf("with VSA recall=%.2f context=%v, want all expected facts", withRecall, withVSA)
	}
	for _, line := range withVSA {
		if strings.Contains(line, "TenantB") {
			t.Fatalf("VSA context leaked another dataset: %q", line)
		}
	}
}

func graphFactRecall(lines, expected []string) float64 {
	if len(expected) == 0 {
		return 1
	}
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		for _, want := range expected {
			if strings.Contains(line, want) {
				seen[want] = true
			}
		}
	}
	return float64(len(seen)) / float64(len(expected))
}

func newVSAABTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/vsa-ab.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	schema := `
CREATE TABLE graph_nodes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL DEFAULT '',
	type TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	dataset_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE graph_edges (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL,
	target_id TEXT NOT NULL,
	relationship_name TEXT NOT NULL DEFAULT '',
	properties TEXT NOT NULL DEFAULT '{}',
	valid_until TEXT,
	dataset_id TEXT NOT NULL DEFAULT ''
);`
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create graph schema: %v", err)
	}
	rows := []string{
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('checkout', 'Checkout', 'Service', 'ds-vsa')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('orders', 'Orders', 'Service', 'ds-vsa')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('payments', 'Payments', 'Service', 'ds-vsa')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('ledger', 'Ledger', 'Service', 'ds-vsa')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('oncall', 'Oncall', 'Team', 'ds-vsa')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id) VALUES ('tenant-b', 'TenantB', 'Service', 'ds-other')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e1', 'checkout', 'orders', 'CALLS', 'ds-vsa')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e2', 'checkout', 'payments', 'CALLS', 'ds-vsa')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e3', 'checkout', 'ledger', 'CALLS', 'ds-vsa')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e4', 'checkout', 'oncall', 'OWNED_BY', 'ds-vsa')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id) VALUES ('e5', 'checkout', 'tenant-b', 'CALLS', 'ds-other')`,
	}
	for _, stmt := range rows {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
	return db
}
