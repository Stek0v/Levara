// ontologies.go — registration shim for the ontology API. The handler
// bodies and the in-memory ontology cache live in internal/api/ontologies
// (Phase-2 route-group slice, code-modernization brief §3); GetOntology
// PromptSuffix keeps its exported name here for the mcp deps adapter.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/ontologies"
)

// GetOntologyPromptSuffix delegates to the extracted package's cache.
func GetOntologyPromptSuffix(collection string) string {
	return ontologies.PromptSuffix(collection)
}

// registerOntologiesAPI wires the ontology management endpoints.
func registerOntologiesAPI(app fiber.Router, cfg APIConfig) {
	ontologies.Register(app, ontologies.Config{
		StoragePath: cfg.StoragePath,
		DB:          cfg.DB,
		Q:           Q,
		QArgs:       QArgs,
	})
}
