// Package mcp is the Model Context Protocol transport + tools layer.
// Wire types live in types.go, session lifecycle in session.go, the tool
// descriptor registry in tools.go. Each MCP tool has a body in its own
// tool_*.go file (see F-4 wave 3j-split). This file holds only the Deps
// interface and the SearchResult type — the seam between the handler in
// internal/http and the tool bodies.
package mcp

import (
	"context"
	"database/sql"

	"github.com/stek0v/cognevra/pipeline"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/router"
	"github.com/stek0v/cognevra/pkg/runreg"
)

// Deps is the narrow application-state surface that MCP tool
// implementations depend on. The concrete implementation lives in
// internal/http (wrapping APIConfig); tests pass their own fake.
//
// Each F-4 wave adds only the methods the next tool needs, so the seam
// stays minimal and tools can be unit-tested without pulling in the full
// HTTP config (embed client, LLM provider, Cluster, ...). Growth so far:
//   - wave 3a: DB + Q (toolDelete)
//   - wave 3b: no growth (toolPrune reused DB)
//   - wave 3c: + HasCollections + ListCollections (toolListData)
//   - wave 3d: + StoragePath (toolAdd)
//   - wave 3e: + CollectionExists (toolSetContext)
//   - wave 3i: + EmbedAvailable + Embed + CollectionInsert + CollectionSearch
//              (toolSaveMemory + toolRecallMemory) — first AI seam. Types
//              stay small ([]float32 and SearchResult) so pkg/mcp doesn't
//              import pkg/embed or internal/store.
//   - wave 3j: + Runs + BaseCognifyConfig + OntologyPromptSuffix +
//              PersistPipelineStatus + LogHeartbeat + RunPipeline
//              (toolCognify + toolCognifyStatus). BaseCognifyConfig brings
//              in pkg/orchestrator as a new pkg/mcp import; Runs brings in
//              pkg/runreg. RunPipeline abstracts orchestrator.Run so the
//              cognify goroutine is testable without the real LLM/embed
//              stack.
//   - wave 3k: + NewSearchPipeline + LLMProvider + LLMModel +
//              SearchCapabilities (toolSearch). NewSearchPipeline returns
//              a SearchPipeline interface (defined in this package) so
//              production wraps *pipeline.SearchPipeline while tests
//              supply a stub. Pkg/mcp now imports pipeline + router +
//              llm; graphrank stays inside tool_search.go (used only
//              there).
//   - wave 3o: + CollectionMeta (toolGetProjectContext + toolCheckDrift).
//              Returns CollectionInfo value type so pkg/mcp stays free of
//              internal/store.CollectionMeta pointer.
type Deps interface {
	// DB returns the shared *sql.DB used for palace / datasets / graph
	// tables. May be nil when no PostgresDSN is configured — tool
	// implementations must guard against it rather than panic.
	DB() *sql.DB
	// Q rewrites a query's placeholder style and syntax to match the
	// active DB dialect (Postgres passthrough, SQLite translation).
	// Tools should always route queries through Q so the SQLite test
	// builds stay working.
	Q(query string) string
	// HasCollections reports whether a vector-collection manager is
	// configured. Some tools (e.g. list_data) return an empty result
	// when this is false, mirroring a deployment that runs without the
	// vector engine.
	HasCollections() bool
	// ListCollections returns the registered collection names. Returns
	// nil when HasCollections() is false. The slice is safe to iterate
	// but must not be mutated.
	ListCollections() []string
	// StoragePath returns the on-disk directory where ingested files
	// are written. An empty string is treated as the legacy default
	// "data/uploads" by tools that need a path.
	StoragePath() string
	// CollectionExists reports whether a collection with the given name
	// is registered. Always false when HasCollections() is false.
	// Callers use this for soft validation — unknown names are still
	// allowed through ToolSetContext on the "will be created when data
	// is added" promise.
	CollectionExists(name string) bool
	// EmbedAvailable reports whether the embedding service + collection
	// manager are both configured. Memory tools fall back to SQL-only
	// paths when false. Cheap boolean gate — separate from HasCollections
	// because some deployments have a vector engine without an embed
	// service.
	EmbedAvailable() bool
	// Embed generates a single-vector embedding for the given text.
	// Caller should guard with EmbedAvailable(); implementations may
	// panic or error if called without a configured service.
	Embed(ctx context.Context, text string) ([]float32, error)
	// CollectionInsert adds a vector + metadata to a named collection.
	// Best-effort — callers ignore the error (matches pre-refactor
	// fire-and-forget ingest behavior for vector-indexed memories).
	CollectionInsert(collection, id string, vec []float32, meta any) error
	// CollectionSearch finds the topK nearest vectors in a named
	// collection. Returns an empty slice + nil error when no matches
	// (caller distinguishes "no hits" from "error" via the err return).
	CollectionSearch(collection string, query []float32, topK int) ([]SearchResult, error)
	// Runs returns the shared pipeline-run registry. Cognify stores a
	// *runreg.Status here so cognify_status (and the REST SSE stream in
	// internal/http) can read progress updates. The concrete pointer is
	// shared across the whole server — MCP-initiated and REST-initiated
	// runs coexist in the same map.
	Runs() *runreg.Registry
	// BaseCognifyConfig returns an orchestrator.Config pre-populated with
	// deployment-level settings: embed endpoint/model, LLM endpoint/model,
	// LLM provider + cache, Neo4j credentials, collection manager, BM25
	// indexes, DB handle. Tool-level fields (Collection, DatasetID, Room,
	// Tags, SystemPrompt, SkipGraph, chunking overrides, ...) are the
	// caller's responsibility and override the returned base.
	BaseCognifyConfig() orchestrator.Config
	// OntologyPromptSuffix returns the ontology-guided extraction text to
	// append to the system prompt for a given collection. Returns empty
	// string when no ontology is configured for the collection. Harmless
	// to concatenate unconditionally.
	OntologyPromptSuffix(collection string) string
	// PersistPipelineStatus writes terminal pipeline state to the data
	// table so subsequent /cognify calls can skip already-processed
	// datasets. Best-effort — errors are swallowed to match the
	// pre-refactor signature which takes no error return.
	PersistPipelineStatus(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64)
	// LogHeartbeat records an event (arbitrary payload) into the
	// heartbeats table when DB is configured, no-op otherwise. Used by
	// long-running tools (cognify, sync, prune) for observability.
	LogHeartbeat(eventType string, payload any)
	// RunPipeline runs the cognify orchestrator end-to-end against texts
	// with the given config, emitting Progress on progressCh and closing
	// it when done. Production wiring calls orchestrator.Run; tests can
	// substitute a stub that closes the channel immediately to exercise
	// the post-run bookkeeping (status transition, persist, heartbeat)
	// without the real LLM + embed stack.
	RunPipeline(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error
	// NewSearchPipeline returns a configured SearchPipeline (embed +
	// collections + optional reranker) or nil when the embed service /
	// collection manager isn't configured. doRerank tells the builder
	// whether to construct a rerank client; callers should still gate
	// rerank-only branches on SearchPipeline.RerankEnabled().
	NewSearchPipeline(doRerank bool) SearchPipeline
	// LLMProvider returns the multi-provider LLM abstraction, or nil
	// when no provider is configured. Used by the multi-query search
	// branch.
	LLMProvider() llm.Provider
	// LLMModel returns the configured LLM model name (from env var in
	// the current wiring). Empty string when unset. The multi-query
	// search branch forwards this to the provider.
	LLMModel() string
	// SearchCapabilities returns the router.Capabilities used by
	// smart-routing (AUTO / FEELING_LUCKY search types). May be a
	// relatively expensive call — the production implementation hits
	// the DB to detect communities.
	SearchCapabilities() router.Capabilities
	// CollectionMeta returns observable metadata for a named collection.
	// Returns zero CollectionInfo when the collection doesn't exist or
	// HasCollections() is false. Used by toolGetProjectContext and
	// toolCheckDrift to read per-collection stats without leaking the
	// internal/store.CollectionMeta pointer into pkg/mcp.
	CollectionMeta(name string) CollectionInfo
}

// SearchPipeline is the narrow interface toolSearch calls into. The
// production implementation in internal/http is a thin adapter over
// *pipeline.SearchPipeline plus the optional *rerank.Client. Tests
// supply their own stub so the dispatch logic can be exercised
// without a live embed/rerank service.
type SearchPipeline interface {
	// SearchByText runs the default vector similarity search.
	SearchByText(ctx context.Context, collection, query string, topK int) ([]pipeline.ScoredResult, error)
	// SearchByTextParentChild uses the parent-child hierarchical
	// chunking strategy.
	SearchByTextParentChild(ctx context.Context, collection, query string, topK int) ([]pipeline.ScoredResult, error)
	// SearchByTextMultiQuery generates n rewritten queries via the
	// given provider/model and merges their results.
	SearchByTextMultiQuery(ctx context.Context, collection, query string, topK int, provider llm.Provider, model string, n int) ([]pipeline.ScoredResult, error)
	// SearchByTextWithRerank runs vector search then cross-encoder
	// rerank when RerankEnabled() is true. The reranked bool in the
	// return tells the caller whether rerank actually ran (it may be
	// skipped when the endpoint is unreachable).
	SearchByTextWithRerank(ctx context.Context, collection, query string, topK int) (results []pipeline.ScoredResult, reranked bool, err error)
	// RerankEnabled reports whether the underlying rerank client is
	// configured + reachable. Callers gate the rerank branch on this.
	RerankEnabled() bool
}

// SearchResult is one entry returned by CollectionSearch. Kept small
// and type-clean so pkg/mcp doesn't need to import internal/store's
// VectroRecord. Data carries raw JSON metadata that callers unmarshal
// on demand.
type SearchResult struct {
	ID    string
	Score float32
	Data  []byte
}

// CollectionInfo is the observable metadata for a single collection
// returned by Deps.CollectionMeta. It is a plain value type — no
// internal/store pointers — so pkg/mcp stays free of that import.
type CollectionInfo struct {
	Name       string
	Records    int
	Dim        int
	Metric     string
	EmbedModel string
}
