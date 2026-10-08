// memories.go — Project/user memory persistence via REST + MCP.
package http

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/memoryindex"
)

// RegisterMemoryAPI registers memory CRUD endpoints.
func RegisterMemoryAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/memories", saveMemoryHandler(cfg))
	app.Get("/memories", listMemoriesHandler(cfg))
	app.Get("/memories/stream", memoryEventsStreamHandler())
	app.Get("/memories/:key", getMemoryHandler(cfg))
	app.Delete("/memories/by-id/:id", deleteMemoryByIDHandler(cfg))
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
		if req.Hall != "" && !mcp.IsValidHall(req.Hall) {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid hall: " + req.Hall})
		}
		callerID, _ := c.Locals("user_id").(string)
		if callerID != "" {
			if req.OwnerID != "" && req.OwnerID != callerID {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "memory owner must match authenticated caller"})
			}
			req.OwnerID = callerID
		} else if cfg.RequireAuth {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"detail": "authentication required"})
		}

		if cfg.DB == nil {
			return c.Status(500).JSON(fiber.Map{"detail": "database not configured"})
		}

		id := uuid.New().String()
		now := time.Now().UTC().Format(time.RFC3339)
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		tx, err := cfg.DB.BeginTx(ctx, nil)
		if err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save failed"})
		}
		defer tx.Rollback()

		// Upsert: insert or update value+type+updated_at on conflict.
		// RETURNING id yields the canonical row id (existing id on conflict)
		// so the response no longer lies about which record was updated
		// (finding H4, 2026-09-03 review).
		upsertSQL := `INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, created_at, updated_at, source_task_id, source_receipt_ids, verification_status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, '', '[]', 'unverified')
			 ON CONFLICT(key, owner_id, collection_name) DO UPDATE SET
			 value = EXCLUDED.value, type = EXCLUDED.type, room = EXCLUDED.room, hall = EXCLUDED.hall,
			 verification_status = CASE WHEN memories.value <> EXCLUDED.value OR memories.type <> EXCLUDED.type OR memories.room <> EXCLUDED.room OR memories.hall <> EXCLUDED.hall THEN 'unverified' ELSE memories.verification_status END,
			 source_task_id = CASE WHEN memories.value <> EXCLUDED.value OR memories.type <> EXCLUDED.type OR memories.room <> EXCLUDED.room OR memories.hall <> EXCLUDED.hall THEN '' ELSE memories.source_task_id END,
			 source_receipt_ids = CASE WHEN memories.value <> EXCLUDED.value OR memories.type <> EXCLUDED.type OR memories.room <> EXCLUDED.room OR memories.hall <> EXCLUDED.hall THEN '[]' ELSE memories.source_receipt_ids END,
			 updated_at = EXCLUDED.updated_at
			 RETURNING id`
		q, qargs := QArgs(upsertSQL,
			id, req.Key, req.Value, req.Type, req.OwnerID, req.CollectionName, req.Room, req.Hall, now, now)
		var canonicalID string
		if err := tx.QueryRowContext(ctx, q, qargs...).Scan(&canonicalID); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save failed"})
		}
		id = canonicalID

		// Enqueue the durable memory-index job so REST-written memories get
		// embedded into the HNSW sidecar exactly like MCP saves (finding H4).
		if cfg.MemoryIndexOutbox != nil && cfg.EmbedEndpoint != "" {
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte(req.Key+"\x00"+req.Value)))
			if _, err := cfg.MemoryIndexOutbox.EnqueueTx(ctx, tx, memoryindex.Job{
				MemoryID: canonicalID, Operation: "upsert_vector", Collection: req.CollectionName,
				OwnerID: req.OwnerID, Digest: digest, Model: cfg.EmbedModel,
			}); err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": "save failed"})
			}
		}
		if err := tx.Commit(); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "save failed"})
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
		ownerID, _ := c.Locals("user_id").(string)

		// Honour documented collection/room/hall query filters (finding M14,
		// 2026-09-03 review): they were parsed in swagger but ignored here.
		conds := []string{"(owner_id = $1 OR owner_id = '')", "superseded_by = ''"}
		args := []any{ownerID}
		pos := 2
		for _, f := range []struct{ name, col string }{
			{"type", "type"}, {"collection", "collection_name"}, {"room", "room"}, {"hall", "hall"},
		} {
			if v := c.Query(f.name, ""); v != "" {
				conds = append(conds, fmt.Sprintf("%s = $%d", f.col, pos))
				args = append(args, v)
				pos++
			}
		}
		query := "SELECT id, key, value, type, owner_id, room, hall, created_at, updated_at FROM memories WHERE " +
			strings.Join(conds, " AND ") + " ORDER BY updated_at DESC LIMIT 100"

		rows, err := cfg.DB.QueryContext(context.Background(), Q(query), args...)
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		items := scanMemoryRows(rows)

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
// @Param       collection query string false "Collection scope; empty selects the default REST collection"
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
		collection := c.Query("collection", "")
		query := `SELECT id, key, value, type, owner_id, room, hall, created_at, updated_at
			 FROM memories WHERE key = $1 AND (owner_id = $2 OR owner_id = '')
			 AND superseded_by = '' AND valid_until IS NULL`
		queryArgs := []any{key, ownerID}
		if collection != "" {
			query += " AND collection_name = $3"
			queryArgs = append(queryArgs, collection)
		}
		query += " ORDER BY CASE WHEN owner_id = $2 THEN 0 ELSE 1 END, collection_name LIMIT 3"
		query, args := QArgs(query, queryArgs...)
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		rows, err := cfg.DB.QueryContext(ctx, query, args...)
		if err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		defer rows.Close()
		type memoryRow struct{ id, key, value, typ, owner, room, hall, created, updated string }
		var own, shared []memoryRow
		for rows.Next() {
			var row memoryRow
			if err := rows.Scan(&row.id, &row.key, &row.value, &row.typ, &row.owner, &row.room, &row.hall, &row.created, &row.updated); err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": "memory read failed"})
			}
			if row.owner == ownerID && ownerID != "" {
				own = append(own, row)
			} else {
				shared = append(shared, row)
			}
		}
		if err := rows.Err(); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": "memory read failed"})
		}
		candidates := shared
		if len(own) > 0 {
			candidates = own
		}
		if len(candidates) == 0 {
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		}
		if collection == "" && len(candidates) > 1 {
			return c.Status(409).JSON(fiber.Map{"detail": "memory key is ambiguous; select collection"})
		}
		selected := candidates[0]
		return c.JSON(fiber.Map{
			"id": selected.id, "key": selected.key, "value": selected.value, "type": selected.typ,
			"owner_id": selected.owner, "room": selected.room, "hall": selected.hall,
			"created_at": selected.created, "updated_at": selected.updated,
		})
	}
}

// deleteMemoryByIDHandler — DELETE /memories/by-id/:id. The path is distinct
// from the legacy key route so an ID is never reinterpreted as a display key.
//
// @Summary     Delete exactly one memory by ID
// @Tags        memories
// @Produce     json
// @Security    BearerAuth
// @Param       id path string true "Memory ID"
// @Success     200 {object} map[string]any
// @Failure     404 {object} map[string]any "memory not found or inaccessible"
// @Router      /memories/by-id/{id} [delete]
func deleteMemoryByIDHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, err := url.PathUnescape(c.Params("id"))
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid memory id"})
		}
		return deleteMemoryHTTP(c, cfg, mcp.DeleteMemoryRequest{MemoryID: id}, false)
	}
}

// deleteMemoryHandler — DELETE /memories/:key. Idempotent for a missing key;
// an ambiguous personal key returns 409 without changing any row.
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
		key, err := url.PathUnescape(c.Params("key"))
		if err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid memory key"})
		}
		return deleteMemoryHTTP(c, cfg, mcp.DeleteMemoryRequest{Key: key, Collection: c.Query("collection")}, true)
	}
}

func deleteMemoryHTTP(c *fiber.Ctx, cfg APIConfig, req mcp.DeleteMemoryRequest, legacyNoop bool) error {
	if cfg.DB == nil {
		return c.Status(500).JSON(fiber.Map{"detail": "database not configured"})
	}
	ctx, cancel := apiRequestContext(c)
	defer cancel()
	ctx = searchEgressContext(c, cfg, ctx)
	target, err := mcp.DeleteMemory(ctx, &mcpHandler{cfg: cfg}, req)
	if err != nil {
		switch {
		case legacyNoop && errors.Is(err, mcp.ErrMemoryDeleteNotFound):
			return c.JSON(fiber.Map{"deleted": true, "key": req.Key})
		case errors.Is(err, mcp.ErrMemoryDeleteNotFound):
			return c.Status(404).JSON(fiber.Map{"detail": "not found"})
		case errors.Is(err, mcp.ErrMemoryDeleteAmbiguous):
			return c.Status(409).JSON(fiber.Map{"detail": "memory key is ambiguous; delete by id"})
		case errors.Is(err, accesspkg.ErrRevokedCredential):
			return c.Status(401).JSON(fiber.Map{"detail": "credential revoked"})
		case errors.Is(err, accesspkg.ErrDocumentForbidden):
			return c.Status(403).JSON(fiber.Map{"detail": "memory delete forbidden"})
		default:
			return c.Status(500).JSON(fiber.Map{"detail": "delete failed: " + err.Error()})
		}
	}

	memoryEvents.Publish(MemoryEvent{
		Kind:      "memory.deleted",
		Key:       target.Key,
		OwnerID:   target.OwnerID,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	})
	return c.JSON(fiber.Map{"deleted": true, "id": target.ID, "key": target.Key})
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
		if callerID != "" {
			if ownerFilter != "" && ownerFilter != callerID {
				return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"detail": "memory owner must match authenticated caller"})
			}
			ownerFilter = callerID
		}
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
		var id, key, value, typ, ownerID, room, hall, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &room, &hall, &ca, &ua); err != nil {
			continue
		}
		items = append(items, fiber.Map{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "room": room, "hall": hall,
			"created_at": ca, "updated_at": ua,
		})
	}
	return items
}
