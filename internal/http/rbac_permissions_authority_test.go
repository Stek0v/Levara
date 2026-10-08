package http

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/valyala/fasthttp"
)

func TestPermissionsMeAuthority(t *testing.T) {
	for _, scenario := range []string{"viewer", "no-project-grants", "readonly-key", "inactive", "jwt-revoked", "expired", "tenant-removed", "key-revoked", "unverified", "share-query-error", "superuser-query-error", "timestamp-error"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				user, key := "viewer", ""
				if scenario == "no-project-grants" {
					user = "peer"
				}
				if scenario == "readonly-key" || scenario == "key-revoked" {
					user = ""
					key = "permissions-key"
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'viewer','read')", key, apikeyHash(key))
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
						f.exec("UPDATE api_keys SET revoked=true WHERE id='permissions-key'")
					case "share-query-error":
						f.exec("ALTER TABLE dataset_shares RENAME COLUMN role TO broken_role")
					case "timestamp-error":
						f.exec("UPDATE dataset_shares SET created_at='10000-01-01 00:00:00' WHERE id='viewer-share'")
					case "superuser-query-error":
						f.exec("ALTER TABLE users RENAME COLUMN is_superuser TO broken_superuser")
					}
					return c.Next()
				})
				if scenario == "unverified" {
					app = fiber.New(fiber.Config{DisableStartupMessage: true})
					app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "viewer"); return c.Next() })
					RegisterRBACAPI(app, APIConfig{DB: f.db})
					user = ""
				}
				req := httptest.NewRequest("GET", "/permissions/me", nil)
				req.Header.Set("X-Tenant-Id", "a")
				if user != "" {
					req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", "chat-scope-secret"))
				}
				if key != "" {
					req.Header.Set("X-API-Key", key)
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
				allowed := scenario == "viewer" || scenario == "no-project-grants" || scenario == "readonly-key"
				if allowed {
					if resp.StatusCode != 200 {
						t.Fatalf("status=%d body=%s", resp.StatusCode, body)
					}
					var got struct {
						UserID string     `json:"user_id"`
						Shares []ShareDTO `json:"shares"`
					}
					if err := json.Unmarshal(body, &got); err != nil {
						t.Fatal(err)
					}
					want := "viewer"
					if scenario == "no-project-grants" {
						want = "peer"
					}
					if got.UserID != want || got.Shares == nil {
						t.Fatalf("incorrect self diagnostic: %s", body)
					}
					if scenario != "no-project-grants" && len(got.Shares) == 0 {
						t.Fatal("missing own grant")
					}
					for _, s := range got.Shares {
						if s.UserID != want {
							t.Fatal("other identity grant leaked")
						}
						if _, err := time.Parse(time.RFC3339, s.CreatedAt); err != nil {
							t.Fatalf("invalid timestamp %q: %v", s.CreatedAt, err)
						}
					}
				} else {
					want := 403
					if scenario == "share-query-error" || scenario == "superuser-query-error" || scenario == "timestamp-error" {
						want = 503
					}
					if resp.StatusCode != want {
						t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, want, body)
					}
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("diagnostic leaked SQL")
				}
			})
		})
	}
}

func TestPermissionsMeRetainsAuthority(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		for _, kind := range []string{"partial", "expiry"} {
			t.Run(kind, func(t *testing.T) {
				f.db.SetMaxOpenConns(1)
				expiry := time.Now().Add(time.Hour).Unix()
				if kind == "expiry" {
					expiry = time.Now().Unix() + 2
				}
				raw := &fasthttp.RequestCtx{}
				raw.Request.Header.SetMethod("GET")
				raw.Request.SetRequestURI("/permissions/me")
				app := fiber.New(fiber.Config{DisableStartupMessage: true})
				app.Use(func(c *fiber.Ctx) error {
					c.Locals("user_id", "viewer")
					c.Locals("tenant_id", "a")
					c.Locals("verified_jwt", jwtPayload{Sub: "viewer", Exp: expiry})
					return c.Next()
				})
				cfg := f.cfg
				cfg.RequireAuth = true
				app.Get("/permissions/me", permissionsMeHandler(cfg))
				app.Handler()(raw)
				stream, ok := raw.Response.BodyStream().(io.ReadCloser)
				if !ok || f.db.Stats().InUse != 1 {
					t.Fatal("self diagnostic lacks retained SQL")
				}
				defer stream.Close()
				buf := make([]byte, 8)
				if n, err := stream.Read(buf); n == 0 || err != nil || f.db.Stats().InUse != 1 {
					t.Fatalf("partial drain lost fence %d %v", n, err)
				}
				if kind == "expiry" {
					time.Sleep(time.Until(time.Unix(expiry, 0)) + 20*time.Millisecond)
					if _, err := stream.Read(buf); err == nil {
						t.Fatal("expired credential continued body")
					}
					if f.db.Stats().InUse != 1 {
						t.Fatal("expiry released SQL before actual Close")
					}
				}
				if err := stream.Close(); err != nil {
					t.Fatal(err)
				}
				if f.db.Stats().InUse != 0 {
					t.Fatal("close retained SQL")
				}
			})
		}
	})
}
