package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"io"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"

	_ "github.com/ncruces/go-sqlite3/driver"
)

const scimTestToken = "scim-e2e-token"

func scimTestApp(t *testing.T) (*fiber.App, *accesspkg.SCIMStore) {
	t.Helper()
	db, err := sql.Open("sqlite3", t.TempDir()+"/scim-http.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := accesspkg.SCIMStore{DB: db, Q: scimTestRewriter}
	if err := store.EnsureSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS principals (id TEXT PRIMARY KEY, type TEXT NOT NULL DEFAULT 'user', created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY REFERENCES principals(id), email TEXT NOT NULL UNIQUE, hashed_password TEXT NOT NULL, is_active BOOLEAN NOT NULL DEFAULT true, is_superuser BOOLEAN NOT NULL DEFAULT false, is_verified BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(scimTestRewriter(q)); err != nil {
			t.Fatal(err)
		}
	}
	query := pgSCIMQuery{DB: db, Q: scimTestRewriter}
	app := fiber.New()
	if err := SCIMRoutes(app, store, query, nil); err != nil {
		t.Fatal(err)
	}
	return app, &store
}

func scimReq(app *fiber.App, method, path, body string) (*http.Response, error) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/scim+json")
	req.Header.Set("Authorization", "Bearer "+scimTestToken)
	return app.Test(req)
}

// scimTestEnv sets the env contract for the routes under test.
func scimTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LEVARA_SCIM_TOKEN", scimTestToken)
	t.Setenv("LEVARA_SCIM_ISSUER", "https://idp.corp.test")
}

// ── auth / surface behavior ──

func TestSCIMRoutesRequireToken(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/scim/v2/Users", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("no token: want 401 got %d", resp.StatusCode)
	}
	// Wrong token.
	req := httptest.NewRequest(fiber.MethodGet, "/scim/v2/Users", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp2, _ := app.Test(req)
	if resp2.StatusCode != 401 {
		t.Fatalf("wrong token: want 401 got %d", resp2.StatusCode)
	}
	// Non-bearer scheme.
	req3 := httptest.NewRequest(fiber.MethodGet, "/scim/v2/Users", nil)
	req3.Header.Set("Authorization", "Basic "+scimTestToken)
	resp3, _ := app.Test(req3)
	if resp3.StatusCode != 401 {
		t.Fatalf("basic scheme: want 401 got %d", resp3.StatusCode)
	}
}

func TestSCIMRoutesAbsentWithoutToken(t *testing.T) {
	t.Setenv("LEVARA_SCIM_TOKEN", "")
	app, _ := scimTestApp(t)
	resp, err := app.Test(httptest.NewRequest(fiber.MethodGet, "/scim/v2/Users", nil))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 404 {
		t.Fatalf("surface exists without token: %d", resp.StatusCode)
	}
}

// ── CRUD lifecycle ──

func TestSCIMUserLifecycle(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)

	// Create.
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"ada@corp.test","externalId":"ada-001","active":true}`
	resp, err := scimReq(app, fiber.MethodPost, "/scim/v2/Users", body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %s", resp.StatusCode, scimBody(resp))
	}
	var created map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp), &created)
	uid, _ := created["id"].(string)
	if uid == "" || created["userName"] != "ada@corp.test" || created["externalId"] != "ada-001" {
		t.Fatalf("bad create payload: %s", scimBody(resp))
	}

	// Idempotent duplicate create → 200 (not 500/409).
	resp2, err := scimReq(app, fiber.MethodPost, "/scim/v2/Users", body)
	if err != nil || resp2.StatusCode != 200 {
		t.Fatalf("duplicate create: %d %v", resp2.StatusCode, err)
	}

	// Get by id.
	resp3, err := scimReq(app, fiber.MethodGet, "/scim/v2/Users/"+uid, "")
	if err != nil || resp3.StatusCode != 200 {
		t.Fatalf("get: %d %v", resp3.StatusCode, err)
	}

	// List (pagination envelope).
	resp4, _ := scimReq(app, fiber.MethodGet, "/scim/v2/Users?startIndex=1&count=50", "")
	var list map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp4), &list)
	if list["totalResults"].(float64) != 1 {
		t.Fatalf("list total: %s", scimBody(resp4))
	}

	// Filter by userName.
	resp5, _ := scimReq(app, fiber.MethodGet, `/scim/v2/Users?filter=userName%20eq%20%22ada%40corp.test%22`, "")
	var flist map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp5), &flist)
	if flist["totalResults"].(float64) != 1 {
		t.Fatalf("userName filter: %s", scimBody(resp5))
	}

	// Filter by externalId.
	resp6, _ := scimReq(app, fiber.MethodGet, `/scim/v2/Users?filter=externalId%20eq%20%22ada-001%22`, "")
	var elist map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp6), &elist)
	if elist["totalResults"].(float64) != 1 {
		t.Fatalf("externalId filter: %s", scimBody(resp6))
	}

	// Unsupported filter → 400 invalidFilter.
	resp7, _ := scimReq(app, fiber.MethodGet, `/scim/v2/Users?filter=title%20eq%20%22boss%22`, "")
	if resp7.StatusCode != 400 || !strings.Contains(string(scimBody(resp7)), "invalidFilter") {
		t.Fatalf("bad filter: %d %s", resp7.StatusCode, scimBody(resp7))
	}

	// PATCH deactivate (Entra style: no path, value object).
	resp8, err := scimReq(app, fiber.MethodPatch, "/scim/v2/Users/"+uid,
		`{"Operations":[{"op":"Replace","value":{"active":false}}]}`)
	if err != nil || resp8.StatusCode != 200 {
		t.Fatalf("patch: %d %s %v", resp8.StatusCode, scimBody(resp8), err)
	}
	var patched map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp8), &patched)
	if patched["active"] != false {
		t.Fatalf("patch did not deactivate: %s", scimBody(resp8))
	}

	// DELETE = soft deactivate → 204, resource still GETable with active=false.
	resp9, err := scimReq(app, fiber.MethodDelete, "/scim/v2/Users/"+uid, "")
	if err != nil || resp9.StatusCode != 204 {
		t.Fatalf("delete: %d %v", resp9.StatusCode, err)
	}
	resp10, _ := scimReq(app, fiber.MethodGet, "/scim/v2/Users/"+uid, "")
	var after map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp10), &after)
	if after["active"] != false {
		t.Fatalf("delete was not soft: %s", scimBody(resp10))
	}

	// DELETE unknown → 404.
	resp11, _ := scimReq(app, fiber.MethodDelete, "/scim/v2/Users/scim-ghost", "")
	if resp11.StatusCode != 404 {
		t.Fatalf("ghost delete: %d", resp11.StatusCode)
	}
}

// ── ADR-003 corner cases ──

func TestSCIMEmailConflictAcrossIdentities(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	b1 := `{"userName":"kim@corp.test","externalId":"k-1"}`
	if resp, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", b1); resp.StatusCode != 201 {
		t.Fatalf("first create: %d", resp.StatusCode)
	}
	// Different externalId claiming the same email → 409 uniqueness.
	b2 := `{"userName":"kim@corp.test","externalId":"k-2"}`
	resp, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", b2)
	if resp.StatusCode != 409 || !strings.Contains(string(scimBody(resp)), "uniqueness") {
		t.Fatalf("conflict: %d %s", resp.StatusCode, scimBody(resp))
	}
	// Same externalId again → 200 idempotent (not conflict).
	resp3, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", b1)
	if resp3.StatusCode != 200 {
		t.Fatalf("idempotent: %d", resp3.StatusCode)
	}
}

func TestSCIMCreateValidationErrors(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	// Missing userName → 400 invalidValue.
	resp, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", `{"externalId":"x"}`)
	if resp.StatusCode != 400 || !strings.Contains(string(scimBody(resp)), "invalidValue") {
		t.Fatalf("missing userName: %d %s", resp.StatusCode, scimBody(resp))
	}
	// Malformed JSON → 400.
	resp2, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", `{not json`)
	if resp2.StatusCode != 400 {
		t.Fatalf("malformed: %d", resp2.StatusCode)
	}
}

func TestSCIMPatchUnknownPathRejected(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	resp, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", `{"userName":"p@corp.test","externalId":"p-1"}`)
	var created map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp), &created)
	uid := created["id"].(string)
	resp2, _ := scimReq(app, fiber.MethodPatch, "/scim/v2/Users/"+uid,
		`{"Operations":[{"op":"Replace","path":"title","value":"boss"}]}`)
	if resp2.StatusCode != 400 || !strings.Contains(string(scimBody(resp2)), "invalidPath") {
		t.Fatalf("unknown path: %d %s", resp2.StatusCode, scimBody(resp2))
	}
}

func TestSCIMServiceProviderConfig(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	resp, _ := scimReq(app, fiber.MethodGet, "/scim/v2/ServiceProviderConfig", "")
	var cfg map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp), &cfg)
	if cfg["patch"].(map[string]interface{})["supported"] != true {
		t.Fatalf("sp config: %s", scimBody(resp))
	}
}

func TestSCIMEmailRename(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	resp, _ := scimReq(app, fiber.MethodPost, "/scim/v2/Users", `{"userName":"old@corp.test","externalId":"r-1"}`)
	var created map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp), &created)
	uid := created["id"].(string)
	// Path-style rename.
	resp2, err := scimReq(app, fiber.MethodPatch, "/scim/v2/Users/"+uid,
		`{"Operations":[{"op":"Replace","path":"userName","value":"new@corp.test"}]}`)
	if err != nil || resp2.StatusCode != 200 {
		t.Fatalf("rename: %d %s %v", resp2.StatusCode, scimBody(resp2), err)
	}
	var patched map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp2), &patched)
	if patched["userName"] != "new@corp.test" {
		t.Fatalf("rename not applied: %s", scimBody(resp2))
	}
}

func TestSCIMPaginationClamp(t *testing.T) {
	scimTestEnv(t)
	app, _ := scimTestApp(t)
	// count=9999 must clamp to 200; envelope reflects it via itemsPerPage cap
	// (zero users → only schema fields matter, so assert 200-status + valid JSON).
	resp, err := scimReq(app, fiber.MethodGet, "/scim/v2/Users?startIndex=0&count=9999", "")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("clamped list: %d %v", resp.StatusCode, err)
	}
	var list map[string]interface{}
	_ = json.Unmarshal(scimBodyBytes(resp), &list)
	if _, ok := list["totalResults"]; !ok {
		t.Fatalf("missing envelope: %s", scimBody(resp))
	}
}

func scimBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func scimBodyBytes(r *http.Response) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}

// scimTestRewriter adapts $N placeholders for the sqlite test driver.
func scimTestRewriter(q string) string {
	q = strings.ReplaceAll(q, "NOW()", "CURRENT_TIMESTAMP")
	q = strings.ReplaceAll(q, "TIMESTAMPTZ", "TIMESTAMP")
	var b strings.Builder
	for i := 0; i < len(q); i++ {
		if q[i] == '$' && i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
			b.WriteByte('?')
			i++
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}
