// sessions.go — Session/interaction tracking for conversational memory.
package http

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

func RegisterSessionAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/interactions", saveInteractionHandler(cfg))
	app.Get("/interactions", listInteractionsHandler(cfg))
	app.Get("/interactions/:sessionId", getSessionHandler(cfg))
}

func saveInteractionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			SessionID  string `json:"session_id"`
			Query      string `json:"query"`
			Response   string `json:"response"`
			SearchType string `json:"search_type"`
		}
		c.BodyParser(&req)
		if req.Query == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "query required"})
		}
		userID, _ := c.Locals("user_id").(string)
		id := uuid.New().String()
		if req.SessionID == "" {
			req.SessionID = uuid.New().String()
		}
		if cfg.DB != nil {
			cfg.DB.ExecContext(c.Context(),
				`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
				 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				id, req.SessionID, userID, req.Query, req.Response, req.SearchType, time.Now().UTC())
		}
		return c.Status(201).JSON(fiber.Map{
			"id": id, "session_id": req.SessionID, "saved": true,
		})
	}
}

func listInteractionsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		userID, _ := c.Locals("user_id").(string)
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, session_id, query, response, search_type, created_at
			 FROM interactions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50`, userID)
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var items []fiber.Map
		for rows.Next() {
			var id, sid, query, resp, st string
			var ca time.Time
			rows.Scan(&id, &sid, &query, &resp, &st, &ca)
			items = append(items, fiber.Map{
				"id": id, "session_id": sid, "query": query, "response": resp,
				"search_type": st, "created_at": ca.Format(time.RFC3339),
			})
		}
		if items == nil {
			items = []fiber.Map{}
		}
		return c.JSON(items)
	}
}

func getSessionHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		sessionID := c.Params("sessionId")
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		rows, err := cfg.DB.QueryContext(c.Context(),
			`SELECT id, query, response, search_type, created_at
			 FROM interactions WHERE session_id = $1 ORDER BY created_at LIMIT 10`, sessionID)
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()
		var items []fiber.Map
		for rows.Next() {
			var id, query, resp, st string
			var ca time.Time
			rows.Scan(&id, &query, &resp, &st, &ca)
			items = append(items, fiber.Map{
				"id": id, "query": query, "response": resp,
				"search_type": st, "created_at": ca.Format(time.RFC3339),
			})
		}
		if items == nil {
			items = []fiber.Map{}
		}
		return c.JSON(items)
	}
}
