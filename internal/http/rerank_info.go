// rerank_info.go — GET /api/v1/models/rerank surface for clients that
// need to verify which reranker is configured. Cheap pure-config read;
// no sidecar round-trip. See docs/search-strategies-guide.md for model
// configuration, fallback behavior and interpreting rerank metrics.

package http

import (
	"github.com/gofiber/fiber/v2"
)

func rerankInfoHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		enabled := cfg.RerankEndpoint != ""
		return c.JSON(fiber.Map{
			"enabled":   enabled,
			"endpoint":  cfg.RerankEndpoint,
			"model":     cfg.RerankModel,
			"budget_ms": cfg.RerankBudgetMs,
		})
	}
}
