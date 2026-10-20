// collections.go — registration shim for the collection metadata API. The
// handler bodies live in internal/api/collections (Phase-2 route-group
// slice, code-modernization brief §3); routes, guard and payloads are
// unchanged. This shim maps APIConfig fields onto the extracted package's
// Config.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/collections"
)

// registerCollectionsAPI wires the collection metadata endpoints under the
// admin guard.
func registerCollectionsAPI(app fiber.Router, cfg APIConfig, adminOnly fiber.Handler) {
	collections.Register(app, collections.Config{
		Collections: cfg.Collections,
		BM25Indexes: cfg.BM25Indexes,
		BM25Store:   cfg.BM25Store,
	}, adminOnly)
}
