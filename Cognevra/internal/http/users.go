// users.go — User management endpoints (protected by JWT middleware).
// GET /users/me — current user profile
// PUT /users/me — update profile (email)
// PUT /users/me/password — change password
package http

import (
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

		err := cfg.DB.QueryRowContext(c.Context(),
			`SELECT id, email, is_active, is_superuser, is_verified, created_at, updated_at
			 FROM users WHERE id = $1`, userID).Scan(
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

		_, err := cfg.DB.ExecContext(c.Context(),
			"UPDATE users SET email = $1, updated_at = NOW() WHERE id = $2",
			req.Email, userID)
		if err != nil {
			return c.Status(409).JSON(fiber.Map{"detail": "email already in use or db error"})
		}

		return c.JSON(fiber.Map{"id": userID, "email": req.Email, "updated": true})
	}
}

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
		err := cfg.DB.QueryRowContext(c.Context(),
			"SELECT hashed_password FROM users WHERE id = $1", userID).Scan(&hashedPassword)
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

		_, err = cfg.DB.ExecContext(c.Context(),
			"UPDATE users SET hashed_password = $1, updated_at = NOW() WHERE id = $2",
			string(newHash), userID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "update failed"})
		}

		return c.JSON(fiber.Map{"updated": true})
	}
}
