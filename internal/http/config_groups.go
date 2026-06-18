// config_groups.go — typed config groups for the Phase 3B layer split.
//
// APIConfig is still a broad service locator: ~30 fields threaded into every
// handler. Splitting it in one shot would touch 300+ `cfg.X` call sites and
// every `APIConfig{...}` literal. Instead we keep APIConfig flat as the
// compatibility wrapper and expose cohesive, transport-independent *projections*
// of it — one struct per concern (identity, access, workspace, search, storage,
// audit). New code (e.g. enterprise adapters) can accept the narrow group it
// needs instead of the whole locator; existing code keeps reading `cfg.X`
// unchanged. Migration is then field-by-field, never big-bang.
//
// Profile validation input is the seventh group; it lives at the bootstrap
// layer as profile.Config (cmd/server) because its facts are env/runtime
// derived, not a subset of APIConfig fields.
//
// Cross-cutting observability fields (Logger, ErrorTracker) intentionally stay
// ungrouped on the wrapper: they are used by every handler regardless of
// concern, so bundling them into one group would not narrow any adapter's
// surface.
//
// Rule for new code: accept the narrowest group that covers the concern. Use
// APIConfig only for compatibility registration code or handlers that genuinely
// combine multiple concerns.
package http

import (
	"database/sql"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/storage"
)

// IdentityConfig carries the authn/credential and instance-identity facts: the
// JWT signing secret, whether auth is required, the outbound sync bearer, and
// the build version surfaced in the sync manifest.
type IdentityConfig struct {
	JWTSecret   string
	RequireAuth bool
	SyncToken   string
	Version     string
}

// Identity projects the identity/credential facts out of the flat wrapper.
func (c APIConfig) Identity() IdentityConfig {
	return IdentityConfig{
		JWTSecret:   c.JWTSecret,
		RequireAuth: c.RequireAuth,
		SyncToken:   c.SyncToken,
		Version:     c.Version,
	}
}

// AccessConfig carries the shared SQL pool that access/policy decisions run
// against (users, tenants, dataset shares, ACL). A future access adapter needs
// only this, not the whole locator.
type AccessConfig struct {
	DB *sql.DB
}

// Access projects the access-policy data source out of the flat wrapper.
func (c APIConfig) Access() AccessConfig {
	return AccessConfig{DB: c.DB}
}

// WorkspaceConfig carries the workspace plane: the on-disk workspace root and
// the polling watcher whose status REST/MCP observability endpoints expose.
type WorkspaceConfig struct {
	WorkspacePath    string
	WorkspaceWatcher *WorkspaceWatchState
}

// Workspace projects the workspace-plane facts out of the flat wrapper.
func (c APIConfig) Workspace() WorkspaceConfig {
	return WorkspaceConfig{
		WorkspacePath:    c.WorkspacePath,
		WorkspaceWatcher: c.WorkspaceWatcher,
	}
}

// SearchConfig carries the retrieval/ranking/graph machinery: embedding client
// and defaults, per-collection HNSW manager, BM25 indexes, LLM cache/provider,
// reranker tuning, the adaptive router, the graph-visualization config, and the
// strategy registry.
type SearchConfig struct {
	EmbedEndpoint           string
	EmbedModel              string
	EmbedClient             *embed.Client
	Collections             *store.CollectionManager
	Neo4jCfg                GraphVisualizationConfig
	BM25Indexes             map[string]*bm25.Index
	BM25Store               *bm25.SnapshotStore
	LLMCache                llmcache.LLMCacher
	LLMProvider             llm.Provider
	RerankEndpoint          string
	RerankModel             string
	RerankTimeoutMs         int
	RerankBudgetMs          int
	RerankScoreGapThreshold float32
	AdaptiveWeights         *router.AdaptiveWeights
	SearchStrategies        *StrategyRegistry
}

// Search projects the retrieval/ranking machinery out of the flat wrapper.
func (c APIConfig) Search() SearchConfig {
	return SearchConfig{
		EmbedEndpoint:           c.EmbedEndpoint,
		EmbedModel:              c.EmbedModel,
		EmbedClient:             c.EmbedClient,
		Collections:             c.Collections,
		Neo4jCfg:                c.Neo4jCfg,
		BM25Indexes:             c.BM25Indexes,
		BM25Store:               c.BM25Store,
		LLMCache:                c.LLMCache,
		LLMProvider:             c.LLMProvider,
		RerankEndpoint:          c.RerankEndpoint,
		RerankModel:             c.RerankModel,
		RerankTimeoutMs:         c.RerankTimeoutMs,
		RerankBudgetMs:          c.RerankBudgetMs,
		RerankScoreGapThreshold: c.RerankScoreGapThreshold,
		AdaptiveWeights:         c.AdaptiveWeights,
		SearchStrategies:        c.SearchStrategies,
	}
}

// StorageConfig carries the persistence layer: the Postgres DSN, the local
// upload/ingest path, and the object-storage backend (local/S3).
type StorageConfig struct {
	PostgresDSN string
	StoragePath string
	FileStorage storage.Storage
}

// Storage projects the persistence-layer facts out of the flat wrapper.
func (c APIConfig) Storage() StorageConfig {
	return StorageConfig{
		PostgresDSN: c.PostgresDSN,
		StoragePath: c.StoragePath,
		FileStorage: c.FileStorage,
	}
}

// AuditConfig carries the audit boundary: the MCP tool-call sink, the optional
// generic workspace-audit event sink for enterprise export, and the agent_id
// cardinality bucket for audit metrics.
type AuditConfig struct {
	MCPAudit           audit.Sink
	WorkspaceAuditSink audit.EventSink
	MCPAgentBucket     *metrics.UserBucket
}

// Audit projects the audit-boundary facts out of the flat wrapper.
func (c APIConfig) Audit() AuditConfig {
	return AuditConfig{
		MCPAudit:           c.MCPAudit,
		WorkspaceAuditSink: c.WorkspaceAuditSink,
		MCPAgentBucket:     c.MCPAgentBucket,
	}
}
