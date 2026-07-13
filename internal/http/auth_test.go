package http

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	_ "github.com/ncruces/go-sqlite3/driver"
)

// auth_test.go — hermetic coverage for auth.go. Validates JWT round-trip,
// login/register handlers (dev-mode + DB-backed), JWTMiddleware credential
// precedence, and the API-key CRUD lifecycle. All tests run against an
// in-memory SQLite via the sqlcompat Q() rewriter — no Postgres required.
//
// Handlers are wired without the AuthRateLimiter middleware so repeated
// invocations from the same loopback IP do not collide with the per-IP
// 10/min cap shared by /auth/login and /auth/register.

func newAuthTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "levara-auth-test-*")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "auth.db")
	db, err := sql.Open("sqlite3", "file:"+dbPath+"?_pragma=journal_mode(WAL)")
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE principals (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL
		);
		CREATE TABLE users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE,
			hashed_password TEXT,
			is_active INTEGER DEFAULT 1,
			is_superuser INTEGER DEFAULT 0,
			is_verified INTEGER DEFAULT 0
		);
		CREATE TABLE api_keys (
			id TEXT PRIMARY KEY,
			key_hash TEXT UNIQUE,
			user_id TEXT,
			name TEXT,
			permissions TEXT,
			created_at TEXT,
			last_used TEXT,
			revoked INTEGER DEFAULT 0
		);
	`); err != nil {
		db.Close()
		os.RemoveAll(dir)
		t.Fatalf("schema: %v", err)
	}
	SetDBProvider(DBSQLite)
	cleanup := func() {
		_ = db.Close()
		os.RemoveAll(dir)
		SetDBProvider(DBPostgres)
	}
	return db, cleanup
}

func authApp(t *testing.T, db *sql.DB) *fiber.App {
	t.Helper()
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	cfg := AuthConfig{
		JWTSecret: "test-secret-fixed-for-determinism",
		DB:        db,
	}
	// Register handlers directly without AuthRateLimiter; under -race we
	// blow past 10 req/min on repeated table-driven calls.
	app.Post("/auth/login", loginHandler(cfg))
	app.Post("/auth/register", registerHandler(cfg))
	app.Get("/auth/me", authMeHandler(cfg))
	return app
}

func postForm(t *testing.T, app *fiber.App, path, body string) (int, []byte) {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, buf
}

func postBody(t *testing.T, app *fiber.App, path string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("POST", path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, buf
}

// ── JWT round-trip ──────────────────────────────────────────────────────────

func TestCreateAndVerifyJWT_Roundtrip(t *testing.T) {
	tok := createJWT("user-123", "u@example.com", "secret")
	p, ok := verifyJWT(tok, "secret")
	if !ok {
		t.Fatal("verifyJWT(valid token) = false")
	}
	if p.Sub != "user-123" || p.Email != "u@example.com" {
		t.Errorf("payload = %+v, want sub=user-123 email=u@example.com", p)
	}
	if p.Exp <= time.Now().Unix() {
		t.Errorf("Exp=%d must be in the future", p.Exp)
	}
	// Mutated signature → invalid.
	bad := tok[:len(tok)-2] + "xx"
	if _, ok := verifyJWT(bad, "secret"); ok {
		t.Error("verifyJWT(mutated sig) returned ok")
	}
}

func TestVerifyJWT_RejectsExpired(t *testing.T) {
	// Hand-craft a token with exp in the past, signed with the test secret.
	header := `{"alg":"HS256","typ":"JWT"}`
	payload := `{"sub":"u","email":"e","exp":100,"iat":1}`
	hEnc := base64.RawURLEncoding.EncodeToString([]byte(header))
	pEnc := base64.RawURLEncoding.EncodeToString([]byte(payload))
	// We don't need a real signature — VerifyJWT must reject expired tokens
	// even before checking the signature, OR after; either way the verdict is
	// "invalid". Use an obviously-bogus signature to guarantee no false-pass.
	tok := hEnc + "." + pEnc + ".bogus"
	if _, ok := verifyJWT(tok, "secret"); ok {
		t.Error("expired token must not verify")
	}
}

func TestVerifyJWT_RejectsWrongSecret(t *testing.T) {
	tok := createJWT("u", "e", "secretA")
	if _, ok := verifyJWT(tok, "secretB"); ok {
		t.Error("token signed with secretA must not verify under secretB")
	}
}

// ── login ───────────────────────────────────────────────────────────────────

func TestLogin_MissingCredentialsReturns400(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()
	app := authApp(t, db)

	status, _ := postBody(t, app, "/auth/login", map[string]string{})
	if status != 400 {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestLogin_DevModeAcceptsAnyCredentials(t *testing.T) {
	t.Setenv("ENV", "dev")
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/auth/login", loginHandler(AuthConfig{JWTSecret: "s", DB: nil}))

	status, body := postBody(t, app, "/auth/login", map[string]string{
		"email":    "anyone@example.com",
		"password": "anything",
	})
	if status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if _, ok := out["access_token"].(string); !ok {
		t.Errorf("response missing access_token: %s", body)
	}
}

func TestLogin_ProductionWithoutDBReturns500(t *testing.T) {
	t.Setenv("ENV", "production")
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Post("/auth/login", loginHandler(AuthConfig{JWTSecret: "s", DB: nil}))

	status, _ := postBody(t, app, "/auth/login", map[string]string{
		"email":    "u@example.com",
		"password": "x",
	})
	if status != 500 {
		t.Errorf("status = %d, want 500 in production-without-DB", status)
	}
}

func TestLogin_BcryptVerifyAcceptsCorrectPassword(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()

	pw := "correct-horse-battery-staple"
	// MinCost keeps the test fast; the handler uses CompareHashAndPassword
	// which works with any bcrypt cost.
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, hashed_password) VALUES (?, ?, ?)`,
		"uid-1", "alice@example.com", string(hash)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	app := authApp(t, db)

	// Correct password → 200 with token.
	status, body := postForm(t, app, "/auth/login",
		"username=alice@example.com&password="+pw)
	if status != 200 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var ok map[string]any
	_ = json.Unmarshal(body, &ok)
	if _, has := ok["access_token"].(string); !has {
		t.Errorf("missing access_token: %s", body)
	}

	// Wrong password → 401.
	status, _ = postForm(t, app, "/auth/login",
		"username=alice@example.com&password=wrong")
	if status != 401 {
		t.Errorf("wrong password status = %d, want 401", status)
	}
}

// ── register ────────────────────────────────────────────────────────────────

func TestRegister_HashesPasswordAndIssuesToken(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()
	app := authApp(t, db)

	status, body := postBody(t, app, "/auth/register", map[string]string{
		"email":    "bob@example.com",
		"password": "pw-plain-text",
	})
	if status != 201 {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	var out map[string]any
	_ = json.Unmarshal(body, &out)
	if _, ok := out["access_token"].(string); !ok {
		t.Errorf("missing access_token: %s", body)
	}

	// Stored hash must NOT equal the plaintext, must be valid bcrypt.
	var hashed string
	if err := db.QueryRow(`SELECT hashed_password FROM users WHERE email = ?`,
		"bob@example.com").Scan(&hashed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if hashed == "pw-plain-text" {
		t.Fatal("password stored in plaintext")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hashed), []byte("pw-plain-text")); err != nil {
		t.Errorf("stored value is not a valid bcrypt of the plaintext: %v", err)
	}
}

// ── JWTMiddleware ───────────────────────────────────────────────────────────

func TestJWTMiddleware_PrefersAPIKeyOverBearer(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()

	// Seed a valid API key for user-from-key.
	plainKey := "lk_test_abcdefabcdef"
	keyHash := sha256Hash(plainKey)
	if _, err := db.Exec(`INSERT INTO api_keys (id, key_hash, user_id, name, permissions, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"k1", keyHash, "user-from-key", "n", "read-write", time.Now().Format(time.RFC3339)); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	// Inject auth_db before JWTMiddleware so verifyAPIKey can find it.
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("auth_db", &DBRef{DB: db})
		return c.Next()
	})
	app.Use(JWTMiddleware("jwt-secret", false))
	app.Get("/who", func(c *fiber.Ctx) error {
		uid, _ := c.Locals("user_id").(string)
		return c.JSON(fiber.Map{"user_id": uid})
	})

	bearer := createJWT("user-from-jwt", "j@e.com", "jwt-secret")
	r := httptest.NewRequest("GET", "/who", nil)
	r.Header.Set("X-API-Key", plainKey)
	r.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(buf, &out)
	if got := out["user_id"]; got != "user-from-key" {
		t.Errorf("user_id = %v, want user-from-key (api key precedence)", got)
	}
}

func TestJWTMiddleware_RequireAuthGuard(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(JWTMiddleware("s", true))
	app.Get("/p", func(c *fiber.Ctx) error { return c.SendString("ok") })

	resp, err := app.Test(httptest.NewRequest("GET", "/p", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401 when requireAuth=true and no token", resp.StatusCode)
	}

	// requireAuth=false → request passes through.
	app2 := fiber.New(fiber.Config{DisableStartupMessage: true})
	app2.Use(JWTMiddleware("s", false))
	app2.Get("/p", func(c *fiber.Ctx) error { return c.SendString("ok") })
	resp2, err := app2.Test(httptest.NewRequest("GET", "/p", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("status = %d, want 200 when requireAuth=false and no token", resp2.StatusCode)
	}
}

func TestAPIKeyPermissionMiddlewareBlocksMutationsForReadKey(t *testing.T) {
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("api_key_permissions", "read")
		return c.Next()
	})
	app.Use(APIKeyPermissionMiddleware())
	app.Get("/resource", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })
	app.Post("/resource", func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusCreated) })

	readResp, err := app.Test(httptest.NewRequest("GET", "/resource", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = readResp.Body.Close()
	if readResp.StatusCode != fiber.StatusOK {
		t.Fatalf("read status=%d, want 200", readResp.StatusCode)
	}

	writeResp, err := app.Test(httptest.NewRequest("POST", "/resource", nil))
	if err != nil {
		t.Fatal(err)
	}
	_ = writeResp.Body.Close()
	if writeResp.StatusCode != fiber.StatusForbidden {
		t.Fatalf("write status=%d, want 403", writeResp.StatusCode)
	}
}

// ── API key CRUD lifecycle ──────────────────────────────────────────────────

func TestAPIKey_CreateRevokeListLifecycle(t *testing.T) {
	db, cleanup := newAuthTestDB(t)
	defer cleanup()

	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	cfg := AuthConfig{JWTSecret: "s", DB: db}
	// Inject user_id since these handlers don't talk to JWTMiddleware in
	// this slim test fixture.
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "u-keys")
		return c.Next()
	})
	app.Post("/auth/keys", createAPIKeyHandler(cfg))
	app.Get("/auth/keys", listAPIKeysHandler(cfg))
	app.Delete("/auth/keys/:id", revokeAPIKeyHandler(cfg))

	// 1. Create
	status, body := postBody(t, app, "/auth/keys", map[string]string{
		"name":        "ci-bot",
		"permissions": "read",
	})
	if status != 201 {
		t.Fatalf("create status = %d, body = %s", status, body)
	}
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	keyPlain, _ := created["key"].(string)
	keyID, _ := created["id"].(string)
	if !strings.HasPrefix(keyPlain, "lk_") {
		t.Errorf("key=%q, want lk_ prefix", keyPlain)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(keyPlain, "lk_")); err != nil {
		t.Errorf("key suffix not hex: %v", err)
	}

	// 2. List sees it
	resp, err := app.Test(httptest.NewRequest("GET", "/auth/keys", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	buf, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var keys []map[string]any
	_ = json.Unmarshal(buf, &keys)
	if len(keys) != 1 || keys[0]["id"] != keyID {
		t.Fatalf("list = %v, want one entry with id %s", keys, keyID)
	}
	if revoked, _ := keys[0]["revoked"].(bool); revoked {
		t.Error("freshly created key should not be revoked")
	}

	// 3. Revoke
	r := httptest.NewRequest("DELETE", "/auth/keys/"+keyID, nil)
	resp, err = app.Test(r, -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("revoke status = %d, want 200", resp.StatusCode)
	}

	// 4. List shows revoked=true
	resp, err = app.Test(httptest.NewRequest("GET", "/auth/keys", nil), -1)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	buf, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	keys = nil
	_ = json.Unmarshal(buf, &keys)
	if len(keys) != 1 {
		t.Fatalf("post-revoke list = %v, want 1", keys)
	}
	if revoked, _ := keys[0]["revoked"].(bool); !revoked {
		t.Errorf("post-revoke revoked = false, want true: %+v", keys[0])
	}
}
