// api_admin.go — maintenance endpoints split out of api.go (T4).
// Covers POST /prune/data, /prune/system, PATCH /datasets/:id/data/:dataId
// and GET /heartbeats.
package http

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/gofiber/fiber/v2"
)

func pruneDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB != nil {
			cfg.DB.ExecContext(context.Background(), "DELETE FROM dataset_data")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM data")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM datasets")
		}
		return c.JSON(fiber.Map{"status": "ok", "pruned": "data"})
	}
}

func pruneSystemHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB != nil {
			cfg.DB.ExecContext(context.Background(), "DELETE FROM graph_nodes")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM graph_edges")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM dataset_data")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM data")
			cfg.DB.ExecContext(context.Background(), "DELETE FROM datasets")
		}
		return c.JSON(fiber.Map{"status": "ok", "pruned": "system"})
	}
}

func updateDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		dataID := c.Params("dataId")
		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database required"})
		}
		body := c.Body()
		if len(body) == 0 {
			return c.Status(400).JSON(fiber.Map{"detail": "content required"})
		}
		_, err := cfg.DB.ExecContext(context.Background(),
			Q("UPDATE data SET name = $1, updated_at = NOW() WHERE id = $2"),
			string(body), dataID)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		return c.JSON(fiber.Map{"id": dataID, "updated": true})
	}
}

func heartbeatsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		eventType := c.Query("type", "")
		limit := c.QueryInt("limit", 20)
		if limit > 100 {
			limit = 100
		}

		var rows *sql.Rows
		var err error
		if eventType != "" {
			q, a := QArgs(`SELECT id, event_type, payload, created_at FROM heartbeats WHERE event_type = $1 ORDER BY created_at DESC LIMIT $2`, eventType, limit)
			rows, err = cfg.DB.QueryContext(c.Context(), q, a...)
		} else {
			rows, err = cfg.DB.QueryContext(c.Context(), Q(`SELECT id, event_type, payload, created_at FROM heartbeats ORDER BY created_at DESC LIMIT $1`), limit)
		}
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		defer rows.Close()

		type hbEntry struct {
			ID        string          `json:"id"`
			EventType string          `json:"event_type"`
			Payload   json.RawMessage `json:"payload"`
			CreatedAt string          `json:"created_at"`
		}
		var events []hbEntry
		for rows.Next() {
			var e hbEntry
			var payload string
			if err := rows.Scan(&e.ID, &e.EventType, &payload, &e.CreatedAt); err != nil {
				continue
			}
			e.Payload = json.RawMessage(payload)
			events = append(events, e)
		}
		if events == nil {
			events = []hbEntry{}
		}
		return c.JSON(events)
	}
}
