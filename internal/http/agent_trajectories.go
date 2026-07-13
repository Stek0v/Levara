package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/trajectory"
	"github.com/stek0v/levara/pkg/audit"
)

func agentTrajectoriesHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		includeArgs := c.QueryBool("include_args", false)
		if includeArgs {
			if err := requireSuperuser(c, cfg); err != nil {
				return err
			}
		}
		rows, err := cfg.MCPAuditReadModel.EventsForTrajectories(c.UserContext(), audit.EventFilter{
			Since:       time.Now().Add(-time.Duration(windowHours(c)) * time.Hour),
			Tool:        c.Query("tool"),
			Client:      c.Query("client"),
			Collection:  c.Query("collection"),
			Limit:       20000,
			IncludeArgs: includeArgs,
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "agent trajectory query failed"})
		}
		traces := trajectory.Build(rows, false)
		limit := c.QueryInt("limit", 50)
		offset := c.QueryInt("offset", 0)
		return c.JSON(fiber.Map{
			"window_hours": windowHours(c),
			"limit":        limit,
			"offset":       offset,
			"total":        len(traces),
			"trajectories": trajectory.Page(traces, limit, offset),
		})
	}
}

func agentTrajectoryDetailHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.MCPAuditReadModel == nil {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{"error": "MCP audit read model unavailable"})
		}
		includeArgs := c.QueryBool("include_args", false)
		if includeArgs {
			if err := requireSuperuser(c, cfg); err != nil {
				return err
			}
		}
		rows, err := cfg.MCPAuditReadModel.EventsForTrajectories(c.UserContext(), audit.EventFilter{
			Since:       time.Now().Add(-time.Duration(windowHours(c)) * time.Hour),
			Limit:       20000,
			IncludeArgs: includeArgs,
		})
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "agent trajectory query failed"})
		}
		for _, tr := range trajectory.Build(rows, true) {
			if tr.ID == c.Params("id") {
				return c.JSON(tr)
			}
		}
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "trajectory not found"})
	}
}

func windowHours(c *fiber.Ctx) int {
	hours := 24
	if n := c.QueryInt("hours", 24); n == 1 || n == 24 || n == 168 || n == 720 {
		hours = n
	}
	return hours
}
