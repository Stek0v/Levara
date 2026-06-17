// Package mcp is the Model Context Protocol transport + tools layer.
// Wire types live in types.go, session lifecycle in session.go, the tool
// descriptor registry in tools.go. Each MCP tool has a body in its own
// tool_*.go file. This file holds the capability-oriented dependency
// interfaces: the seam between the HTTP handler and transport-independent
// tool bodies.
package mcp

import (
	"context"
	"database/sql"

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/runreg"
)

// SQLDeps is the database surface used by tools that read/write palace,
// datasets, graph, feedback, chat, and other SQL-backed records.
type SQLDeps interface {
	DB() *sql.DB
	Q(query string) string
}

// CollectionDeps is the vector collection surface used by data/search/memory
// tools. It deliberately avoids exposing internal/store types.
type CollectionDeps interface {
	HasCollections() bool
	ListCollections() []string
	CollectionExists(name string) bool
	CollectionInsert(collection, id string, vec []float32, meta any) error
	// CollectionDelete tombstones a single record by id (CollectionManager.
	// Delete). Used by delete_memory to drop the vector sidecar entry when a
	// memory row is removed, so it stops surfacing in recall. Missing
	// collection/id is a no-op, not an error.
	CollectionDelete(collection, id string) error
	CollectionSearch(collection string, query []float32, topK int) ([]SearchResult, error)
	// CollectionHasRecord reports whether id exists in collection via a
	// synchronous index lookup (not a vector search), so it is true the
	// instant CollectionInsert returns. Used to verify memory writes landed.
	CollectionHasRecord(collection, id string) bool
	CollectionMeta(name string) CollectionInfo
}

// StorageDeps is the filesystem/object-storage surface used by ingest tools.
type StorageDeps interface {
	StoragePath() string
}

// EmbedDeps is the embedding surface used by memory, codify, and search tools.
type EmbedDeps interface {
	EmbedAvailable() bool
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
	EmbedEndpoint() string
	EmbedModel() string
}

// MemoryDeps groups the capabilities needed by the memory palace tools.
type MemoryDeps interface {
	SQLDeps
	CollectionDeps
	EmbedDeps
}

// PipelineDeps is the cognify/git-ingest pipeline surface.
type PipelineDeps interface {
	Runs() *runreg.Registry
	BaseCognifyConfig() orchestrator.Config
	OntologyPromptSuffix(collection string) string
	PersistPipelineStatus(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64)
	RunPipeline(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error
}

// SearchDeps is the semantic/lexical/graph search surface.
type SearchDeps interface {
	CollectionDeps
	EmbedDeps
	NewSearchPipeline(doRerank bool) SearchPipeline
	LLMProvider() llm.Provider
	LLMModel() string
	SearchCapabilities() router.Capabilities
	AllowedDatasetIDs(ctx context.Context) []string
	ListLexicalCollections() []string
	LexicalSearch(collection, query string, topK int) ([]LexicalResult, error)
}

// SyncDeps is the cross-instance sync surface.
type SyncDeps interface {
	DoSync(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (result, manifest map[string]any, err error)
}

// ObservabilityDeps is the heartbeat/ops event surface.
type ObservabilityDeps interface {
	LogHeartbeat(eventType string, payload any)
}

// Deps is the full application-state surface that the current MCP tool set
// depends on. New tools should prefer the narrow capability interface they
// actually need (SQLDeps, SearchDeps, PipelineDeps, SyncDeps, etc.) so the
// transport boundary stays understandable as MCP grows.
type Deps interface {
	SQLDeps
	CollectionDeps
	StorageDeps
	EmbedDeps
	PipelineDeps
	SearchDeps
	SyncDeps
	ObservabilityDeps
}

// SearchPipeline is the narrow interface toolSearch calls into. The production
// implementation in internal/http is a thin adapter over *pipeline.SearchPipeline
// plus the optional reranker. Tests supply their own stub so dispatch logic can
// be exercised without a live embed/rerank service.
type SearchPipeline interface {
	SearchByText(ctx context.Context, collection, query string, topK int) ([]pipeline.ScoredResult, error)
	SearchByTextParentChild(ctx context.Context, collection, query string, topK int) ([]pipeline.ScoredResult, error)
	SearchByTextMultiQuery(ctx context.Context, collection, query string, topK int, provider llm.Provider, model string, n int) ([]pipeline.ScoredResult, error)
	ApplyRerank(ctx context.Context, query string, in []pipeline.ScoredResult, topK int) (bool, []pipeline.ScoredResult)
	RerankEnabled() bool
}

// SearchResult is one entry returned by CollectionSearch. Kept small and
// type-clean so pkg/mcp doesn't need to import internal/store's VectroRecord.
type SearchResult struct {
	ID    string
	Score float32
	Data  []byte
}

// LexicalResult is one BM25/keyword hit returned by Deps.LexicalSearch.
type LexicalResult struct {
	ID       string
	Score    float64
	Metadata []byte
}

// CollectionInfo is observable collection metadata without internal pointers.
type CollectionInfo struct {
	Name         string
	Records      int
	Dim          int
	Metric       string
	EmbedModel   string
	EmbedVersion string
}
