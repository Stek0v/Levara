// dualsearch.go — Dual-search across multiple collections with different models/dimensions.
// During migration: search old + new collections, merge results, rerank by score.
package http

import (
	"context"
	"encoding/json"
	"sort"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/embed"
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
	app.Post("/search/dual", dualSearchHandler(cfg))
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
		if cfg.Collections == nil || cfg.EmbedEndpoint == "" {
			return c.JSON([]dualSearchResult{})
		}

		// Determine which collections to search
		collections := req.Collections
		if len(collections) == 0 {
			collections = cfg.Collections.List()
		}

		// Group collections by dimension — need different embeddings per dim
		type colGroup struct {
			dim   int
			model string
			names []string
		}
		groups := make(map[int]*colGroup)
		for _, name := range collections {
			if !cfg.Collections.Has(name) {
				continue
			}
			meta := cfg.Collections.GetMeta(name)
			dim := cfg.Collections.Dim(name)
			g, ok := groups[dim]
			if !ok {
				model := ""
				if meta != nil {
					model = meta.EmbeddingModel
				}
				g = &colGroup{dim: dim, model: model}
				groups[dim] = g
			}
			g.names = append(g.names, name)
		}

		// Search each dimension group in parallel
		var allResults []dualSearchResult
		var mu sync.Mutex
		var wg sync.WaitGroup

		ctx := context.Background()

		for _, group := range groups {
			wg.Add(1)
			go func(g *colGroup) {
				defer wg.Done()

				// Embed query with appropriate model
				model := g.model
				if model == "" {
					model = cfg.EmbedModel
				}
				client := embed.NewClient(cfg.EmbedEndpoint, model, 1, 1)
				vecs, err := client.EmbedTexts(ctx, []string{req.QueryText})
				if err != nil || len(vecs) == 0 {
					return
				}
				queryVec := vecs[0]

				// Check dimension match
				if len(queryVec) != g.dim {
					return // model output dim doesn't match collection dim
				}

				// Search each collection in this group
				for _, name := range g.names {
					results, err := cfg.Collections.Search(name, queryVec, req.TopK)
					if err != nil {
						continue
					}
					meta := cfg.Collections.GetMeta(name)
					colModel := ""
					if meta != nil {
						colModel = meta.EmbeddingModel
					}

					mu.Lock()
					for _, r := range results {
						allResults = append(allResults, dualSearchResult{
							ID:         r.ID,
							Score:      r.Score,
							Collection: name,
							Model:      colModel,
							Dim:        g.dim,
							Metadata:   r.Data,
						})
					}
					mu.Unlock()
				}
			}(group)
		}

		wg.Wait()

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
