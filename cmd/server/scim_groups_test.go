package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
)

func newManagedSCIMTestApp(t *testing.T, dialect string) (*fiber.App, *access.SCIMStore) {
	t.Helper()
	t.Setenv("LEVARA_SCIM_TOKEN", "")
	store := newBrowserAuthStore(t, dialect)
	// The browser fixture uses a minimal empty key table. Use the complete base
	// schema for these document/group integration tests.
	if _, err := store.DB.Exec("DROP TABLE api_keys"); err != nil {
		t.Fatal(err)
	}
	if err := httpapi.MigrateSchema(store.DB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB.Exec(store.Q("INSERT INTO tenants(id,name) VALUES($1,$2)"), "tenant-a", "Tenant A"); err != nil {
		t.Fatal(err)
	}
	scimTestEnv(t)
	t.Setenv("LEVARA_SCIM_TENANT_ID", "tenant-a")
	app := fiber.New()
	if err := SCIMRoutes(app, *store, pgSCIMQuery{DB: store.DB, Q: store.Q}, nil); err != nil {
		t.Fatal(err)
	}
	return app, store
}

func managedSCIMRequest(t *testing.T, app *fiber.App, method, path, body string, status int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+scimTestToken)
	req.Header.Set("Content-Type", "application/scim+json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != status {
		t.Fatalf("%s %s status=%d want=%d response=%v", method, path, resp.StatusCode, status, out)
	}
	return out
}

func TestSCIMManagedGroupsAvailable(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, _ := newManagedSCIMTestApp(t, dialect)
			out := managedSCIMRequest(t, app, "GET", "/scim/v2/Groups", "", 200)
			if out["totalResults"] != float64(0) {
				t.Fatalf("unexpected groups: %v", out)
			}
		})
	}
}

func TestSCIMManagedConfigurationRequiresStableExistingTenant(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, store := newManagedSCIMTestApp(t, dialect)
			ctx := context.Background()
			for _, tenant := range []string{"tenant-a", "tenant-a"} {
				t.Setenv("LEVARA_SCIM_TENANT_ID", tenant)
				_, managed, err := configureSCIMStore(ctx, *store, "https://idp.corp.test")
				if err != nil || managed == nil || managed.TenantID != tenant {
					t.Fatalf("idempotent startup binding failed: %+v %v", managed, err)
				}
			}
			if _, err := store.DB.Exec("INSERT INTO tenants(id,name) VALUES('tenant-b','Tenant B')"); err != nil {
				t.Fatal(err)
			}
			for _, tenant := range []string{"missing", "tenant-b", " tenant-a "} {
				t.Setenv("LEVARA_SCIM_TENANT_ID", tenant)
				if _, _, err := configureSCIMStore(ctx, store, "https://idp.corp.test"); err == nil {
					t.Fatalf("invalid/reassigned tenant %q accepted", tenant)
				}
			}
			t.Setenv("LEVARA_SCIM_TENANT_ID", "tenant-a")
			if _, _, err := configureSCIMStore(ctx, access.SCIMStore{}, "https://idp.corp.test"); err == nil {
				t.Fatal("managed directory accepted nil SQL database")
			}
			t.Setenv("LEVARA_SCIM_TENANT_ID", "")
			legacy := fiber.New()
			if err := SCIMRoutes(legacy, *store, pgSCIMQuery{DB: store.DB, Q: store.Q}, nil); err != nil {
				t.Fatal(err)
			}
			managedSCIMRequest(t, legacy, "GET", "/scim/v2/Groups", "", 404)
			out := managedSCIMRequest(t, legacy, "GET", "/scim/v2/ResourceTypes", "", 200)
			if out["totalResults"] != float64(1) {
				t.Fatalf("legacy mode advertises managed resources: %v", out)
			}
		})
	}
}

func TestSCIMRejectsUnsupportedPatchOperation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, store := newManagedSCIMTestApp(t, dialect)
			u := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"operation@test.invalid","externalId":"subject","active":true}`, 201)
			id := u["id"].(string)
			managedSCIMRequest(t, app, "PATCH", "/scim/v2/Users/"+id, `{"Operations":[{"op":"delete","path":"active","value":false}]}`, 400)
			var active bool
			if err := store.DB.QueryRowContext(context.Background(), store.Q("SELECT is_active FROM users WHERE id=$1"), id).Scan(&active); err != nil || !active {
				t.Fatalf("unsupported patch changed user: active=%v err=%v", active, err)
			}
		})
	}
}

func TestSCIMManagedGroupHTTPMutationsAndDiscovery(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			app, store := newManagedSCIMTestApp(t, dialect)
			user := managedSCIMRequest(t, app, "POST", "/scim/v2/Users", `{"userName":"member@test.invalid","externalId":"member"}`, 201)["id"].(string)
			if _, err := store.DB.Exec(store.Q("INSERT INTO user_tenant(user_id,tenant_id) VALUES($1,'tenant-a')"), user); err != nil {
				t.Fatal(err)
			}
			body := `{"externalId":"readers","displayName":"Readers","members":[{"value":"` + user + `","type":"User"}]}`
			created := managedSCIMRequest(t, app, "POST", "/scim/v2/Groups", body, 201)
			id := created["id"].(string)
			again := managedSCIMRequest(t, app, "POST", "/scim/v2/Groups", `{"externalId":"readers","displayName":"Changed","members":[]}`, 200)
			if again["id"] != id || again["displayName"] != "Readers" {
				t.Fatalf("retry changed identity/state: %v", again)
			}
			version := created["meta"].(map[string]any)["version"].(string)
			request := func(method, path, body, etag string, status int) map[string]any {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+scimTestToken)
				req.Header.Set("Content-Type", "application/scim+json")
				if etag != "" {
					req.Header.Set("If-Match", etag)
				}
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var out map[string]any
				_ = json.NewDecoder(resp.Body).Decode(&out)
				if resp.StatusCode != status {
					t.Fatalf("status=%d want=%d body=%v", resp.StatusCode, status, out)
				}
				if !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/scim+json") || resp.Header.Get("Cache-Control") != "no-store" {
					t.Fatalf("response headers: %v", resp.Header)
				}
				return out
			}
			patch := `{"Operations":[{"op":"replace","path":"displayName","value":"Renamed"},{"op":"remove","path":"members[value eq \"` + user + `\"]"}]}`
			updated := request("PATCH", "/scim/v2/Groups/"+id, patch, version, 200)
			if updated["id"] != id || updated["displayName"] != "Renamed" || len(updated["members"].([]any)) != 0 {
				t.Fatalf("patch=%v", updated)
			}
			request("PATCH", "/scim/v2/Groups/"+id, `{"Operations":[{"op":"add","path":"members","value":[{"value":"`+user+`"}]}]}`, version, 412)
			for _, invalid := range []string{
				`{"Operations":[{"op":"delete","path":"members"}]}`,
				`{"Operations":[{"op":"add","path":"members","value":[{"value":"` + user + `","type":"Group"}]}]}`,
				`{"Operations":[{"op":"add","path":"members","value":[{"value":"` + user + `","$ref":"https://foreign.test/scim/v2/Users/` + user + `"}]}]}`,
				`{"Operations":[{"op":"replace","path":"displayName","value":"Partial"},{"op":"add","path":"members","value":[{"value":"unknown"}]}]}`,
			} {
				request("PATCH", "/scim/v2/Groups/"+id, invalid, "", 400)
			}
			current := managedSCIMRequest(t, app, "GET", "/scim/v2/Groups/"+id, "", 200)
			if current["displayName"] != "Renamed" {
				t.Fatalf("partial patch committed: %v", current)
			}
			replaced := request("PUT", "/scim/v2/Groups/"+id, `{"displayName":"Replaced","externalId":"readers","members":[{"value":"`+user+`"}]}`, "", 200)
			if len(replaced["members"].([]any)) != 1 {
				t.Fatalf("PUT lost members: %v", replaced)
			}
			request("PUT", "/scim/v2/Groups/"+id, `{"displayName":"Replaced","externalId":"other"}`, "", 400)
			filtered := managedSCIMRequest(t, app, "GET", "/scim/v2/Groups?filter=externalId%20eq%20%22readers%22", "", 200)
			if filtered["totalResults"] != float64(1) {
				t.Fatalf("filter=%v", filtered)
			}
			for _, filter := range []string{"displayName%20eq%20Readers", "displayName%20co%20%22Read%22", "externalId%20eq%20%22readers%22%20or%20displayName%20pr"} {
				managedSCIMRequest(t, app, "GET", "/scim/v2/Groups?filter="+filter, "", 400)
			}
			for path, total := range map[string]int{"/scim/v2/ResourceTypes": 2, "/scim/v2/Schemas": 3} {
				out := managedSCIMRequest(t, app, "GET", path, "", 200)
				if out["totalResults"] != float64(total) {
					t.Fatalf("discovery=%v", out)
				}
			}
			managedSCIMRequest(t, app, "DELETE", "/scim/v2/Groups/"+id, "", 204)
			managedSCIMRequest(t, app, "GET", "/scim/v2/Groups/"+id, "", 404)
			recreated := managedSCIMRequest(t, app, "POST", "/scim/v2/Groups", body, 201)
			if recreated["id"] == id {
				t.Fatal("recreate revived old grant identity")
			}
		})
	}
}

// Occupy either the pool or the directory write lock. The inherited short
// deadline keeps this test quick while exercising real database cancellation.
func TestSCIMRequestDeadlineCancelsSQLWait(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			for _, blocked := range []string{"pool", "directory-lock"} {
				for _, resource := range []string{"User", "Group"} {
					t.Run(blocked+"/"+resource, func(t *testing.T) {
						initial, store := newManagedSCIMTestApp(t, dialect)
						uid := managedSCIMRequest(t, initial, "POST", "/scim/v2/Users", `{"userName":"timeout@test.invalid","externalId":"timeout"}`, 201)["id"].(string)
						group := managedSCIMRequest(t, initial, "POST", "/scim/v2/Groups", `{"displayName":"Timeout","externalId":"timeout-group"}`, 201)
						app := fiber.New()
						app.Use(func(c *fiber.Ctx) error {
							ctx, cancel := context.WithTimeout(c.UserContext(), 75*time.Millisecond)
							defer cancel()
							c.SetUserContext(ctx)
							return c.Next()
						})
						if err := SCIMRoutes(app, *store, pgSCIMQuery{DB: store.DB, Q: store.Q}, nil); err != nil {
							t.Fatal(err)
						}
						store.DB.SetMaxOpenConns(1)
						if blocked == "directory-lock" {
							store.DB.SetMaxOpenConns(2)
						}
						conn, err := store.DB.Conn(context.Background())
						if err != nil {
							t.Fatal(err)
						}
						release := func() { _ = conn.Close() }
						if blocked == "directory-lock" {
							tx, err := conn.BeginTx(context.Background(), nil)
							if err != nil {
								t.Fatal(err)
							}
							if _, err = tx.ExecContext(context.Background(), store.Q("UPDATE scim_directories SET tenant_id=tenant_id WHERE issuer=$1"), "https://idp.corp.test"); err != nil {
								t.Fatal(err)
							}
							release = func() { _ = tx.Rollback(); _ = conn.Close() }
						}
						defer release()
						method, path, body := "DELETE", "/scim/v2/Users/"+uid, ""
						if resource == "Group" {
							method = "PATCH"
							path = "/scim/v2/Groups/" + group["id"].(string)
							body = `{"Operations":[{"op":"replace","path":"displayName","value":"Must not commit"}]}`
						}
						req := httptest.NewRequest(method, path, strings.NewReader(body))
						req.Header.Set("Authorization", "Bearer "+scimTestToken)
						req.Header.Set("Content-Type", "application/scim+json")
						started := time.Now()
						res, requestErr := app.Test(req, 1000)
						elapsed := time.Since(started)
						release()
						if requestErr != nil {
							t.Fatalf("request outlived its SQL deadline: %v", requestErr)
						}
						defer res.Body.Close()
						if res.StatusCode != 500 || elapsed > 800*time.Millisecond {
							t.Fatalf("wait was not cancelled: status=%d elapsed=%s", res.StatusCode, elapsed)
						}
						current := managedSCIMRequest(t, initial, "GET", "/scim/v2/Users/"+uid, "", 200)
						if current["active"] != true {
							t.Fatal("timed-out deactivation committed")
						}
						current = managedSCIMRequest(t, initial, "GET", "/scim/v2/Groups/"+group["id"].(string), "", 200)
						if current["displayName"] != "Timeout" || current["meta"].(map[string]any)["version"] != group["meta"].(map[string]any)["version"] {
							t.Fatal("timed-out group patch committed")
						}
					})
				}
			}
		})
	}
}

type deadlineSCIMQuery struct {
	scimQuerier
	remaining time.Duration
}

func (q *deadlineSCIMQuery) ByID(ctx context.Context, issuer, id string) (string, bool, string, error) {
	if deadline, ok := ctx.Deadline(); ok {
		q.remaining = time.Until(deadline)
	}
	return q.scimQuerier.ByID(ctx, issuer, id)
}
func TestSCIMGuardProvidesDefaultDeadline(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			initial, store := newManagedSCIMTestApp(t, dialect)
			uid := managedSCIMRequest(t, initial, "POST", "/scim/v2/Users", `{"userName":"deadline@test.invalid","externalId":"deadline"}`, 201)["id"].(string)
			query := &deadlineSCIMQuery{scimQuerier: pgSCIMQuery{DB: store.DB, Q: store.Q}}
			app := fiber.New()
			if err := SCIMRoutes(app, *store, query, nil); err != nil {
				t.Fatal(err)
			}
			managedSCIMRequest(t, app, "GET", "/scim/v2/Users/"+uid, "", 200)
			if query.remaining <= 0 || query.remaining > 5*time.Second {
				t.Fatalf("SCIM SQL context has no bounded deadline: %s", query.remaining)
			}
		})
	}
}
