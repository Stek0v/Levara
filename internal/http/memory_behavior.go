package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/trajectory"
	"github.com/stek0v/levara/pkg/audit"
)

func memoryBehaviorHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		rows, err := cfg.MCPAuditReadModel.EventsForTrajectories(c.UserContext(), audit.EventFilter{
			Since:       time.Now().Add(-time.Duration(windowHours(c)) * time.Hour),
			Client:      c.Query("client"),
			Collection:  c.Query("collection"),
			Limit:       20000,
			IncludeArgs: true,
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "memory behavior query failed"})
		}
		traces := trajectory.Build(rows, true)
		return c.JSON(fiber.Map{
			"window_hours": windowHours(c),
			"collection":   c.Query("collection"),
			"client":       c.Query("client"),
			"summary":      trajectory.AnalyzeBehavior(traces),
		})
	}
}
