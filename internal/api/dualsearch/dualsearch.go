// dualsearch.go — Dual-search across multiple collections with different models/dimensions.
// During migration: search old + new collections, merge results, rerank by score.
//
// Extracted verbatim from internal/http/dualsearch.go (Phase-2 route-group
// slice, code-modernization brief §3). The search-request context builder
// stays in internal/http and is threaded through Config.
package dualsearch

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pipeline"
)

// Request is the dual-search input: explicit collection list or empty for
// all, plus the shared top-k and the merge-sort flag.
type Request struct {
	QueryText   string   `json:"query_text"`
	Collections []string `json:"collections"` // explicit list, or empty = all
	TopK        int      `json:"top_k"`
	Rerank      bool     `json:"rerank"` // merge + sort by score
}

// Result is one merged hit, annotated with the collection/model/dimension
// it came from.
type Result struct {
	ID         string          `json:"id"`
	Score      float32         `json:"score"`
	Collection string          `json:"collection"`
	Model      string          `json:"model"`
	Dim        int             `json:"dim"`
	Metadata   json.RawMessage `json:"metadata"`
}

// Config carries the dual-search plane's dependencies.
type Config struct {
	Collections  *store.CollectionManager
	EmbedEndpoint string
	EmbedModel    string
	EmbedClient   *embed.Client

	SearchRequestContext func(*fiber.Ctx) (context.Context, context.CancelFunc)
}

// Handler returns the POST /search/dual endpoint.
func Handler(cfg Config) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req Request
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
			return c.JSON([]Result{})
		}

		// Determine which collections to search
		collections := req.Collections
		if len(collections) == 0 {
			collections = cfg.Collections.List()
		}

		ctx, cancel := cfg.SearchRequestContext(c)
		defer cancel()
		var allResults []Result
		for _, name := range collections {
			if ctx.Err() != nil {
				return c.JSON([]Result{})
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
				allResults = append(allResults, Result{
					ID: r.ID, Score: r.Score, Collection: name, Model: model,
					Dim: cfg.Collections.Dim(name), Metadata: r.Metadata,
				})
			}
		}
		if ctx.Err() != nil {
			return c.JSON([]Result{})
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
			allResults = []Result{}
		}

		return c.JSON(allResults)
	}
}
