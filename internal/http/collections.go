// collections.go — Collection metadata API endpoints.
package http

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/embcontract"
)

func collectionCreateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		var req struct {
			Name               string `json:"name"`
			EmbeddingModel     string `json:"embedding_model"`
			EmbeddingDim       int    `json:"embedding_dim"`
			DistanceMetric     string `json:"distance_metric"`
			EmbeddingTokenizer string `json:"embedding_tokenizer"`
			EmbeddingPooling   string `json:"embedding_pooling"`
			EmbeddingNormalize string `json:"embedding_normalization"`
			EmbeddingVersion   string `json:"embedding_version"`
		}
		if err := c.BodyParser(&req); err != nil || req.Name == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "name required"})
		}
		if err := cfg.Collections.CreateWithDim(req.Name, req.EmbeddingDim, req.EmbeddingModel, req.DistanceMetric); err != nil {
			return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
		}
		if req.EmbeddingVersion != "" || req.EmbeddingTokenizer != "" || req.EmbeddingPooling != "" || req.EmbeddingNormalize != "" {
			contract := embcontract.Contract{
				Encoder:       req.EmbeddingModel,
				Tokenizer:     req.EmbeddingTokenizer,
				Pooling:       req.EmbeddingPooling,
				Normalization: req.EmbeddingNormalize,
				Dim:           req.EmbeddingDim,
				Metric:        req.DistanceMetric,
			}
			if err := cfg.Collections.UpdateEmbeddingContract(req.Name, contract); err != nil {
				return c.Status(500).JSON(fiber.Map{"detail": err.Error()})
			}
		}
		return c.Status(201).JSON(cfg.Collections.GetMeta(req.Name))
	}
}

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
			return c.Status(404).JSON(fiber.Map{"detail": "collections not configured"})
		}
		meta := cfg.Collections.GetMeta(name)
		if meta == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "collection not found"})
		}
		return c.JSON(meta)
	}
}

func collectionDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil {
			return c.SendStatus(204)
		}
		cfg.Collections.Drop(name)
		return c.SendStatus(204)
	}
}

// collectionRecordDeleteHandler removes a single vector by id from a
// collection — the per-record primitive the P1.4 orphan-vector GC uses
// to delete vectors whose memory_id no longer exists in the SQL memories
// table. 204 on success, 404 when the id (or collection) is absent.
func collectionRecordDeleteHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		id := c.Params("id")
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		if err := cfg.Collections.Delete(name, id); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "not found") {
				return c.Status(404).JSON(fiber.Map{"detail": msg})
			}
			return c.Status(500).JSON(fiber.Map{"detail": msg})
		}
		return c.SendStatus(204)
	}
}

func collectionRenameHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Collections == nil {
			return c.Status(503).JSON(fiber.Map{"detail": "collections not configured"})
		}
		oldName := c.Params("name")
		var req struct {
			NewName string `json:"new_name"`
		}
		if err := c.BodyParser(&req); err != nil || req.NewName == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "new_name required"})
		}
		if err := cfg.Collections.Rename(oldName, req.NewName); err != nil {
			msg := err.Error()
			// Map known classes of error to appropriate status codes so
			// the migration runbook can distinguish "retry" from "stop".
			switch {
			case strings.Contains(msg, "not found"):
				return c.Status(404).JSON(fiber.Map{"detail": msg})
			case strings.Contains(msg, "already exists"), strings.Contains(msg, "identical"):
				return c.Status(409).JSON(fiber.Map{"detail": msg})
			case strings.Contains(msg, "invalid"), strings.Contains(msg, "must be non-empty"):
				return c.Status(400).JSON(fiber.Map{"detail": msg})
			default:
				return c.Status(500).JSON(fiber.Map{"detail": msg})
			}
		}
		return c.JSON(cfg.Collections.GetMeta(req.NewName))
	}
}

func collectionMetaUpdateHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil {
			return c.Status(404).JSON(fiber.Map{"detail": "collections not configured"})
		}

		var req struct {
			EmbeddingModel         string `json:"embedding_model"`
			DistanceMetric         string `json:"distance_metric"`
			EmbeddingVersion       string `json:"embedding_version"`
			EmbeddingTokenizer     string `json:"embedding_tokenizer"`
			EmbeddingPooling       string `json:"embedding_pooling"`
			EmbeddingNormalization string `json:"embedding_normalization"`
		}
		c.BodyParser(&req)

		if err := cfg.Collections.UpdateMeta(name, req.EmbeddingModel, req.DistanceMetric, req.EmbeddingVersion); err != nil {
			return c.Status(404).JSON(fiber.Map{"detail": err.Error()})
		}
		if req.EmbeddingTokenizer != "" || req.EmbeddingPooling != "" || req.EmbeddingNormalization != "" {
			meta := cfg.Collections.GetMeta(name)
			if meta != nil {
				contract := embcontract.Contract{
					Encoder:       firstNonEmpty(req.EmbeddingModel, meta.EmbeddingModel),
					Tokenizer:     req.EmbeddingTokenizer,
					Pooling:       req.EmbeddingPooling,
					Normalization: req.EmbeddingNormalization,
					Dim:           meta.EmbeddingDim,
					Metric:        firstNonEmpty(req.DistanceMetric, meta.DistanceMetric),
				}
				if err := cfg.Collections.UpdateEmbeddingContract(name, contract); err != nil {
					return c.Status(404).JSON(fiber.Map{"detail": err.Error()})
				}
			}
		}

		return c.JSON(cfg.Collections.GetMeta(name))
	}
}
