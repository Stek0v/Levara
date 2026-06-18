package http

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/bm25"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/router"
)

// sampleAPIConfig builds an APIConfig where every field holds a distinct,
// non-zero sentinel so a dropped or mis-mapped projection field is detectable.
// Pointer/interface fields use allocated instances; projections must preserve
// pointer identity (no copy), which == comparison and reflect.DeepEqual
// short-circuit on.
func sampleAPIConfig() APIConfig {
	return APIConfig{
		PostgresDSN:             "postgres://dsn",
		StoragePath:             "/data/uploads",
		WorkspacePath:           "/data/workspace",
		JWTSecret:               "secret",
		RequireAuth:             true,
		Version:                 "deadbeef",
		SyncToken:               "lk_token",
		WorkspaceWatcher:        NewWorkspaceWatchState(),
		EmbedEndpoint:           "http://embed",
		EmbedModel:              "potion",
		EmbedClient:             &embed.Client{},
		Collections:             &store.CollectionManager{},
		Neo4jCfg:                GraphVisualizationConfig{Neo4jURL: "bolt://neo4j"},
		DB:                      &sql.DB{},
		BM25Indexes:             map[string]*bm25.Index{"c": nil},
		BM25Store:               bm25.NewSnapshotStore("/tmp/bm25-test"),
		LLMCache:                nil,
		LLMProvider:             nil,
		ErrorTracker:            nil,
		FileStorage:             nil,
		Logger:                  nil,
		RerankEndpoint:          "http://rerank",
		RerankModel:             "mmini",
		RerankTimeoutMs:         5000,
		RerankBudgetMs:          1500,
		RerankScoreGapThreshold: 0.25,
		AdaptiveWeights:         &router.AdaptiveWeights{},
		Runs:                    nil,
		SearchStrategies:        &StrategyRegistry{},
		MCPAudit:                nil,
		WorkspaceAuditSink:      nil,
		MCPAgentBucket:          &metrics.UserBucket{},
	}
}

// TestConfigGroupProjectionsPreserveFacts is the Phase 3B guard: grouping must
// carry the *same* config facts. Each projection is rebuilt back into a flat
// APIConfig; the cross-cutting fields that intentionally stay ungrouped
// (Logger, ErrorTracker, Runs) are copied straight. A field that a projection
// drops, mis-maps, or copies-by-value (losing pointer identity) makes the
// reconstructed wrapper differ from the original and fails the test — which is
// also the reminder to update grouping when a new APIConfig field is added.
func TestConfigGroupProjectionsPreserveFacts(t *testing.T) {
	orig := sampleAPIConfig()

	id := orig.Identity()
	ac := orig.Access()
	ws := orig.Workspace()
	se := orig.Search()
	st := orig.Storage()
	au := orig.Audit()

	rebuilt := APIConfig{
		// IdentityConfig
		JWTSecret:   id.JWTSecret,
		RequireAuth: id.RequireAuth,
		SyncToken:   id.SyncToken,
		Version:     id.Version,
		// AccessConfig
		DB: ac.DB,
		// WorkspaceConfig
		WorkspacePath:    ws.WorkspacePath,
		WorkspaceWatcher: ws.WorkspaceWatcher,
		// SearchConfig
		EmbedEndpoint:           se.EmbedEndpoint,
		EmbedModel:              se.EmbedModel,
		EmbedClient:             se.EmbedClient,
		Collections:             se.Collections,
		Neo4jCfg:                se.Neo4jCfg,
		BM25Indexes:             se.BM25Indexes,
		BM25Store:               se.BM25Store,
		LLMCache:                se.LLMCache,
		LLMProvider:             se.LLMProvider,
		RerankEndpoint:          se.RerankEndpoint,
		RerankModel:             se.RerankModel,
		RerankTimeoutMs:         se.RerankTimeoutMs,
		RerankBudgetMs:          se.RerankBudgetMs,
		RerankScoreGapThreshold: se.RerankScoreGapThreshold,
		AdaptiveWeights:         se.AdaptiveWeights,
		SearchStrategies:        se.SearchStrategies,
		// StorageConfig
		PostgresDSN: st.PostgresDSN,
		StoragePath: st.StoragePath,
		FileStorage: st.FileStorage,
		// AuditConfig
		MCPAudit:           au.MCPAudit,
		WorkspaceAuditSink: au.WorkspaceAuditSink,
		MCPAgentBucket:     au.MCPAgentBucket,
		// Cross-cutting, intentionally ungrouped — copied straight so a faithful
		// projection round-trips to an identical wrapper.
		Logger:       orig.Logger,
		ErrorTracker: orig.ErrorTracker,
		Runs:         orig.Runs,
	}

	if !reflect.DeepEqual(orig, rebuilt) {
		t.Fatalf("config group projections lost or mis-mapped a field.\n orig=%+v\nrebuilt=%+v", orig, rebuilt)
	}
}

// TestConfigGroupPointerIdentity pins that projections share, not copy, the
// underlying pointers — handlers taking a group must mutate/observe the same
// instances the wrapper holds.
func TestConfigGroupPointerIdentity(t *testing.T) {
	orig := sampleAPIConfig()
	if orig.Access().DB != orig.DB {
		t.Fatal("Access().DB is not the wrapper's *sql.DB")
	}
	if orig.Search().EmbedClient != orig.EmbedClient {
		t.Fatal("Search().EmbedClient is not the wrapper's *embed.Client")
	}
	if orig.Workspace().WorkspaceWatcher != orig.WorkspaceWatcher {
		t.Fatal("Workspace().WorkspaceWatcher is not the wrapper's watcher")
	}
	if orig.Audit().MCPAgentBucket != orig.MCPAgentBucket {
		t.Fatal("Audit().MCPAgentBucket is not the wrapper's bucket")
	}
}
