package http

import (
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestExtractEvidenceChunkIDs_DedupAndLimit(t *testing.T) {
	chunks := []fiber.Map{
		{"id": "a"},
		{"id": "b"},
		{"id": "a"},
		{"id": "c"},
	}
	got := extractEvidenceChunkIDs(chunks, 2)
	if len(got) != 2 {
		t.Fatalf("len=%d, want 2", len(got))
	}
	if got[0] != "a" || got[1] != "b" {
		t.Fatalf("got=%v, want [a b]", got)
	}
}

