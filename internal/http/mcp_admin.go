package http

import (
	"context"
	"sort"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/mcp"
)

type mcpAdminTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Group       string         `json:"group"`
	Status      string         `json:"status"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type mcpAdminSession struct {
	SessionID  string `json:"session_id"`
	Count      int    `json:"count"`
	LastAt     string `json:"last_at,omitempty"`
	SearchType string `json:"search_type,omitempty"`
}

func RegisterMCPAdminAPI(app fiber.Router, cfg APIConfig) {
	app.Get("/admin/mcp/tools", mcpAdminToolsHandler())
	app.Get("/admin/mcp/summary", mcpAdminSummaryHandler(cfg))
	app.Get("/admin/mcp/sessions", mcpAdminSessionsHandler(cfg))
}

func mcpAdminToolsHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		inventory := mcp.MCPInventory()
		byName := make(map[string]struct {
			Group  string
			Status string
		}, len(inventory))
		for _, item := range inventory {
			byName[item.Name] = struct {
				Group  string
				Status string
			}{Group: item.Group, Status: string(item.Status)}
		}
		descriptors := configuredMCPToolDescriptors()
		tools := make([]mcpAdminTool, 0, len(descriptors))
		for _, descriptor := range descriptors {
			meta := byName[descriptor.Name]
			tools = append(tools, mcpAdminTool{
				Name:        descriptor.Name,
				Description: descriptor.Description,
				Group:       meta.Group,
				Status:      meta.Status,
				InputSchema: descriptor.InputSchema,
			})
		}
		sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
		return c.JSON(fiber.Map{"tools": tools, "total": len(tools)})
	}
}

func mcpAdminSummaryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tools := mcp.MCPInventory()
		byGroup := map[string]int{}
		byStatus := map[string]int{}
		for _, tool := range tools {
			byGroup[tool.Group]++
			byStatus[string(tool.Status)]++
		}
		sessions, _ := listMCPAdminSessions(c.UserContext(), cfg, 5)
		pinned, missingMetadata := 0, 0
		if cfg.DB != nil {
			_ = cfg.DB.QueryRowContext(c.UserContext(), Q(`SELECT COUNT(*) FROM memories WHERE is_pinned = TRUE OR is_pinned = 1`)).Scan(&pinned)
			_ = cfg.DB.QueryRowContext(c.UserContext(), Q(`SELECT COUNT(*) FROM memories WHERE COALESCE(room, '') = '' OR COALESCE(hall, '') = ''`)).Scan(&missingMetadata)
		}
		return c.JSON(fiber.Map{
			"tools_total":              len(tools),
			"tools_by_group":           byGroup,
			"tools_by_status":          byStatus,
			"recent_sessions":          sessions,
			"pinned_memories":          pinned,
			"memory_metadata_warnings": missingMetadata,
			"audit_enabled":            cfg.MCPAudit != nil,
		})
	}
}

func mcpAdminSessionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 20)
		sessions, err := listMCPAdminSessions(c.UserContext(), cfg, limit)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": err.Error()})
		}
		return c.JSON(fiber.Map{"sessions": sessions, "total": len(sessions)})
	}
}

func listMCPAdminSessions(ctx context.Context, cfg APIConfig, limit int) ([]mcpAdminSession, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if cfg.DB == nil {
		return []mcpAdminSession{}, nil
	}
	rows, err := cfg.DB.QueryContext(
		ctx,
		Q(`SELECT session_id, COUNT(*), COALESCE(MAX(created_at), ''), COALESCE(MAX(search_type), '')
		   FROM interactions
		   WHERE session_id <> ''
		   GROUP BY session_id
		   ORDER BY COALESCE(MAX(created_at), '') DESC
		   LIMIT $1`),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []mcpAdminSession{}
	for rows.Next() {
		var session mcpAdminSession
		if err := rows.Scan(&session.SessionID, &session.Count, &session.LastAt, &session.SearchType); err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}
