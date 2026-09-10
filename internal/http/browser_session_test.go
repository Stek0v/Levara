package http

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

func TestBrowserExternalAssertionCannotUpgradeAcrossRevocation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "external-user")
			ctx := context.Background()
			issuedAt := time.Now().Add(-time.Minute).Unix()
			epoch, err := accesspkg.ExternalCredentialEpoch(ctx, db, Q, "external-user", issuedAt)
			if err != nil {
				t.Fatal(err)
			}
			// Deterministic interleaving at the actual validation/issuance boundary:
			// no sleeps, test-only production hook or probabilistic scheduling.
			provisioner := accesspkg.SQLProvisioner{DB: db, Q: Q}
			if err := provisioner.DeactivateUser(ctx, "external-user"); err != nil {
				t.Fatal(err)
			}
			if err := provisioner.ProvisionUser(ctx, accesspkg.ProvisionedUser{UserID: "external-user", Active: true}); err != nil {
				t.Fatal(err)
			}
			token, err := issueBrowserSessionAtEpoch(ctx, db, "external-user", "external@example.test", "secret", epoch)
			if err == nil {
				claims, valid := verifyJWT(token, "secret")
				if valid && validSession(ctx, db, true, claims) {
					t.Fatalf("pre-revocation assertion upgraded to live epoch %d", claims.CredentialEpoch)
				}
			}
			if !errors.Is(err, accesspkg.ErrRevokedCredential) || token != "" {
				t.Fatalf("revoked issuance returned token=%t err=%v", token != "", err)
			}
			var sessions int
			if err := db.QueryRow("SELECT COUNT(*) FROM auth_sessions").Scan(&sessions); err != nil || sessions != 0 {
				t.Fatalf("revoked issuance created rows=%d err=%v", sessions, err)
			}
			fresh, err := IssueExternalBrowserSessionJWT(ctx, db, "external-user", "external@example.test", "secret", time.Now().Add(time.Second).Unix())
			if err != nil {
				t.Fatal(err)
			}
			claims, valid := verifyJWT(fresh, "secret")
			if !valid || claims.CredentialEpoch != epoch+1 || !validSession(ctx, db, true, claims) {
				t.Fatalf("fresh assertion claims=%+v valid=%v", claims, valid)
			}
			// A deactivation after issuance still invalidates this fixed epoch.
			if err := provisioner.DeactivateUser(ctx, "external-user"); err != nil {
				t.Fatal(err)
			}
			if validSession(ctx, db, true, claims) {
				t.Fatal("already-issued session survived deactivation")
			}
		})
	}
}

func TestBrowserVerifiedCredentialLocals(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "proof-user")
			token, err := IssueBrowserSessionJWT(context.Background(), db, "proof-user", "proof@example.test", "secret")
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := verifyJWT(token, "secret")
			if _, err := db.Exec(Q(`INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,$3,$4)`), "key-id", apikeyHash("key-secret"), "proof-user", "read"); err != nil {
				t.Fatal(err)
			}
			external := &stubOIDCBearer{userID: "proof-user", email: "proof@example.test", issuedAt: time.Now().Unix(), expiresAt: time.Now().Add(time.Minute).Unix(), accept: func(token string) bool { return token == "external-secret" }}
			for _, transport := range []string{"rest", "mcp"} {
				for _, kind := range []string{"jwt", "cookie", "api_key", "external"} {
					t.Run(transport+"/"+kind, func(t *testing.T) {
						app := fiber.New()
						check := func(c *fiber.Ctx) error {
							switch kind {
							case "jwt", "cookie":
								p, ok := c.Locals("verified_jwt").(jwtPayload)
								if !ok || p != *payload {
									t.Errorf("JWT proof=%+v ok=%v", p, ok)
								}
							case "api_key":
								p, ok := c.Locals("verified_api_key").(accesspkg.APIKeyIdentity)
								if !ok || p.KeyID != "key-id" || p.UserID != "proof-user" || p.Permissions != "read" {
									t.Errorf("key proof=%+v ok=%v", p, ok)
								}
								if c.Locals("verified_jwt") != nil {
									t.Error("API key did not take precedence")
								}
							case "external":
								p, ok := c.Locals("verified_external").(ExternalPrincipal)
								if !ok || p.UserID != "proof-user" || p.IssuedAt != external.issuedAt || p.ExpiresAt != external.expiresAt {
									t.Errorf("external proof=%+v ok=%v", p, ok)
								}
							}
							return c.SendStatus(204)
						}
						if transport == "rest" {
							app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
							app.Use(JWTMiddlewareWithOIDC("secret", true, external))
							app.Get("/proof", check)
						} else {
							h := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true, JWTSecret: "secret", OIDCBearer: external}}
							app.Get("/proof", func(c *fiber.Ctx) error {
								if _, err := h.authenticateMCPRequest(c); err != nil {
									return c.SendStatus(401)
								}
								return check(c)
							})
						}
						req := httptest.NewRequest("GET", "http://localhost/proof", nil)
						switch kind {
						case "jwt":
							req.Header.Set("Authorization", "Bearer "+token)
						case "cookie":
							req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
						case "api_key":
							req.Header.Set("X-API-Key", "key-secret")
							req.Header.Set("Authorization", "Bearer "+token)
						case "external":
							req.Header.Set("Authorization", "Bearer external-secret")
						}
						resp, err := app.Test(req)
						if err != nil {
							t.Fatal(err)
						}
						defer resp.Body.Close()
						if resp.StatusCode != 204 {
							t.Fatalf("proof handler status=%d", resp.StatusCode)
						}
					})
				}
			}
		})
	}
}

func TestBrowserCookieCSRFAndSelectiveLogout(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "browser-user")
			ctx := context.Background()
			if err := accesspkg.EnsureBrowserSessionSchema(ctx, db, Q); err != nil {
				t.Fatal(err)
			}
			first, err := IssueBrowserSessionJWT(ctx, db, "browser-user", "browser@example.test", "secret")
			if err != nil {
				t.Fatal(err)
			}
			second, err := IssueBrowserSessionJWT(ctx, db, "browser-user", "browser@example.test", "secret")
			if err != nil {
				t.Fatal(err)
			}
			cfg := AuthConfig{DB: db, RequireAuth: true, JWTSecret: "secret", CookieOrigins: []string{"https://app.example.test"}, CookieSecure: true}
			app := fiber.New()
			app.Post("/auth/logout", logoutHandler(cfg))
			app.Get("/auth/me", authMeHandler(cfg))
			mcp := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true, JWTSecret: "secret", AuthCookieOrigins: cfg.CookieOrigins}}
			app.Post("/mcp-auth", func(c *fiber.Ctx) error {
				if _, err := mcp.authenticateMCPRequest(c); err != nil {
					return c.SendStatus(401)
				}
				return c.SendStatus(204)
			})
			app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
			app.Use(JWTMiddleware("secret", true, cfg.CookieOrigins...))
			app.Post("/mutation", func(c *fiber.Ctx) error { return c.SendStatus(204) })
			request := func(method, path, token, origin string, bearer bool) *http.Response {
				t.Helper()
				req := httptest.NewRequest(method, "https://app.example.test"+path, nil)
				if bearer {
					req.Header.Set("Authorization", "Bearer "+token)
				} else {
					req.AddCookie(&http.Cookie{Name: "auth_token", Value: token})
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
				req.Header.Set("X-Forwarded-Host", "attacker.test")
				req.Header.Set("X-Forwarded-Proto", "https")
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { resp.Body.Close() })
				return resp
			}
			for _, path := range []string{"/auth/logout", "/mutation"} {
				for _, origin := range []string{"", "https://attacker.test", "null"} {
					if resp := request("POST", path, first, origin, false); resp.StatusCode != 403 {
						t.Errorf("%s origin=%q status=%d", path, origin, resp.StatusCode)
					}
				}
			}
			if resp := request("POST", "/mutation", first, "https://app.example.test", false); resp.StatusCode != 204 {
				t.Fatalf("same-origin mutation=%d", resp.StatusCode)
			}
			if resp := request("POST", "/mutation", first, "https://attacker.test", true); resp.StatusCode != 204 {
				t.Fatalf("bearer unexpectedly needs origin=%d", resp.StatusCode)
			}
			for _, origin := range []string{"", "https://attacker.test"} {
				if resp := request("POST", "/mcp-auth", first, origin, false); resp.StatusCode != 401 {
					t.Fatalf("MCP cookie origin=%q accepted=%d", origin, resp.StatusCode)
				}
			}
			if resp := request("POST", "/mcp-auth", first, "https://app.example.test", false); resp.StatusCode != 204 {
				t.Fatalf("MCP valid cookie=%d", resp.StatusCode)
			}
			if resp := request("POST", "/mcp-auth", first, "", true); resp.StatusCode != 204 {
				t.Fatalf("MCP bearer needs origin=%d", resp.StatusCode)
			}
			resp := request("POST", "/auth/logout", first, "https://app.example.test", false)
			if resp.StatusCode != 204 {
				t.Fatalf("logout=%d", resp.StatusCode)
			}
			cookies := resp.Cookies()
			if len(cookies) != 1 || cookies[0].MaxAge >= 0 || !cookies[0].Secure || !cookies[0].HttpOnly {
				t.Fatalf("logout did not securely clear cookie: %v", cookies)
			}
			for _, bearer := range []bool{false, true} {
				if resp := request("GET", "/auth/me", first, "", bearer); resp.StatusCode != 401 {
					t.Errorf("revoked session bearer=%v status=%d", bearer, resp.StatusCode)
				}
				if resp := request("POST", "/mcp-auth", first, "https://app.example.test", bearer); resp.StatusCode != 401 {
					t.Errorf("revoked MCP session bearer=%v status=%d", bearer, resp.StatusCode)
				}
			}
			if resp := request("GET", "/auth/me", second, "", false); resp.StatusCode != 200 {
				t.Fatalf("other session revoked=%d", resp.StatusCode)
			}
			if _, err := db.Exec(Q(`UPDATE auth_sessions SET expires_at=$1`), time.Now().Add(-time.Second).Unix()); err != nil {
				t.Fatal(err)
			}
			if resp := request("GET", "/auth/me", second, "", false); resp.StatusCode != 401 {
				t.Fatalf("expired session accepted=%d", resp.StatusCode)
			}
		})
	}
}

func TestBrowserReturnPathRejectsRedirectConfusion(t *testing.T) {
	for _, path := range []string{"//evil.test", "/\\evil.test", "/%5cevil.test", "/%2fevil.test", "/%252fevil.test", "/\x00bad", "/bad\r\nLocation:evil", "https://evil.test/", "/path#fragment"} {
		if _, err := BrowserReturnPath(path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
	for _, path := range []string{"", "/", "/chat", "/datasets?tab=shared"} {
		if _, err := BrowserReturnPath(path); err != nil {
			t.Errorf("rejected %q: %v", path, err)
		}
	}
}

func TestLoginRejectsForeignOrigin(t *testing.T) {
	app := fiber.New()
	app.Post("/auth/login", loginHandler(AuthConfig{JWTSecret: "secret", CookieOrigins: []string{"https://app.example.test"}}))
	req := httptest.NewRequest("POST", "https://app.example.test/auth/login", nil)
	req.Header.Set("Origin", "https://attacker.test")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("login CSRF=%d", resp.StatusCode)
	}
}
