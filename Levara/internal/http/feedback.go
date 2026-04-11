// feedback.go — Search result feedback collection and stats.
package http

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RegisterFeedbackAPI registers feedback endpoints.
func RegisterFeedbackAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/feedback", feedbackSubmitHandler(cfg))
	app.Get("/feedback/stats", feedbackStatsHandler(cfg))
	app.Get("/feedback", feedbackListHandler(cfg))
}

func feedbackSubmitHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Query      string `json:"query"`
			ResultID   string `json:"result_id"`
			Collection string `json:"collection"`
			SearchType string `json:"search_type"`
			Rating     int    `json:"rating"` // 1-5
			Comment    string `json:"comment"`
		}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid body"})
		}
		if req.Rating < 1 || req.Rating > 5 {
			return c.Status(400).JSON(fiber.Map{"detail": "rating must be 1-5"})
		}
		if req.Query == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "query required"})
		}
		if cfg.DB == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "database not configured"})
		}

		id := uuid.New().String()
		userID, _ := c.Locals("user_id").(string)

		cfg.DB.ExecContext(context.Background(),
			Q(`INSERT INTO search_feedback (id, query, result_id, collection, search_type, rating, comment, user_id)
			   VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
			id, req.Query, req.ResultID, req.Collection, req.SearchType, req.Rating, req.Comment, userID)

		// Feed back to adaptive router weights
		if cfg.AdaptiveWeights != nil && req.SearchType != "" {
			cfg.AdaptiveWeights.RecordFeedback(req.SearchType, req.Rating)
		}

		return c.Status(201).JSON(fiber.Map{"id": id, "saved": true})
	}
}

func feedbackStatsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON(fiber.Map{"total": 0})
		}
		collection := c.Query("collection")

		var total, avgRating int
		var worstQuery string

		if collection != "" {
			cfg.DB.QueryRowContext(context.Background(),
				Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback WHERE collection = $1`),
				collection).Scan(&total, &avgRating)
			cfg.DB.QueryRowContext(context.Background(),
				Q(`SELECT query FROM search_feedback WHERE collection = $1 ORDER BY rating ASC LIMIT 1`),
				collection).Scan(&worstQuery)
		} else {
			cfg.DB.QueryRowContext(context.Background(),
				Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback`)).Scan(&total, &avgRating)
			cfg.DB.QueryRowContext(context.Background(),
				Q(`SELECT query FROM search_feedback ORDER BY rating ASC LIMIT 1`)).Scan(&worstQuery)
		}

		return c.JSON(fiber.Map{
			"total":       total,
			"avg_rating":  avgRating,
			"worst_query": worstQuery,
			"collection":  collection,
		})
	}
}

func feedbackListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		collection := c.Query("collection")
		limit := c.QueryInt("limit", 20)

		var rows interface{ Next() bool; Scan(...any) error; Close() error }
		var err error
		if collection != "" {
			rows, err = cfg.DB.QueryContext(context.Background(),
				Q(`SELECT id, query, result_id, collection, search_type, rating, comment, user_id, created_at
				   FROM search_feedback WHERE collection = $1 ORDER BY created_at DESC LIMIT $2`),
				collection, limit)
		} else {
			rows, err = cfg.DB.QueryContext(context.Background(),
				Q(`SELECT id, query, result_id, collection, search_type, rating, comment, user_id, created_at
				   FROM search_feedback ORDER BY created_at DESC LIMIT $1`), limit)
		}
		if err != nil {
			return c.JSON([]any{})
		}
		defer rows.Close()

		var feedback []fiber.Map
		for rows.Next() {
			var id, query, resultID, coll, st, comment, uid, ca string
			var rating int
			rows.Scan(&id, &query, &resultID, &coll, &st, &rating, &comment, &uid, &ca)
			feedback = append(feedback, fiber.Map{
				"id": id, "query": query, "result_id": resultID, "collection": coll,
				"search_type": st, "rating": rating, "comment": comment,
				"user_id": uid, "created_at": ca,
			})
		}
		if feedback == nil {
			feedback = []fiber.Map{}
		}
		return c.JSON(feedback)
	}
}
