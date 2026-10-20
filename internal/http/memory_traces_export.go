// memory_traces_export.go — adapter shim. The curated trace-export handler
// lives in internal/api/memtraces (Phase-2 route-group slice,
// code-modernization brief §3); the admin gate, audit scoping and window
// parsing stay here and are threaded as closures. The handler constructor
// keeps its package-local name so api.go and the contract tests are
// unchanged.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/memtraces"
	"github.com/stek0v/levara/pkg/audit"
)

func memoryTraceExportHandler(cfg APIConfig) fiber.Handler {
	return memtraces.Handler(memtraces.Config{
		MCPAuditReadModel: cfg.MCPAuditReadModel,
		RequireSuperuser: func(c *fiber.Ctx) error {
			return requireSuperuser(c, cfg)
		},
		ScopedAuditFilter: func(c *fiber.Ctx, f audit.EventFilter) (audit.EventFilter, error) {
			return scopedAuditFilter(c, cfg, f)
		},
		WindowHours: windowHours,
	})
}
