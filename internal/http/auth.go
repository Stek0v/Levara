// auth.go — JWT authentication for multi-user mode.
// Simple stateless JWT auth with bcrypt password hashing.
package http

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	accesspkg "github.com/stek0v/levara/pkg/access"
	vectorAuth "github.com/stek0v/levara/pkg/auth"
)

// AuthConfig holds auth settings.
type AuthConfig struct {
	DirectoryAuth ExternalPasswordAuth // optional provisioned directory password login
	CookieOrigins []string             // public callback origins for cookie CSRF validation behind proxies
	CookieSecure  bool
	RequireAuth   bool // only explicit no-auth mode permits a missing SQL identity store
	PostgresDSN   string
	JWTSecret     string  // random secret for signing tokens
	DB            *sql.DB // shared connection pool (nil if no PostgresDSN)
}

// RegisterAuthAPI registers local and optional directory login and session routes.
// It mutates cfg.JWTSecret in-place if empty (generates random secret).
//
// Swagger annotations (T13) for the endpoints registered below live
// directly on loginHandler / registerHandler / authMeHandler below so
// `swag init` picks them up regardless of registration order.
func RegisterAuthAPI(app fiber.Router, cfg *AuthConfig) {
	if cfg.JWTSecret == "" {
		// Generate random secret if not provided
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		cfg.JWTSecret = hex.EncodeToString(b)
	}

	// Per-IP rate limit on local/directory login and registration (T2 / D10): caps
	// credential stuffing at 10 req/min per source IP. /auth/me is read-only
	// and falls under the per-user limiter added later in the chain.
	//
	// SHARED bucket intent (20.04 review M4): all credential routes go through the
	// SAME limiter instance, so the budget is combined — an attacker cannot
	// burn 10 logins and then 10 registrations from the same IP in the same
	// minute. If you're tempted to split them per-route to give users more
	// headroom on register/login separately, DON'T — the combined budget is
	// the security guarantee we're trading UX for.
	authRL := authRateLimitFromEnv()
	authLimiter := AuthRateLimiter(authRL)
	app.Post("/auth/login", authLimiter, loginHandler(*cfg))
	app.Post("/auth/register", authLimiter, registerHandler(*cfg))
	if cfg.DirectoryAuth != nil {
		app.Post("/auth/directory/login", authLimiter, directoryLoginHandler(*cfg))
	}

	// /auth/me — Levara frontend calls this to check current user after login
	app.Get("/auth/me", authMeHandler(*cfg))
	app.Post("/auth/logout", logoutHandler(*cfg))
}

// authRateLimitFromEnv tunes the combined local/directory login + register bucket.
// Defaults: 10 req/min per IP (credential-stuffing guard).
// Dev/bench: RATE_LIMIT_AUTH_MAX=10000 (see deploy/profiles/local.postgres.env.example).
func authRateLimitFromEnv() RateLimitConfig {
	cfg := RateLimitConfig{}
	if v := os.Getenv("RATE_LIMIT_AUTH_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.AuthMax = n
		}
	}
	if v := os.Getenv("RATE_LIMIT_AUTH_WINDOW_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.AuthWindow = time.Duration(n) * time.Second
		}
	}
	return cfg
}

// ── JWT helpers ──

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtPayload struct {
	Sub             string `json:"sub"` // user ID
	Email           string `json:"email"`
	Exp             int64  `json:"exp"` // expiry timestamp
	CredentialEpoch int64  `json:"credential_epoch,omitempty"`
	SessionID       string `json:"sid,omitempty"`
	Iat             int64  `json:"iat"` // issued at
}

// CreateSessionJWT is the legacy epoch-zero issuer for no-auth development.
// SQL-backed authentication callbacks must use IssueSessionJWT instead.
func CreateSessionJWT(userID, email, secret string) string {
	return createJWT(userID, email, secret)
}

// IssueSessionJWT verifies the live user and stamps the current credential
// epoch. External authentication callbacks must use this SQL-backed issuer.
func IssueSessionJWT(ctx context.Context, db *sql.DB, userID, email, secret string) (string, error) {
	epoch, err := accesspkg.CurrentCredentialEpoch(ctx, db, Q, userID)
	if err != nil {
		return "", err
	}
	return createJWTAtEpoch(userID, email, secret, epoch), nil
}

func createJWT(userID, email, secret string) string {
	return createJWTAtEpoch(userID, email, secret, 0)
}

func createJWTAtEpoch(userID, email, secret string, epoch int64) string {
	payload := jwtPayload{
		CredentialEpoch: epoch,
		Sub:             userID,
		Email:           email,
		Exp:             time.Now().Add(24 * time.Hour).Unix(),
		Iat:             time.Now().Unix(),
	}

	return signSessionPayload(payload, secret)
}

func signSessionPayload(payload jwtPayload, secret string) string {
	header := jwtHeader{Alg: "HS256", Typ: "JWT"}
	hJSON, _ := json.Marshal(header)
	pJSON, _ := json.Marshal(payload)

	hEnc := base64.RawURLEncoding.EncodeToString(hJSON)
	pEnc := base64.RawURLEncoding.EncodeToString(pJSON)

	sigInput := hEnc + "." + pEnc
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	return sigInput + "." + sig
}

// verifyJWT delegates to pkg/auth.VerifyJWT so the HTTP and gRPC sides
// share a single implementation. T19: gRPC interceptors need to verify
// the same tokens the HTTP handlers issue; moving the logic to pkg/auth
// lets both import it without going through internal/http (which would
// create a package cycle for gRPC).
func verifyJWT(token, secret string) (*jwtPayload, bool) {
	p, ok := vectorAuth.VerifyJWT(token, secret)
	if !ok {
		return nil, false
	}
	return &jwtPayload{Sub: p.Sub, Email: p.Email, Exp: p.Exp, Iat: p.Iat, CredentialEpoch: p.CredentialEpoch, SessionID: p.SessionID}, true
}

// ── Handlers ──

// loginHandler handles POST /auth/login.
//
// @Summary     Exchange credentials for a JWT
// @Description Accepts either form-encoded (Levara frontend) or JSON body. On success returns a 24h HS256 JWT in the response + a secure http-only cookie.
// @Tags        auth
// @Accept      json
// @Accept      x-www-form-urlencoded
// @Produce     json
// @Param       body body object true "email + password"
// @Success     200  {object} map[string]string
// @Failure     400  {object} map[string]any "Missing credentials"
// @Failure     401  {object} map[string]any "Invalid credentials"
// @Failure     429  {object} map[string]any "Rate-limited (shared bucket with /auth/register)"
// @Router      /auth/login [post]
func loginHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		c.Set("Cache-Control", "no-store")
		if !CookieRequestAllowed(c, false, cfg.CookieOrigins...) {
			return c.SendStatus(fiber.StatusForbidden)
		}
		// Support both JSON and form-encoded (Levara frontend uses form)
		email := c.FormValue("username")
		password := c.FormValue("password")

		if email == "" || password == "" {
			var req struct {
				Email    string `json:"email"`
				Username string `json:"username"`
				Password string `json:"password"`
			}
			c.BodyParser(&req)
			if req.Email != "" {
				email = req.Email
			}
			if req.Username != "" {
				email = req.Username
			}
			if req.Password != "" {
				password = req.Password
			}
		}

		if email == "" || password == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "email and password required"})
		}

		if cfg.DB == nil {
			if cfg.RequireAuth || os.Getenv("ENV") == "production" {
				return c.Status(500).JSON(fiber.Map{"detail": "database required in production mode"})
			}
			// No DB — accept any credentials in dev mode
			log.Printf("[WARN] dev-mode login: accepting any credentials for %s", email)
			token := createJWT("dev-user", email, cfg.JWTSecret)
			setAuthCookie(c, token, cfg.CookieSecure)
			return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
		}

		var userID, hashedPassword string
		err := cfg.DB.QueryRowContext(c.UserContext(),
			Q("SELECT id, hashed_password FROM users WHERE email = $1"), email).Scan(&userID, &hashedPassword)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}

		if bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password)) != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}

		token, err := IssueBrowserSessionJWT(c.UserContext(), cfg.DB, userID, email, cfg.JWTSecret)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}
		setAuthCookie(c, token, cfg.CookieSecure)
		return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
	}
}

// registerHandler handles POST /auth/register.
//
// @Summary     Create a user account
// @Description Bcrypt-hashes the password and issues a fresh JWT on success.
// @Tags        auth
// @Accept      json
// @Produce     json
// @Param       body body object true "email + password"
// @Success     201  {object} map[string]string
// @Failure     400  {object} map[string]any "Missing or malformed fields"
// @Failure     409  {object} map[string]any "Email already registered"
// @Failure     429  {object} map[string]any "Rate-limited (shared bucket with /auth/login)"
// @Router      /auth/register [post]
func registerHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.Set("Cache-Control", "no-store")
		if !CookieRequestAllowed(c, false, cfg.CookieOrigins...) {
			return c.SendStatus(fiber.StatusForbidden)
		}
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := c.BodyParser(&req); err != nil || req.Email == "" || req.Password == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "email and password required"})
		}

		if cfg.DB == nil && (cfg.RequireAuth || os.Getenv("ENV") == "production") {
			return c.Status(503).JSON(fiber.Map{"detail": "database required for registration"})
		}
		hashedPw, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "hash error"})
		}

		userID := generateUUID()

		if cfg.DB != nil {
			// A duplicate email or database failure must not leave an orphan
			// principal. Only the email conflict is a 409; SQL faults are retryable.
			tx, err := cfg.DB.BeginTx(ctx, nil)
			if err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "registration unavailable"})
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, Q("INSERT INTO principals (id, type) VALUES ($1, 'user')"), userID); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "registration unavailable"})
			}
			var inserted string
			err = tx.QueryRowContext(ctx,
				Q(`INSERT INTO users (id, email, hashed_password, is_active, is_superuser, is_verified)
				 VALUES ($1, $2, $3, true, false, false) ON CONFLICT(email) DO NOTHING RETURNING id`),
				userID, req.Email, string(hashedPw)).Scan(&inserted)
			if errors.Is(err, sql.ErrNoRows) {
				return c.Status(409).JSON(fiber.Map{"detail": "email already registered"})
			}
			if err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "registration unavailable"})
			}
			if err = tx.Commit(); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "registration unavailable"})
			}
		}

		token := createJWT(userID, req.Email, cfg.JWTSecret)
		if cfg.DB != nil {
			token, err = IssueBrowserSessionJWT(ctx, cfg.DB, userID, req.Email, cfg.JWTSecret)
			if err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "cannot issue session"})
			}
		}
		setAuthCookie(c, token, cfg.CookieSecure)
		return c.Status(201).JSON(fiber.Map{
			"id":           userID,
			"email":        req.Email,
			"access_token": token,
			"token_type":   "bearer",
		})
	}
}

func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return hex.EncodeToString(b[:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:])
}

// authMeHandler returns current user from JWT token.
// GET /auth/me — called by Levara frontend after login to verify session.
//
// @Summary     Return the current user
// @Description Validates the bearer token and returns the associated user record. Also the probe called by the WebUI auth guard on every dashboard mount (T1).
// @Tags        auth
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]any "id, email, username"
// @Failure     401 {object} map[string]any "Unauthenticated"
// @Router      /auth/me [get]
func authMeHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		c.Set("Cache-Control", "no-store")
		token := ""
		auth := c.Get("Authorization")
		if auth != "" {
			t := strings.TrimPrefix(auth, "Bearer ")
			if t != "" && t != "null" && t != "undefined" {
				token = t
			}
		}
		if token == "" {
			token = c.Cookies("auth_token")
		}
		if token == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "not authenticated"})
		}
		payload, valid := verifyJWT(token, cfg.JWTSecret)
		if !valid || !validSession(c.UserContext(), cfg.DB, cfg.RequireAuth, payload) {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid token"})
		}

		// If DB available, fetch full user record
		if cfg.DB != nil {
			var email string
			var isActive, isSuperuser, isVerified bool
			err := cfg.DB.QueryRowContext(c.UserContext(),
				Q("SELECT email, is_active, is_superuser, is_verified FROM users WHERE id = $1"),
				payload.Sub).Scan(&email, &isActive, &isSuperuser, &isVerified)
			if err == nil && isActive {
				return c.JSON(fiber.Map{
					"id":           payload.Sub,
					"email":        email,
					"is_active":    isActive,
					"is_superuser": isSuperuser,
					"is_verified":  isVerified,
				})
			}
			return c.Status(401).JSON(fiber.Map{"detail": "invalid user"})
		}

		return c.JSON(fiber.Map{
			"id":    payload.Sub,
			"email": payload.Email,
		})
	}
}

// setAuthCookie sets the JWT token as an HttpOnly cookie for browser sessions.
func setAuthCookie(c *fiber.Ctx, token string, secure bool) {
	c.Response().Header.Add("Set-Cookie", BrowserSessionCookie(token, secure || c.Context().IsTLS()).String())
}

// JWTMiddleware validates JWT token on protected routes.
// Reads token from: 1) Authorization header, 2) auth_token cookie.
// If requireAuth is true, requests without a token are rejected (401).
// If false, unauthenticated requests pass through (dev mode).
func JWTMiddleware(secret string, requireAuth bool, cookieOrigins ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		// 1. Try X-API-Key or X-Api-Key header (programmatic access)
		apiKey := c.Get("X-API-Key")
		if apiKey == "" {
			apiKey = c.Get("X-Api-Key")
		}
		if apiKey != "" {
			// auth_db may be wrapped in a struct (to prevent fasthttp io.Closer auto-close)
			authDB := extractAuthDB(c)
			if authDB != nil {
				id := verifyAPIKey(c.UserContext(), authDB, apiKey)
				if id.Valid() {
					c.Locals("verified_api_key", id)
					c.Locals("user_id", id.UserID)
					c.Locals("api_key_permissions", id.Permissions)
					return c.Next()
				}
			}
			return c.Status(401).JSON(fiber.Map{"detail": "invalid API key"})
		}

		// 2. Try Authorization: Bearer <JWT>
		token := ""
		auth := c.Get("Authorization")
		if auth != "" {
			t := strings.TrimPrefix(auth, "Bearer ")
			if t != "" && t != "null" && t != "undefined" {
				token = t
			}
		}

		// 3. Fallback: cookie
		if token == "" {
			token = c.Cookies("auth_token")
			if token != "" && !cookieMutationAllowed(c, cookieOrigins...) {
				return c.SendStatus(fiber.StatusForbidden)
			}
		}

		if token == "" {
			if requireAuth {
				return c.Status(401).JSON(fiber.Map{"detail": "authorization required"})
			}
			return c.Next()
		}

		payload, valid := verifyJWT(token, secret)
		if !valid || !validSession(c.UserContext(), extractAuthDB(c), requireAuth, payload) {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid token"})
		}

		c.Locals("user_id", payload.Sub)
		c.Locals("email", payload.Email)
		c.Locals("verified_jwt", *payload)
		return c.Next()
	}
}

// JWTMiddlewareWithOIDC is JWTMiddleware with an additional fallback: tokens
// that are not Levara-issued JWTs are verified against an external OIDC
// provider (A1) and resolved through the external identity bridge.
// Order of attempts per request: API key → Levara JWT → OIDC bearer.
// When oidc is nil this is equivalent to JWTMiddleware.
func JWTMiddlewareWithOIDC(secret string, requireAuth bool, oidc ExternalBearerAuth, cookieOrigins ...string) fiber.Handler {
	base := JWTMiddleware(secret, requireAuth, cookieOrigins...)
	if oidc == nil {
		return base
	}
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		if c.Get("X-API-Key") == "" && strings.HasPrefix(c.Get("Authorization"), "Bearer ") {
			token := bearerToken(c.Get("Authorization"))
			if _, valid := verifyJWT(token, secret); token != "" && !valid {
				if principal, err := oidc.Authenticate(c.UserContext(), token); err == nil && activeExternalUser(c.UserContext(), extractAuthDB(c), principal) {
					c.Locals("user_id", principal.UserID)
					c.Locals("email", principal.Email)
					c.Locals("principal", principal)
					c.Locals("verified_external", principal)
					return c.Next()
				}
			}
		}
		return base(c)
	}
}

func validSession(ctx context.Context, db *sql.DB, requireAuth bool, payload *jwtPayload) bool {
	if payload == nil || payload.Sub == "" {
		return false
	}
	if db == nil {
		return !requireAuth
	}
	return accesspkg.ValidateCredential(ctx, db, Q, payload.Sub, payload.CredentialEpoch) == nil && accesspkg.ValidateBrowserSession(ctx, db, Q, payload.Sub, payload.SessionID) == nil
}

func activeExternalUser(ctx context.Context, db *sql.DB, principal ExternalPrincipal) bool {
	err := accesspkg.ValidateExternalCredential(ctx, db, Q, principal.UserID, principal.IssuedAt)
	return err == nil
}

// ExternalBearerAuth is the minimal seam the HTTP layer needs from an
// external token verifier (backlog A1). Protocol adapter code lives above
// this seam (see the architecture guard in pkg/access) — the HTTP layer
// only ever sees verified identity facts.
type ExternalBearerAuth interface {
	// Authenticate verifies the raw bearer token and returns the resolved
	// user identity, or an error when the token is not acceptable.
	Authenticate(ctx context.Context, token string) (ExternalPrincipal, error)
}

// ExternalPrincipal is the identity fact set the HTTP middleware consumes.
type ExternalPrincipal struct {
	IssuedAt  int64 // verified external token iat; never taken from an unverified payload
	ExpiresAt int64 // verified bearer exp; directory password login instead creates a local session
	UserID    string
	Email     string
}

// APIKeyPermissionMiddleware enforces the read/write label attached by
// JWTMiddleware across the complete REST surface. JWT and anonymous dev-mode
// requests have no API-key permission local and pass unchanged.
func APIKeyPermissionMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		permissions, _ := c.Locals("api_key_permissions").(string)
		if permissions == "" {
			return c.Next()
		}
		action := accesspkg.ActionWrite
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			action = accesspkg.ActionRead
		}
		if !accesspkg.APIKeyAllows(permissions, action) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "API key permissions denied"})
		}
		return c.Next()
	}
}

// verifyAPIKey checks X-API-Key against api_keys table. Token hashing and the
// key→user lookup stay here in the auth layer; the result is returned as the
// transport-independent accesspkg.APIKeyIdentity (zero value when invalid).
func verifyAPIKey(ctx context.Context, db *sql.DB, key string) accesspkg.APIKeyIdentity {
	h := apikeyHash(key)
	var keyID, userID, permissions string
	err := db.QueryRowContext(ctx,
		Q(`SELECT k.id, k.user_id, k.permissions FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.key_hash = $1 AND k.revoked = FALSE AND u.is_active = true`), h,
	).Scan(&keyID, &userID, &permissions)
	if err != nil || keyID == "" {
		return accesspkg.APIKeyIdentity{}
	}
	// Update last_used
	db.ExecContext(ctx, Q(`UPDATE api_keys SET last_used = $1 WHERE key_hash = $2`),
		time.Now().UTC().Format(time.RFC3339), h)
	if ctx.Err() != nil {
		return accesspkg.APIKeyIdentity{}
	}
	return accesspkg.APIKeyIdentity{KeyID: keyID, UserID: userID, Permissions: permissions}
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// apikeyHash hashes an API key for storage/lookup. When
// LEVARA_API_KEY_PEPPER (or JWT_SECRET as fallback) is set, keys are
// HMAC-SHA256'd with it so a stolen database cannot be attacked offline
// even for weaker keys (finding M24, 2026-09-03 review). Without a pepper
// the legacy bare SHA-256 is kept — generated keys are 32 random bytes,
// so this remains safe; rotating the pepper invalidates existing keys.
func apikeyHash(key string) string {
	pepper := os.Getenv("LEVARA_API_KEY_PEPPER")
	if pepper == "" {
		pepper = os.Getenv("JWT_SECRET")
	}
	if pepper == "" {
		return sha256Hash(key)
	}
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(key))
	return hex.EncodeToString(mac.Sum(nil))
}

// RegisterAPIKeyEndpoints registers API key management endpoints.
func RegisterAPIKeyEndpoints(app fiber.Router, cfg AuthConfig) {
	app.Post("/auth/keys", createAPIKeyHandler(cfg))
	app.Get("/auth/keys", listAPIKeysHandler(cfg))
	app.Delete("/auth/keys/:id", revokeAPIKeyHandler(cfg))
}

func createAPIKeyHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "authentication required to create API key"})
		}
		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}

		var req struct {
			Name        string `json:"name"`
			Permissions string `json:"permissions"`
		}
		c.BodyParser(&req)
		if req.Name == "" {
			req.Name = "default"
		}
		if req.Permissions == "" {
			req.Permissions = "read-write"
		}

		// Generate random key
		keyBytes := make([]byte, 32)
		if _, err := rand.Read(keyBytes); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "random generation failed"})
		}
		plainKey := "lk_" + hex.EncodeToString(keyBytes)
		keyHash := apikeyHash(plainKey)
		id := generateUUID()

		tx, err := cfg.DB.BeginTx(c.UserContext(), nil)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database unavailable"})
		}
		defer tx.Rollback()
		result, err := tx.ExecContext(c.UserContext(), Q(`UPDATE users SET is_active = is_active WHERE id = $1 AND is_active = true`), userID)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database unavailable"})
		}
		if n, err := result.RowsAffected(); err != nil || n != 1 {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid user"})
		}
		_, err = tx.ExecContext(c.UserContext(),
			Q(`INSERT INTO api_keys (id, key_hash, user_id, name, permissions, created_at)
			   VALUES ($1, $2, $3, $4, $5, $6)`),
			id, keyHash, userID, req.Name, req.Permissions,
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "failed to create key: " + err.Error()})
		}

		if err := tx.Commit(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "failed to create key"})
		}
		return c.Status(201).JSON(fiber.Map{
			"id":          id,
			"key":         plainKey, // shown only once!
			"name":        req.Name,
			"permissions": req.Permissions,
			"message":     "Save this key — it will not be shown again",
		})
	}
}

func listAPIKeysHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "authentication required"})
		}
		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}

		rows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, name, permissions, created_at, last_used, revoked
			   FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`), userID)
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "key listing unavailable"})
		}
		defer rows.Close()

		var keys []fiber.Map
		for rows.Next() {
			var id, name, perms, created string
			var lastUsed sql.NullString
			var revoked bool
			if err := rows.Scan(&id, &name, &perms, &created, &lastUsed, &revoked); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "key listing unavailable"})
			}
			keys = append(keys, fiber.Map{
				"id": id, "name": name, "permissions": perms,
				"created_at": created, "last_used": lastUsed.String,
				"revoked": revoked,
			})
		}
		if err := rows.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "key listing unavailable"})
		}
		if keys == nil {
			keys = []fiber.Map{}
		}
		return c.JSON(keys)
	}
}

func revokeAPIKeyHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.SetUserContext(ctx)
		keyID := c.Params("id")
		userID, _ := c.Locals("user_id").(string)
		if cfg.DB == nil {
			return c.JSON(fiber.Map{"revoked": false})
		}
		cfg.DB.ExecContext(c.UserContext(),
			Q(`UPDATE api_keys SET revoked = TRUE WHERE id = $1 AND user_id = $2`),
			keyID, userID)
		return c.JSON(fiber.Map{"revoked": true})
	}
}
