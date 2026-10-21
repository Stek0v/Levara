// verify.go — adapter delegates. The verification helpers live in
// internal/api/verify (Phase-2 route-group slice, code-modernization
// brief §3) and are shared by the lexical and graph search paths. These
// package-local delegates keep api_search.go, graph_search.go,
// confidence.go and the existing tests unchanged; they retire when those
// slices move onto the package directly.
package http

import (
	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/api/verify"
)

type resultVerification = verify.ResultVerification

func verifyScoredResults(results []fiber.Map, minScore float64, verifyMeta bool) ([]fiber.Map, resultVerification) {
	return verify.VerifyScoredResults(results, minScore, verifyMeta)
}

func emitRAGMetrics(searchType string, confidence float64, abstained bool, abstainReason string, v resultVerification) {
	verify.EmitRAGMetrics(searchType, confidence, abstained, abstainReason, v)
}

func extractResultScore(r fiber.Map) float64 {
	return verify.ExtractResultScore(r)
}

func hasValidMetadata(r fiber.Map) bool {
	return verify.HasValidMetadata(r)
}
