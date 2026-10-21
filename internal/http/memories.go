// memories.go — registration shim for the REST memory API. The
// implementation lives in internal/api/memories (Phase-1 pilot extraction,
// MODERNIZATION_BRIEF.md §3): routes, status codes, payloads and SSE
// framing are unchanged; this file maps APIConfig onto the extracted
// package's Config and threads the package-internal helpers as closures so
// the new package never imports internal/http.
package http

import (
	"context"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/memories"
	"github.com/stek0v/levara/pkg/mcp"
)

// RegisterMemoryAPI registers memory CRUD endpoints.
func RegisterMemoryAPI(app fiber.Router, cfg APIConfig) {
	memories.Register(app, memories.Config{
		DB:                cfg.DB,
		RequireAuth:       cfg.RequireAuth,
		MemoryIndexOutbox: cfg.MemoryIndexOutbox,
		EmbedEndpoint:     cfg.EmbedEndpoint,
		EmbedModel:        cfg.EmbedModel,
		Q:                 Q,
		QArgs:             QArgs,
		RequestContext:    apiRequestContext,
		EgressContext: func(c *fiber.Ctx, ctx context.Context) context.Context {
			return searchEgressContext(c, cfg, ctx)
		},
		DeleteMemory: func(ctx context.Context, req mcp.DeleteMemoryRequest) (memories.DeletedMemory, error) {
			target, err := mcp.DeleteMemory(ctx, &mcpHandler{cfg: cfg}, req)
			if err != nil {
				return memories.DeletedMemory{}, err
			}
			return memories.DeletedMemory{ID: target.ID, Key: target.Key, OwnerID: target.OwnerID}, nil
		},
	})
}
