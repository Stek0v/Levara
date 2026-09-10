package http

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestAdminDenialStopsEffect(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			if _, err := db.Exec(`UPDATE users SET is_superuser=true WHERE id='bob'`); err != nil {
				t.Fatal(err)
			}
			for _, user := range []string{"", "alice", "missing", "bob"} {
				app := fiber.New()
				called := false
				app.Post("/", func(c *fiber.Ctx) error {
					c.Locals("user_id", user)
					if err := requireSuperuser(c, APIConfig{DB: db}); err != nil {
						return err
					}
					called = true
					return c.SendStatus(200)
				})
				resp, err := app.Test(httptest.NewRequest("POST", "/", nil))
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if called != (user == "bob") {
					t.Errorf("user %q: effect=%v", user, called)
				}
				if user == "bob" {
					if _, err := db.Exec(`UPDATE users SET is_active=false WHERE id='bob'`); err != nil {
						t.Fatal(err)
					}
					called = false
					resp, err := app.Test(httptest.NewRequest("POST", "/", nil))
					if err != nil {
						t.Fatal(err)
					}
					resp.Body.Close()
					if called {
						t.Error("inactive administrator reached effect")
					}
				}
			}
		})
	}
}

func TestGlobalResourceRoutesRequireActiveSuperuser(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{"GET", "/api/v1/collections"},
		{"POST", "/api/v1/collections"},
		{"DELETE", "/api/v1/collections/demo"},
		{"DELETE", "/api/v1/collections/demo/records/id"},
		{"GET", "/api/v1/collections/demo/meta"},
		{"PUT", "/api/v1/collections/demo/meta"},
		{"POST", "/api/v1/collections/demo/rename"},
		{"POST", "/api/v1/reembed"},
		{"GET", "/api/v1/reembed/run/status"},
		{"POST", "/api/v1/search/dual"},
		{"POST", "/api/v1/embedding-migrations"},
		{"GET", "/api/v1/embedding-migrations/run/status"},
		{"POST", "/api/v1/embedding-migrations/run/retry"},
		{"POST", "/api/v1/embedding-migrations/run/cutover"},
		{"GET", "/api/v1/embedding-migrations/dual-write"},
		{"DELETE", "/api/v1/embedding-migrations/dual-write/source"},
		{"POST", "/api/v1/embedding-migrations/shadow-read"},
		{"POST", "/api/v1/insert"},
		{"POST", "/api/v1/batch_insert"},
		{"POST", "/api/v1/search"},
		{"POST", "/api/v1/delete"},
	}

	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t, dialect)
			if _, err := db.Exec(`UPDATE users SET is_superuser=true WHERE id='bob'`); err != nil {
				t.Fatal(err)
			}
			newApp := func(user string, requireAuth bool) *fiber.App {
				cfg := APIConfig{DB: db, RequireAuth: requireAuth, StoragePath: t.TempDir(), WorkspacePath: t.TempDir()}
				app := fiber.New()
				app.Use(func(c *fiber.Ctx) error {
					c.Locals("user_id", user)
					return c.Next()
				})
				api := app.Group("/api/v1")
				RegisterLegacyVectorAPI(api, cfg, NewHandler(nil, 3))
				RegisterAPI(api, cfg)
				return app
			}
			check := func(app *fiber.App, wantForbidden bool) {
				t.Helper()
				for _, route := range routes {
					var body io.Reader
					if route.method != "GET" {
						body = strings.NewReader(`{}`)
					}
					resp, err := app.Test(httptest.NewRequest(route.method, route.path, body))
					if err != nil {
						t.Fatalf("%s %s: %v", route.method, route.path, err)
					}
					resp.Body.Close()
					if (resp.StatusCode == fiber.StatusForbidden) != wantForbidden {
						t.Errorf("%s %s: status=%d, wantForbidden=%v", route.method, route.path, resp.StatusCode, wantForbidden)
					}
				}
			}

			check(newApp("alice", true), true)
			adminApp := newApp("bob", true)
			check(adminApp, false)
			req := httptest.NewRequest("POST", "/api/v1/search", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			resp, err := adminApp.Test(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != fiber.StatusBadRequest || !strings.Contains(string(body), "vector is required") {
				t.Fatalf("legacy /search owner changed: status=%d body=%s", resp.StatusCode, body)
			}
			if _, err := db.Exec(`UPDATE users SET is_active=false WHERE id='bob'`); err != nil {
				t.Fatal(err)
			}
			check(newApp("bob", true), true)
			check(newApp("", false), false)
		})
	}
}
