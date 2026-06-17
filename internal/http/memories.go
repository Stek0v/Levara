// memories.go — Project/user memory persistence via REST + MCP.
package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RegisterMemoryAPI registers memory CRUD endpoints.
func RegisterMemoryAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/memories", saveMemoryHandler(cfg))
	app.Get("/memories", listMemoriesHandler(cfg))
	app.Get("/memories/stream", memoryEventsStreamHandler())
	app.Get("/memories/:key", getMemoryHandler(cfg))
	app.Delete("/memories/:key", deleteMemoryHandler(cfg))
}

// saveMemoryHandler — POST /memories. Stores a key/value memory with
// optional type/room/hall metadata for filtered retrieval.
//
// @Summary     Save a project memory
// @Description Mirror of the MCP `save_memory` tool — same key/value/hall vocab, same per-collection scoping. Used by the WebUI memory page.
// @Tags        memories
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "key + value, optional type/room/hall/collection"
// @Success     200 {object} map[string]any
// @Failure     400 {object} map[string]any "missing key or value"
// @Router      /memories [post]
func saveMemoryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Key            string `json:"key"`
			Value          string `json:"value"`
			Type           string `json:"type"`
			OwnerID        string `json:"owner_id"`
			CollectionName string `json:"collection_name"`
			Room           string `json:"room"`
			Hall           string `json:"hall"`
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
		allowedTypes := map[string]bool{
			"fact": true, "event": true, "decision": true, "preference": true,
			"advice": true, "discovery": true, "project": true, "user": true,
			"feedback": true, "reference": true,
		}
		if !allowedTypes[req.Type] {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid memory type: " + req.Type})
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
		upsertSQL := `INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET value = $11, type = $12, room = $13, hall = $14, updated_at = $15`
		q, qargs := QArgs(upsertSQL,
			id, req.Key, req.Value, req.Type, req.OwnerID, req.CollectionName, req.Room, req.Hall, now, now,
			req.Value, req.Type, req.Room, req.Hall, now)
		_, err := cfg.DB.ExecContext(context.Background(), q, qargs...)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save failed: " + err.Error()})
		}

		memoryEvents.Publish(MemoryEvent{
			Kind:      "memory.saved",
			Key:       req.Key,
			Value:     req.Value,
			Type:      req.Type,
			OwnerID:   req.OwnerID,
			Timestamp: now,
		})

		return c.Status(201).JSON(fiber.Map{
			"id": id, "key": req.Key, "saved": true,
		})
	}
}

// listMemoriesHandler — GET /memories.
//
// @Summary     List project memories
// @Tags        memories
// @Produce     json
// @Security    BearerAuth
// @Param       type       query string false "Optional filter: user | project | feedback"
// @Param       collection query string false "Optional collection scope"
// @Param       room       query string false "Optional sub-topic filter"
// @Param       hall       query string false "Optional genre filter"
// @Success     200 {array} map[string]any
// @Router      /memories [get]
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
				 FROM memories WHERE type = $1 AND (owner_id = $2 OR owner_id = '') AND superseded_by = ''
				 ORDER BY updated_at DESC LIMIT 100`), filterType, ownerID)
			if err != nil {
				return c.JSON([]any{})
			}
			defer rows.Close()
			items = scanMemoryRows(rows)
		} else {
			rows, err := cfg.DB.QueryContext(context.Background(),
				Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
				 FROM memories WHERE (owner_id = $1 OR owner_id = '') AND superseded_by = ''
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

// getMemoryHandler — GET /memories/:key.
//
// @Summary     Fetch a single memory by key
// @Tags        memories
// @Produce     json
// @Security    BearerAuth
// @Param       key path string true "Memory key"
// @Success     200 {object} map[string]any
// @Failure     404 {object} map[string]any "key not found"
// @Router      /memories/{key} [get]
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

// deleteMemoryHandler — DELETE /memories/:key. Idempotent — missing key
// still returns 200 to keep retries safe.
//
// @Summary     Delete a memory by key (idempotent)
// @Tags        memories
// @Produce     json
// @Security    BearerAuth
// @Param       key path string true "Memory key"
// @Success     200 {object} map[string]bool
// @Router      /memories/{key} [delete]
func deleteMemoryHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		key := c.Params("key")
		if cfg.DB == nil {
			return c.Status(500).JSON(fiber.Map{"detail": "database not configured"})
		}
		ownerID, _ := c.Locals("user_id").(string)

		if _, err := cfg.DB.ExecContext(context.Background(),
			Q(`DELETE FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '')`), key, ownerID); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "delete failed: " + err.Error()})
		}

		memoryEvents.Publish(MemoryEvent{
			Kind:      "memory.deleted",
			Key:       key,
			OwnerID:   ownerID,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		})

		return c.JSON(fiber.Map{"deleted": true, "key": key})
	}
}

// memoryEventsStreamHandler — GET /memories/stream. Streams memory
// mutations as Server-Sent Events. Optional ?owner_id=&type=&key_prefix=
// filters narrow the stream to the caller's interest. Connection holds
// open until the client disconnects or the request ctx is cancelled.
//
// Event format: `event: <kind>\ndata: <json>\n\n` plus a periodic
// `: keepalive\n\n` comment to keep proxies happy.
//
// @Summary     Subscribe to memory mutation events (SSE)
// @Description Push-based replacement for polling /memories. Emits memory.saved and memory.deleted events for the authenticated owner. Filters: owner_id, type, key_prefix. Keepalive comment every 25s.
// @Tags        memories
// @Produce     text/event-stream
// @Security    BearerAuth
// @Param       owner_id   query string false "Filter by owner_id (defaults to caller)"
// @Param       type       query string false "Filter by memory type"
// @Param       key_prefix query string false "Filter to keys starting with this prefix"
// @Success     200 {string} string "SSE stream"
// @Router      /memories/stream [get]
func memoryEventsStreamHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		callerID, _ := c.Locals("user_id").(string)
		ownerFilter := c.Query("owner_id", callerID)
		typeFilter := c.Query("type", "")
		keyPrefix := c.Query("key_prefix", "")

		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("X-Accel-Buffering", "no")

		ch, cancel := memoryEvents.Subscribe()
		ctx := c.Context()

		c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
			defer cancel()

			// Send a hello frame so clients know the subscription is live
			// even before the first mutation arrives.
			fmt.Fprintf(w, "event: ready\ndata: {\"subscribed\":true}\n\n")
			_ = w.Flush()

			keepalive := time.NewTicker(25 * time.Second)
			defer keepalive.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-keepalive.C:
					if _, err := w.WriteString(": keepalive\n\n"); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
				case ev, ok := <-ch:
					if !ok {
						return
					}
					if ownerFilter != "" && ev.OwnerID != "" && ev.OwnerID != ownerFilter {
						continue
					}
					if typeFilter != "" && ev.Type != typeFilter {
						continue
					}
					if keyPrefix != "" {
						if len(ev.Key) < len(keyPrefix) || ev.Key[:len(keyPrefix)] != keyPrefix {
							continue
						}
					}
					data, _ := json.Marshal(ev)
					if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, data); err != nil {
						return
					}
					if err := w.Flush(); err != nil {
						return
					}
				}
			}
		})
		return nil
	}
}

func scanMemoryRows(rows interface {
	Next() bool
	Scan(...any) error
}) []fiber.Map {
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
