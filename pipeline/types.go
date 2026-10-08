package pipeline

import "encoding/json"

// ScoredResult represents a search result with score and metadata.
type ScoredResult struct {
	Collection string          `json:"-"` // Server-selected retrieval origin, never indexed metadata.
	ID         string          `json:"id"`
	Score      float32         `json:"score"`
	Metadata   json.RawMessage `json:"metadata,omitempty"`
}
