// Package vectorstore provides a pluggable interface for vector storage backends.
// Default: HNSW in-process (wraps internal/store.CollectionManager).
// Future: Qdrant, PGVector, Milvus.
package vectorstore

import "errors"

var ErrEmptyFilter = errors.New("metadata filter must not be empty")

// SearchResult is a single vector search result.
type SearchResult struct {
	ID       string  `json:"id"`
	Score    float32 `json:"score"`
	Metadata []byte  `json:"metadata"`
}

// UpsertRecord is one record written to a vector collection.
type UpsertRecord struct {
	ID       string      `json:"id"`
	Vector   []float32   `json:"vector"`
	Metadata interface{} `json:"metadata"`
}

// StoredRecord is one durable vector-store record.
type StoredRecord struct {
	ID       string    `json:"id"`
	Vector   []float32 `json:"vector"`
	Metadata []byte    `json:"metadata"`
}

// MetadataFilter is a strict equality-style metadata filter.
// All keys must match. When a metadata value is an array, scalar filter values
// match any array element; array filter values must all be present.
type MetadataFilter map[string]any

// CollectionMeta describes a vector collection.
type CollectionMeta struct {
	Name           string `json:"name"`
	RecordCount    int    `json:"record_count"`
	Dimension      int    `json:"dimension"`
	Model          string `json:"model"`
	DistanceMetric string `json:"distance_metric,omitempty"`
}

// VectorStore is the interface for vector storage backends.
type VectorStore interface {
	// Insert adds a vector with metadata to a collection.
	Insert(collection, id string, vector []float32, metadata interface{}) error

	// BatchUpsert adds or replaces multiple vectors in a collection.
	BatchUpsert(collection string, records []UpsertRecord) []error

	// Search finds the top-K nearest vectors in a collection.
	Search(collection string, queryVec []float32, topK int) ([]SearchResult, error)

	// Get returns one record by ID.
	Get(collection, id string) (StoredRecord, bool, error)

	// Scan returns all live records in a collection.
	Scan(collection string) ([]StoredRecord, error)

	// Delete removes a vector by ID from a collection.
	Delete(collection, id string) error

	// DeleteMany removes multiple vector IDs from a collection.
	DeleteMany(collection string, ids []string) []error

	// DeleteByFilter deletes records whose JSON metadata strictly matches filter.
	// Implementations may use a scan fallback when no native filter delete exists.
	DeleteByFilter(collection string, filter MetadataFilter) ([]string, []error)

	// Has checks if a collection exists.
	Has(collection string) bool

	// Create creates a new collection.
	Create(collection string) error

	// Drop deletes a collection and all of its vectors.
	Drop(collection string) error

	// List returns all collection names.
	List() []string

	// Count returns the number of vectors in a collection.
	Count(collection string) int

	// Metadata returns observable collection metadata.
	Metadata(collection string) (CollectionMeta, bool)

	// Checkpoint asks the backend to compact/flush durable state when supported.
	Checkpoint() error

	// Close releases all resources.
	Close() error
}
