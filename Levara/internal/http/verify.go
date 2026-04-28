package http

import (
	"encoding/json"
	"math"

	"github.com/gofiber/fiber/v2"
)

type resultVerification struct {
	Total             int `json:"total"`
	Kept              int `json:"kept"`
	DroppedLowScore   int `json:"dropped_low_score"`
	DroppedBadMeta    int `json:"dropped_bad_metadata"`
}

func (r resultVerification) Enabled() bool {
	return r.Total > 0
}

func verifyScoredResults(results []fiber.Map, minScore float64, verifyMeta bool) ([]fiber.Map, resultVerification) {
	v := resultVerification{Total: len(results)}
	if len(results) == 0 {
		return results, v
	}
	out := make([]fiber.Map, 0, len(results))
	for _, r := range results {
		if minScore > 0 {
			score := extractResultScore(r)
			if score < minScore {
				v.DroppedLowScore++
				continue
			}
		}
		if verifyMeta {
			if !hasValidMetadata(r) {
				v.DroppedBadMeta++
				continue
			}
		}
		out = append(out, r)
	}
	v.Kept = len(out)
	return out, v
}

func extractResultScore(r fiber.Map) float64 {
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

func hasValidMetadata(r fiber.Map) bool {
	m, ok := r["metadata"]
	if !ok {
		return false
	}
	switch v := m.(type) {
	case json.RawMessage:
		var tmp map[string]any
		return json.Unmarshal(v, &tmp) == nil
	case []byte:
		var tmp map[string]any
		return json.Unmarshal(v, &tmp) == nil
	case string:
		var tmp map[string]any
		return json.Unmarshal([]byte(v), &tmp) == nil
	case map[string]any:
		return true
	default:
		return false
	}
}

