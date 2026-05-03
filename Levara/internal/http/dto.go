package http

import "encoding/json"

type InsertRequest struct {
	Collection string         `json:"collection,omitempty"`
	ID         string         `json:"id"`
	Vector     []float32      `json:"vector"`
	Data       map[string]any `json:"metadata"`
}

// BatchInsertItem is a single record inside a BatchInsertRequest.
type BatchInsertItem struct {
	ID     string         `json:"id"`
	Vector []float32      `json:"vector"`
	Data   map[string]any `json:"metadata"`
}

// BatchInsertRequest allows inserting many records in one HTTP call,
// reducing round-trips and amortising Raft consensus cost across records.
//
// Collection routes the batch into a per-collection HNSW+Arena+WAL stack
// via CollectionManager when set; an empty value falls back to the legacy
// cluster store for backward compatibility.
type BatchInsertRequest struct {
	Collection string            `json:"collection,omitempty"`
	Records    []BatchInsertItem `json:"records"`
}

// BatchInsertResponse reports per-record success/failure.
type BatchInsertResponse struct {
	Inserted int      `json:"inserted"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

type SearchRequest struct {
	Collection string    `json:"collection,omitempty"`
	Vector     []float32 `json:"vector"`
	TopK       int       `json:"k"`
}

type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

type SearchResult struct {
	ID    string          `json:"id"`
	Score float32         `json:"score"`
	Data  json.RawMessage `json:"metadata"`
}

// Delete DTOs
type DeleteRequest struct {
	Collection string   `json:"collection,omitempty"`
	IDs        []string `json:"ids"`
}

type DeleteResponse struct {
	Deleted int      `json:"deleted"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// Collection DTOs
type CreateCollectionRequest struct {
	Name string `json:"name"`
}

type ListCollectionsResponse struct {
	Collections []string `json:"collections"`
}
