package http

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestResolveDCDRouteCandidatesRanksExactDocumentRoute(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()
	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	seedDCDRouteResolverRows(t, db)

	candidates, err := resolveDCDRouteCandidates(context.Background(), db,
		"open the checkout incident playbook for billing operations",
		dcdRouteScope{OwnerID: "alice", TeamID: "team-a", AllowedDatasetIDs: []string{"ds-a"}},
		dcdRoutePolicy{MaxCandidates: 3, MinConfidence: 0.1, AllowGlobalFallback: true})
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no route candidates")
	}
	got := candidates[0]
	if got.DomainID != "billing-domain" || got.CollectionID != "runbooks-collection" || got.DocumentID != "checkout-playbook" {
		t.Fatalf("top route = %+v", got)
	}
	if got.Confidence <= 0 {
		t.Fatalf("confidence = %.3f, want positive", got.Confidence)
	}
}

func TestResolveDCDRouteCandidatesFiltersOwnerTeamDataset(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()
	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	seedDCDRouteResolverRows(t, db)

	candidates, err := resolveDCDRouteCandidates(context.Background(), db,
		"checkout incident playbook",
		dcdRouteScope{OwnerID: "alice", TeamID: "team-a", AllowedDatasetIDs: []string{"ds-b"}},
		dcdRoutePolicy{MaxCandidates: 3, MinConfidence: 0.1})
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("cross-dataset candidates = %+v, want none", candidates)
	}
}

func TestResolveDCDRouteCandidatesAppliesThresholdAndLimit(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()
	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	seedDCDRouteResolverRows(t, db)

	candidates, err := resolveDCDRouteCandidates(context.Background(), db,
		"billing",
		dcdRouteScope{OwnerID: "alice", TeamID: "team-a", AllowedDatasetIDs: []string{"ds-a"}},
		dcdRoutePolicy{MaxCandidates: 1, MinConfidence: 0.1})
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidate count=%d, want 1: %+v", len(candidates), candidates)
	}

	candidates, err = resolveDCDRouteCandidates(context.Background(), db,
		"billing",
		dcdRouteScope{OwnerID: "alice", TeamID: "team-a", AllowedDatasetIDs: []string{"ds-a"}},
		dcdRoutePolicy{MaxCandidates: 3, MinConfidence: 0.99})
	if err != nil {
		t.Fatalf("resolve candidates high threshold: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("high-threshold candidates = %+v, want none", candidates)
	}
}

func TestResolveDCDRouteCandidatesUsesBM25RouteText(t *testing.T) {
	db := newDCDRouteSchemaDB(t)
	defer db.Close()
	withSQLiteProvider(t)
	if err := MigrateSchema(db); err != nil {
		t.Fatalf("migration: %v", err)
	}
	seedDCDRouteResolverRows(t, db)

	candidates, err := resolveDCDRouteCandidates(context.Background(), db,
		"invoices",
		dcdRouteScope{OwnerID: "alice", TeamID: "team-a", AllowedDatasetIDs: []string{"ds-a"}},
		dcdRoutePolicy{MaxCandidates: 3, MinConfidence: 0.01})
	if err != nil {
		t.Fatalf("resolve candidates: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("no candidates from BM25 route text")
	}
	if !strings.Contains(candidates[0].Reason, "bm25") {
		t.Fatalf("top candidate reason=%q, want bm25 contribution", candidates[0].Reason)
	}
}

func seedDCDRouteResolverRows(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description, aliases_json)
		VALUES ('billing-domain', 'alice', 'team-a', 'ds-a', 'Billing', 'payments and invoices', '["money ops","billing operations"]')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name, description, aliases_json)
		VALUES ('runbooks-collection', 'billing-domain', 'alice', 'team-a', 'ds-a', 'Runbooks', 'incident response', '["incident guides"]')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title, description, aliases_json)
		VALUES ('checkout-playbook', 'runbooks-collection', 'billing-domain', 'alice', 'team-a', 'ds-a', 'Checkout Incident Playbook', 'checkout failures', '["checkout incident"]')`)

	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description, aliases_json)
		VALUES ('bob-billing-domain', 'bob', 'team-b', 'ds-b', 'Billing', 'other tenant', '["billing operations"]')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name)
		VALUES ('bob-runbooks-collection', 'bob-billing-domain', 'bob', 'team-b', 'ds-b', 'Runbooks')`)
	mustExecDCDRouteSchema(t, db, `INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title)
		VALUES ('bob-checkout-playbook', 'bob-runbooks-collection', 'bob-billing-domain', 'bob', 'team-b', 'ds-b', 'Checkout Incident Playbook')`)
}
