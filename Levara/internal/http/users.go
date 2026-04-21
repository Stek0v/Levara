// users.go — User management endpoints (protected by JWT middleware).
// GET /users/me — current user profile
// PUT /users/me — update profile (email)
// PUT /users/me/password — change password
package http

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"golang.org/x/crypto/bcrypt"
)

type UserDTO struct {
	ID         string  `json:"id"`
	Email      string  `json:"email"`
	IsActive   bool    `json:"is_active"`
	IsSuperuser bool   `json:"is_superuser"`
	IsVerified bool    `json:"is_verified"`
	CreatedAt  string  `json:"created_at"`
	UpdatedAt  *string `json:"updated_at"`
}

// userLookupHandler — GET /users?email=... — admin-side lookup used by
// the WebUI when sharing a dataset to a user identified by email.
//
// @Summary     Look up a user by email
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Param       email query string true "Exact email"
// @Success     200 {object} UserDTO
// @Failure     400 {object} map[string]any "email query parameter required"
// @Failure     404 {object} map[string]any "no user with that email"
// @Router      /users [get]
func userLookupHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		email := c.Query("email")
		if email == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "email query parameter required"})
		}
		if cfg.DB == nil {
			return c.JSON([]UserDTO{})
		}
		var u UserDTO
		err := cfg.DB.QueryRowContext(context.Background(),
			Q("SELECT id, email, COALESCE(is_superuser,false), created_at FROM users WHERE email = $1"), email,
		).Scan(&u.ID, &u.Email, &u.IsSuperuser, &u.CreatedAt)
		if err != nil {
			return c.JSON([]UserDTO{})
		}
		return c.JSON([]UserDTO{u})
	}
}

// userMeHandler — GET /users/me. Returns the JWT-resolved user record.
// Distinct from /auth/me which validates the token; this one assumes
// auth has already passed and just renders the DTO.
//
// @Summary     Return the authenticated user's full record
// @Tags        users
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} UserDTO
// @Failure     401 {object} map[string]any "no user_id in context"
// @Router      /users/me [get]
func userMeHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "not authenticated"})
		}

		if cfg.DB == nil {
			// Dev mode — return minimal info from JWT
			email, _ := c.Locals("email").(string)
			return c.JSON(UserDTO{
				ID: userID, Email: email, IsActive: true,
			})
		}

		var u UserDTO
		var createdAt time.Time
		var updatedAt *time.Time

		err := cfg.DB.QueryRowContext(context.Background(),
			Q(`SELECT id, email, is_active, is_superuser, is_verified, created_at, updated_at
			 FROM users WHERE id = $1`), userID).Scan(
			&u.ID, &u.Email, &u.IsActive, &u.IsSuperuser, &u.IsVerified, &createdAt, &updatedAt)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": "user not found"})
		}

		u.CreatedAt = createdAt.Format(time.RFC3339)
		if updatedAt != nil {
			s := updatedAt.Format(time.RFC3339)
			u.UpdatedAt = &s
		}

		return c.JSON(u)
	}
}

// userUpdateHandler — PUT /users/me. Updates email / username on the
// caller's own record only; can't modify other users (RBAC handled
// separately at /admin endpoints).
//
// @Summary     Update the caller's profile
// @Tags        users
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "email + username"
// @Success     200 {object} UserDTO
// @Failure     400 {object} map[string]any "invalid body"
// @Router      /users/me [put]
func userUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "not authenticated"})
		}

		var req struct {
			Email string `json:"email"`
		}
		if err := c.BodyParser(&req); err != nil || req.Email == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "email required"})
		}

		if cfg.DB == nil {
			return c.JSON(fiber.Map{"id": userID, "email": req.Email, "updated": true})
		}

		_, err := cfg.DB.ExecContext(context.Background(),
			Q("UPDATE users SET email = $1, updated_at = NOW() WHERE id = $2"),
			req.Email, userID)
		if err != nil {
			return c.Status(409).JSON(fiber.Map{"detail": "email already in use or db error"})
		}

		return c.JSON(fiber.Map{"id": userID, "email": req.Email, "updated": true})
	}
}

// userChangePasswordHandler — PUT /users/me/password. Requires the
// current password as a re-auth check before bcrypt-hashing the new one.
//
// @Summary     Change the caller's password
// @Tags        users
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "current_password + new_password"
// @Success     200 {object} map[string]any
// @Failure     400 {object} map[string]any "missing fields"
// @Failure     401 {object} map[string]any "current_password mismatch"
// @Router      /users/me/password [put]
func userChangePasswordHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, _ := c.Locals("user_id").(string)
		if userID == "" {
			return c.Status(401).JSON(fiber.Map{"detail": "not authenticated"})
		}

		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := c.BodyParser(&req); err != nil || req.CurrentPassword == "" || req.NewPassword == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "current_password and new_password required"})
		}

		if len(req.NewPassword) < 6 {
			return c.Status(400).JSON(fiber.Map{"detail": "new password must be at least 6 characters"})
		}

		if cfg.DB == nil {
			return c.JSON(fiber.Map{"updated": true})
		}

		// Verify current password
		var hashedPassword string
		err := cfg.DB.QueryRowContext(context.Background(),
			Q("SELECT hashed_password FROM users WHERE id = $1"), userID).Scan(&hashedPassword)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": "user not found"})
		}

		if bcrypt.CompareHashAndPassword([]byte(hashedPassword), []byte(req.CurrentPassword)) != nil {
			return c.Status(401).JSON(fiber.Map{"detail": "current password is incorrect"})
		}

		// Hash and update
		newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "hash error"})
		}

		_, err = cfg.DB.ExecContext(context.Background(),
			Q("UPDATE users SET hashed_password = $1, updated_at = NOW() WHERE id = $2"),
			string(newHash), userID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "update failed"})
		}

		return c.JSON(fiber.Map{"updated": true})
	}
}
