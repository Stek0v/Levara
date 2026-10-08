// feedback.go — Search result feedback collection and stats.
package http

import (
	"database/sql"
	"errors"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// RegisterFeedbackAPI registers feedback endpoints.
func RegisterFeedbackAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/feedback", feedbackSubmitHandler(cfg))
	app.Get("/feedback/stats", feedbackStatsHandler(cfg))
	app.Get("/feedback", feedbackListHandler(cfg))
}

// feedbackSubmitHandler — POST /feedback. Stores a 1-5 rating against a
// (query, result_id, search_type) triple. The adaptive router uses these
// scores to learn per-strategy weight adjustments.
//
// @Summary     Rate a search result
// @Tags        feedback
// @Accept      json
// @Produce     json
// @Security    BearerAuth
// @Param       body body object true "query, rating (1-5), optional result_id, search_type, comment"
// @Success     200 {object} map[string]any
// @Failure     400 {object} map[string]any "missing query or rating"
// @Router      /feedback [post]
func feedbackSubmitHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
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

		if _, err := cfg.DB.ExecContext(ctx,
			Q(`INSERT INTO search_feedback (id, query, result_id, collection, search_type, rating, comment, user_id)
			   VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
			id, req.Query, req.ResultID, req.Collection, req.SearchType, req.Rating, req.Comment, userID); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}

		// Feed back to adaptive router weights
		if cfg.AdaptiveWeights != nil && req.SearchType != "" {
			cfg.AdaptiveWeights.RecordFeedback(req.SearchType, req.Rating)
		}

		return c.Status(201).JSON(fiber.Map{"id": id, "saved": true})
	}
}

// feedbackStatsHandler — GET /feedback/stats. Aggregated metrics over
// all submitted feedback rows; the WebUI Analytics page consumes this.
//
// @Summary     Aggregate feedback statistics
// @Tags        feedback
// @Produce     json
// @Security    BearerAuth
// @Param       collection query string false "Filter stats by collection"
// @Success     200 {object} map[string]any "total, avg_rating, worst_query"
// @Router      /feedback/stats [get]
func feedbackStatsHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		if cfg.DB == nil {
			return c.JSON(fiber.Map{"total": 0})
		}
		collection := c.Query("collection")

		var total int
		var avgRating float64
		var worstQuery string

		where := ""
		args := []any{}
		if collection != "" {
			where = " WHERE collection = $1"
			args = append(args, collection)
		}
		if err := cfg.DB.QueryRowContext(ctx, Q("SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback"+where), args...).Scan(&total, &avgRating); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		if err := cfg.DB.QueryRowContext(ctx, Q("SELECT query FROM search_feedback"+where+" ORDER BY rating ASC LIMIT 1"), args...).Scan(&worstQuery); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		if ctx.Err() != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}

		return c.JSON(fiber.Map{
			"total":       total,
			"avg_rating":  avgRating,
			"worst_query": worstQuery,
			"collection":  collection,
		})
	}
}

// feedbackListHandler — GET /feedback. Paginated raw feedback rows.
//
// @Summary     List recent feedback rows
// @Tags        feedback
// @Produce     json
// @Security    BearerAuth
// @Param       limit query int false "Max rows (default 50, max 200)"
// @Success     200 {array} map[string]any
// @Router      /feedback [get]
func feedbackListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx, cancel := apiRequestContext(c)
		defer cancel()
		if cfg.DB == nil {
			return c.JSON([]any{})
		}
		collection := c.Query("collection")
		limit := c.QueryInt("limit", 20)
		// Bound the page size (finding M12, 2026-09-03 review): negative or
		// huge limit values went straight into SQL.
		if limit <= 0 || limit > 100 {
			limit = 20
		}

		var rows *sql.Rows
		var err error
		if collection != "" {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, query, result_id, collection, search_type, rating, comment, user_id, created_at
				   FROM search_feedback WHERE collection = $1 ORDER BY created_at DESC LIMIT $2`),
				collection, limit)
		} else {
			rows, err = cfg.DB.QueryContext(ctx,
				Q(`SELECT id, query, result_id, collection, search_type, rating, comment, user_id, created_at
				   FROM search_feedback ORDER BY created_at DESC LIMIT $1`), limit)
		}
		if err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		defer rows.Close()

		var feedback []fiber.Map
		for rows.Next() {
			var id, query, resultID, coll, st, comment, uid, ca string
			var rating int
			if err := rows.Scan(&id, &query, &resultID, &coll, &st, &rating, &comment, &uid, &ca); err != nil {
				return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
			}
			feedback = append(feedback, fiber.Map{
				"id": id, "query": query, "result_id": resultID, "collection": coll,
				"search_type": st, "rating": rating, "comment": comment,
				"user_id": uid, "created_at": ca,
			})
		}
		if err := rows.Err(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		if err := rows.Close(); err != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		if ctx.Err() != nil {
			return c.Status(503).JSON(fiber.Map{"detail": "feedback storage unavailable"})
		}
		if feedback == nil {
			feedback = []fiber.Map{}
		}
		return c.JSON(feedback)
	}
}
