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
	"log"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
)

// AuthConfig holds auth settings.
type AuthConfig struct {
	PostgresDSN string
	JWTSecret   string  // random secret for signing tokens
	DB          *sql.DB // shared connection pool (nil if no PostgresDSN)
}

// RegisterAuthAPI registers /auth/login and /auth/register.
// It mutates cfg.JWTSecret in-place if empty (generates random secret).
func RegisterAuthAPI(app fiber.Router, cfg *AuthConfig) {
	if cfg.JWTSecret == "" {
		// Generate random secret if not provided
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand failed: " + err.Error())
		}
		cfg.JWTSecret = hex.EncodeToString(b)
	}

	app.Post("/auth/login", loginHandler(*cfg))
	app.Post("/auth/register", registerHandler(*cfg))

	// /auth/me — Cognee frontend calls this to check current user after login
	app.Get("/auth/me", authMeHandler(*cfg))
}

// ── JWT helpers ──

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtPayload struct {
	Sub   string `json:"sub"`   // user ID
	Email string `json:"email"`
	Exp   int64  `json:"exp"`   // expiry timestamp
	Iat   int64  `json:"iat"`   // issued at
}

func createJWT(userID, email, secret string) string {
	header := jwtHeader{Alg: "HS256", Typ: "JWT"}
	payload := jwtPayload{
		Sub:   userID,
		Email: email,
		Exp:   time.Now().Add(24 * time.Hour).Unix(),
		Iat:   time.Now().Unix(),
	}

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

func verifyJWT(token, secret string) (*jwtPayload, bool) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, false
	}

	// Verify signature
	sigInput := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sigInput))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return nil, false
	}

	// Decode payload
	pJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var payload jwtPayload
	if json.Unmarshal(pJSON, &payload) != nil {
		return nil, false
	}

	// Check expiry
	if payload.Exp < time.Now().Unix() {
		return nil, false
	}

	return &payload, true
}

// ── Handlers ──

func loginHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Support both JSON and form-encoded (Cognee frontend uses form)
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
			if os.Getenv("ENV") == "production" {
				return c.Status(500).JSON(fiber.Map{"detail": "database required in production mode"})
			}
			// No DB — accept any credentials in dev mode
			log.Printf("[WARN] dev-mode login: accepting any credentials for %s", email)
			token := createJWT("dev-user", email, cfg.JWTSecret)
			setAuthCookie(c, token)
			return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
		}

		var userID, hashedPassword string
		err := cfg.DB.QueryRowContext(context.Background(),
			Q("SELECT id, hashed_password FROM users WHERE email = $1"), email).Scan(&userID, &hashedPassword)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}

		if bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(password)) != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}

		token := createJWT(userID, email, cfg.JWTSecret)
		setAuthCookie(c, token)
		return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
	}
}

func registerHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}
		if err := c.BodyParser(&req); err != nil || req.Email == "" || req.Password == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "email and password required"})
		}

		hashedPw, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "hash error"})
		}

		userID := generateUUID()

		if cfg.DB != nil {
			// Insert into principals first (FK requirement)
			_, _ = cfg.DB.ExecContext(context.Background(),
				Q("INSERT INTO principals (id, type) VALUES ($1, 'user') ON CONFLICT DO NOTHING"), userID)

			_, err = cfg.DB.ExecContext(context.Background(),
				Q(`INSERT INTO users (id, email, hashed_password, is_active, is_superuser, is_verified)
				 VALUES ($1, $2, $3, true, false, false)`),
				userID, req.Email, string(hashedPw))
			if err != nil {
				return c.Status(409).JSON(fiber.Map{"detail": "user already exists or db error: " + err.Error()})
			}
		}

		token := createJWT(userID, req.Email, cfg.JWTSecret)
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
// GET /auth/me — called by Cognee frontend after login to verify session.
func authMeHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
		if !valid {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid token"})
		}

		// If DB available, fetch full user record
		if cfg.DB != nil {
			var email string
			var isActive, isSuperuser, isVerified bool
			err := cfg.DB.QueryRowContext(context.Background(),
				Q("SELECT email, is_active, is_superuser, is_verified FROM users WHERE id = $1"),
				payload.Sub).Scan(&email, &isActive, &isSuperuser, &isVerified)
			if err == nil {
				return c.JSON(fiber.Map{
					"id":           payload.Sub,
					"email":        email,
					"is_active":    isActive,
					"is_superuser": isSuperuser,
					"is_verified":  isVerified,
				})
			}
		}

		return c.JSON(fiber.Map{
			"id":    payload.Sub,
			"email": payload.Email,
		})
	}
}

// setAuthCookie sets the JWT token as an HttpOnly cookie for browser sessions.
func setAuthCookie(c *fiber.Ctx, token string) {
	c.Cookie(&fiber.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		MaxAge:   86400, // 24 hours
		HTTPOnly: true,
		SameSite: "Lax",
		Secure:   c.Protocol() == "https",
	})
}

// JWTMiddleware validates JWT token on protected routes.
// Reads token from: 1) Authorization header, 2) auth_token cookie.
// If requireAuth is true, requests without a token are rejected (401).
// If false, unauthenticated requests pass through (dev mode).
func JWTMiddleware(secret string, requireAuth bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// 1. Try X-API-Key or X-Api-Key header (programmatic access)
		apiKey := c.Get("X-API-Key")
		if apiKey == "" {
			apiKey = c.Get("X-Api-Key")
		}
		if apiKey != "" {
			// auth_db may be wrapped in a struct (to prevent fasthttp io.Closer auto-close)
			authDB := extractAuthDB(c)
			if authDB != nil {
				userID, perms := verifyAPIKey(authDB, apiKey)
				if userID != "" {
					c.Locals("user_id", userID)
					c.Locals("api_key_permissions", perms)
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
		}

		if token == "" {
			if requireAuth {
				return c.Status(401).JSON(fiber.Map{"detail": "authorization required"})
			}
			return c.Next()
		}

		payload, valid := verifyJWT(token, secret)
		if !valid {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid token"})
		}

		c.Locals("user_id", payload.Sub)
		c.Locals("email", payload.Email)
		return c.Next()
	}
}

// verifyAPIKey checks X-API-Key against api_keys table.
// Returns (user_id, permissions) if valid, ("", "") if not.
func verifyAPIKey(db *sql.DB, key string) (string, string) {
	h := sha256Hash(key)
	var userID, permissions string
	err := db.QueryRow(
		Q(`SELECT user_id, permissions FROM api_keys WHERE key_hash = $1 AND revoked = FALSE`), h,
	).Scan(&userID, &permissions)
	if err != nil {
		return "", ""
	}
	// Update last_used
	db.Exec(Q(`UPDATE api_keys SET last_used = $1 WHERE key_hash = $2`),
		time.Now().UTC().Format(time.RFC3339), h)
	return userID, permissions
}

func sha256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// RegisterAPIKeyEndpoints registers API key management endpoints.
func RegisterAPIKeyEndpoints(app fiber.Router, cfg AuthConfig) {
	app.Post("/auth/keys", createAPIKeyHandler(cfg))
	app.Get("/auth/keys", listAPIKeysHandler(cfg))
	app.Delete("/auth/keys/:id", revokeAPIKeyHandler(cfg))
}

func createAPIKeyHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
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
		keyHash := sha256Hash(plainKey)
		id := generateUUID()

		_, err := cfg.DB.Exec(
			Q(`INSERT INTO api_keys (id, key_hash, user_id, name, permissions, created_at)
			   VALUES ($1, $2, $3, $4, $5, $6)`),
			id, keyHash, userID, req.Name, req.Permissions,
			time.Now().UTC().Format(time.RFC3339))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "failed to create key: " + err.Error()})
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
		userID, _ := c.Locals("user_id").(string)
		if cfg.DB == nil {
			return c.JSON([]any{})
		}

		rows, err := cfg.DB.Query(
			Q(`SELECT id, name, permissions, created_at, last_used, revoked
			   FROM api_keys WHERE user_id = $1 ORDER BY created_at DESC`), userID)
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()

		var keys []fiber.Map
		for rows.Next() {
			var id, name, perms, created string
			var lastUsed sql.NullString
			var revoked bool
			rows.Scan(&id, &name, &perms, &created, &lastUsed, &revoked)
			keys = append(keys, fiber.Map{
				"id": id, "name": name, "permissions": perms,
				"created_at": created, "last_used": lastUsed.String,
				"revoked": revoked,
			})
		}
		if keys == nil {
			keys = []fiber.Map{}
		}
		return c.JSON(keys)
	}
}

func revokeAPIKeyHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		keyID := c.Params("id")
		userID, _ := c.Locals("user_id").(string)
		if cfg.DB == nil {
			return c.JSON(fiber.Map{"revoked": false})
		}
		cfg.DB.Exec(
			Q(`UPDATE api_keys SET revoked = TRUE WHERE id = $1 AND user_id = $2`),
			keyID, userID)
		return c.JSON(fiber.Map{"revoked": true})
	}
}
