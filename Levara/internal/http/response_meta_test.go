package http

import (
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/router"
	"github.com/valyala/fasthttp"
)

func TestAttachSearchDebugMetadata_Defaults(t *testing.T) {
	out := attachSearchDebugMetadata(nil, fiber.Map{"ok": true})
	if _, ok := out["debug"]; ok {
		t.Fatalf("debug should not be added for nil ctx")
	}

	app := fiber.New()
	ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(ctx)

	out = attachSearchDebugMetadata(ctx, fiber.Map{"ok": true})
	debug, ok := out["debug"].(fiber.Map)
	if !ok {
		t.Fatalf("debug block missing")
	}
	if debug["source"] != "explicit" {
		t.Fatalf("expected explicit source, got %v", debug["source"])
	}
}

func TestAttachSearchDebugMetadata_WithRoutingDecision(t *testing.T) {
	app := fiber.New()
	ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(ctx)

	ctx.Locals("routing_source", "routed")
	ctx.Locals("routing_decision", &router.Decision{
		SearchType: "HYBRID",
		Reason:     "query looks like keyword",
		Confidence: 0.8,
	})

	out := attachSearchDebugMetadata(ctx, fiber.Map{})
	debug, _ := out["debug"].(fiber.Map)
	if debug["source"] != "routed" {
		t.Fatalf("source mismatch: got %v", debug["source"])
	}
	if debug["strategy"] != "HYBRID" {
		t.Fatalf("strategy mismatch: got %v", debug["strategy"])
	}
}

