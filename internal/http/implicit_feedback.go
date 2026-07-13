package http

import (
	"github.com/gofiber/fiber/v2"
)

func implicitFeedbackHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON(fiber.Map{"total": 0, "by_signal": fiber.Map{}})
		}
		owner, _ := c.Locals("user_id").(string)
		query := `SELECT signal,COUNT(*) FROM implicit_feedback`
		args := []any{}
		if owner != "" {
			query += ` WHERE owner_id=$1`
			args = append(args, owner)
		}
		query += ` GROUP BY signal`
		rows, err := cfg.DB.QueryContext(c.UserContext(), Q(query), args...)
		if err != nil {
			return c.JSON(fiber.Map{"total": 0, "by_signal": fiber.Map{}})
		}
		defer rows.Close()
		by := map[string]int{}
		total := 0
		for rows.Next() {
			var signal string
			var n int
			if rows.Scan(&signal, &n) == nil {
				by[signal] = n
				total += n
			}
		}
		return c.JSON(fiber.Map{"total": total, "by_signal": by})
	}
}
