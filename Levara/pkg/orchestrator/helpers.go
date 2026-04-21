// helpers.go — small utilities shared across pipeline.go and extract.go,
// split out so the main pipeline file isn't padded with one-liners.
package orchestrator

import (
	"encoding/json"
	"time"
)

// ms returns elapsed milliseconds since start. Used for Progress events
// where the channel consumer wants integer ms instead of a Duration.
func ms(start time.Time) int64 {
	return time.Since(start).Milliseconds()
}

// buildEmbedText prepends contextual headers to chunk text before
// embedding. Document/section context goes into the vector so retrieval
// can match queries against meaningful provenance, not just bag-of-words
// in the chunk body. The original chunk text stays in metadata for
// display purposes.
func buildEmbedText(text, documentTitle, section string) string {
	var prefix string
	if documentTitle != "" {
		prefix += "[Document: " + documentTitle + "]\n"
	}
	if section != "" {
		prefix += "[Section: " + section + "]\n"
	}
	return prefix + text
}

// mustJSON returns s as a JSON-safe quoted string. Used in spots where
// we're assembling an SQL VALUES literal and need to embed an arbitrary
// user string without breaking the surrounding JSON. Errors collapse to
// "" since the caller has no recovery path.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// defaultExtractionPrompt is the system prompt used by extractEntities
// when the caller hasn't supplied one. Kept here (not in extract.go) so
// the surrounding text easily references it via grep without pulling in
// the LLM-call body.
const defaultExtractionPrompt = `Extract entities and relationships from the following text.
Return a JSON object with this exact structure:
{
  "nodes": [{"id": "unique_id", "name": "Entity Name", "type": "EntityType", "description": "Brief description"}],
  "edges": [{"source": "source_id", "target": "target_id", "relationship": "RELATIONSHIP_TYPE", "edge_text": "description of relationship"}]
}
Return ONLY the JSON object, no other text.`
