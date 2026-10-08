package http

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

func audienceApp(f *documentHTTPFixture, hook fiber.Handler) *fiber.App {
	cfg := f.cfg
	cfg.RequireAuth = true
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: f.db}); return c.Next() })
	app.Use(JWTMiddleware("chat-scope-secret", true), APIKeyPermissionMiddleware(), TenantMiddleware(AccessConfig{DB: f.db}))
	if hook != nil {
		app.Use(hook)
	}
	RegisterRBACAPI(app, cfg)
	return app
}

func audienceSnapshot(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT * FROM dataset_shares ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for rows.Next() {
		values := make([]any, len(cols))
		targets := make([]any, len(cols))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%#v", values))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDatasetAudienceAuthority(t *testing.T) {
	for _, scenario := range []string{"owner", "admin", "editor", "global-admin", "foreign-project", "inactive-after-middleware", "jwt-revoked-after-middleware", "expired-after-middleware", "tenant-removed-after-middleware", "key-revoked-after-middleware", "key-permission-changed-after-middleware"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role,granted_by) VALUES('peer-admin','alpha','peer','admin','owner')")
				user, key := "peer", ""
				switch scenario {
				case "owner":
					user = "owner"
				case "editor":
					f.exec("UPDATE dataset_shares SET role='editor' WHERE id='peer-admin'")
				case "global-admin":
					user = "root"
					f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('root','a')")
				case "foreign-project":
					f.exec("UPDATE dataset_shares SET dataset_id='beta' WHERE id='peer-admin'")
				case "key-revoked-after-middleware", "key-permission-changed-after-middleware":
					key = "audience-key"
					user = ""
					f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'peer','read-write')", key, apikeyHash(key))
				}
				hook := func(c *fiber.Ctx) error {
					switch scenario {
					case "inactive-after-middleware":
						f.exec("UPDATE users SET is_active=false WHERE id='peer'")
					case "jwt-revoked-after-middleware":
						f.exec("INSERT INTO credential_epochs(user_id,epoch) VALUES('peer',1) ON CONFLICT(user_id) DO UPDATE SET epoch=1")
					case "expired-after-middleware":
						jwt := c.Locals("verified_jwt").(jwtPayload)
						jwt.Exp = 1
						c.Locals("verified_jwt", jwt)
					case "tenant-removed-after-middleware":
						f.exec("DELETE FROM user_tenant WHERE user_id='peer' AND tenant_id='a'")
					case "key-revoked-after-middleware":
						f.exec("UPDATE api_keys SET revoked=true WHERE id='audience-key'")
					case "key-permission-changed-after-middleware":
						f.exec("UPDATE api_keys SET permissions='read-only' WHERE id='audience-key'")
					}
					return c.Next()
				}
				app := audienceApp(f, hook)
				before := audienceSnapshot(t, f.db)
				status, p := scopedChatRequest(t, app, user, "a", key, "POST", "/datasets/alpha/shares", `{"email":"foreign@test.invalid","role":"viewer"}`)
				allowed := scenario == "owner" || scenario == "admin"
				if allowed {
					if status != 201 {
						t.Fatalf("grant status=%d body=%v", status, p)
					}
					id := p["id"].(string)
					status, p = scopedChatRequest(t, app, user, "a", key, "POST", "/datasets/alpha/shares", `{"user_id":"foreign","role":"editor"}`)
					if status != 201 || p["id"] != id {
						t.Fatalf("upsert status=%d body=%v", status, p)
					}
					status, _ = scopedChatRequest(t, app, user, "a", key, "DELETE", "/datasets/alpha/shares/"+id, "")
					if status != 200 {
						t.Fatalf("revoke=%d", status)
					}
					status, _ = scopedChatRequest(t, app, user, "a", key, "DELETE", "/datasets/alpha/shares/unknown", "")
					if status != 200 {
						t.Fatalf("unknown revoke=%d", status)
					}
				} else {
					if status != 403 {
						t.Fatalf("grant status=%d body=%v", status, p)
					}
					// Restore only middleware-accepted facts for a second independent revoke.
					f.exec("UPDATE users SET is_active=true WHERE id='peer'")
					f.exec("DELETE FROM credential_epochs WHERE user_id='peer'")
					f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','a') ON CONFLICT DO NOTHING")
					if key != "" {
						f.exec("UPDATE api_keys SET revoked=false,permissions='read-write' WHERE id='audience-key'")
					}
					status, p = scopedChatRequest(t, app, user, "a", key, "DELETE", "/datasets/alpha/shares/viewer-share", "")
					if status != 403 {
						t.Fatalf("revoke status=%d body=%v", status, p)
					}
				}
				if !reflect.DeepEqual(before, audienceSnapshot(t, f.db)) {
					t.Fatal("unrelated/full share row controls changed")
				}
			})
		})
	}
}

func TestDatasetAudienceAcquisition(t *testing.T) {
	for _, scenario := range []string{"admin-revoked", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				f.db.SetMaxOpenConns(1)
				f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('peer-admin','alpha','peer','admin')")
				for _, method := range []string{"POST", "DELETE"} {
					t.Run(method, func(t *testing.T) {
						f.exec("UPDATE dataset_shares SET role='admin' WHERE id='peer-admin'")
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						held := make(chan *sql.Conn, 1)
						app := audienceApp(f, func(c *fiber.Ctx) error {
							conn, err := f.db.Conn(ctx)
							if err != nil {
								return err
							}
							c.SetUserContext(ctx)
							held <- conn
							return c.Next()
						})
						path := "/datasets/alpha/shares"
						if method == "DELETE" {
							path += "/viewer-share"
						}
						req := httptest.NewRequest(method, path, strings.NewReader(`{"user_id":"foreign","role":"viewer"}`))
						req.Header.Set("Content-Type", "application/json")
						req.Header.Set("Authorization", "Bearer "+createJWT("peer", "peer@test.invalid", "chat-scope-secret"))
						req.Header.Set("X-Tenant-Id", "a")
						type result struct {
							status int
							err    error
						}
						done := make(chan result, 1)
						waits := f.db.Stats().WaitCount
						go func() {
							resp, err := app.Test(req, -1)
							if err != nil {
								done <- result{err: err}
								return
							}
							resp.Body.Close()
							done <- result{status: resp.StatusCode}
						}()
						var conn *sql.Conn
						select {
						case conn = <-held:
						case <-ctx.Done():
							t.Fatal("middleware did not reach acquisition")
						}
						defer conn.Close()
						for f.db.Stats().WaitCount <= waits && ctx.Err() == nil {
							time.Sleep(time.Millisecond)
						}
						if ctx.Err() != nil {
							t.Fatal("handler never waited for pool")
						}
						if scenario == "canceled" {
							cancel()
						} else {
							if _, err := conn.ExecContext(ctx, "UPDATE dataset_shares SET role='viewer' WHERE id='peer-admin'"); err != nil {
								t.Fatal(err)
							}
							conn.Close()
						}
						select {
						case r := <-done:
							if r.err != nil {
								t.Fatal(r.err)
							}
							want := 403
							if scenario == "canceled" {
								want = 503
							}
							if r.status != want {
								t.Fatalf("status=%d want=%d", r.status, want)
							}
						case <-time.After(12 * time.Second):
							t.Fatal("handler failed to drain")
						}
						conn.Close()
						var n int
						if err := f.db.QueryRow("SELECT COUNT(*) FROM dataset_shares WHERE user_id='foreign' OR id='viewer-share'").Scan(&n); err != nil || n != 1 {
							t.Fatalf("unauthorized audience changed n=%d err=%v", n, err)
						}
					})
				}
			})
		})
	}
}

func TestDatasetAudienceUnverifiedLocals(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "owner"); return c.Next() })
		RegisterRBACAPI(app, APIConfig{DB: f.db})
		before := audienceSnapshot(t, f.db)
		for _, method := range []string{"POST", "DELETE"} {
			path := "/datasets/alpha/shares"
			if method == "DELETE" {
				path += "/viewer-share"
			}
			status, p := scopedChatRequest(t, app, "", "", "", method, path, `{"user_id":"foreign"}`)
			if status != 403 {
				t.Fatalf("%s status=%d body=%v", method, status, p)
			}
		}
		if !reflect.DeepEqual(before, audienceSnapshot(t, f.db)) {
			t.Fatal("unverified local identity modified audience")
		}
	})
}

func TestDatasetAudienceExpiryBeforeCommit(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
				before := audienceSnapshot(t, f.db)
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				sqlite := GetDBProvider() == DBSQLite
				var rowTx *sql.Tx
				var holderPID int
				if sqlite {
					f.db.SetMaxOpenConns(1)
				} else {
					f.db.SetMaxOpenConns(3)
					var err error
					rowTx, err = f.db.BeginTx(ctx, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer rowTx.Rollback()
					if err := rowTx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holderPID); err != nil {
						t.Fatal(err)
					}
					var id string
					if err := rowTx.QueryRowContext(ctx, "SELECT id FROM dataset_shares WHERE id='viewer-share' FOR UPDATE").Scan(&id); err != nil {
						t.Fatal(err)
					}
				}
				captured := make(chan int64, 1)
				held := make(chan *sql.Conn, 1)
				app := audienceApp(f, func(c *fiber.Ctx) error {
					jwt := c.Locals("verified_jwt").(jwtPayload)
					jwt.Exp = time.Now().Unix() + 3
					c.Locals("verified_jwt", jwt)
					c.SetUserContext(ctx)
					if sqlite {
						conn, err := f.db.Conn(ctx)
						if err != nil {
							return err
						}
						held <- conn
					}
					captured <- jwt.Exp
					return c.Next()
				})
				path := "/datasets/alpha/shares"
				if method == "DELETE" {
					path += "/viewer-share"
				}
				req := httptest.NewRequest(method, path, strings.NewReader(`{"user_id":"viewer","role":"editor"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer "+createJWT("owner", "owner@test.invalid", "chat-scope-secret"))
				req.Header.Set("X-Tenant-Id", "a")
				type result struct {
					status int
					err    error
				}
				done := make(chan result, 1)
				waits := f.db.Stats().WaitCount
				go func() {
					resp, err := app.Test(req, -1)
					if err != nil {
						done <- result{err: err}
						return
					}
					resp.Body.Close()
					done <- result{status: resp.StatusCode}
				}()
				var expiry int64
				select {
				case expiry = <-captured:
				case <-ctx.Done():
					t.Fatal("actor was not captured")
				}
				var conn *sql.Conn
				if sqlite {
					conn = <-held
					defer conn.Close()
					for f.db.Stats().WaitCount <= waits && ctx.Err() == nil {
						time.Sleep(time.Millisecond)
					}
				} else {
					blocked := 0
					for blocked == 0 && ctx.Err() == nil {
						if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%dataset_shares%' ", holderPID).Scan(&blocked); err != nil {
							t.Fatal(err)
						}
						if blocked == 0 {
							time.Sleep(time.Millisecond)
						}
					}
				}
				if ctx.Err() != nil {
					t.Fatal("expected SQL/pool blocking was not observed")
				}
				if delay := time.Until(time.Unix(expiry, 0)); delay > 0 {
					select {
					case <-time.After(delay):
					case <-ctx.Done():
						t.Fatal("expiry wait exceeded deadline")
					}
				}
				if sqlite {
					conn.Close()
				} else {
					if err := rowTx.Rollback(); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case r := <-done:
					if r.err != nil {
						t.Fatal(r.err)
					}
					if r.status != 403 {
						t.Fatalf("expired actor committed %s: status=%d", method, r.status)
					}
				case <-ctx.Done():
					t.Fatal("expired writer failed to drain")
				}
				if !reflect.DeepEqual(before, audienceSnapshot(t, f.db)) {
					t.Fatal("expired actor changed full audience rows")
				}
			})
		})
	}
}

func TestDatasetAudienceSelfAdminChanges(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('peer-admin','alpha','peer','admin')")
		app := audienceApp(f, nil)
		status, p := scopedChatRequest(t, app, "peer", "a", "", "POST", "/datasets/alpha/shares", `{"user_id":"peer","role":"editor"}`)
		if status != 201 || p["id"] != "peer-admin" {
			t.Fatalf("self-demotion rejected %d %v", status, p)
		}
		f.exec("UPDATE dataset_shares SET role='admin' WHERE id='peer-admin'")
		status, p = scopedChatRequest(t, app, "peer", "a", "", "DELETE", "/datasets/alpha/shares/peer-admin", "")
		if status != 200 {
			t.Fatalf("self-revoke rejected %d %v", status, p)
		}
	})
}
