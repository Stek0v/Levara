package http

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

func teamOnboardingDB(t *testing.T, dialect string) *sql.DB {
	t.Helper()
	previous := GetDBProvider()
	t.Cleanup(func() { SetDBProvider(previous) })
	var db *sql.DB
	var schema string
	if dialect == "sqlite" {
		SetDBProvider(DBSQLite)
		var err error
		db, err = sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "team.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		SetDBProvider(DBPostgres)
		if os.Getenv("LEVARA_TEST_POSTGRES_DSN") == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
		}
		cfg, err := pgx.ParseConfig(os.Getenv("LEVARA_TEST_POSTGRES_DSN"))
		if err != nil {
			t.Fatal(err)
		}
		schema = fmt.Sprintf("team_onboarding_%d", time.Now().UnixNano())
		cfg.RuntimeParams["search_path"] = schema
		db = stdlib.OpenDB(*cfg)
		if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if schema != "" {
			_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		}
		_ = db.Close()
	})
	if err := MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func(context.Context, *sql.DB, accesspkg.QueryRewriter) error{accesspkg.EnsureIdentitySchema, accesspkg.EnsureBrowserSessionSchema} {
		if err := ensure(context.Background(), db, Q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestTeamOnboardingTruthfulAPIErrors(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := teamOnboardingDB(t, dialect)
			cfg := AuthConfig{DB: db, JWTSecret: "isolated-team-test", RequireAuth: true}
			app := fiber.New()
			app.Post("/auth/register", registerHandler(cfg))
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "owner"); return c.Next() })
			RegisterAPIKeyEndpoints(app, cfg)
			RegisterRBACAPI(app, APIConfig{DB: db})
			execSQL := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			request := func(method, path, payload string, want int) {
				t.Helper()
				req := httptest.NewRequest(method, path, bytes.NewBufferString(payload))
				req.Header.Set("Content-Type", "application/json")
				resp, err := app.Test(req, -1)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != want {
					t.Errorf("%s %s status=%d want=%d body=%s", method, path, resp.StatusCode, want, body)
				}
				if strings.Contains(string(body), "SQLSTATE") || strings.Contains(string(body), "hidden_team_") {
					t.Error("response leaked SQL implementation details")
				}
			}
			execSQL("INSERT INTO principals(id,type) VALUES('owner','user')")
			execSQL("INSERT INTO users(id,email,hashed_password) VALUES('owner','owner@example.invalid','locked')")
			execSQL("INSERT INTO datasets(id,name,owner_id) VALUES('owned','Owned','owner')")
			request("POST", "/auth/register", `{"email":"Юлия@example.invalid","password":"test-password"}`, 201)
			request("POST", "/auth/register", `{"email":"Юлия@example.invalid","password":"test-password"}`, 409)
			var principals, users int
			db.QueryRow("SELECT COUNT(*) FROM principals").Scan(&principals)
			db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
			if principals != users {
				t.Errorf("duplicate registration left orphan principal: principals=%d users=%d", principals, users)
			}
			execSQL("ALTER TABLE users RENAME TO hidden_team_users")
			request("POST", "/auth/register", `{"email":"new@example.invalid","password":"test-password"}`, 503)
			execSQL("ALTER TABLE hidden_team_users RENAME TO users")
			db.QueryRow("SELECT COUNT(*) FROM principals").Scan(&principals)
			if principals != users {
				t.Errorf("failed registration left principal: %d != %d", principals, users)
			}
			execSQL("ALTER TABLE api_keys RENAME TO hidden_team_keys")
			request("GET", "/auth/keys", "", 503)
			execSQL("ALTER TABLE hidden_team_keys RENAME TO api_keys")
			execSQL("ALTER TABLE dataset_shares RENAME TO hidden_team_shares")
			request("GET", "/datasets/owned/shares", "", 503)
			execSQL("ALTER TABLE hidden_team_shares RENAME TO dataset_shares")
		})
	}
}

type teamDeadlineExternal func(context.Context, string) (ExternalPrincipal, error)

func (f teamDeadlineExternal) Authenticate(ctx context.Context, token string) (ExternalPrincipal, error) {
	return f(ctx, token)
}

func TestTeamAuthHonorsBoundedContext(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT_MS", "80")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := teamOnboardingDB(t, dialect)
			db.SetMaxOpenConns(1)
			for _, kind := range []string{"login", "me", "jwt", "key", "oidc", "register", "keys-list", "keys-create"} {
				t.Run(kind, func(t *testing.T) {
					app := fiber.New()
					app.Use(func(c *fiber.Ctx) error {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
						defer cancel()
						c.SetUserContext(ctx)
						c.Locals("auth_db", &DBRef{DB: db})
						c.Locals("user_id", "owner")
						return c.Next()
					})
					cfg := AuthConfig{DB: db, JWTSecret: "test", RequireAuth: true}
					req := httptest.NewRequest("GET", "/", nil)
					req.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test", "test"))
					switch kind {
					case "login", "register":
						req = httptest.NewRequest("POST", "/", strings.NewReader(`{"email":"owner@test","password":"test-password"}`))
						req.Header.Set("Content-Type", "application/json")
						if kind == "login" {
							app.Post("/", loginHandler(cfg))
						} else {
							app.Post("/", registerHandler(cfg))
						}
					case "me":
						app.Get("/", authMeHandler(cfg))
					case "keys-list":
						app.Get("/", listAPIKeysHandler(cfg))
					case "keys-create":
						app.Get("/", createAPIKeyHandler(cfg))
					case "jwt", "key":
						if kind == "key" {
							req.Header.Set("X-API-Key", "fixture-key")
						}
						app.Use(JWTMiddleware("test", true))
						app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
					case "oidc":
						req.Header.Set("Authorization", "Bearer external-fixture")
						app.Use(JWTMiddlewareWithOIDC("test", true, teamDeadlineExternal(func(ctx context.Context, _ string) (ExternalPrincipal, error) {
							if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 70*time.Millisecond {
								t.Error("OIDC verifier lost shorter parent deadline")
							}
							return ExternalPrincipal{UserID: "owner", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
						})))
						app.Get("/", func(c *fiber.Ctx) error { return c.SendStatus(200) })
					}
					conn, err := db.Conn(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					start := time.Now()
					testTimeout, limit := 350, 250*time.Millisecond
					if kind == "register" {
						// bcrypt has fixed synchronous CPU cost and is much slower
						// under -race. After hashing, the expired request context must
						// still abort the occupied-pool wait; an unbounded SQL call
						// would remain stuck until app.Test times out.
						testTimeout, limit = 3500, 3*time.Second
					}
					resp, err := app.Test(req, testTimeout)
					conn.Close()
					if err != nil {
						t.Errorf("auth waited beyond request deadline: %v", err)
						return
					}
					resp.Body.Close()
					if resp.StatusCode < 400 || time.Since(start) > limit {
						t.Errorf("auth returned %d after %v", resp.StatusCode, time.Since(start))
					}
				})
			}
		})
	}
}

func TestTeamOnboardingConcurrentRegistrationAndMalformedLists(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := teamOnboardingDB(t, dialect)
			app := fiber.New()
			cfg := AuthConfig{DB: db, JWTSecret: "isolated-team", RequireAuth: true}
			app.Post("/register", registerHandler(cfg))
			const clients = 4
			done := make(chan int, clients)
			for range clients {
				go func() {
					req := httptest.NewRequest("POST", "/register", strings.NewReader(`{"email":"repeat@example.invalid","password":"test-password"}`))
					req.Header.Set("Content-Type", "application/json")
					resp, err := app.Test(req, -1)
					if err != nil {
						done <- 0
						return
					}
					resp.Body.Close()
					done <- resp.StatusCode
				}()
			}
			created, conflicts := 0, 0
			for range clients {
				switch status := <-done; status {
				case 201:
					created++
				case 409:
					conflicts++
				default:
					t.Errorf("concurrent register status=%d", status)
				}
			}
			var users, principals int
			db.QueryRow("SELECT COUNT(*) FROM users").Scan(&users)
			db.QueryRow("SELECT COUNT(*) FROM principals").Scan(&principals)
			if created != 1 || conflicts != 3 || users != 1 || principals != 1 {
				t.Fatalf("non-atomic duplicate registration: created=%d conflict=%d users=%d principals=%d", created, conflicts, users, principals)
			}
			var owner string
			if err := db.QueryRow("SELECT id FROM users").Scan(&owner); err != nil {
				t.Fatal(err)
			}
			execSQL := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(Q(query), args...); err != nil {
					t.Fatal(err)
				}
			}
			execSQL("INSERT INTO api_keys(id,key_hash,user_id,name) VALUES('key','fixture',$1,'Key')", owner)
			execSQL("INSERT INTO datasets(id,name,owner_id) VALUES('owned','Owned',$1)", owner)
			execSQL("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('share','owned',$1,'viewer')", owner)
			app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", owner); return c.Next() })
			RegisterAPIKeyEndpoints(app, cfg)
			RegisterRBACAPI(app, APIConfig{DB: db})
			for _, kind := range []string{"keys", "shares"} {
				path := "/auth/keys"
				if kind == "keys" {
					execSQL("ALTER TABLE api_keys RENAME TO hidden_team_rows")
					execSQL("CREATE VIEW api_keys AS SELECT id,key_hash,user_id,NULL AS name,permissions,created_at,last_used,revoked FROM hidden_team_rows")
				} else {
					path = "/datasets/owned/shares"
					execSQL("ALTER TABLE dataset_shares RENAME TO hidden_team_rows")
					execSQL("CREATE VIEW dataset_shares AS SELECT id,dataset_id,user_id,NULL AS role,granted_by,created_at FROM hidden_team_rows")
				}
				resp, err := app.Test(httptest.NewRequest("GET", path, nil), -1)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 503 {
					t.Errorf("malformed %s row returned %d", kind, resp.StatusCode)
				}
				if kind == "keys" {
					execSQL("DROP VIEW api_keys")
					execSQL("ALTER TABLE hidden_team_rows RENAME TO api_keys")
				} else {
					execSQL("DROP VIEW dataset_shares")
					execSQL("ALTER TABLE hidden_team_rows RENAME TO dataset_shares")
				}
			}
		})
	}
}
