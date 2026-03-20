// collections.go — Collection metadata API endpoints.
package http

import (
	"github.com/gofiber/fiber/v2"
)

func collectionsListHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Collections == nil {
			return c.JSON([]any{})
		}
		return c.JSON(cfg.Collections.ListWithMeta())
	}
}

func collectionMetaHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil {
			return c.Status(404).JSON(fiber.Map{"error": "collections not configured"})
		}
		meta := cfg.Collections.GetMeta(name)
		if meta == nil {
			return c.Status(404).JSON(fiber.Map{"error": "collection not found"})
		}
		return c.JSON(meta)
	}
}

func collectionMetaUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil {
			return c.Status(404).JSON(fiber.Map{"error": "collections not configured"})
		}

		var req struct {
			EmbeddingModel   string `json:"embedding_model"`
			DistanceMetric   string `json:"distance_metric"`
			EmbeddingVersion string `json:"embedding_version"`
		}
		c.BodyParser(&req)

		if err := cfg.Collections.UpdateMeta(name, req.EmbeddingModel, req.DistanceMetric, req.EmbeddingVersion); err != nil {
			return c.Status(404).JSON(fiber.Map{"error": err.Error()})
		}

		return c.JSON(cfg.Collections.GetMeta(name))
	}
}
