package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "github.com/ncruces/go-sqlite3/driver"
	httpapi "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/ingest"
)

func teamIntegrationServer(t *testing.T, dialect string) (*httptest.Server, *sql.DB, *atomic.Bool) {
	t.Helper()
	previous := httpapi.GetDBProvider()
	t.Cleanup(func() { httpapi.SetDBProvider(previous) })
	var db *sql.DB
	var schema string
	if dialect == "sqlite" {
		httpapi.SetDBProvider(httpapi.DBSQLite)
		ingest.SetSQLiteMode(true)
		t.Cleanup(func() { ingest.SetSQLiteMode(false) })
		var err error
		db, err = sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "team.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
	} else {
		httpapi.SetDBProvider(httpapi.DBPostgres)
		dsn := os.Getenv("LEVARA_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("LEVARA_TEST_POSTGRES_DSN not set")
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		schema = fmt.Sprintf("team_cli_%d", time.Now().UnixNano())
		cfg.RuntimeParams["search_path"] = schema
		db = stdlib.OpenDB(*cfg)
		if _, err = db.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if schema != "" {
			db.Exec("DROP SCHEMA " + schema + " CASCADE")
		}
		db.Close()
	})
	if err := httpapi.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	for _, ensure := range []func(context.Context, *sql.DB, access.QueryRewriter) error{access.EnsureIdentitySchema, access.EnsureBrowserSessionSchema} {
		if err := ensure(context.Background(), db, httpapi.Q); err != nil {
			t.Fatal(err)
		}
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	api := app.Group("/api/v1")
	cfg := httpapi.AuthConfig{DB: db, JWTSecret: "isolated-team-cli-session-secret", RequireAuth: true}
	httpapi.RegisterAuthAPI(api, &cfg)
	api.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &httpapi.DBRef{DB: db}); return c.Next() })
	api.Use(httpapi.JWTMiddleware(cfg.JWTSecret, true), httpapi.APIKeyPermissionMiddleware(), httpapi.TenantMiddleware(httpapi.AccessConfig{DB: db}))
	httpapi.RegisterAPIKeyEndpoints(api, cfg)
	httpapi.RegisterAPI(api, httpapi.APIConfig{DB: db, RequireAuth: true, StoragePath: filepath.Join(t.TempDir(), "uploads")})
	var breakGrants atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Authorization"), "isolated-cli-token") {
			t.Error("global admin token reached real API")
		}
		if breakGrants.Load() && strings.HasSuffix(r.URL.Path, "/shares") && r.Method == "POST" {
			http.Error(w, "do not echo fixture-secret-token", 503)
			return
		}
		resp, err := app.Test(r, -1)
		if err != nil {
			http.Error(w, "fixture error", 500)
			return
		}
		defer resp.Body.Close()
		for k, values := range resp.Header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	t.Cleanup(server.Close)
	return server, db, &breakGrants
}

func TestTeamApplyRealAPIRepeatAndPartialResume(t *testing.T) {
	t.Setenv("LEVARA_TEAM_TEST_A", "long-local-A-password")
	t.Setenv("LEVARA_TEAM_TEST_B", "long-local-B-password")
	t.Setenv("LEVARA_TENANT_ENFORCED", "0")
	// The application fixture keeps the real limiter; the test exercises three
	// applies in seconds, so its isolated bucket is explicitly sized for them.
	t.Setenv("RATE_LIMIT_AUTH_MAX", "30")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			server, db, breakGrants := teamIntegrationServer(t, dialect)
			plan := writeTeamPlan(t, teamTestPlan())
			state := filepath.Join(t.TempDir(), "team-state.json")
			count := func(table string) int {
				t.Helper()
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}
			breakGrants.Store(true)
			out, err := runCLICommand(t, server.URL, "team", "apply", "--plan="+plan, "--state="+state)
			if err == nil || !strings.Contains(out, `"status":"partial"`) || strings.Contains(out, "fixture-secret-token") {
				t.Fatalf("partial execution: %v %s", err, out)
			}
			if count("users") != 2 || count("principals") != 2 || count("api_keys") != 2 || count("datasets") != 2 || count("dataset_shares") != 0 {
				t.Fatal("unexpected partial state")
			}
			breakGrants.Store(false)
			for range 2 {
				out, err = runCLICommand(t, server.URL, "team", "apply", "--plan="+plan, "--state="+state)
				if err != nil {
					t.Fatalf("apply: %v %s", err, out)
				}
				var report teamReport
				if json.Unmarshal([]byte(out), &report) != nil || report.Status != "complete" {
					t.Fatalf("invalid report %s", out)
				}
				checks := 0
				for _, step := range report.Steps {
					if strings.HasPrefix(step.Step, "access/") && step.Status == "verified" {
						checks++
					}
				}
				if checks != 4 {
					t.Fatalf("A/B acceptance checks=%d", checks)
				}
				if count("users") != 2 || count("principals") != 2 || count("api_keys") != 2 || count("datasets") != 2 || count("dataset_shares") != 1 {
					t.Fatal("repeat created duplicate resources")
				}
				for _, secret := range []string{"long-local-A-password", "long-local-B-password", "isolated-cli-token", "lk_", "access_token"} {
					if strings.Contains(out, secret) {
						t.Fatalf("output leaked secret %s", secret)
					}
				}
			}
			var journal teamState
			if err := readTeamJSON(state, &journal, true); err != nil {
				t.Fatal(err)
			}
			for _, u := range journal.Users {
				if u.KeyID == "" || u.Key == "" || u.KeyPending {
					t.Fatal("key not durably recorded")
				}
			}
			var role string
			if err := db.QueryRow("SELECT role FROM dataset_shares").Scan(&role); err != nil || role != "viewer" {
				t.Fatalf("viewer probe changed grant: %s %v", role, err)
			}
			var liveSessions int
			if err := db.QueryRow("SELECT COUNT(*) FROM auth_sessions WHERE revoked=false").Scan(&liveSessions); err != nil || liveSessions != 0 {
				t.Fatalf("login sessions not closed: %d %v", liveSessions, err)
			}
		})
	}
}
