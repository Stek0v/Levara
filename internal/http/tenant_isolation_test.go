// tenant_isolation_test.go — negative tests for tenant isolation across the
// three request surfaces called out in docs/full-testing-scenarios.md (P4
// Enterprise + the S8 security-isolation suite): graph, search, workspace.
//
// The existing RBAC tests (rbac_search_test.go, workspace_test.go) prove the
// dataset-share axis end to end. These tests cover the *tenant* axis instead,
// and only its failure modes:
//
//   - Search surface: a spoofed X-Tenant-Id for a tenant the caller does not
//     belong to is rejected at the TenantMiddleware boundary; the search
//     handler never runs, so no downstream tenant_id is established (P4:
//     "expect HTTP 403 and no downstream tenant_id local"), and the generic
//     denial leaks neither the spoofed tenant id nor the target collection.
//   - Graph surface: a cross-dataset graph edge from a visible node to a
//     foreign-tenant node must not leak the foreign node's name into the
//     caller's graph context (the both-endpoints filter in
//     graphContextFromPostgres).
//   - Workspace surface: a spoofed X-Tenant-Id on a workspace mutation is
//     intercepted by the tenant gate before any workspace handler runs, and
//     the denial leaks no project id or file path.
package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Search surface: a foreign X-Tenant-Id is rejected before the search handler
// runs, no downstream tenant_id is established, and the denial leaks nothing.
func TestTenantIsolation_SearchForeignTenantHeaderRejectedNoLeak(t *testing.T) {
	db := newTenantTestDB(t) // seeds user_tenant with (user-a, tenant-a) only

	var reached bool
	var downstreamTenant string
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-a")
		return c.Next()
	})
	app.Use(TenantMiddleware(AccessConfig{DB: db}))
	app.Post("/search/text", func(c *fiber.Ctx) error {
		reached = true
		downstreamTenant = ResolveTenantID(c)
		return c.JSON(fiber.Map{"results": []any{}})
	})

	const foreignTenant = "tenant-b"
	const privateColl = "tenant-b-private-collection"
	payload, _ := json.Marshal(map[string]any{
		"query_text": "find competitor secrets",
		"query_type": "CHUNKS",
		"collection": privateColl,
	})

	req := httptest.NewRequest("POST", "/search/text", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", foreignTenant)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("foreign-tenant search status=%d, want 403; body=%s", resp.StatusCode, body)
	}
	if reached {
		t.Fatalf("search handler ran under spoofed tenant (downstream tenant_id=%q)", downstreamTenant)
	}
	for _, leak := range []string{foreignTenant, privateColl} {
		if bytes.Contains(body, []byte(leak)) {
			t.Fatalf("denied response leaked %q: %s", leak, body)
		}
	}

	// Positive control: the user's own tenant resolves and reaches the handler,
	// proving the 403 above is the isolation gate, not an unrelated failure.
	reached, downstreamTenant = false, ""
	req2 := httptest.NewRequest("POST", "/search/text", bytes.NewReader(payload))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Tenant-Id", "tenant-a")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != fiber.StatusOK || !reached {
		t.Fatalf("own-tenant search status=%d reached=%v, want 200/true", resp2.StatusCode, reached)
	}
	if downstreamTenant != "tenant-a" {
		t.Fatalf("own-tenant downstream tenant_id=%q, want tenant-a", downstreamTenant)
	}
}

// Graph surface: a cross-dataset edge must not leak the foreign node's name
// into the caller's graph context. The caller (user-b) can see only ds-b; a
// graph edge from their node (ds-b) to a foreign node (ds-a) is dropped by the
// both-endpoints dataset filter in graphContextFromPostgres, while a same-tenant
// edge stays visible.
func TestTenantIsolation_GraphContextDoesNotLeakForeignDatasetNode(t *testing.T) {
	env := newSearchTestEnv(t)
	env.cfg.LLMProvider = nil
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_MODEL", "")
	env.startWithUser("user-b")

	env.insertUser("user-a", "a@example.com", false)
	env.insertUser("user-b", "b@example.com", false)
	env.insertDataset("ds-a", "user-a") // foreign tenant's dataset
	env.insertDataset("ds-b", "user-b") // caller's own dataset

	vec := []float32{1, 0, 0, 0}
	// Vector hit in the caller's dataset; its name seeds the graph expansion.
	env.insertVector("entities", "e-mine", vec, map[string]any{
		"name": "my-entity", "dataset_id": "ds-b",
	})

	const foreignName = "secret-foreign-entity"
	env.insertNode("n-mine", "my-entity", "Person", "ds-b")
	env.insertNode("n-friend", "my-friend", "Person", "ds-b")
	env.insertNode("n-foreign", foreignName, "Person", "ds-a")
	env.insertEdge("e-ok", "n-mine", "n-friend", "KNOWS")    // same tenant → visible
	env.insertEdge("e-leak", "n-mine", "n-foreign", "KNOWS") // cross-tenant → must be filtered

	_, body := env.postSearch(map[string]any{
		"query_text": "my-entity",
		"query_type": "GRAPH_COMPLETION",
		"collection": "entities",
	})

	raw, _ := json.Marshal(body)
	if strings.Contains(string(raw), foreignName) {
		t.Fatalf("foreign-dataset node leaked into graph response: %s", raw)
	}
	// Positive control: the same-tenant edge must still surface, proving the
	// graph expansion ran and only the cross-dataset edge was filtered.
	ctxArr, _ := body["context"].([]any)
	var sawFriend bool
	for _, c := range ctxArr {
		if s, _ := c.(string); strings.Contains(s, "my-friend") {
			sawFriend = true
		}
	}
	if !sawFriend {
		t.Fatalf("same-tenant graph edge missing from context %v; expansion may not have run", ctxArr)
	}
}

// Workspace surface: a foreign X-Tenant-Id on a workspace mutation is rejected
// by the tenant gate before any workspace handler runs, and the generic denial
// leaks neither the project id nor the file path.
func TestTenantIsolation_WorkspaceForeignTenantHeaderRejectedNoLeak(t *testing.T) {
	cfg, closeFn := newWorkspaceACLTestConfig(t)
	defer closeFn()
	if _, err := cfg.DB.Exec(`CREATE TABLE user_tenant (user_id TEXT, tenant_id TEXT)`); err != nil {
		t.Fatalf("create user_tenant: %v", err)
	}
	if _, err := cfg.DB.Exec(`INSERT INTO user_tenant(user_id, tenant_id) VALUES ('user-a', 'tenant-a')`); err != nil {
		t.Fatalf("seed user_tenant: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "user-a")
		return c.Next()
	})
	app.Use(TenantMiddleware(cfg.Access()))
	RegisterWorkspaceAPI(app, cfg)

	const foreignTenant = "tenant-b"
	const foreignProject = "tenant-b-private-project"
	const secretPath = "docs/secret-roadmap.md"
	payload, _ := json.Marshal(map[string]any{
		"project_id": foreignProject,
		"branch":     "main",
		"path":       secretPath,
		"text":       "# Secret\n\nForeign tenant content.",
		"index":      false,
	})

	req := httptest.NewRequest("POST", "/workspace/write", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-Id", foreignTenant)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("foreign-tenant workspace write status=%d, want 403; body=%s", resp.StatusCode, body)
	}
	// The tenant gate intercepts before the workspace handler: the denial is the
	// generic tenant message, never a workspace response.
	if !bytes.Contains(body, []byte("not a member of this tenant")) {
		t.Fatalf("workspace denial did not come from the tenant gate: %s", body)
	}
	for _, leak := range []string{foreignTenant, foreignProject, secretPath} {
		if bytes.Contains(body, []byte(leak)) {
			t.Fatalf("denied workspace response leaked %q: %s", leak, body)
		}
	}

	// Positive control: the caller's own tenant clears the tenant gate (whatever
	// the workspace handler then decides), proving the 403 above is isolation.
	req2 := httptest.NewRequest("POST", "/workspace/write", bytes.NewReader(payload))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-Tenant-Id", "tenant-a")
	resp2, err := app.Test(req2)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if bytes.Contains(body2, []byte("not a member of this tenant")) {
		t.Fatalf("own-tenant workspace request wrongly blocked by the tenant gate: %s", body2)
	}
}
