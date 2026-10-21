// dualsearch.go — adapter shim. The dual-search handler lives in
// internal/api/dualsearch (Phase-2 route-group slice, code-modernization
// brief §3); the admin guard and the search-request context stay here.
// Package-local names (handler constructor, request/result DTOs) are kept
// as aliases so api.go and the t10 dual-search tests are unchanged.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/dualsearch"
)

type dualUnifiedSearchRequest = dualsearch.Request
type dualSearchResult = dualsearch.Result

func dualSearchHandler(cfg APIConfig) fiber.Handler {
	return dualsearch.Handler(dualsearch.Config{
		Collections:          cfg.Collections,
		EmbedEndpoint:        cfg.EmbedEndpoint,
		EmbedModel:           cfg.EmbedModel,
		EmbedClient:          cfg.EmbedClient,
		SearchRequestContext: searchRequestContext,
	})
}

// RegisterDualSearchAPI registers the dual-search endpoint under the
// global-resource admin guard.
func RegisterDualSearchAPI(app fiber.Router, cfg APIConfig) {
	app.Post("/search/dual", GlobalResourceAdminOnly(cfg), dualSearchHandler(cfg))
}
