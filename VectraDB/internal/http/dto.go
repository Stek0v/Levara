package http

import "encoding/json"

type InsertRequest struct {
	ID     string         `json:"id"`
	Vector []float32      `json:"vector"`
	Data   map[string]any `json:"metadata"`
}

// BatchInsertItem is a single record inside a BatchInsertRequest.
type BatchInsertItem struct {
	ID     string         `json:"id"`
	Vector []float32      `json:"vector"`
	Data   map[string]any `json:"metadata"`
}

// BatchInsertRequest allows inserting many records in one HTTP call,
// reducing round-trips and amortising Raft consensus cost across records.
type BatchInsertRequest struct {
	Records []BatchInsertItem `json:"records"`
}

// BatchInsertResponse reports per-record success/failure.
type BatchInsertResponse struct {
	Inserted int      `json:"inserted"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

type SearchRequest struct {
	Vector []float32 `json:"vector"`
	TopK   int       `json:"k"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

type SearchResult struct {
	ID    string          `json:"id"`
	Score float32         `json:"score"`
	Data  json.RawMessage `json:"metadata"`
}
