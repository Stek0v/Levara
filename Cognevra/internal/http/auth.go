// auth.go — JWT authentication for multi-user mode.
// Simple stateless JWT auth with bcrypt password hashing.
package http

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"

	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
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
		rand.Read(b)
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
			// No DB — accept any credentials in dev mode
			token := createJWT("dev-user", email, cfg.JWTSecret)
			setAuthCookie(c, token)
			return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
		}

		var userID, hashedPassword string
		err := cfg.DB.QueryRowContext(c.Context(),
			"SELECT id, hashed_password FROM users WHERE email = $1", email).Scan(&userID, &hashedPassword)
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
			_, _ = cfg.DB.ExecContext(c.Context(),
				"INSERT INTO principals (id, type) VALUES ($1, 'user') ON CONFLICT DO NOTHING", userID)

			_, err = cfg.DB.ExecContext(c.Context(),
				`INSERT INTO users (id, email, hashed_password, is_active, is_superuser, is_verified)
				 VALUES ($1, $2, $3, true, false, false)`,
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
	rand.Read(b)
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
			err := cfg.DB.QueryRowContext(c.Context(),
				"SELECT email, is_active, is_superuser, is_verified FROM users WHERE id = $1",
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
	})
}

// JWTMiddleware validates JWT token on protected routes.
// Reads token from: 1) Authorization header, 2) auth_token cookie.
// If requireAuth is true, requests without a token are rejected (401).
// If false, unauthenticated requests pass through (dev mode).
func JWTMiddleware(secret string, requireAuth bool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Try Authorization header first
		token := ""
		auth := c.Get("Authorization")
		if auth != "" {
			t := strings.TrimPrefix(auth, "Bearer ")
			// Ignore "null", "undefined", empty Bearer values
			if t != "" && t != "null" && t != "undefined" {
				token = t
			}
		}

		// Fallback: read from cookie
		if token == "" {
			token = c.Cookies("auth_token")
		}

		if token == "" {
			if requireAuth {
				return c.Status(401).JSON(fiber.Map{"detail": "authorization required"})
			}
			return c.Next() // dev mode: allow unauthenticated
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
