// verify.go — score/metadata verification for search results.
//
// Extracted verbatim from internal/http/verify.go (Phase-2 route-group
// slice, code-modernization brief §3). These pure helpers are shared by
// the lexical and graph search paths; extracting them now means the
// upcoming api_search and graph_search slices import one package instead
// of duplicating the logic.
package verify

import (
	"encoding/json"
	"math"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/internal/metrics"
)

// EmitRAGMetrics records confidence distribution, abstain counts, and
// verify-stage drops for a single RAG completion. Default thresholds keep
// abstain/verify behavior off; these metrics let an operator observe live
// distributions before opting in.
func EmitRAGMetrics(searchType string, confidence float64, abstained bool, abstainReason string, v ResultVerification) {
	metrics.RAGConfidence.WithLabelValues(searchType).Observe(confidence)
	if abstained && abstainReason != "" {
		metrics.RAGAbstainTotal.WithLabelValues(searchType, abstainReason).Inc()
	}
	if v.DroppedLowScore > 0 {
		metrics.RAGVerifyDroppedTotal.WithLabelValues(searchType, "low_score").Add(float64(v.DroppedLowScore))
	}
	if v.DroppedBadMeta > 0 {
		metrics.RAGVerifyDroppedTotal.WithLabelValues(searchType, "bad_metadata").Add(float64(v.DroppedBadMeta))
	}
}

// ResultVerification reports how many results the verify stage kept and
// why it dropped the rest.
type ResultVerification struct {
	Total           int `json:"total"`
	Kept            int `json:"kept"`
	DroppedLowScore int `json:"dropped_low_score"`
	DroppedBadMeta  int `json:"dropped_bad_metadata"`
}

// Enabled reports whether verification ran at all.
func (r ResultVerification) Enabled() bool {
	return r.Total > 0
}

// VerifyScoredResults drops results below minScore and, when verifyMeta is
// set, results whose metadata does not decode to a non-nil object.
func VerifyScoredResults(results []fiber.Map, minScore float64, verifyMeta bool) ([]fiber.Map, ResultVerification) {
	v := ResultVerification{Total: len(results)}
	if len(results) == 0 {
		return results, v
	}
	out := make([]fiber.Map, 0, len(results))
	for _, r := range results {
		if minScore > 0 {
			score := ExtractResultScore(r)
			if score < minScore {
				v.DroppedLowScore++
				continue
			}
		}
		if verifyMeta {
			if !HasValidMetadata(r) {
				v.DroppedBadMeta++
				continue
			}
		}
		out = append(out, r)
	}
	v.Kept = len(out)
	return out, v
}

// ExtractResultScore reads the numeric score off a result map, preferring
// "score" and falling back to "fused_score"; NaN/Inf scores read as 0.
func ExtractResultScore(r fiber.Map) float64 {
	switch s := r["score"].(type) {
	case float64:
		if !math.IsNaN(s) && !math.IsInf(s, 0) {
			return s
		}
	case float32:
		v := float64(s)
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v
		}
	}
	if fs, ok := r["fused_score"].(float64); ok && !math.IsNaN(fs) && !math.IsInf(fs, 0) {
		return fs
	}
	return 0
}

// HasValidMetadata reports whether the result's metadata decodes to a
// non-nil object across the representations handlers produce.
func HasValidMetadata(r fiber.Map) bool {
	m, ok := r["metadata"]
	if !ok {
		return false
	}
	switch v := m.(type) {
	case json.RawMessage:
		var tmp map[string]any
		return json.Unmarshal(v, &tmp) == nil && tmp != nil
	case []byte:
		var tmp map[string]any
		return json.Unmarshal(v, &tmp) == nil && tmp != nil
	case string:
		var tmp map[string]any
		return json.Unmarshal([]byte(v), &tmp) == nil && tmp != nil
	case map[string]any:
		return v != nil
	case fiber.Map:
		return v != nil
	default:
		return false
	}
}
