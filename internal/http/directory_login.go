package http

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

// ExternalPasswordAuth returns a verified, provisioned identity. Protocol
// connections and directory-specific claims belong in the composition layer.
type ExternalPasswordAuth interface {
	AuthenticateCredentials(ctx context.Context, username, password string) (ExternalPrincipal, error)
}

// directoryLoginHandler authenticates through the explicitly configured directory.
// @Summary Directory password login
// @Tags auth
// @Accept json,x-www-form-urlencoded
// @Produce json
// @Success 200 {object} map[string]string
// @Failure 401 {object} map[string]string
// @Router /auth/directory/login [post]
func directoryLoginHandler(cfg AuthConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		c.Set("Cache-Control", "no-store")
		if !CookieRequestAllowed(c, false, cfg.CookieOrigins...) {
			return c.SendStatus(fiber.StatusForbidden)
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if c.Is("json") {
			if c.BodyParser(&req) != nil {
				return c.Status(400).JSON(fiber.Map{"detail": "invalid credentials input"})
			}
		} else {
			req.Username = c.FormValue("username")
			req.Password = c.FormValue("password")
		}
		if req.Username == "" || req.Password == "" || len(req.Username) > 1024 || len(req.Password) > 4096 {
			return c.Status(400).JSON(fiber.Map{"detail": "username and password required"})
		}
		if cfg.DB == nil || cfg.DirectoryAuth == nil {
			return c.SendStatus(fiber.StatusServiceUnavailable)
		}
		principal, err := cfg.DirectoryAuth.AuthenticateCredentials(ctx, req.Username, req.Password)
		if err != nil || principal.IssuedAt <= 0 {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}
		token, err := IssueExternalBrowserSessionJWT(ctx, cfg.DB, principal.UserID, principal.Email, cfg.JWTSecret, principal.IssuedAt)
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "invalid credentials"})
		}
		setAuthCookie(c, token, cfg.CookieSecure)
		return c.JSON(fiber.Map{"access_token": token, "token_type": "bearer"})
	}
}
