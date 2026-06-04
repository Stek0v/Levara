// api.go — Levara REST API endpoints for React frontend.
// Implements: health, datasets CRUD, file upload, cognify trigger, search.
package http

import (
	"database/sql"
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
	"github.com/stek0v/levara/pkg/observe"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/runreg"
	"github.com/stek0v/levara/pkg/storage"
	"github.com/stek0v/levara/pkg/workspace"
)

// APIConfig holds configuration for Levara API endpoints.
type APIConfig struct {
	PostgresDSN   string
	StoragePath   string
	WorkspacePath string
	JWTSecret     string
	RequireAuth   bool
	// Version is the build SHA (cmd/server.GitSHA) surfaced in the sync
	// manifest so a pull/push can warn on instance version skew.
	Version string
	// SyncToken is the bearer token injected into outbound cross-instance
	// sync requests (manifest/export/import). Empty = unauthenticated (the
	// pre-existing behaviour) — required when the remote enforces auth.
	SyncToken string
	// WorkspaceWatcher exposes polling watcher status to REST/MCP observability
	// endpoints. Nil means watcher status is unavailable/disabled.
	WorkspaceWatcher *WorkspaceWatchState
	EmbedEndpoint    string
	EmbedModel       string
	// EmbedClient is a shared *embed.Client initialised once in main.go and reused
	// across all handlers that call the default embed endpoint. Before T3 each
	// search/cognify handler constructed a new Client per request — fresh http
	// transport, fresh TCP pool, 10–50ms of needless dial+TLS latency. The Client
	// is safe for concurrent use (Transport with idle pool, no shared mutable state).
	// Handlers that need non-default parameters (custom endpoint/model/batchSize —
	// e.g. reembed migration, dual-search per-collection models, gRPC request-
	// driven params) continue to construct their own client.
	EmbedClient  *embed.Client
	Collections  *store.CollectionManager
	Neo4jCfg     GraphVisualizationConfig
	DB           *sql.DB                // shared connection pool (nil if no PostgresDSN)
	BM25Indexes  map[string]*bm25.Index // shared BM25 indexes (same as gRPC service)
	LLMCache     llmcache.LLMCacher     // shared LLM response cache (nil = no caching)
	LLMProvider  llm.Provider           // multi-provider LLM abstraction (nil = legacy raw HTTP)
	ErrorTracker *observe.ErrorTracker  // error tracking (nil = disabled)
	// FileStorage is wired from server bootstrap (local/S3). Upload hot-path writes
	// local ingest artifacts first, then mirrors them into non-local backends and
	// persists storage:// locations in metadata.
	FileStorage storage.Storage
	Logger      *observe.Logger // structured logger (nil = use log.Printf fallback)
	// Reranker configuration (optional, all empty = disabled)
	RerankEndpoint  string // e.g., "http://localhost:9100/rerank"
	RerankModel     string // e.g., "mmini-L12-int8"
	RerankTimeoutMs int    // HTTP timeout in ms, 0 = default 5000ms
	// RerankBudgetMs caps total time spent on the rerank pass; on overshoot
	// the search falls back to the un-reranked ranking. 0 = no budget.
	RerankBudgetMs int
	// RerankScoreGapThreshold gates the cross-encoder pass on candidate
	// confidence. When > 0 and the spread between the top and bottom
	// candidate vector/fused score exceeds the threshold, the ranking is
	// already considered confident and the sidecar call is skipped —
	// outcome=skipped_gap. 0 (default) keeps the unconditional behaviour.
	RerankScoreGapThreshold float32
	// Adaptive router (feedback-driven weight learning)
	AdaptiveWeights *router.AdaptiveWeights // nil = no adaptive routing
	// Runs tracks background cognify / analyze-commits pipelines. Must be
	// the same *runreg.Registry as the one passed to RegisterMCPAPI so that
	// MCP clients can poll REST-initiated runs and vice versa.
	Runs *runreg.Registry
	// SearchStrategies maps query_type to its strategy handler (T5). When
	// nil the searchHandler constructs a default registry on demand — main
	// wiring sets this explicitly so tests can override with stubs.
	SearchStrategies *StrategyRegistry
	// MCPAudit records every MCP tool call (sanitized args, latency, outcome,
	// result size) to its configured writer. Nil disables audit logging —
	// metrics still emit, but no JSONL trail is kept.
	MCPAudit audit.Sink
	// MCPAgentBucket bounds agent_id cardinality on MCP audit metrics. Nil
	// degrades to a fixed "unknown" label.
	MCPAgentBucket *metrics.UserBucket
}

// RegisterAPI registers all Levara endpoints on the Fiber app.
func RegisterAPI(app fiber.Router, cfg APIConfig) {
	cfg.StoragePath, cfg.WorkspacePath = workspace.ResolveRuntimePaths(cfg.StoragePath, cfg.WorkspacePath)
	// BL-2: log MkdirAll failures so ops can see permission / disk-full
	// issues before the first upload attempt returns a cryptic 500. We
	// don't fail startup — readonly filesystems are a legit deployment
	// mode (e.g. stateless replicas) and the upload handler will surface
	// its own error when it actually tries to write.
	if err := os.MkdirAll(cfg.StoragePath, 0755); err != nil {
		log.Printf("[api] MkdirAll %q: %v (uploads may fail)", cfg.StoragePath, err)
	}
	if err := os.MkdirAll(cfg.WorkspacePath, 0755); err != nil {
		log.Printf("[api] MkdirAll %q: %v (workspace indexing may fail)", cfg.WorkspacePath, err)
	}

	// U1: Health is registered as public route in main.go (before JWT middleware)

	// U2: Datasets CRUD
	app.Get("/datasets", datasetsListHandler(cfg))
	app.Post("/datasets", datasetCreateHandler(cfg))
	app.Delete("/datasets/:id", datasetDeleteHandler(cfg))
	app.Get("/datasets/:id/data", datasetDataHandler(cfg))
	app.Delete("/datasets/:id/data/:dataId", datasetDataDeleteHandler(cfg))
	app.Get("/datasets/:id/data/:dataId/raw", datasetDataRawHandler(cfg))
	app.Get("/datasets/:id/data/:dataId/raw/url", datasetDataRawURLHandler(cfg))
	app.Get("/datasets/status", datasetStatusHandler(cfg))

	// U3: File upload (multipart)
	app.Post("/add", addHandler(cfg))

	// OCR endpoint: extract text from image via vision model
	app.Post("/ocr", ocrHandler(cfg))

	// U4: Cognify trigger + status + SSE stream
	app.Post("/cognify", cognifyHandler(cfg))
	app.Get("/cognify/:runId/status", cognifyStatusHandler(cfg))
	app.Get("/cognify/:runId/stream", cognifyStreamHandler(cfg))

	// U6: Memify — post-cognify graph enrichment + SSE stream
	app.Post("/memify", memifyHandler(cfg))
	app.Get("/memify/:runId/status", memifyStatusHandler())
	app.Get("/memify/:runId/stream", memifyStreamHandler())

	// U7: User management (protected)
	app.Get("/users/me", userMeHandler(cfg))
	app.Get("/users", userLookupHandler(cfg)) // lookup by ?email=
	app.Put("/users/me", userUpdateHandler(cfg))
	app.Put("/users/me/password", userChangePasswordHandler(cfg))

	// U8: Settings API (protected)
	app.Get("/settings", settingsGetHandler(cfg))
	app.Put("/settings", settingsPutHandler(cfg))

	// U11: Collections metadata
	app.Get("/collections", collectionsListHandler(cfg))
	app.Post("/collections", collectionCreateHandler(cfg))
	app.Delete("/collections/:name", collectionDeleteHandler(cfg))
	app.Delete("/collections/:name/records/:id", collectionRecordDeleteHandler(cfg))
	app.Get("/collections/:name/meta", collectionMetaHandler(cfg))
	app.Put("/collections/:name/meta", collectionMetaUpdateHandler(cfg))
	app.Post("/collections/:name/rename", collectionRenameHandler(cfg))

	// Phase 2: rerank info surface (resolves design open question — clients
	// need a cheap way to confirm which reranker variant is configured).
	app.Get("/models/rerank", rerankInfoHandler(cfg))

	// U12: Re-embedding migration
	RegisterReembedAPI(app, cfg)

	// U13: Dual-search across collections with different models/dims
	RegisterDualSearchAPI(app, cfg)

	// U14: Prune endpoints (cleanup)
	app.Post("/prune/data", pruneDataHandler(cfg))
	app.Post("/prune/system", pruneSystemHandler(cfg))

	// U15: Update data endpoint
	app.Patch("/datasets/:id/data/:dataId", updateDataHandler(cfg))

	// U16: Tenant management + ACL
	RegisterTenantAPI(app, cfg)

	// U17: Session/interaction tracking
	RegisterSessionAPI(app, cfg)

	// U19: Project memory store
	RegisterMemoryAPI(app, cfg)

	// U20: Cross-instance sync
	RegisterSyncAPI(app, cfg)

	// U20b: Markdown-native workspace indexing
	RegisterWorkspaceAPI(app, cfg)

	// U21: Search feedback
	RegisterFeedbackAPI(app, cfg)

	// U22: Ontology upload (already registered via RegisterAPI for ontology list/upload)

	// U18: Ontology management
	app.Post("/ontologies", ontologyUploadHandler(cfg))
	app.Get("/ontologies", ontologyListHandler(cfg))
	app.Delete("/ontologies/:id", ontologyDeleteHandler(cfg))

	// U10: RBAC — dataset sharing + permissions
	RegisterRBACAPI(app, cfg)

	// U9: Notebooks CRUD + cell execution
	app.Get("/notebooks", notebooksListHandler(cfg))
	app.Post("/notebooks", notebookCreateHandler(cfg))
	app.Get("/notebooks/:id", notebookGetHandler(cfg))
	app.Put("/notebooks/:id", notebookUpdateHandler(cfg))
	app.Delete("/notebooks/:id", notebookDeleteHandler(cfg))
	app.Post("/notebooks/:id/cells", cellAddHandler(cfg))
	app.Put("/notebooks/:id/cells/:cellId", cellUpdateHandler(cfg))
	app.Delete("/notebooks/:id/cells/:cellId", cellDeleteHandler(cfg))
	app.Post("/notebooks/:id/cells/:cellId/run", cellRunHandler(cfg))
	app.Post("/notebooks/:id/:cellId/run", cellRunHandler(cfg)) // Levara frontend compat

	// U5: Levara search (separate from legacy vector /search)
	app.Post("/search/text", searchHandler(cfg))
	app.Post("/search/", searchHandler(cfg)) // Levara frontend compat alias
	app.Post("/search", searchHandler(cfg))  // without trailing slash

	// U6: Heartbeat event log (system activity history)
	app.Get("/heartbeats", heartbeatsHandler(cfg))

	// T5: Graph path/traversal — shortest-path edges with as_of + cursor.
	app.Get("/graph/path", graphPathHandler(cfg))
}

// ── U1: Health ──
// Already inline above.
