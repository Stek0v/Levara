package http

import (
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestRAGAbstainThreshold_DefaultAndEnv(t *testing.T) {
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "")
	if got := ragAbstainThreshold(); got != defaultAbstainThreshold {
		t.Fatalf("default threshold: got %.2f want %.2f", got, defaultAbstainThreshold)
	}

	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0.62")
	if got := ragAbstainThreshold(); got != 0.62 {
		t.Fatalf("env threshold: got %.2f want 0.62", got)
	}

	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "oops")
	if got := ragAbstainThreshold(); got != defaultAbstainThreshold {
		t.Fatalf("invalid env fallback: got %.2f want %.2f", got, defaultAbstainThreshold)
	}
}

func TestRAGAbstainThresholdFor_PerSearchTypeOverride(t *testing.T) {
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0.25")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION", "0.70")
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD_GRAPH_COMPLETION", "")

	if got := ragAbstainThresholdFor("RAG_COMPLETION"); got != 0.70 {
		t.Fatalf("rag-specific threshold: got %.2f want 0.70", got)
	}
	if got := ragAbstainThresholdFor("GRAPH_COMPLETION"); got != 0.25 {
		t.Fatalf("graph fallback threshold: got %.2f want 0.25", got)
	}
}

func TestConfidenceFromChunks(t *testing.T) {
	empty := confidenceFromChunks(nil)
	if empty != 0 {
		t.Fatalf("empty confidence: got %.2f want 0", empty)
	}

	strong := confidenceFromChunks([]fiber.Map{
		{"score": 0.93},
		{"score": 0.81},
		{"score": 0.77},
	})
	weak := confidenceFromChunks([]fiber.Map{
		{"score": 0.25},
		{"score": 0.24},
	})
	if strong <= weak {
		t.Fatalf("expected strong > weak confidence: strong=%.3f weak=%.3f", strong, weak)
	}
}

func TestNormalizeScore_DistanceLike(t *testing.T) {
	if got := normalizeScore(2.0); got <= 0 || got >= 1 {
		t.Fatalf("distance-like score normalization out of range: %.3f", got)
	}
}

func TestCombinedRAGConfidence_Clamped(t *testing.T) {
	conf := combinedRAGConfidence(nil, []fiber.Map{{"score": 0.9}})
	if conf < 0 || conf > 1 {
		t.Fatalf("combined confidence not clamped: %.3f", conf)
	}
}

func TestBuildConfidenceBreakdown(t *testing.T) {
	b := buildConfidenceBreakdown(nil, []fiber.Map{{"score": 0.9}}, 0.4)
	if b.Combined < 0 || b.Combined > 1 {
		t.Fatalf("combined out of range: %.3f", b.Combined)
	}
	if b.Threshold != 0.4 {
		t.Fatalf("threshold mismatch: %.2f", b.Threshold)
	}
}
