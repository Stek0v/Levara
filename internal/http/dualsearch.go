// dualsearch.go — Dual-search across multiple collections with different models/dimensions.
// During migration: search old + new collections, merge results, rerank by score.
package http

import (
	"encoding/json"
	"sort"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pipeline"
)

type dualUnifiedSearchRequest struct {
	QueryText   string   `json:"query_text"`
	Collections []string `json:"collections"` // explicit list, or empty = all
	TopK        int      `json:"top_k"`
	Rerank      bool     `json:"rerank"` // merge + sort by score
}

type dualSearchResult struct {
	ID         string          `json:"id"`
	Score      float32         `json:"score"`
	Collection string          `json:"collection"`
	Model      string          `json:"model"`
	Dim        int             `json:"dim"`
	Metadata   json.RawMessage `json:"metadata"`
}

func RegisterDualSearchAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/search/dual", GlobalResourceAdminOnly(cfg), dualSearchHandler(cfg))
}

func dualSearchHandler(cfg APIConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req dualUnifiedSearchRequest
		if err := c.BodyParser(&req); err != nil {
			return c.Status(400).JSON(fiber.Map{"detail": "invalid request"})
		}
		if req.QueryText == "" {
			return c.Status(400).JSON(fiber.Map{"detail": "query_text required"})
		}
		if req.TopK <= 0 {
			req.TopK = 10
		}
		if cfg.Collections == nil || cfg.EmbedEndpoint == "" || cfg.EmbedClient == nil {
			return c.JSON([]dualSearchResult{})
		}

		// Determine which collections to search
		collections := req.Collections
		if len(collections) == 0 {
			collections = cfg.Collections.List()
		}

		ctx, cancel := searchRequestContext(c)
		defer cancel()
		var allResults []dualSearchResult
		for _, name := range collections {
			if ctx.Err() != nil {
				return c.JSON([]dualSearchResult{})
			}
			if !cfg.Collections.Has(name) {
				continue
			}
			model := cfg.EmbedModel
			if meta := cfg.Collections.GetMeta(name); meta != nil && meta.EmbeddingModel != "" {
				model = meta.EmbeddingModel
			}
			// Equal dimensions do not imply equal encoder contracts.
			sp := pipeline.NewSearchPipeline(cfg.EmbedClient.WithModel(model), cfg.Collections, nil)
			results, err := sp.SearchByText(ctx, name, req.QueryText, req.TopK)
			if err != nil {
				continue
			}
			for _, r := range results {
				allResults = append(allResults, dualSearchResult{
					ID: r.ID, Score: r.Score, Collection: name, Model: model,
					Dim: cfg.Collections.Dim(name), Metadata: r.Metadata,
				})
			}
		}
		if ctx.Err() != nil {
			return c.JSON([]dualSearchResult{})
		}

		// Rerank: sort by score descending (higher cosine similarity = better)
		if req.Rerank && len(allResults) > 0 {
			sort.Slice(allResults, func(i, j int) bool {
				return allResults[i].Score > allResults[j].Score
			})
		}

		// Trim to top_k
		if len(allResults) > req.TopK {
			allResults = allResults[:req.TopK]
		}

		if allResults == nil {
			allResults = []dualSearchResult{}
		}

		return c.JSON(allResults)
	}
}
