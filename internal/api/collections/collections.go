// collections.go — Collection metadata API endpoints.
//
// Extracted verbatim from internal/http/collections.go (Phase-2 route-group
// slice, code-modernization brief §3). The HTTP surface (routes, status
// codes, payloads) is unchanged; internal/http keeps the registration
// wiring and passes its adminOnly guard in. The handlers' only dependencies
// are the collection manager and the BM25 sidecars, so Config carries
// exactly those three fields.
package collections

import (
	"log"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embcontract"
)

// Config carries the collection-plane dependencies the handlers need.
type Config struct {
	Collections *store.CollectionManager
	BM25Indexes *bm25.IndexRegistry
	BM25Store   *bm25.SnapshotStore
}

// Register wires the collection metadata endpoints under the caller's
// guard (internal/http passes its adminOnly middleware).
func Register(app fiber.Router, cfg Config, guard fiber.Handler) {
	app.Get("/collections", guard, collectionListHandler(cfg))
	app.Post("/collections", guard, collectionCreateHandler(cfg))
	app.Delete("/collections/:name", guard, collectionDeleteHandler(cfg))
	app.Delete("/collections/:name/records/:id", guard, collectionRecordDeleteHandler(cfg))
	app.Get("/collections/:name/meta", guard, collectionMetaHandler(cfg))
	app.Put("/collections/:name/meta", guard, collectionMetaUpdateHandler(cfg))
	app.Post("/collections/:name/rename", guard, collectionRenameHandler(cfg))
}

func collectionCreateHandler(cfg Config) fiber.Handler {
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

func collectionListHandler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if cfg.Collections == nil {
			return c.JSON([]any{})
		}
		return c.JSON(cfg.Collections.ListWithMeta())
	}
}

func collectionMetaHandler(cfg Config) fiber.Handler {
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

func collectionDeleteHandler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		name := c.Params("name")
		if cfg.Collections == nil {
			return c.SendStatus(204)
		}
		cfg.Collections.Drop(name)
		// Drop the lexical sidecar with the collection (finding M4).
		if cfg.BM25Indexes != nil {
			cfg.BM25Indexes.Delete(name)
			if cfg.BM25Store != nil {
				if err := cfg.BM25Store.Remove(name); err != nil {
					log.Printf("[collections] remove BM25 sidecar %q: %v", name, err)
				}
			}
		}
		return c.SendStatus(204)
	}
}

// collectionRecordDeleteHandler removes a single vector by id from a
// collection — the per-record primitive the P1.4 orphan-vector GC uses
// to delete vectors whose memory_id no longer exists in the SQL memories
// table. 204 on success, 404 when the id (or collection) is absent.
func collectionRecordDeleteHandler(cfg Config) fiber.Handler {
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
		// Keep the lexical sidecar in sync with the vector store (finding
		// M4, 2026-09-03 review): a BM25 hit on a deleted vector misleads.
		if cfg.BM25Indexes != nil {
			if idx := cfg.BM25Indexes.Get(name); idx != nil {
				idx.Remove(id)
			}
		}
		return c.SendStatus(204)
	}
}

func collectionRenameHandler(cfg Config) fiber.Handler {
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

func collectionMetaUpdateHandler(cfg Config) fiber.Handler {
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

// firstNonEmpty returns the first non-empty value. Local copy of the
// internal/http helper so this package does not import its former home.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
