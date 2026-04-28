package http

import "github.com/gofiber/fiber/v2"

// extractEvidenceChunkIDs extracts up to limit chunk IDs for response grounding metadata.
func extractEvidenceChunkIDs(chunks []fiber.Map, limit int) []string {
	if limit <= 0 {
		limit = 10
	}
	out := make([]string, 0, limit)
	seen := make(map[string]bool, limit)
	for _, c := range chunks {
		id, _ := c["id"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	return out
}

