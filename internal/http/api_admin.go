// api_admin.go — maintenance endpoints split out of api.go (T4).
// Covers POST /prune/data, /prune/system, PATCH /datasets/:id/data/:dataId
// and GET /heartbeats.
package http

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/gofiber/fiber/v2"
	accesspkg "github.com/stek0v/levara/pkg/access"
)

// requireSuperuser returns nil when the caller's JWT resolves to a user
// with is_superuser=true, or a 403/503 fiber error otherwise. Used by the
// destructive /prune/* handlers — the JWT middleware proves identity, but
// the /prune/* endpoints need an extra role gate because they wipe ALL
// datasets and graph state.
//
// Missing user_id (no JWT) falls through as unauthorized; missing DB
// (server in memory-only mode) blocks the op since we can't verify the
// role — prune is destructive enough to justify fail-closed.
func requireSuperuser(c *fiber.Ctx, cfg APIConfig) error {
	if cfg.DB == nil {
		return c.Status(fiber.StatusServiceUnavailable).
			JSON(fiber.Map{"detail": "database required to verify superuser role"})
	}
	userID, _ := c.Locals("user_id").(string)
	if userID == "" {
		return c.Status(fiber.StatusForbidden).
			JSON(fiber.Map{"detail": "superuser role required"})
	}
	isSuperuser, err := (accesspkg.SQLPolicy{DB: cfg.DB, Q: Q}).IsSuperuser(c.Context(), userID)
	if err != nil {
		return c.Status(fiber.StatusForbidden).
			JSON(fiber.Map{"detail": "superuser role required"})
	}
	if !isSuperuser {
		return c.Status(fiber.StatusForbidden).
			JSON(fiber.Map{"detail": "superuser role required"})
	}
	return nil
}

// pruneDataHandler — POST /prune/data. Destructive: wipes datasets +
// dataset_data + data. Superuser-only (M5).
//
// @Summary     Wipe all datasets and their data (superuser-only)
// @Tags        admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]string
// @Failure     403 {object} map[string]any "superuser role required"
// @Failure     500 {object} map[string]any "SQL error"
// @Router      /prune/data [post]
func pruneDataHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := requireSuperuser(c, cfg); err != nil {
			return err
		}
		ctx := c.Context()
		if _, err := cfg.DB.ExecContext(ctx, "DELETE FROM dataset_data"); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		if _, err := cfg.DB.ExecContext(ctx, "DELETE FROM data"); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		if _, err := cfg.DB.ExecContext(ctx, "DELETE FROM datasets"); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		return c.JSON(fiber.Map{"status": "ok", "pruned": "data"})
	}
}

// pruneSystemHandler — POST /prune/system. Destructive: everything
// pruneData clears PLUS the graph (graph_nodes + graph_edges). Used when
// you want a fresh-from-scratch deployment without reimaging disks.
//
// @Summary     Wipe datasets + graph (superuser-only)
// @Tags        admin
// @Produce     json
// @Security    BearerAuth
// @Success     200 {object} map[string]string
// @Failure     403 {object} map[string]any "superuser role required"
// @Failure     500 {object} map[string]any "SQL error"
// @Router      /prune/system [post]
func pruneSystemHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := requireSuperuser(c, cfg); err != nil {
			return err
		}
		ctx := c.Context()
		// Fail fast on the first SQL error rather than plowing through a
		// partial wipe; the caller can inspect the error and retry.
		for _, stmt := range []string{
			"DELETE FROM graph_nodes",
			"DELETE FROM graph_edges",
			"DELETE FROM dataset_data",
			"DELETE FROM data",
			"DELETE FROM datasets",
		} {
			if _, err := cfg.DB.ExecContext(ctx, stmt); err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
			}
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
