package http

import (
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/audit"
)

func mcpAnalyticsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		hours := 24
		if raw := c.Query("hours"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && (n == 1 || n == 24 || n == 168 || n == 720) {
				hours = n
			}
		}
		summary, err := cfg.MCPAuditReadModel.Summary(c.UserContext(), time.Now().Add(-time.Duration(hours)*time.Hour))
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "MCP analytics query failed"})
		}
		return c.JSON(fiber.Map{"window_hours": hours, "summary": summary, "projection": cfg.MCPAuditReadModel.Health()})
	}
}

func mcpAnalyticsEventsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MCPAuditReadModel == nil {
			return c.Status(503).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		hours := 24
		if n, err := strconv.Atoi(c.Query("hours", "24")); err == nil && (n == 1 || n == 24 || n == 168 || n == 720) {
			hours = n
		}
		limit, _ := strconv.Atoi(c.Query("limit", "50"))
		offset, _ := strconv.Atoi(c.Query("offset", "0"))
		includeArgs := c.QueryBool("include_args", false)
		if includeArgs {
			if err := requireSuperuser(c, cfg); err != nil {
				return err
			}
		}
		events, err := cfg.MCPAuditReadModel.Events(c.UserContext(), audit.EventFilter{Since: time.Now().Add(-time.Duration(hours) * time.Hour), Tool: c.Query("tool"), Outcome: c.Query("outcome"), Client: c.Query("client"), Collection: c.Query("collection"), Limit: limit, Offset: offset, IncludeArgs: includeArgs})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "MCP audit event query failed"})
		}
		return c.JSON(fiber.Map{"events": events, "limit": limit, "offset": offset})
	}
}
