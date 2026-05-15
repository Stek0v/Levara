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

	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/runreg"
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
//     (toolSaveMemory + toolRecallMemory) — first AI seam. Types
//     stay small ([]float32 and SearchResult) so pkg/mcp doesn't
//     import pkg/embed or internal/store.
//   - wave 3j: + Runs + BaseCognifyConfig + OntologyPromptSuffix +
//     PersistPipelineStatus + LogHeartbeat + RunPipeline
//     (toolCognify + toolCognifyStatus). BaseCognifyConfig brings
//     in pkg/orchestrator as a new pkg/mcp import; Runs brings in
//     pkg/runreg. RunPipeline abstracts orchestrator.Run so the
//     cognify goroutine is testable without the real LLM/embed
//     stack.
//   - wave 3k: + NewSearchPipeline + LLMProvider + LLMModel +
//     SearchCapabilities (toolSearch). NewSearchPipeline returns
//     a SearchPipeline interface (defined in this package) so
//     production wraps *pipeline.SearchPipeline while tests
//     supply a stub. Pkg/mcp now imports pipeline + router +
//     llm; graphrank stays inside tool_search.go (used only
//     there).
//   - wave 3o: + CollectionMeta (toolGetProjectContext + toolCheckDrift).
//     Returns CollectionInfo value type so pkg/mcp stays free of
//     internal/store.CollectionMeta pointer.
//   - wave 3q: + DoSync (toolSync). One high-level method absorbs all
//     internal/http sync helpers (SyncPull, syncPush,
//     syncPullCollections, syncPushCollections) so pkg/mcp doesn't
//     need to know about APIConfig or *store.CollectionManager.
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
	// EmbedBatch generates vectors for multiple texts, reusing the shared
	// embedding client (same TCP pool). Returns one vector per input text
	// in order, or an error if the underlying call failed. Callers should
	// still guard with EmbedAvailable() before invoking.
	//
	// Added for tool_codify which previously constructed its own
	// embed.Client per request; that defeated the shared-pool win from T3.
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
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
	//
	// For tools that only need one or two settings, prefer the narrow
	// accessors below (EmbedEndpoint / EmbedModel) — they don't pull
	// orchestrator.Config into your file's type surface, which keeps
	// tests lighter and the seam easier to reason about. BaseCognifyConfig
	// is still the right choice when you mutate many fields to build a
	// pipeline config from this template (see tool_cognify, tool_git).
	BaseCognifyConfig() orchestrator.Config
	// EmbedEndpoint returns the deployment-wide embedding service URL. Empty
	// string when the embed service isn't configured — callers must guard.
	EmbedEndpoint() string
	// EmbedModel returns the deployment-wide embedding model name. Empty
	// string when not configured.
	EmbedModel() string
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
	// AllowedDatasetIDs returns the dataset/project scopes visible to the
	// caller represented by ctx. Nil means no filtering (dev mode, anonymous,
	// or superuser); an empty non-nil slice means fail closed.
	AllowedDatasetIDs(ctx context.Context) []string
	// ListLexicalCollections returns collection names known to the lexical
	// index. It may differ from ListCollections on deployments that have BM25
	// indexes but no vector engine.
	ListLexicalCollections() []string
	// LexicalSearch performs a BM25/keyword search against one collection.
	// Returns an empty slice when the collection has no lexical index.
	LexicalSearch(collection, query string, topK int) ([]LexicalResult, error)
	// CollectionMeta returns observable metadata for a named collection.
	// Returns zero CollectionInfo when the collection doesn't exist or
	// HasCollections() is false. Used by toolGetProjectContext and
	// toolCheckDrift to read per-collection stats without leaking the
	// internal/store.CollectionMeta pointer into pkg/mcp.
	CollectionMeta(name string) CollectionInfo
	// DoSync orchestrates a bidirectional sync operation with a remote
	// Levara instance. It returns the per-type sync result map and the
	// remote manifest map. The caller is responsible for adding
	// remote_manifest to result and calling LogHeartbeat. error is
	// returned only for connectivity failures (manifest fetch); per-type
	// sync errors are folded into the result map under "<type>_error"
	// keys, matching the pre-refactor behaviour. One method absorbs all
	// internal/http sync helpers so pkg/mcp doesn't need APIConfig or
	// *store.CollectionManager.
	DoSync(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (result, manifest map[string]any, err error)
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
	// ApplyRerank reranks an ACL-prefiltered slice against `query` using
	// the configured cross-encoder. Returns (reranked, ordered) — true
	// only when the sidecar successfully scored at least one row. Callers
	// MUST ACL-filter `in` first; passing unfiltered candidates leaks
	// forbidden text to the third-party reranker (see Phase 2.5 RCA).
	ApplyRerank(ctx context.Context, query string, in []pipeline.ScoredResult, topK int) (bool, []pipeline.ScoredResult)
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

// LexicalResult is one BM25/keyword hit returned by Deps.LexicalSearch.
// Score follows BM25 convention: higher is better.
type LexicalResult struct {
	ID       string
	Score    float64
	Metadata []byte
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
