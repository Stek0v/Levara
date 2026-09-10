package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

type directoryAuthFunc func(context.Context, string, string) (ExternalPrincipal, error)

func (f directoryAuthFunc) AuthenticateCredentials(c context.Context, u, p string) (ExternalPrincipal, error) {
	return f(c, u, p)
}

func TestDirectoryLoginSessionAndFailures(t *testing.T) {
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "directory-user")
			for _, mode := range []string{"json", "form", "wrong password", "missing user", "inactive", "foreign origin", "revoked during bind"} {
				t.Run(mode, func(t *testing.T) {
					if _, err := db.Exec(Q(`UPDATE users SET is_active=true WHERE id=$1`), "directory-user"); err != nil {
						t.Fatal(err)
					}
					if mode == "inactive" {
						if _, err := db.Exec(Q(`UPDATE users SET is_active=false WHERE id=$1`), "directory-user"); err != nil {
							t.Fatal(err)
						}
					}
					called := 0
					directory := directoryAuthFunc(func(ctx context.Context, username, password string) (ExternalPrincipal, error) {
						called++
						if username != "renamed-user" || password != "directory-password" {
							t.Error("directory did not receive original credentials")
						}
						if mode == "wrong password" {
							return ExternalPrincipal{}, errors.New("upstream password diagnostic must not leak")
						}
						p := ExternalPrincipal{UserID: "directory-user", Email: "stored@example.test", IssuedAt: time.Now().Unix()}
						if mode == "missing user" {
							p.UserID = "unknown"
						}
						if mode == "revoked during bind" {
							p.IssuedAt = time.Now().Add(-time.Minute).Unix()
							provisioner := accesspkg.SQLProvisioner{DB: db, Q: Q}
							if err := provisioner.DeactivateUser(ctx, p.UserID); err != nil {
								t.Fatal(err)
							}
							if err := provisioner.ProvisionUser(ctx, accesspkg.ProvisionedUser{UserID: p.UserID, Active: true}); err != nil {
								t.Fatal(err)
							}
						}
						return p, nil
					})
					cfg := AuthConfig{DB: db, JWTSecret: "secret", RequireAuth: true, CookieOrigins: []string{"https://ui.test"}, CookieSecure: true, DirectoryAuth: directory}
					app := fiber.New()
					app.Post("/auth/directory/login", directoryLoginHandler(cfg))
					app.Post("/auth/logout", logoutHandler(cfg))
					app.Get("/auth/me", authMeHandler(cfg))
					body := `{"username":"renamed-user","password":"directory-password"}`
					contentType := "application/json"
					if mode == "form" {
						body = "username=renamed-user&password=directory-password"
						contentType = "application/x-www-form-urlencoded"
					}
					req := httptest.NewRequest("POST", "https://ui.test/auth/directory/login", strings.NewReader(body))
					req.Header.Set("Content-Type", contentType)
					req.Header.Set("Origin", "https://ui.test")
					if mode == "foreign origin" {
						req.Header.Set("Origin", "https://other.test")
					}
					resp, err := app.Test(req)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					if mode != "json" && mode != "form" {
						want := 401
						if mode == "foreign origin" {
							want = 403
							if called != 0 {
								t.Fatal("cross-origin request reached directory")
							}
						}
						if resp.StatusCode != want {
							t.Fatalf("failure status=%d want=%d", resp.StatusCode, want)
						}
						var detail map[string]string
						_ = json.NewDecoder(resp.Body).Decode(&detail)
						if strings.Contains(detail["detail"], "diagnostic") {
							t.Fatal("provider error leaked")
						}
						return
					}
					if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
						t.Fatalf("login=%d", resp.StatusCode)
					}
					var response struct {
						Token string `json:"access_token"`
					}
					if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
						t.Fatal(err)
					}
					payload, valid := verifyJWT(response.Token, "secret")
					if !valid || payload.Sub != "directory-user" || payload.SessionID == "" {
						t.Fatalf("session=%+v valid=%v", payload, valid)
					}
					cookies := resp.Cookies()
					if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
						t.Fatal("unsafe cookie")
					}
					logout := httptest.NewRequest("POST", "https://ui.test/auth/logout", nil)
					logout.Header.Set("Origin", "https://ui.test")
					logout.AddCookie(cookies[0])
					out, err := app.Test(logout)
					if err != nil {
						t.Fatal(err)
					}
					out.Body.Close()
					if out.StatusCode != 204 || validSession(context.Background(), db, true, payload) {
						t.Fatal("directory session not revoked by logout")
					}
				})
			}
		})
	}
}

func TestDirectoryLoginSharesAuthRateLimitAndIsOptional(t *testing.T) {
	t.Setenv("RATE_LIMIT_AUTH_MAX", "2")
	t.Setenv("RATE_LIMIT_AUTH_WINDOW_SECONDS", "3600")
	db, cleanup := newAuthTestDB(t)
	defer cleanup()
	called := 0
	cfg := &AuthConfig{DB: db, JWTSecret: "secret", RequireAuth: true, DirectoryAuth: directoryAuthFunc(func(context.Context, string, string) (ExternalPrincipal, error) {
		called++
		return ExternalPrincipal{}, errors.New("invalid")
	})}
	app := fiber.New()
	RegisterAuthAPI(app, cfg)
	for _, path := range []string{"/auth/login", "/auth/register", "/auth/directory/login"} {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{"username":"unknown","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if path == "/auth/directory/login" && resp.StatusCode != 429 {
			t.Fatalf("directory bypassed shared rate limit=%d", resp.StatusCode)
		}
	}
	if called != 0 {
		t.Fatal("rate-limited attempt reached directory")
	}
	other := fiber.New()
	RegisterAuthAPI(other, &AuthConfig{JWTSecret: "secret"})
	resp, err := other.Test(httptest.NewRequest("POST", "/auth/directory/login", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("disabled directory route=%d", resp.StatusCode)
	}
}

func TestDirectoryLoginBoundsSQLAfterBind(t *testing.T) {
	t.Setenv("HTTP_REQUEST_TIMEOUT_MS", "40")
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := newIdentityAuthDialectDB(t, dialect)
			seedAuthUser(t, db, "directory-user")
			db.SetMaxOpenConns(1)
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			cfg := AuthConfig{DB: db, JWTSecret: "test", DirectoryAuth: directoryAuthFunc(func(ctx context.Context, _, _ string) (ExternalPrincipal, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("directory login has no overall deadline")
				}
				return ExternalPrincipal{UserID: "directory-user", IssuedAt: time.Now().Unix()}, nil
			})}
			app := fiber.New()
			app.Post("/login", directoryLoginHandler(cfg))
			req := httptest.NewRequest("POST", "/login", strings.NewReader(`{"username":"user","password":"secret"}`))
			req.Header.Set("Content-Type", "application/json")
			response, err := app.Test(req, 500)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 401 || len(response.Cookies()) != 0 {
				t.Fatalf("timed-out session issued: %d", response.StatusCode)
			}
			var n int
			if err := db.QueryRow("SELECT COUNT(*) FROM auth_sessions").Scan(&n); err != nil || n != 0 {
				t.Fatalf("sessions=%d err=%v", n, err)
			}
		})
	}
}
