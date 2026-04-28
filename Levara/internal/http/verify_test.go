package http

import (
	"encoding/json"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestVerifyScoredResults_FiltersLowScoreAndBadMetadata(t *testing.T) {
	results := []fiber.Map{
		{"id": "ok", "score": 0.9, "metadata": json.RawMessage(`{"text":"ok"}`)},
		{"id": "low", "score": 0.1, "metadata": json.RawMessage(`{"text":"low"}`)},
		{"id": "badmeta", "score": 0.9, "metadata": json.RawMessage(`{`)},
	}
	got, v := verifyScoredResults(results, 0.5, true)
	if len(got) != 1 || got[0]["id"] != "ok" {
		t.Fatalf("got=%v, want only 'ok'", got)
	}
	if v.DroppedLowScore != 1 || v.DroppedBadMeta != 1 || v.Kept != 1 {
		t.Fatalf("verification=%+v, want low=1 bad=1 kept=1", v)
	}
}

