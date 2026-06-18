package http

import (
	"context"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/vsamemory"
)

func TestRerankVSACandidatesByDCDRouteBoostsMatchingDocument(t *testing.T) {
	candidates := []vsamemory.Candidate{
		{
			TargetID:     "wrong",
			DatasetID:    "ds-a",
			DomainID:     "other-domain",
			CollectionID: "other-collection",
			DocumentID:   "other-document",
			Similarity:   0.90,
			RerankScore:  0.90,
		},
		{
			TargetID:     "correct",
			DatasetID:    "ds-a",
			DomainID:     "billing-domain",
			CollectionID: "runbooks-collection",
			DocumentID:   "checkout-playbook",
			Similarity:   0.70,
			RerankScore:  0.70,
		},
	}
	routes := []dcdRouteCandidate{{
		DomainID:     "billing-domain",
		CollectionID: "runbooks-collection",
		DocumentID:   "checkout-playbook",
		DatasetID:    "ds-a",
		Confidence:   1,
	}}

	got := rerankVSACandidatesByDCDRoute(candidates, routes)
	if got[0].TargetID != "correct" {
		t.Fatalf("top candidate=%q, want route-matching candidate", got[0].TargetID)
	}
	if candidates[0].TargetID != "wrong" {
		t.Fatalf("rerank mutated input slice")
	}
}

func TestRerankVSACandidatesByDCDRouteDoesNotBoostCrossDatasetRoute(t *testing.T) {
	candidates := []vsamemory.Candidate{
		{
			TargetID:     "higher-similarity",
			DatasetID:    "ds-a",
			DomainID:     "other-domain",
			CollectionID: "other-collection",
			DocumentID:   "other-document",
			Similarity:   0.90,
			RerankScore:  0.90,
		},
		{
			TargetID:     "route-shape-match-foreign-dataset",
			DatasetID:    "ds-a",
			DomainID:     "billing-domain",
			CollectionID: "runbooks-collection",
			DocumentID:   "checkout-playbook",
			Similarity:   0.70,
			RerankScore:  0.70,
		},
	}
	routes := []dcdRouteCandidate{{
		DomainID:     "billing-domain",
		CollectionID: "runbooks-collection",
		DocumentID:   "checkout-playbook",
		DatasetID:    "ds-b",
		Confidence:   1,
	}}

	got := rerankVSACandidatesByDCDRoute(candidates, routes)
	if got[0].TargetID != "higher-similarity" {
		t.Fatalf("top candidate=%q, cross-dataset route must not boost ds-a candidate", got[0].TargetID)
	}
	if boost := dcdRouteCandidateBoost(candidates[1], routes); boost != 0 {
		t.Fatalf("cross-dataset boost=%.3f, want 0", boost)
	}
}

func TestSearchHandlerDCDRouteBoostReranksVSAContext(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "boost")
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", graphContextOrderVSAFirst)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "4")
	t.Setenv("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", "4")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteBoostGraph(t, env)
	env.startWithUser("alice")

	status, body := env.postSearch(map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	vsaCtx, _ := body["context_vsa"].([]any)
	if len(vsaCtx) == 0 {
		t.Fatalf("context_vsa empty: %#v", body)
	}
	first, _ := vsaCtx[0].(string)
	if !strings.Contains(first, "PCI Validator") {
		t.Fatalf("first VSA context=%q, want route-boosted PCI Validator", first)
	}
	if body["graph_context_route_boost_enabled"] != true {
		t.Fatalf("graph_context_route_boost_enabled=%v, want true", body["graph_context_route_boost_enabled"])
	}
	if boosted, _ := body["graph_context_route_boosted_count"].(float64); boosted <= 0 {
		t.Fatalf("graph_context_route_boosted_count=%v, want > 0", body["graph_context_route_boosted_count"])
	}
	if hits, _ := body["graph_context_route_metadata_hit_count"].(float64); hits <= 0 {
		t.Fatalf("graph_context_route_metadata_hit_count=%v, want > 0", body["graph_context_route_metadata_hit_count"])
	}
	debug, _ := body["debug"].(map[string]any)
	dcd, _ := debug["dcd_route"].(map[string]any)
	if dcd["mode"] != dcdRouteBoostMode {
		t.Fatalf("dcd_route.mode=%v, want %s", dcd["mode"], dcdRouteBoostMode)
	}
}

func TestSearchHandlerDCDRouteBoostWrongRouteStillKeepsAllowedVSACandidate(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "boost")
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", graphContextOrderVSAFirst)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "4")
	t.Setenv("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", "4")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteWrongRows(t, env)
	seedDCDRouteBoostGraph(t, env)
	env.startWithUser("alice")

	_, body := env.postSearch(map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	vsaCtx, _ := body["context_vsa"].([]any)
	if !stringSliceContains(vsaCtx, "PCI Validator") {
		t.Fatalf("context_vsa=%v, wrong route must not filter allowed VSA candidate", vsaCtx)
	}
	debug, _ := body["debug"].(map[string]any)
	dcd, _ := debug["dcd_route"].(map[string]any)
	candidates, _ := dcd["candidates"].([]any)
	if len(candidates) == 0 {
		t.Fatalf("expected wrong route candidate in debug: %#v", dcd)
	}
	top, _ := candidates[0].(map[string]any)
	if top["domain_id"] != "other-domain" {
		t.Fatalf("top route=%#v, want wrong other-domain route for this test", top)
	}
}

func TestSearchHandlerDCDRouteBoostEmptyRouteKeepsAllowedVSA(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "boost")
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", graphContextOrderVSAFirst)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "4")
	t.Setenv("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", "4")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteBoostGraph(t, env)
	env.startWithUser("alice")

	_, body := env.postSearch(map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	vsaCtx, _ := body["context_vsa"].([]any)
	if !stringSliceContains(vsaCtx, "PCI Validator") {
		t.Fatalf("context_vsa=%v, empty route must keep global allowed VSA behavior", vsaCtx)
	}
	debug, _ := body["debug"].(map[string]any)
	dcd, _ := debug["dcd_route"].(map[string]any)
	if dcd["candidate_count"] != float64(0) {
		t.Fatalf("candidate_count=%v, want 0", dcd["candidate_count"])
	}
	if body["graph_context_route_boost_enabled"] != false {
		t.Fatalf("graph_context_route_boost_enabled=%v, want false without route candidates", body["graph_context_route_boost_enabled"])
	}
}

func TestSearchHandlerDCDRouteBoostMissingRouteMetadataKeepsAllowedVSA(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "boost")
	t.Setenv("LEVARA_GRAPH_CONTEXT_ORDER", graphContextOrderVSAFirst)
	t.Setenv("LEVARA_GRAPH_CONTEXT_LIMIT", "4")
	t.Setenv("LEVARA_GRAPH_CONTEXT_VSA_RESERVE", "4")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteBoostGraphWithoutRouteMetadata(t, env)
	env.startWithUser("alice")

	_, body := env.postSearch(map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	vsaCtx, _ := body["context_vsa"].([]any)
	if !stringSliceContains(vsaCtx, "PCI Validator") {
		t.Fatalf("context_vsa=%v, missing route metadata must keep global allowed VSA behavior", vsaCtx)
	}
	if body["graph_context_route_boost_enabled"] != true {
		t.Fatalf("graph_context_route_boost_enabled=%v, want true with route candidates", body["graph_context_route_boost_enabled"])
	}
	if boosted, _ := body["graph_context_route_boosted_count"].(float64); boosted != 0 {
		t.Fatalf("graph_context_route_boosted_count=%v, want 0 without route metadata", body["graph_context_route_boosted_count"])
	}
	if misses, _ := body["graph_context_route_metadata_miss_count"].(float64); misses <= 0 {
		t.Fatalf("graph_context_route_metadata_miss_count=%v, want > 0", body["graph_context_route_metadata_miss_count"])
	}
}

func seedDCDRouteBoostGraph(t *testing.T, env *searchTestEnv) {
	t.Helper()
	env.insertVector("entities", "checkout-vector", []float32{1, 0, 0, 0}, map[string]any{
		"name":       "Checkout",
		"dataset_id": "ds-a",
	})
	statements := []string{
		`INSERT INTO graph_nodes(id, name, type, dataset_id)
		 VALUES ('checkout', 'Checkout', 'Service', 'ds-a')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id, domain_id, collection_id, document_id)
		 VALUES ('wrong-playbook', 'Checkout Incident Playbook Archive', 'Document', 'ds-a', 'other-domain', 'other-collection', 'other-document')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id, domain_id, collection_id, document_id)
		 VALUES ('pci-validator', 'PCI Validator', 'Service', 'ds-a', 'billing-domain', 'runbooks-collection', 'checkout-playbook')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id, domain_id, collection_id, document_id)
		 VALUES ('e-wrong', 'checkout', 'wrong-playbook', 'CALLS', 'ds-a', 'other-domain', 'other-collection', 'other-document')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id, domain_id, collection_id, document_id)
		 VALUES ('e-correct', 'checkout', 'pci-validator', 'CALLS', 'ds-a', 'billing-domain', 'runbooks-collection', 'checkout-playbook')`,
	}
	for _, stmt := range statements {
		if _, err := env.db.Exec(strings.TrimSpace(stmt)); err != nil {
			t.Fatalf("seed DCD route boost graph: %v", err)
		}
	}
	if err := vsaStoreForDB(env.db, 1024, 1).RebuildFromGraph(context.Background(), "ds-a"); err != nil {
		t.Fatalf("rebuild VSA boost fixture: %v", err)
	}
}

func seedDCDRouteWrongRows(t *testing.T, env *searchTestEnv) {
	t.Helper()
	statements := []string{
		`INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description, aliases_json)
		 VALUES ('other-domain', 'alice', '', 'ds-a', 'Checkout', 'wrong domain for boost degradation', '["checkout incident playbook"]')`,
		`INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name)
		 VALUES ('other-collection', 'other-domain', 'alice', '', 'ds-a', 'Runbooks')`,
		`INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title)
		 VALUES ('other-document', 'other-collection', 'other-domain', 'alice', '', 'ds-a', 'Checkout Incident Playbook')`,
	}
	for _, stmt := range statements {
		if _, err := env.db.Exec(strings.TrimSpace(stmt)); err != nil {
			t.Fatalf("seed wrong DCD route row: %v", err)
		}
	}
}

func seedDCDRouteBoostGraphWithoutRouteMetadata(t *testing.T, env *searchTestEnv) {
	t.Helper()
	env.insertVector("entities", "checkout-vector", []float32{1, 0, 0, 0}, map[string]any{
		"name":       "Checkout",
		"dataset_id": "ds-a",
	})
	statements := []string{
		`INSERT INTO graph_nodes(id, name, type, dataset_id)
		 VALUES ('checkout', 'Checkout', 'Service', 'ds-a')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id)
		 VALUES ('wrong-playbook', 'Checkout Incident Playbook Archive', 'Document', 'ds-a')`,
		`INSERT INTO graph_nodes(id, name, type, dataset_id)
		 VALUES ('pci-validator', 'PCI Validator', 'Service', 'ds-a')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id)
		 VALUES ('e-wrong', 'checkout', 'wrong-playbook', 'CALLS', 'ds-a')`,
		`INSERT INTO graph_edges(id, source_id, target_id, relationship_name, dataset_id)
		 VALUES ('e-correct', 'checkout', 'pci-validator', 'CALLS', 'ds-a')`,
	}
	for _, stmt := range statements {
		if _, err := env.db.Exec(strings.TrimSpace(stmt)); err != nil {
			t.Fatalf("seed DCD route boost graph without metadata: %v", err)
		}
	}
	if err := vsaStoreForDB(env.db, 1024, 1).RebuildFromGraph(context.Background(), "ds-a"); err != nil {
		t.Fatalf("rebuild missing-metadata VSA fixture: %v", err)
	}
}
