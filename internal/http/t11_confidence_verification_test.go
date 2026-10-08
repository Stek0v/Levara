package http

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/router"
	"github.com/valyala/fasthttp"
)

func TestT11ConfidenceNumericParity(t *testing.T) {
	chunks32 := []fiber.Map{{"score": float32(0.875)}, {"score": float32(0.5)}}
	chunks64 := []fiber.Map{{"score": float64(0.875)}, {"score": float64(0.5)}}
	want := 0.875*0.6 + (0.875-0.5)*0.25 + 0.2*0.15
	for _, chunks := range [][]fiber.Map{chunks32, chunks64} {
		if got := confidenceFromChunks(chunks); math.Abs(got-want) > 1e-12 {
			t.Fatalf("numeric score parity lost: got=%v want=%v", got, want)
		}
	}
	if got := confidenceFromChunks([]fiber.Map{{"fused_score": 0.875}, {"score": 0.5}}); math.Abs(got-want) > 1e-12 {
		t.Fatalf("fused score was ignored: %v", got)
	}
}

func TestT11ThresholdRejectsNonFinite(t *testing.T) {
	t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD", "0.4")
	for _, raw := range []string{"NaN", "Inf", "+Inf", "-Inf", "-0.1", "1.1", "oops", ""} {
		t.Run(raw, func(t *testing.T) {
			if got, ok := parseThreshold(raw); ok || got != defaultAbstainThreshold {
				t.Fatalf("invalid threshold accepted: got=%v ok=%v", got, ok)
			}
			t.Setenv("LEVARA_RAG_ABSTAIN_THRESHOLD_RAG_COMPLETION", raw)
			if got := ragAbstainThresholdFor("RAG_COMPLETION"); got != 0.4 {
				t.Fatalf("invalid override did not fall back: %v", got)
			}
		})
	}
	for _, raw := range []string{"0", "0.5", "1"} {
		if _, ok := parseThreshold(raw); !ok {
			t.Fatalf("valid threshold rejected: %s", raw)
		}
	}
}

func TestT11ConfidenceNonFiniteOutput(t *testing.T) {
	app := fiber.New()
	ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(ctx)
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := normalizeScore(value); got != 0 {
			t.Errorf("nonfinite score normalized to %v", got)
		}
		ctx.Locals("routing_decision", &router.Decision{Confidence: float32(value)})
		breakdown := buildConfidenceBreakdown(ctx, []fiber.Map{{"score": value}}, value)
		if _, err := json.Marshal(breakdown); err != nil {
			t.Errorf("confidence cannot be encoded: %+v: %v", breakdown, err)
		}
		if breakdown.Retrieval < 0 || breakdown.Retrieval > 1 || breakdown.Routing < 0 || breakdown.Routing > 1 || breakdown.Combined < 0 || breakdown.Combined > 1 {
			t.Errorf("confidence out of range: %+v", breakdown)
		}
	}
}

func TestT11MetadataMustBeNonNullObject(t *testing.T) {
	for _, raw := range []string{"null", "[]", "[{}]", "1", "true", `"text"`, "{", ""} {
		for _, value := range []any{raw, []byte(raw), json.RawMessage(raw)} {
			if hasValidMetadata(fiber.Map{"metadata": value}) {
				t.Errorf("invalid metadata accepted: %q (%T)", raw, value)
			}
		}
	}
	for _, value := range []any{nil, map[string]any(nil), fiber.Map(nil), []byte(nil), json.RawMessage(nil)} {
		if hasValidMetadata(fiber.Map{"metadata": value}) {
			t.Errorf("nil metadata accepted: %T", value)
		}
	}
	for _, value := range []any{`{}`, []byte(`{"text":"ok"}`), json.RawMessage(`{"text":"ok"}`), map[string]any{}, fiber.Map{"text": "ok"}} {
		if !hasValidMetadata(fiber.Map{"metadata": value}) {
			t.Errorf("valid object rejected: %T", value)
		}
	}
	results := []fiber.Map{{"id": "null", "score": float32(0.9), "metadata": "null"}, {"id": "valid", "score": float32(0.9), "metadata": json.RawMessage(`{}`)}}
	kept, counts := verifyScoredResults(results, 0.5, true)
	if len(kept) != 1 || kept[0]["id"] != "valid" || counts.DroppedBadMeta != 1 || counts.Kept != 1 {
		t.Fatalf("verification counts wrong: %v %+v", kept, counts)
	}
	kept, counts = verifyScoredResults(results, 0, false)
	if len(kept) != 2 || counts.DroppedBadMeta != 0 || counts.DroppedLowScore != 0 {
		t.Fatal("default verification became enabled")
	}
}
