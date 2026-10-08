package http

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

func TestDatasetShareListAuthority(t *testing.T) {
	for _, scenario := range []string{"owner", "viewer", "readonly-key", "empty", "foreign", "inactive", "jwt-revoked", "expired", "tenant-removed", "key-revoked", "key-permission-changed", "unverified", "target-outside-tenant"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				user, key := "viewer", ""
				switch scenario {
				case "owner", "empty":
					user = "owner"
				case "foreign":
					user = "foreign"
				case "readonly-key", "key-revoked", "key-permission-changed":
					user = ""
					key = "share-list-key"
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'viewer','read')", key, apikeyHash(key))
				case "target-outside-tenant":
					f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('viewer','b')")
				}
				if scenario == "empty" {
					f.exec("DELETE FROM dataset_shares WHERE dataset_id='alpha'")
				}
				app := audienceApp(f, func(c *fiber.Ctx) error {
					switch scenario {
					case "inactive":
						f.exec("UPDATE users SET is_active=false WHERE id='viewer'")
					case "jwt-revoked":
						f.exec("INSERT INTO credential_epochs(user_id,epoch) VALUES('viewer',1)")
					case "expired":
						jwt := c.Locals("verified_jwt").(jwtPayload)
						jwt.Exp = 1
						c.Locals("verified_jwt", jwt)
					case "tenant-removed":
						f.exec("DELETE FROM user_tenant WHERE user_id='viewer' AND tenant_id='a'")
					case "key-revoked":
						f.exec("UPDATE api_keys SET revoked=true WHERE id='share-list-key'")
					case "key-permission-changed":
						f.exec("UPDATE api_keys SET permissions='delete' WHERE id='share-list-key'")
					}
					return c.Next()
				})
				if scenario == "unverified" {
					app = fiber.New(fiber.Config{DisableStartupMessage: true})
					app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "owner"); return c.Next() })
					RegisterRBACAPI(app, APIConfig{DB: f.db})
					user = ""
				}
				req := httptest.NewRequest("GET", "/datasets/alpha/shares", nil)
				tenant := "a"
				if scenario == "foreign" || scenario == "target-outside-tenant" {
					tenant = "b"
				}
				req.Header.Set("X-Tenant-Id", tenant)
				if user != "" {
					req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", "chat-scope-secret"))
				}
				if key != "" {
					req.Header.Set("X-Api-Key", key)
				}
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				allowed := scenario == "owner" || scenario == "viewer" || scenario == "readonly-key" || scenario == "empty"
				if allowed {
					if resp.StatusCode != 200 {
						t.Fatalf("status=%d body=%s", resp.StatusCode, body)
					}
					var shares []ShareDTO
					if err := json.Unmarshal(body, &shares); err != nil {
						t.Fatal(err)
					}
					if shares == nil {
						t.Fatal("empty list must be []")
					}
					if scenario == "empty" && len(shares) != 0 {
						t.Fatal("nonempty list")
					}
					if scenario != "empty" && len(shares) == 0 {
						t.Fatal("missing share rows")
					}
				} else if resp.StatusCode != 403 {
					t.Fatalf("invalid authority accepted: status=%d body=%s", resp.StatusCode, body)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("response leaked SQL")
				}
			})
		})
	}
}

func TestDatasetShareListRetainsAuthority(t *testing.T) {
	for _, pool := range []int{1, 2} {
		t.Run(fmt.Sprintf("pool%d", pool), func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(pool)
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/datasets/alpha/shares")
				// The route supplies Params while the handler preserves the actual response.
				app := fiber.New(fiber.Config{DisableStartupMessage: true})
				app.Use(func(route *fiber.Ctx) error {
					route.Locals("user_id", "owner")
					route.Locals("tenant_id", "a")
					route.Locals("verified_jwt", jwtPayload{Sub: "owner", Exp: time.Now().Add(time.Hour).Unix()})
					return route.Next()
				})
				cfg := f.cfg
				cfg.RequireAuth = true
				app.Get("/datasets/:id/shares", datasetSharesListHandler(cfg))
				app.Handler()(raw)
				stream, ok := raw.Response.BodyStream().(io.ReadCloser)
				if !ok || f.db.Stats().InUse != 1 {
					t.Fatal("share metadata lacks retained SQL")
				}
				defer stream.Close()
				buf := make([]byte, 8)
				if n, err := stream.Read(buf); n == 0 || err != nil || f.db.Stats().InUse != 1 {
					t.Fatalf("partial drain lost fence %d %v", n, err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				if _, err := f.db.ExecContext(ctx, "DELETE FROM dataset_shares WHERE id='viewer-share'"); err == nil {
					t.Fatal("revoke overtook body")
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec("DELETE FROM dataset_shares WHERE id='viewer-share'"); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("closed body retained SQL")
				}
			})
		})
	}
}
