// memories.go — Project/user memory persistence via REST + MCP.
package http

import (
	"context"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RegisterMemoryAPI registers memory CRUD endpoints.
func RegisterMemoryAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/memories", saveMemoryHandler(cfg))
	app.Get("/memories", listMemoriesHandler(cfg))
	app.Get("/memories/:key", getMemoryHandler(cfg))
	app.Delete("/memories/:key", deleteMemoryHandler(cfg))
}

func saveMemoryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Key     string `json:"key"`
			Value   string `json:"value"`
			Type    string `json:"type"`
			OwnerID string `json:"owner_id"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid body"})
		}
		if req.Key == "" || req.Value == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "key and value required"})
		}
		if req.Type == "" {
			req.Type = "project"
		}
		if req.OwnerID == "" {
			req.OwnerID, _ = c.Locals("user_id").(string)
		}

		if cfg.DB == nil {
			return c.Status(500).JSON(fiber.Map{"detail": "database not configured"})
		}

		id := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)

		// Upsert: insert or update value+type+updated_at on conflict
		upsertSQL := `INSERT INTO memories (id, key, value, type, owner_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT(key, owner_id) DO UPDATE SET value = $3, type = $4, updated_at = $7`
		q, qargs := QArgs(upsertSQL, id, req.Key, req.Value, req.Type, req.OwnerID, now, now)
		_, err := cfg.DB.ExecContext(context.Background(), q, qargs...)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save failed: " + err.Error()})
		}

		return c.Status(201).JSON(fiber.Map{
			"id": id, "key": req.Key, "saved": true,
		})
	}
}

func listMemoriesHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		filterType := c.Query("type", "")
		ownerID, _ := c.Locals("user_id").(string)

		var items []fiber.Map
		if filterType != "" {
			rows, err := cfg.DB.QueryContext(context.Background(),
				Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
				 FROM memories WHERE type = $1 AND (owner_id = $2 OR owner_id = '')
				 ORDER BY updated_at DESC LIMIT 100`), filterType, ownerID)
			if err != nil {
				return c.JSON([]any{})
			}
			defer rows.Close()
			items = scanMemoryRows(rows)
		} else {
			rows, err := cfg.DB.QueryContext(context.Background(),
				Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
				 FROM memories WHERE owner_id = $1 OR owner_id = ''
				 ORDER BY updated_at DESC LIMIT 100`), ownerID)
			if err != nil {
				return c.JSON([]any{})
			}
			defer rows.Close()
			items = scanMemoryRows(rows)
		}

		if items == nil {
			items = []fiber.Map{}
		}
		return c.JSON(items)
	}
}

func getMemoryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		key := c.Params("key")
		if cfg.DB == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		ownerID, _ := c.Locals("user_id").(string)

		row := cfg.DB.QueryRowContext(context.Background(),
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '') LIMIT 1`), key, ownerID)

		var id, k, v, t, oid, ca, ua string
		if err := row.Scan(&id, &k, &v, &t, &oid, &ca, &ua); err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		return c.JSON(fiber.Map{
			"id": id, "key": k, "value": v, "type": t,
			"owner_id": oid, "created_at": ca, "updated_at": ua,
		})
	}
}

func deleteMemoryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		key := c.Params("key")
		if cfg.DB == nil {
			return c.Status(500).JSON(fiber.Map{"detail": "database not configured"})
		}
		ownerID, _ := c.Locals("user_id").(string)

		cfg.DB.ExecContext(context.Background(),
			Q(`DELETE FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '')`), key, ownerID)

		return c.JSON(fiber.Map{"deleted": true, "key": key})
	}
}

func scanMemoryRows(rows interface{ Next() bool; Scan(...any) error }) []fiber.Map {
	var items []fiber.Map
	for rows.Next() {
		var id, key, value, typ, ownerID, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &ca, &ua); err != nil {
			continue
		}
		items = append(items, fiber.Map{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "created_at": ca, "updated_at": ua,
		})
	}
	return items
}
