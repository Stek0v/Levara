package http

import (
	"net/http/httptest"
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
