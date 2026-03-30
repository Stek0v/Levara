// Package vectorstore provides a pluggable interface for vector storage backends.
// Default: HNSW in-process (wraps internal/store.CollectionManager).
// Future: Qdrant, PGVector, Milvus.
package vectorstore

// SearchResult is a single vector search result.
type SearchResult struct {
	ID       string  `json:"id"`
	Score    float32 `json:"score"`
	Metadata []byte  `json:"metadata"`
}

// CollectionMeta describes a vector collection.
type CollectionMeta struct {
	Name       string `json:"name"`
	RecordCount int   `json:"record_count"`
	Dimension   int   `json:"dimension"`
	Model       string `json:"model"`
}

// VectorStore is the interface for vector storage backends.
type VectorStore interface {
	// Insert adds a vector with metadata to a collection.
	Insert(collection, id string, vector []float32, metadata interface{}) error

	// Search finds the top-K nearest vectors in a collection.
	Search(collection string, queryVec []float32, topK int) ([]SearchResult, error)

	// Delete removes a vector by ID from a collection.
	Delete(collection, id string) error

	// Has checks if a collection exists.
	Has(collection string) bool

	// Create creates a new collection.
	Create(collection string) error

	// List returns all collection names.
	List() []string

	// Count returns the number of vectors in a collection.
	Count(collection string) int

	// Close releases all resources.
	Close() error
}
