package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"golang.org/x/crypto/bcrypt"
)

func TestCredentialRevocationSurvivesSCIMReactivation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			s := accesspkg.SCIMStore{DB: db, Q: Q}
			ctx := context.Background()
			if err := s.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			u := accesspkg.SCIMUser{Issuer: "directory", ExternalID: "immutable-sub", Email: "user@example.test", Active: true}
			uid, _, err := s.ProvisionCreate(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			token := createJWT(uid, u.Email, "secret")
			if _, err := db.Exec(Q(`INSERT INTO api_keys (id, key_hash, user_id, permissions) VALUES ($1, $2, $3, 'read-write')`), "key", apikeyHash("old-key"), uid); err != nil {
				t.Fatal(err)
			}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
			app.Use(JWTMiddleware("secret", true))
			app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
			app.Post("/auth/keys", createAPIKeyHandler(AuthConfig{DB: db, RequireAuth: true, JWTSecret: "secret"}))
			check := func(header, credential string, want int) {
				t.Helper()
				r := httptest.NewRequest("GET", "/", nil)
				r.Header.Set(header, credential)
				resp, err := app.Test(r)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != want {
					t.Errorf("%s status = %d, want %d", header, resp.StatusCode, want)
				}
			}
			check("Authorization", "Bearer "+token, 200)
			check("X-API-Key", "old-key", 200)
			if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
				t.Fatal(err)
			}
			check("Authorization", "Bearer "+token, 401)
			check("Cookie", "auth_token="+token, 401)
			check("X-API-Key", "old-key", 401)
			if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
				t.Fatal(err)
			}
			check("Authorization", "Bearer "+token, 401)
			check("X-API-Key", "old-key", 401)
			freshToken, err := IssueSessionJWT(ctx, db, uid, u.Email, "secret")
			if err != nil {
				t.Fatal(err)
			}
			check("Authorization", "Bearer "+freshToken, 200)
			r := httptest.NewRequest("POST", "/auth/keys", nil)
			r.Header.Set("Authorization", "Bearer "+freshToken)
			resp, err := app.Test(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 201 {
				t.Fatalf("new key after reactivation=%d", resp.StatusCode)
			}
			var key struct {
				Key string `json:"key"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&key); err != nil {
				t.Fatal(err)
			}
			check("X-API-Key", key.Key, 200)

		})
	}
}

func TestAuthenticatedJWTMissingUserDenied(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
	app.Use(JWTMiddleware("secret", true))
	app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer "+createJWT("missing", "old@example.test", "secret"))
	resp, err := app.Test(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("missing user's JWT status = %d, want 401", resp.StatusCode)
	}
}

func TestAPIKeyIdentityDenialAndUnavailableAreDistinct(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "api-owner")
			if _, err := db.Exec(Q(`INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,$3,'read')`), "api-key", apikeyHash("valid-key"), "api-owner"); err != nil {
				t.Fatal(err)
			}

			reached := false
			rest := fiber.New(fiber.Config{DisableStartupMessage: true})
			rest.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
			rest.Use(JWTMiddleware("secret", true))
			rest.Get("/", func(c *fiber.Ctx) error { reached = true; return c.SendStatus(fiber.StatusNoContent) })
			restStatus := func(key string) int {
				t.Helper()
				reached = false
				req := httptest.NewRequest("GET", "/", nil)
				req.Header.Set("X-API-Key", key)
				resp, err := rest.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}

			hMCP := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true}, sessions: mcp.NewSessionStore()}
			mcpApp := fiber.New(fiber.Config{DisableStartupMessage: true})
			mcpApp.Post("/mcp", hMCP.handleRPC)
			mcpStatus := func(key string) int {
				t.Helper()
				req := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"save_memory","arguments":{"key":"must-not-run","value":"x"}}}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-API-Key", key)
				resp, err := mcpApp.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}

			if got := restStatus("missing-key"); got != fiber.StatusUnauthorized || reached {
				t.Fatalf("REST denied status=%d reached=%v", got, reached)
			}
			if got := mcpStatus("missing-key"); got != fiber.StatusNotFound {
				t.Fatalf("MCP denied status=%d", got)
			}
			if got := restStatus("valid-key"); got != fiber.StatusNoContent || !reached {
				t.Fatalf("REST valid status=%d reached=%v", got, reached)
			}
			if _, err := db.Exec(Q(`UPDATE api_keys SET revoked=TRUE WHERE id=$1`), "api-key"); err != nil {
				t.Fatal(err)
			}
			if got := restStatus("valid-key"); got != fiber.StatusUnauthorized || reached {
				t.Fatalf("REST revoked status=%d reached=%v", got, reached)
			}
			if got := mcpStatus("valid-key"); got != fiber.StatusNotFound {
				t.Fatalf("MCP revoked status=%d", got)
			}
			if _, err := db.Exec(Q(`UPDATE api_keys SET revoked=FALSE WHERE id=$1`), "api-key"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(Q(`UPDATE users SET is_active=FALSE WHERE id=$1`), "api-owner"); err != nil {
				t.Fatal(err)
			}
			if got := restStatus("valid-key"); got != fiber.StatusUnauthorized || reached {
				t.Fatalf("REST inactive status=%d reached=%v", got, reached)
			}
			if got := mcpStatus("valid-key"); got != fiber.StatusNotFound {
				t.Fatalf("MCP inactive status=%d", got)
			}
			if _, err := db.Exec(Q(`UPDATE users SET is_active=TRUE WHERE id=$1`), "api-owner"); err != nil {
				t.Fatal(err)
			}
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := verifyAPIKey(cancelled, db, "valid-key"); err == nil {
				t.Fatal("cancelled API-key lookup reported credential denial")
			}

			sessionID := hMCP.createSession("api-owner")

			if _, err := db.Exec(`ALTER TABLE api_keys RENAME TO unavailable_api_keys`); err != nil {
				t.Fatal(err)
			}
			if got := restStatus("valid-key"); got != fiber.StatusServiceUnavailable || reached {
				t.Fatalf("REST unavailable status=%d reached=%v", got, reached)
			}
			if got := mcpStatus("valid-key"); got != fiber.StatusServiceUnavailable {
				t.Fatalf("MCP unavailable status=%d", got)
			}
			deleteReq := httptest.NewRequest("DELETE", "/mcp", nil)
			deleteReq.Header.Set("Mcp-Session-Id", sessionID)
			deleteReq.Header.Set("X-API-Key", "valid-key")
			mcpApp.Delete("/mcp", hMCP.handleDeleteSession)
			deleteResp, err := mcpApp.Test(deleteReq)
			if err != nil {
				t.Fatal(err)
			}
			deleteResp.Body.Close()
			if deleteResp.StatusCode != fiber.StatusServiceUnavailable || hMCP.getOrValidateSession(sessionID) == nil {
				t.Fatalf("MCP session unavailable status=%d session=%v", deleteResp.StatusCode, hMCP.getOrValidateSession(sessionID) != nil)
			}

			nilDBREST := fiber.New(fiber.Config{DisableStartupMessage: true})
			nilDBREST.Use(JWTMiddleware("secret", true))
			nilDBREST.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusNoContent) })
			nilReq := httptest.NewRequest("GET", "/", nil)
			nilReq.Header.Set("X-API-Key", "valid-key")
			nilResp, err := nilDBREST.Test(nilReq)
			if err != nil {
				t.Fatal(err)
			}
			nilResp.Body.Close()
			if nilResp.StatusCode != fiber.StatusServiceUnavailable {
				t.Fatalf("REST missing DB status=%d", nilResp.StatusCode)
			}
			nilMCP := &mcpHandler{cfg: APIConfig{RequireAuth: true}, sessions: mcp.NewSessionStore()}
			nilMCPApp := fiber.New(fiber.Config{DisableStartupMessage: true})
			nilMCPApp.Post("/mcp", nilMCP.handleRPC)
			nilMCPReq := httptest.NewRequest("POST", "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"levara_instructions","arguments":{}}}`))
			nilMCPReq.Header.Set("Content-Type", "application/json")
			nilMCPReq.Header.Set("X-API-Key", "valid-key")
			nilMCPResp, err := nilMCPApp.Test(nilMCPReq)
			if err != nil {
				t.Fatal(err)
			}
			nilMCPResp.Body.Close()
			if nilMCPResp.StatusCode != fiber.StatusServiceUnavailable {
				t.Fatalf("MCP missing DB status=%d", nilMCPResp.StatusCode)
			}
		})
	}
}

func seedAuthUser(t *testing.T, db *sql.DB, uid string) {
	t.Helper()
	if _, err := db.Exec(Q("INSERT INTO users (id, email, is_active) VALUES ($1, $2, true)"), uid, uid+"@example.test"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionLoginMeAndMCPUseLiveIdentity(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()
	s := accesspkg.SCIMStore{DB: db, Q: Q}
	ctx := context.Background()
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	u := accesspkg.SCIMUser{Issuer: "directory", ExternalID: "subject", Email: "user@example.test", Active: true}
	uid, _, err := s.ProvisionCreate(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(Q("UPDATE users SET hashed_password = $1 WHERE id = $2"), string(hash), uid); err != nil {
		t.Fatal(err)
	}
	const secret = "test-secret-fixed-for-determinism"
	app := authApp(t, db)
	h := &mcpHandler{cfg: APIConfig{DB: db, JWTSecret: secret, RequireAuth: true, OIDCBearer: &stubOIDCBearer{userID: uid, accept: func(token string) bool { return token == "external" }}}}
	app.Get("/mcp-check", func(c *fiber.Ctx) error {
		if _, err := h.authenticateMCPRequest(c); err != nil {
			return c.SendStatus(mcpAuthFailureStatus(err, 401))
		}
		return c.SendStatus(200)
	})
	app.Get("/rest-check", func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() }, JWTMiddlewareWithOIDC(secret, true, h.cfg.OIDCBearer), func(c *fiber.Ctx) error { return c.SendStatus(200) })
	login := func(want int) string {
		t.Helper()
		code, body := postBody(t, app, "/auth/login", map[string]string{"email": u.Email, "password": "password"})
		if code != want {
			t.Fatalf("login=%d, body=%s, want=%d", code, body, want)
		}
		var result struct {
			Token string `json:"access_token"`
		}
		json.Unmarshal(body, &result)
		return result.Token
	}
	check := func(path, token string, want int) {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s status=%d want=%d", path, resp.StatusCode, want)
		}
	}
	old := login(200)
	for _, path := range []string{"/auth/me", "/mcp-check", "/rest-check"} {
		check(path, old, 200)
	}
	check("/mcp-check", "external", 200)
	check("/rest-check", "external", 200)
	if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
		t.Fatal(err)
	}
	login(401)
	for _, path := range []string{"/auth/me", "/mcp-check", "/rest-check"} {
		check(path, old, 401)
	}
	check("/mcp-check", "external", 401)
	check("/rest-check", "external", 401)
	if _, _, err := s.ProvisionCreate(ctx, u); err != nil {
		t.Fatal(err)
	}
	fresh := login(200)
	for _, path := range []string{"/auth/me", "/mcp-check", "/rest-check"} {
		check(path, old, 401)
		check(path, fresh, 200)
	}
	check("/mcp-check", "external", 401)
	check("/rest-check", "external", 401)
	var watermark int64
	if err := db.QueryRow(Q("SELECT revoked_before FROM credential_epochs WHERE user_id = $1"), uid).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	h.cfg.OIDCBearer.(*stubOIDCBearer).issuedAt = watermark + 1
	check("/mcp-check", "external", 200)
	check("/rest-check", "external", 200)
	if _, err := db.Exec("DROP TABLE credential_epochs"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/me", "/mcp-check", "/rest-check"} {
		check(path, fresh, 503)
	}
	check("/rest-check", "external", 503)
}

func TestAuthenticatedModeRequiresSQLIdentityStore(t *testing.T) {
	app := fiber.New()
	cfg := AuthConfig{RequireAuth: true, JWTSecret: "secret"}
	app.Post("/auth/login", loginHandler(cfg))
	app.Post("/auth/register", registerHandler(cfg))
	app.Get("/auth/me", authMeHandler(cfg))
	for _, path := range []string{"/auth/login", "/auth/register"} {
		code, _ := postBody(t, app, path, map[string]string{"email": "x@example.test", "password": "pw"})
		if code < 400 {
			t.Errorf("%s accepted without identity DB: %d", path, code)
		}
	}
	r := httptest.NewRequest("GET", "/auth/me", nil)
	r.Header.Set("Authorization", "Bearer "+createJWT("dev-user", "x@example.test", "secret"))
	resp, err := app.Test(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 503 {
		t.Errorf("me without DB=%d", resp.StatusCode)
	}
}

func newIdentityAuthDialectDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	if dialect == "sqlite" {
		db, cleanup := newAuthTestDB(t)
		t.Cleanup(cleanup)
		return db
	}
	dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LEVARA_TEST_POSTGRES_DSN is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identity_auth_%d", time.Now().UnixNano())
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	oldProvider := GetDBProvider()
	SetDBProvider(DBPostgres)
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); db.Close(); SetDBProvider(oldProvider) })
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE principals(id TEXT PRIMARY KEY,type TEXT)`,
		`CREATE TABLE users(id TEXT PRIMARY KEY,email TEXT UNIQUE,hashed_password TEXT,is_active BOOLEAN DEFAULT true,is_superuser BOOLEAN DEFAULT false,is_verified BOOLEAN DEFAULT false)`,
		`CREATE TABLE api_keys(id TEXT PRIMARY KEY,key_hash TEXT UNIQUE,user_id TEXT,name TEXT,permissions TEXT,created_at TEXT,last_used TEXT,revoked BOOLEAN DEFAULT false)`,
		`CREATE TABLE user_tenant(user_id TEXT,tenant_id TEXT,PRIMARY KEY(user_id,tenant_id))`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := accesspkg.EnsureIdentitySchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	if err := accesspkg.EnsureBrowserSessionSchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAPIKeyCreationSerializedWithDeactivation(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			ctx := context.Background()
			s := accesspkg.SCIMStore{DB: db, Q: Q}
			if err := s.EnsureSchema(ctx); err != nil {
				t.Fatal(err)
			}
			u := accesspkg.SCIMUser{Issuer: "directory", ExternalID: "subject", Email: "user@example.test", Active: true}
			uid, _, err := s.ProvisionCreate(ctx, u)
			if err != nil {
				t.Fatal(err)
			}
			app := fiber.New()
			app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: db}); return c.Next() })
			app.Use(JWTMiddleware("secret", true))
			app.Post("/auth/keys", createAPIKeyHandler(AuthConfig{DB: db, RequireAuth: true, JWTSecret: "secret"}))
			token := createJWT(uid, u.Email, "secret")
			start := make(chan struct{})
			var wg sync.WaitGroup
			for range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					r := httptest.NewRequest("POST", "/auth/keys", nil)
					r.Header.Set("Authorization", "Bearer "+token)
					resp, err := app.Test(r, -1)
					if err != nil {
						t.Error(err)
						return
					}
					defer resp.Body.Close()
					if resp.StatusCode != 201 && resp.StatusCode != 401 {
						t.Errorf("concurrent key issuance status=%d", resp.StatusCode)
					}
				}()
			}
			close(start)
			if err := s.ProvisionDeactivate(ctx, u.Issuer, u.ExternalID); err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			var count int
			if err := db.QueryRow(Q("SELECT COUNT(*) FROM api_keys WHERE user_id = $1 AND revoked = false"), uid).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("%d keys escaped deactivation", count)
			}
		})
	}
}
