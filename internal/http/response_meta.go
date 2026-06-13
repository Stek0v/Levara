package http

import (
	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/router"
)

// attachSearchDebugMetadata enriches response payload with routing metadata
// so clients can inspect why a strategy was chosen.
func attachSearchDebugMetadata(c *fiber.Ctx, payload fiber.Map) fiber.Map {
	if payload == nil {
		payload = fiber.Map{}
	}
	if c == nil {
		return payload
	}

	source, _ := c.Locals("routing_source").(string)
	if source == "" {
		source = "explicit"
	}
	debug := fiber.Map{
		"source": source,
	}
	if rd, ok := c.Locals("routing_decision").(*router.Decision); ok && rd != nil {
		debug["strategy"] = rd.SearchType
		debug["confidence"] = rd.Confidence
		debug["reason"] = rd.Reason
		alternatives := rd.Alternatives
		if alternatives == nil {
			alternatives = []router.Alternative{}
		}
		debug["alternatives"] = alternatives
	}
	payload["debug"] = debug
	return payload
}

// respondSearchItems keeps backward compatibility for list-style endpoints:
// - default: returns plain array payload (legacy contract)
// - include_debug=true: returns envelope with debug metadata
func respondSearchItems(c *fiber.Ctx, req UnifiedSearchRequest, searchType string, items any) error {
	if !req.IncludeDebug {
		return c.JSON(items)
	}
	return c.JSON(attachSearchDebugMetadata(c, fiber.Map{
		"items":       items,
		"search_type": searchType,
	}))
}
