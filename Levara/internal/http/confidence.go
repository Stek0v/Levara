package http

import (
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/router"
)

const (
	defaultAbstainThreshold = 0.0
	defaultAbstainMessage   = "Недостаточно подтвержденного контекста для надежного ответа. Уточните запрос или добавьте больше данных."
)

type confidenceBreakdown struct {
	Retrieval float64 `json:"retrieval"`
	Routing   float64 `json:"routing"`
	Combined  float64 `json:"combined"`
	Threshold float64 `json:"threshold"`
}

// ragAbstainThreshold returns the confidence threshold below which the API abstains.
// LEVARA_RAG_ABSTAIN_THRESHOLD can override the default in [0,1].
func ragAbstainThreshold() float64 {
	v, _ := parseThreshold(os.Getenv("LEVARA_RAG_ABSTAIN_THRESHOLD"))
	return v
}

// ragAbstainThresholdFor returns per-search-type threshold when configured, and
// falls back to global LEVARA_RAG_ABSTAIN_THRESHOLD otherwise.
//
// Example keys:
//   LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION
//   LEVARA_RAG_ABSTAIN_THRESHOLD_GRAPH_COMPLETION
func ragAbstainThresholdFor(searchType string) float64 {
	st := strings.ToUpper(strings.TrimSpace(searchType))
	if st != "" {
		key := "LEVARA_RAG_ABSTAIN_THRESHOLD_" + st
		if v, ok := parseThreshold(os.Getenv(key)); ok {
			return v
		}
	}
	return ragAbstainThreshold()
}

func parseThreshold(raw string) (float64, bool) {
	if raw == "" {
		return defaultAbstainThreshold, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		return defaultAbstainThreshold, false
	}
	return v, true
}

// normalizeScore maps both similarity-like and distance-like scores to [0,1].
func normalizeScore(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v <= 1:
		return v
	default:
		// Distance-like scores (larger is worse) compressed into [0,1).
		return 1.0 / (1.0 + v)
	}
}

func confidenceFromChunks(chunks []fiber.Map) float64 {
	if len(chunks) == 0 {
		return 0
	}
	top := 0.0
	second := 0.0
	for i, c := range chunks {
		s, ok := c["score"].(float64)
		if !ok {
			continue
		}
		n := normalizeScore(s)
		if i == 0 || n > top {
			second = top
			top = n
		} else if n > second {
			second = n
		}
	}
	gap := top - second
	if gap < 0 {
		gap = 0
	}
	coverage := math.Min(float64(len(chunks))/10.0, 1.0)
	// Weighted blend: score quality + confidence gap + retrieval coverage.
	conf := top*0.6 + gap*0.25 + coverage*0.15
	if conf < 0 {
		return 0
	}
	if conf > 1 {
		return 1
	}
	return conf
}

func confidenceFromRouting(c *fiber.Ctx) float64 {
	if c == nil {
		return 0.5
	}
	rd, ok := c.Locals("routing_decision").(*router.Decision)
	if !ok || rd == nil {
		return 0.5
	}
	return float64(rd.Confidence)
}

func combinedRAGConfidence(c *fiber.Ctx, chunks []fiber.Map) float64 {
	return buildConfidenceBreakdown(c, chunks, 0).Combined
}

func buildConfidenceBreakdown(c *fiber.Ctx, chunks []fiber.Map, threshold float64) confidenceBreakdown {
	retr := confidenceFromChunks(chunks)
	route := confidenceFromRouting(c)
	conf := retr*0.7 + route*0.3
	if conf < 0 {
		conf = 0
	}
	if conf > 1 {
		conf = 1
	}
	return confidenceBreakdown{
		Retrieval: retr,
		Routing:   route,
		Combined:  conf,
		Threshold: threshold,
	}
}
