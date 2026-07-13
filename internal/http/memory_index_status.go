package http

import "github.com/gofiber/fiber/v2"

func memoryIndexStatusHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MemoryIndexOutbox == nil {
			return c.JSON(fiber.Map{"counts": fiber.Map{}, "jobs": []any{}})
		}
		owner, _ := c.Locals("user_id").(string)
		counts, err := cfg.MemoryIndexOutbox.Counts(c.UserContext())
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory index status failed"})
		}
		jobs, err := cfg.MemoryIndexOutbox.List(c.UserContext(), owner, 20)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory index jobs failed"})
		}
		return c.JSON(fiber.Map{"counts": counts, "jobs": jobs})
	}
}
