// feedback.go — registration shim for the feedback API. The handler
// bodies live in internal/api/feedback (Phase-2 route-group slice,
// code-modernization brief §3); this shim maps APIConfig onto the
// extracted package's Config and keeps the RegisterFeedbackAPI signature
// unchanged so callers (api.go, t11 feedback tests) need no edits.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/feedback"
)

// RegisterFeedbackAPI registers feedback endpoints.
func RegisterFeedbackAPI(app fiber.Router, cfg APIConfig) {
	feedback.Register(app, feedback.Config{
		DB:              cfg.DB,
		AdaptiveWeights: cfg.AdaptiveWeights,
		Q:               Q,
		RequestContext:  apiRequestContext,
	})
}
