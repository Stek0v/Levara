// rerank_info.go — GET /api/v1/models/rerank surface for clients that
// need to verify which reranker is configured. Cheap pure-config read;
// no sidecar round-trip. Answers the Phase 2 design's open question
// (docs/phase2-rerank-default-design.md): "Should we expose the model
// behind a /models/rerank endpoint?" Yes — needed for dashboards and
// for E2E gates that assert the expected variant is serving.

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
