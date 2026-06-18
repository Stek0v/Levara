package http

import (
	"strings"
	"testing"
)

func TestSearchHandlerDCDRouteObserveDebugMetadata(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "observe")
	t.Setenv("LEVARA_DCD_ROUTE_MAX_CANDIDATES", "2")
	t.Setenv("LEVARA_DCD_ROUTE_MIN_CONFIDENCE", "0.01")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteObserveVector(t, env)
	env.startWithUser("alice")

	status, out := postSearchAny(t, env, map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	body, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("response type=%T, want object", out)
	}
	debug, ok := body["debug"].(map[string]any)
	if !ok {
		t.Fatalf("debug missing: %#v", body)
	}
	dcd, ok := debug["dcd_route"].(map[string]any)
	if !ok {
		t.Fatalf("dcd_route debug missing: %#v", debug)
	}
	if dcd["mode"] != dcdRouteObserveMode {
		t.Fatalf("dcd_route.mode=%v, want %s", dcd["mode"], dcdRouteObserveMode)
	}
	if dcd["candidate_count"] != float64(1) {
		t.Fatalf("candidate_count=%v, want 1", dcd["candidate_count"])
	}
	candidates, ok := dcd["candidates"].([]any)
	if !ok || len(candidates) != 1 {
		t.Fatalf("candidates=%#v, want one", dcd["candidates"])
	}
	top, ok := candidates[0].(map[string]any)
	if !ok {
		t.Fatalf("top candidate type=%T", candidates[0])
	}
	if top["domain_id"] != "billing-domain" || top["collection_id"] != "runbooks-collection" || top["document_id"] != "checkout-playbook" {
		t.Fatalf("top candidate=%#v", top)
	}
}

func TestSearchHandlerDCDRouteObserveSkipsChunks(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "observe")
	t.Setenv("LEVARA_DCD_ROUTE_MIN_CONFIDENCE", "0.01")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteObserveVector(t, env)
	env.startWithUser("alice")

	status, out := postSearchAny(t, env, map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "CHUNKS",
		"collection":    "entities",
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	body, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("response type=%T, want object", out)
	}
	debug, ok := body["debug"].(map[string]any)
	if !ok {
		t.Fatalf("debug missing: %#v", body)
	}
	if _, ok := debug["dcd_route"]; ok {
		t.Fatalf("dcd_route debug present for CHUNKS: %#v", debug["dcd_route"])
	}
}

func TestSearchHandlerDCDRouteObserveKeepsLegacyArrayWhenDebugOff(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "observe")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteObserveVector(t, env)
	env.startWithUser("alice")

	status, out := postSearchAny(t, env, map[string]any{
		"query_text": "checkout incident playbook",
		"query_type": "CHUNKS",
		"collection": "entities",
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	if _, ok := out.([]any); !ok {
		t.Fatalf("response type=%T, want legacy array", out)
	}
}

func TestSearchHandlerDCDRouteObserveDisabledByDefault(t *testing.T) {
	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteObserveVector(t, env)
	env.startWithUser("alice")

	status, out := postSearchAny(t, env, map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	body, _ := out.(map[string]any)
	debug, _ := body["debug"].(map[string]any)
	if _, ok := debug["dcd_route"]; ok {
		t.Fatalf("dcd_route debug present when flag is disabled: %#v", debug["dcd_route"])
	}
}

func TestSearchHandlerDCDRouteFilterDowngradesToBoostDebug(t *testing.T) {
	t.Setenv("LEVARA_DCD_ROUTER", "filter")

	env := newSearchTestEnv(t)
	prepareDCDRouteObserveSchema(t, env)
	env.insertUser("alice", "alice@example.com", false)
	env.insertDataset("ds-a", "alice")
	seedDCDRouteObserveRows(t, env)
	seedDCDRouteObserveVector(t, env)
	env.startWithUser("alice")

	status, out := postSearchAny(t, env, map[string]any{
		"query_text":    "checkout incident playbook",
		"query_type":    "GRAPH_COMPLETION",
		"collection":    "entities",
		"include_debug": true,
	})
	if status != 200 {
		t.Fatalf("status=%d, want 200", status)
	}
	body, _ := out.(map[string]any)
	debug, _ := body["debug"].(map[string]any)
	dcd, ok := debug["dcd_route"].(map[string]any)
	if !ok {
		t.Fatalf("dcd_route debug missing: %#v", debug)
	}
	if dcd["requested_mode"] != dcdRouteFilterMode {
		t.Fatalf("requested_mode=%v, want %s", dcd["requested_mode"], dcdRouteFilterMode)
	}
	if dcd["mode"] != dcdRouteBoostMode {
		t.Fatalf("mode=%v, want effective %s", dcd["mode"], dcdRouteBoostMode)
	}
	if dcd["filter_active"] != false {
		t.Fatalf("filter_active=%v, want false until filter is implemented", dcd["filter_active"])
	}
}

func prepareDCDRouteObserveSchema(t *testing.T, env *searchTestEnv) {
	t.Helper()
	if _, err := env.db.Exec(`ALTER TABLE datasets ADD COLUMN name TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("prepare datasets.name: %v", err)
	}
	if err := MigrateSchema(env.db); err != nil {
		t.Fatalf("migration: %v", err)
	}
}

func seedDCDRouteObserveRows(t *testing.T, env *searchTestEnv) {
	t.Helper()
	statements := []string{
		`INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name, description, aliases_json)
		 VALUES ('billing-domain', 'alice', '', 'ds-a', 'Billing', 'payments and invoices', '["money ops"]')`,
		`INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name, description, aliases_json)
		 VALUES ('runbooks-collection', 'billing-domain', 'alice', '', 'ds-a', 'Runbooks', 'incident response', '["incident guides"]')`,
		`INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title, description, aliases_json)
		 VALUES ('checkout-playbook', 'runbooks-collection', 'billing-domain', 'alice', '', 'ds-a', 'Checkout Incident Playbook', 'checkout failures', '["checkout incident"]')`,
		`INSERT INTO knowledge_domains(id, owner_id, team_id, dataset_id, name)
		 VALUES ('bob-domain', 'bob', '', 'ds-b', 'Billing')`,
		`INSERT INTO knowledge_collections(id, domain_id, owner_id, team_id, dataset_id, name)
		 VALUES ('bob-collection', 'bob-domain', 'bob', '', 'ds-b', 'Runbooks')`,
		`INSERT INTO knowledge_documents(id, collection_id, domain_id, owner_id, team_id, dataset_id, title)
		 VALUES ('bob-playbook', 'bob-collection', 'bob-domain', 'bob', '', 'ds-b', 'Checkout Incident Playbook')`,
	}
	for _, stmt := range statements {
		if _, err := env.db.Exec(strings.TrimSpace(stmt)); err != nil {
			t.Fatalf("seed DCD route observe row: %v", err)
		}
	}
}

func seedDCDRouteObserveVector(t *testing.T, env *searchTestEnv) {
	t.Helper()
	env.insertVector("entities", "checkout-playbook-chunk", []float32{1, 0, 0, 0}, map[string]any{
		"dataset_id": "ds-a",
		"name":       "Checkout Incident Playbook",
		"text":       "checkout incident playbook",
	})
}
