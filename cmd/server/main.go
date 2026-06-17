// Levara server — HTTP + gRPC vector database with HNSW indexing and WAL durability.
//
// Usage:
//
//	# Standalone mode (default) — WAL-only, no Raft consensus
//	./server -standalone -dim 1024 -shards 3 -port 8080 -grpc-port 50051 -data-dir /data
//
//	# Raft consensus mode (multi-node)
//	./server -bootstrap -standalone=false -dim 1024
//
// Flags:
//
//	-dim           Vector dimension (default 128; must match embedding model output)
//	-port          HTTP API port (default 8080)
//	-grpc-port     gRPC API port, 0 to disable (default 50051)
//	-shards        Number of independent HNSW shards (default 3)
//	-data-dir      Root directory for WAL and metadata files (default "data")
//	-standalone    Use WAL-only mode without Raft (default true)
//	-bootstrap     Bootstrap Raft cluster as leader (Raft mode only)
//	-hnsw-m        HNSW M: max neighbors per node (default 16)
//	-hnsw-ef-mult  HNSW efSearch multiplier: efSearch = k * mult (default 8)
//	-hnsw-ef-min   HNSW minimum efSearch value (default 64)
//
// HTTP endpoints (prefix /api/v1):
//
//	POST /insert           Insert a single record
//	POST /batch_insert     Insert multiple records in one fsync
//	POST /search           Vector similarity search
//	POST /delete           Delete a record by ID
//	GET  /metrics          Prometheus metrics
//
// The server handles SIGTERM and SIGINT with graceful shutdown: all WAL buffers
// are flushed and disk stores are closed before the process exits.
//
// Swagger / OpenAPI (T13): the swaggo annotations below generate
// docs/swagger.{json,yaml} via `make swag`. Swagger UI is mounted at
// /swagger/* — publicly in dev, admin-gated in prod. The /auth/* and /prune/*
// endpoints are deliberately annotated so security reviewers can diff the
// contract in CI.
//
// @title       Levara API
// @version     v1
// @description Levara HNSW + BM25 + Neo4j vector DB HTTP API.
// @description See CLAUDE.md for architecture; pkg/mcp for the MCP alternate transport.
// @BasePath    /api/v1
// @schemes     http https
// @securityDefinitions.apikey BearerAuth
// @in          header
// @name        Authorization
// @description "Bearer <jwt>" from POST /auth/login or an API key from POST /auth/keys.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"       // pgx via database/sql (binary protocol, prepared stmts)
	_ "github.com/ncruces/go-sqlite3/driver" // pure-Go SQLite driver (no CGO, ARM64 ready)
	"github.com/stek0v/levara/internal/cluster"
	vectorGrpc "github.com/stek0v/levara/internal/grpc"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/internal/store"

	_ "github.com/stek0v/levara/docs" // swaggo-generated OpenAPI spec (T13)
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/consolidate"
	"github.com/stek0v/levara/pkg/embcontract"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/llmcache"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/observe"
	"github.com/stek0v/levara/pkg/profile"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/runreg"

	"github.com/gofiber/fiber/v2"

	vectorHttp "github.com/stek0v/levara/internal/http"
)

// Build metadata injected via -ldflags "-X main.GitSHA=… -X main.BuildTime=…".
// Defaults make the unset case ("go run") obvious instead of empty strings.
var (
	GitSHA    = "dev"
	BuildTime = "unknown"
)

const mcpProtocolVersion = "2024-11-05"

func versionPayload() fiber.Map {
	return fiber.Map{
		"version":    GitSHA,
		"build_time": BuildTime,
		"go_version": runtime.Version(),
		"protocol_versions": fiber.Map{
			"grpc": []string{"v1", "v2"},
			"mcp":  mcpProtocolVersion,
		},
	}
}

func workspaceWatchEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LEVARA_WORKSPACE_WATCH"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func workspaceIndexWorkerEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LEVARA_WORKSPACE_INDEX_WORKER"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func workspaceWatchAsyncIndexEnabled() bool {
	v := strings.TrimSpace(os.Getenv("LEVARA_WORKSPACE_WATCH_ASYNC_INDEX"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err == nil && d > 0 {
		return d
	}
	n, err := strconv.Atoi(v)
	if err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// firstNonEmpty returns the first non-empty string (for flag/env fallback pattern).
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "mcp" {
		if err := runMCPStdio(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "mcp:", err)
			os.Exit(1)
		}
		return
	}
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the Raft cluster (Leader only)")
	standalone := flag.Bool("standalone", true, "Standalone mode: WAL-only, no Raft consensus (fastest)")
	dim := flag.Int("dim", 128, "Vector dimension size (must match embedding model output)")
	port := flag.Int("port", 8080, "HTTP API port")
	numShardsFlag := flag.Int("shards", 3, "Number of shards")
	defaultDataDir := "data"
	if envDir := strings.TrimSpace(os.Getenv("LEVARA_DATA_DIR")); envDir != "" {
		defaultDataDir = envDir
	}
	dataDir := flag.String("data-dir", defaultDataDir, "Directory for persistent data storage (overrides $LEVARA_DATA_DIR)")
	grpcPort := flag.Int("grpc-port", 50051, "gRPC API port (0 to disable)")
	hnswM := flag.Int("hnsw-m", 16, "HNSW M parameter: max neighbors per node")
	hnswEfMult := flag.Int("hnsw-ef-mult", 8, "HNSW efSearch multiplier: efSearch = k * this value")
	hnswEfMin := flag.Int("hnsw-ef-min", 64, "HNSW minimum efSearch value")
	llmProxyPort := flag.Int("llm-proxy-port", 0, "LLM proxy port (0 to disable)")
	llmUpstream := flag.String("llm-upstream", "", "LLM upstream URL (e.g. http://localhost:11434/v1)")
	llmCacheSize := flag.Int("llm-cache-size", 10000, "LLM response cache max entries")
	llmMaxInflight := flag.Int("llm-max-inflight", 10, "Max concurrent LLM requests")
	neo4jURL := flag.String("neo4j-url", "", "Neo4j bolt URL for graph visualization (e.g. bolt://localhost:7687)")
	neo4jUser := flag.String("neo4j-user", "neo4j", "Neo4j username")
	neo4jPassword := flag.String("neo4j-password", "", "Neo4j password")
	neo4jDatabase := flag.String("neo4j-database", "neo4j", "Neo4j database name")
	requireAuth := flag.Bool("require-auth", false, "Require JWT auth on protected endpoints (default: dev mode, no auth)")
	nodeIDFlag := flag.String("node-id", "", "Unique node identifier (default: hostname or 'node1')")
	raftAddr := flag.String("raft-addr", "127.0.0.1", "Raft bind address (e.g. 0.0.0.0 or 10.23.0.53)")
	raftPortBase := flag.Int("raft-port", 9000, "Base port for Raft (shard N listens on base+N)")
	joinAddr := flag.String("join-addr", "", "Primary node address to join as replica (e.g. 10.23.0.53:8080)")
	mcpAuditPath := flag.String("mcp-audit-log", "", "Directory for daily-rolled MCP audit logs (empty = stderr; '-' disables)")
	configCheck := flag.Bool("config-check", false, "Validate the resolved profile/config from env + flags and exit (no listeners, no DB, no network)")
	embedKeepalive := flag.String("embed-keepalive-interval", "10m", "Embed keep-alive ping interval (0 or negative to disable, e.g. 5m, 30m). Default 10m — prevents Ollama/vLLM eviction")
	embedEndpointF := flag.String("embed-endpoint", os.Getenv("EMBEDDING_ENDPOINT"), "Embedding API endpoint URL (falls back to $EMBEDDING_ENDPOINT)")
	embedModelF := flag.String("embed-model", firstNonEmpty(os.Getenv("EMBEDDING_MODEL"), "text-embedding-3-small"), "Embedding model name (falls back to $EMBEDDING_MODEL)")
	embedRequire := flag.Bool("embed-require", false, "Fail startup if embedding endpoint is unreachable")
	pgURL := flag.String("pg-url", os.Getenv("DATABASE_URL"), "PostgreSQL connection URL (falls back to $DATABASE_URL)")
	profileName := flag.String("profile", "", "Functional profile: standalone, standalone-embed, or full (default)")

	flag.Parse()

	// Build a set of explicitly-provided flags so profile doesn't overwrite them.
	provided := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	// Apply functional profile — pre-sets flags for common use cases.
	// Explicit flags always override profile defaults (checked via `provided`).
	switch *profileName {
	case "standalone":
		if !provided["grpc-port"] {
			*grpcPort = 0
		}
		if !provided["require-auth"] {
			*requireAuth = false
		}
		if !provided["raft-port"] {
			*raftPortBase = 0
		}
		if !provided["bootstrap"] {
			*bootstrap = false
		}
		if !provided["join-addr"] {
			*joinAddr = ""
		}
		if !provided["llm-proxy-port"] {
			*llmProxyPort = 0
		}
		if !provided["neo4j-url"] {
			*neo4jURL = ""
		}
		if !provided["pg-url"] {
			*pgURL = ""
		}
		if !provided["embed-endpoint"] {
			*embedEndpointF = ""
		}
		log.Printf("Profile: standalone — Raft, gRPC, Neo4j, PG, LLM, auth, embed disabled")
	case "standalone-embed":
		if !provided["grpc-port"] {
			*grpcPort = 0
		}
		if !provided["require-auth"] {
			*requireAuth = false
		}
		if !provided["raft-port"] {
			*raftPortBase = 0
		}
		if !provided["bootstrap"] {
			*bootstrap = false
		}
		if !provided["join-addr"] {
			*joinAddr = ""
		}
		if !provided["llm-proxy-port"] {
			*llmProxyPort = 0
		}
		if !provided["neo4j-url"] {
			*neo4jURL = ""
		}
		if !provided["pg-url"] {
			*pgURL = ""
		}
		log.Printf("Profile: standalone-embed — as standalone, embed left enabled")
	case "full", "":
		// all flags available (default)
	default:
		log.Fatalf("Unknown profile %q — valid: standalone, standalone-embed, full", *profileName)
	}

	// Dry-run config validation: resolve and check the runtime profile, then
	// exit. Runs before any external init (storage, vector, SQL, listeners) so
	// an operator — or `make profile-smoke` — can verify a profile preset with
	// no services up. Exit 0 = acceptable, 1 = strict-mode fatal.
	if *configCheck {
		os.Exit(runConfigCheck(os.Stdout, *requireAuth, *mcpAuditPath, truthyEnv("LEVARA_PROFILE_STRICT")))
	}

	// ---------------------------------------------------------------
	// Structured logging + error tracker (P3.4)
	// ---------------------------------------------------------------
	srvLog := observe.NewLogger("server")
	errTracker := observe.NewErrorTracker(200)

	// ---------------------------------------------------------------
	// Storage backend (P3.5): STORAGE_BACKEND=local|s3
	// ---------------------------------------------------------------
	fileStore, storageBackend, storagePath := initStorageBackend(*dataDir, srvLog)

	hnswCfg := store.HNSWConfig{
		M:            *hnswM,
		M0:           *hnswM * 2,
		EfSearchMult: *hnswEfMult,
		EfSearchMin:  *hnswEfMin,
		LevelMult:    1.0 / 0.69,
	}

	nodeID := *nodeIDFlag
	if nodeID == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			nodeID = h
		} else {
			nodeID = "node1"
		}
	}
	basePort := *raftPortBase
	numShards := *numShardsFlag

	if *standalone {
		log.Printf("Levara standalone mode (WAL-only, no Raft)")
	} else {
		log.Printf("Levara Raft consensus mode")
	}

	vectorRuntime := initVectorRuntime(vectorRuntimeConfig{
		Dim:        *dim,
		DataDir:    *dataDir,
		NodeID:     nodeID,
		NumShards:  numShards,
		Standalone: *standalone,
		Bootstrap:  *bootstrap,
		RaftAddr:   *raftAddr,
		RaftPort:   basePort,
		JoinAddr:   *joinAddr,
		HNSW:       hnswCfg,
	})
	shards := vectorRuntime.Shards
	c := vectorRuntime.Cluster
	replServer := vectorRuntime.Replication
	replDB := replicationDB(shards)

	httpRuntime := initHTTPRuntime(c, *dim, replServer, errTracker)
	app := httpRuntime.App
	api := httpRuntime.API
	handler := httpRuntime.VectorHandler

	// Neo4j schema bootstrap is one-time at startup. Per-request handlers
	// should not execute DDL for latency and side-effect reasons.
	if *neo4jURL != "" && shouldBootstrapNeo4jSchema(os.Getenv("NEO4J_BOOTSTRAP_SCHEMA")) {
		neoCtx, neoCancel := context.WithTimeout(context.Background(), 10*time.Second)
		w, neoErr := graphdb.NewWriterWithSchema(neoCtx, *neo4jURL, *neo4jUser, *neo4jPassword, *neo4jDatabase)
		if neoErr != nil {
			log.Printf("[startup] neo4j schema bootstrap skipped: %v", neoErr)
		} else {
			_ = w.Close(neoCtx)
			log.Printf("[startup] neo4j schema bootstrap complete")
		}
		neoCancel()
	} else if *neo4jURL != "" {
		log.Printf("[startup] neo4j schema bootstrap disabled (NEO4J_BOOTSTRAP_SCHEMA=%q)", os.Getenv("NEO4J_BOOTSTRAP_SCHEMA"))
	}

	// Graph visualization config (used by both public and protected routes)
	vizCfg := vectorHttp.GraphVisualizationConfig{
		Neo4jURL: *neo4jURL, Neo4jUser: *neo4jUser,
		Neo4jPassword: *neo4jPassword, Neo4jDatabase: *neo4jDatabase,
		// DB set after pgDB init below
	}

	// api.Get("/visualize", ...) — moved below after DB init to include cfg.DB

	// Initialize CollectionManager for native collections (used by gRPC + the
	// collection-aware HTTP write/search/delete paths).
	colManager, err := store.NewCollectionManager(*dim, *dataDir+"/"+nodeID, hnswCfg)
	if err != nil {
		log.Fatalf("Failed to init CollectionManager: %v", err)
	}
	// Wire CollectionManager into the HTTP handler so requests carrying a
	// `collection` field route to the per-tenant HNSW stack instead of the
	// shared cluster store. Requests without `collection` keep using cluster
	// for backward compatibility.
	handler.SetCollections(colManager)

	sqlRuntime := initSQLRuntime(*dataDir, *pgURL)
	pgDSN := sqlRuntime.DSN
	pgDB := sqlRuntime.DB
	profileStrict := truthyEnv("LEVARA_PROFILE_STRICT")
	if enforceRuntimeProfile(srvLog, buildRuntimeProfileConfig(pgDB, *requireAuth, *mcpAuditPath), profileStrict) {
		srvLog.Error("runtime_profile_strict_fatal", nil, map[string]any{
			"profile": profile.Normalize(os.Getenv("LEVARA_PROFILE")),
			"hint":    "fix the profile requirements above or unset LEVARA_PROFILE_STRICT to start in warn-only mode",
		})
		os.Exit(1)
	}
	vizCfg.DB = pgDB // PostgreSQL/SQLite fallback for graph visualization
	api.Get("/visualize", vectorHttp.VisualizeHTML(&vizCfg))
	if pgDB != nil {
		log.Printf("Graph visualization: SQL fallback enabled")
	}

	// One-shot raw-data location backfill for non-local storage backends.
	// Migrates data.raw_data_location from file://... to storage://... keys so
	// dataset raw downloads and cognify reads can use shared object storage.
	if pgDB != nil && storageBackend != "" && storageBackend != "local" {
		backfillLimit := 5000
		if v := os.Getenv("STORAGE_BACKFILL_LIMIT"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				backfillLimit = n
			}
		}
		backfillTimeout := 10 * time.Minute
		if v := os.Getenv("STORAGE_BACKFILL_TIMEOUT_SECONDS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				backfillTimeout = time.Duration(n) * time.Second
			}
		}
		backfillCtx, backfillCancel := context.WithTimeout(context.Background(), backfillTimeout)
		report, backfillErr := vectorHttp.BackfillRawLocationsToStorage(backfillCtx, vectorHttp.APIConfig{
			DB:          pgDB,
			FileStorage: fileStore,
		}, backfillLimit)
		backfillCancel()
		if backfillErr != nil {
			srvLog.Warn("storage backfill failed", map[string]any{"error": backfillErr.Error()})
		} else if report.Scanned > 0 {
			srvLog.Info("storage backfill completed", map[string]any{
				"scanned":  report.Scanned,
				"migrated": report.Migrated,
				"skipped":  report.Skipped,
				"missing":  report.Missing,
				"failed":   report.Failed,
				"limit":    backfillLimit,
			})
		}
	}

	embedEndpoint := *embedEndpointF
	embedModel := *embedModelF
	if embedModel == "" {
		embedModel = "text-embedding-3-small"
	}
	// Resolve keep-alive interval from flag; 0 or negative disables it.
	keepaliveDur, err := time.ParseDuration(*embedKeepalive)
	if err != nil || keepaliveDur <= 0 {
		if *embedKeepalive != "0" {
			log.Printf("embed-keepalive-interval: invalid or disabled (%q), keep-alive off", *embedKeepalive)
		}
		keepaliveDur = 0
	}
	// Embed-require: fail startup if embed endpoint is configured but unreachable.
	if *embedRequire && embedEndpoint != "" {
		ec := embed.NewClient(embedEndpoint, embedModel, 1, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, perr := ec.EmbedSingle(ctx, "startup-check"); perr != nil {
			log.Fatalf("embed-require: embedding endpoint unreachable: %v", perr)
		}
		log.Printf("embed-require: embedding endpoint reachable (%s/%s)", embedEndpoint, embedModel)
	}
	// Stamp the configured embedder onto collections auto-created by the lazy
	// Insert path (e.g. _memories_* sidecars from a memory write) so they don't
	// inherit empty embedding_model metadata (findings P2.1).
	colManager.SetDefaultModel(embedModel)
	colManager.SetDefaultEmbeddingContract(embcontract.FromEnv(embedModel, *dim, "cosine"))

	// Auth endpoints (public — no JWT required)
	jwtSecret := os.Getenv("JWT_SECRET")
	authCfg := &vectorHttp.AuthConfig{
		PostgresDSN: pgDSN,
		JWTSecret:   jwtSecret,
		DB:          pgDB,
	}
	vectorHttp.RegisterAuthAPI(api, authCfg) // may generate JWTSecret if empty

	// Inject DB for API key verification (used by JWTMiddleware).
	// IMPORTANT: Wrap in DBRef to prevent fasthttp from calling Close() on it.
	// fasthttp calls io.Closer.Close() on all c.Locals values when the request
	// context is recycled — storing *sql.DB directly kills the connection pool.
	authDB := &vectorHttp.DBRef{DB: pgDB}
	api.Use(func(c *fiber.Ctx) error {
		c.Locals("auth_db", authDB)
		return c.Next()
	})

	// JWT + API Key middleware on all protected routes below this point
	api.Use(vectorHttp.JWTMiddleware(authCfg.JWTSecret, *requireAuth))

	// Per-user rate limit (T2 / D10): 100 req/min keyed on the user_id resolved
	// by JWTMiddleware above, falling back to source IP for anonymous paths.
	// Must come AFTER JWTMiddleware so c.Locals("user_id") is populated.
	//
	// Tunable via env so the integrated stack (where the MemoryFS indexer
	// hammers /api/v1/batch_insert from a single source IP) can lift the cap
	// without recompiling. Empty/invalid → defaults (100 req/min).
	rlCfg := vectorHttp.RateLimitConfig{}
	if v := os.Getenv("RATE_LIMIT_USER_MAX"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rlCfg.UserMax = n
		}
	}
	if v := os.Getenv("RATE_LIMIT_USER_WINDOW_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rlCfg.UserWindow = time.Duration(n) * time.Second
		}
	}
	api.Use(vectorHttp.UserRateLimiter(rlCfg))

	// Per-endpoint Prometheus instrumentation with bounded user_id
	// cardinality (T17 / D14). UserBucket promotes the top-50 active users
	// to real labels and buckets the long tail into "other"; refreshed
	// every minute so a burst can't permanently pin a user.
	userBucket := metrics.NewUserBucket(50, time.Minute)
	defer userBucket.Stop()
	api.Use(vectorHttp.PromInstrumentationMiddleware("api", userBucket))

	// Tenant isolation middleware (resolves tenant from user or X-Tenant-Id header)
	api.Use(vectorHttp.TenantMiddleware(vectorHttp.AccessConfig{DB: pgDB}))

	// API key management (requires auth)
	vectorHttp.RegisterAPIKeyEndpoints(api, *authCfg)

	// Protected routes: vector ops
	api.Post("/insert", handler.Insert)
	api.Post("/batch_insert", handler.BatchInsert)
	api.Post("/search", handler.Search)
	api.Post("/delete", handler.Delete)
	api.Get("/datasets/:id/graph", vectorHttp.DatasetGraph(vizCfg))

	// Create gRPC service (shared between gRPC server and HTTP handlers for BM25 indexes)
	grpcSvc := vectorGrpc.NewService(colManager, c, *dim)

	// LLM response cache — eliminates redundant LLM calls for identical inputs
	// PersistentCache: survives restarts via append-only JSONL file
	cachePath := *dataDir + "/llm_cache.jsonl"
	var llmCache llmcache.LLMCacher
	if pc, err := llmcache.NewPersistent(10000, cachePath); err != nil {
		log.Printf("LLM cache persistence failed (%v), using in-memory only", err)
		llmCache = llmcache.New(10000, 0)
	} else {
		llmCache = pc
		defer pc.Close()
	}

	// LLM provider + Langfuse + outbound rate-limit are all driven by env
	// vars; bootstrap.go owns the wiring so this file stays scannable.
	llmProvider := initLLMProvider()

	// Adaptive router weights (feedback-driven learning)
	adaptiveWeights := router.NewAdaptiveWeights(pgDB, 0.1)

	// Shared background-pipeline registry. Must be the same *runreg.Registry
	// for both REST and MCP so a cognify run started via /api/v1/cognify is
	// visible to MCP's cognify_status, and vice versa.
	runs := runreg.New()
	// Background janitor: evict terminal (COMPLETED / FAILED) runs older
	// than 1h every 10m. Without this the registry grows unbounded for the
	// lifetime of the process (M3 from the 20.04 review). Active RUNNING
	// entries are always kept — a stuck run is a bug we want to preserve.
	runsJanitorStop := runs.StartJanitor(10*time.Minute, time.Hour)
	defer runsJanitorStop()

	// Shared embedding client (T3). Replaces 20+ per-request embed.NewClient()
	// calls in http handlers — reuses one TCP pool, saves ~10–50ms per request.
	// Handlers needing non-default params (custom endpoint/model/batchSize, e.g.
	// reembed migration, per-collection dual-search, gRPC request-driven params)
	// still construct their own client.
	sharedEmbed := embed.NewClient(embedEndpoint, embedModel, 16, 3)
	grpcSvc.SetEmbedDefaults(sharedEmbed, embedEndpoint, embedModel)

	// Shared search-strategy registry (T5) — owned by main so tests can
	// substitute strategies without touching NewDefaultStrategyRegistry.
	searchStrategies := vectorHttp.NewDefaultStrategyRegistry()
	rerankCfg := rerankConfigFromEnv()
	workspaceWatcher := vectorHttp.NewWorkspaceWatchState()

	// Optional enterprise audit-export adapter (Phase 4A). When enabled it sits
	// in the WorkspaceAuditSink slot the workspace handlers already mirror
	// sanitized events into — pluggable without editing those handlers. Shared
	// (concurrency-safe) across the REST and MCP configs; drained on shutdown.
	var wsAuditSink audit.EventSink
	if wsAuditExporter := initWorkspaceAuditExporter(*dataDir, srvLog); wsAuditExporter != nil {
		wsAuditSink = wsAuditExporter
		defer wsAuditExporter.Close()
	}

	apiCfg := vectorHttp.APIConfig{
		PostgresDSN:             pgDSN,
		StoragePath:             *dataDir + "/uploads",
		WorkspacePath:           *dataDir + "/workspace",
		JWTSecret:               authCfg.JWTSecret,
		RequireAuth:             *requireAuth,
		Version:                 GitSHA,
		SyncToken:               os.Getenv("LEVARA_TOKEN"),
		WorkspaceWatcher:        workspaceWatcher,
		EmbedEndpoint:           embedEndpoint,
		EmbedModel:              embedModel,
		EmbedClient:             sharedEmbed,
		Collections:             colManager,
		Neo4jCfg:                vizCfg,
		DB:                      pgDB,
		BM25Indexes:             grpcSvc.BM25Indexes(),
		LLMCache:                llmCache,
		LLMProvider:             llmProvider,
		ErrorTracker:            errTracker,
		FileStorage:             fileStore,
		Logger:                  srvLog,
		RerankEndpoint:          rerankCfg.Endpoint,
		RerankModel:             rerankCfg.Model,
		RerankTimeoutMs:         rerankCfg.TimeoutMs,
		RerankBudgetMs:          rerankCfg.BudgetMs,
		RerankScoreGapThreshold: rerankCfg.ScoreGapThreshold,
		AdaptiveWeights:         adaptiveWeights,
		Runs:                    runs,
		SearchStrategies:        searchStrategies,
		WorkspaceAuditSink:      wsAuditSink,
	}

	// Protected routes: Levara API (datasets, upload, cognify, search)
	vectorHttp.RegisterAPI(api, apiCfg)

	mcpCfg := vectorHttp.APIConfig{
		EmbedEndpoint:           embedEndpoint,
		EmbedModel:              embedModel,
		EmbedClient:             sharedEmbed,
		WorkspacePath:           *dataDir + "/workspace",
		JWTSecret:               authCfg.JWTSecret,
		RequireAuth:             *requireAuth,
		Version:                 GitSHA,
		SyncToken:               os.Getenv("LEVARA_TOKEN"),
		WorkspaceWatcher:        workspaceWatcher,
		Collections:             colManager,
		DB:                      pgDB,
		BM25Indexes:             grpcSvc.BM25Indexes(),
		LLMCache:                llmCache,
		LLMProvider:             llmProvider,
		RerankEndpoint:          rerankCfg.Endpoint,
		RerankModel:             rerankCfg.Model,
		RerankTimeoutMs:         rerankCfg.TimeoutMs,
		RerankBudgetMs:          rerankCfg.BudgetMs,
		RerankScoreGapThreshold: rerankCfg.ScoreGapThreshold,
		Runs:                    runs,
		MCPAudit:                initMCPAuditSink(*mcpAuditPath, srvLog),
		MCPAgentBucket:          metrics.NewUserBucket(20, time.Minute),
		WorkspaceAuditSink:      wsAuditSink,
	}

	// MCP (Model Context Protocol) server — JSON-RPC 2.0 for AI agent integration
	vectorHttp.RegisterMCPAPI(app, mcpCfg)

	// Opt-in background memory-consolidation janitor. Off unless
	// CONSOLIDATION_INTERVAL is a positive Go duration (e.g. "30m"). Sweeps
	// every non-internal collection against its own _memories_<c> sidecar.
	if v := os.Getenv("CONSOLIDATION_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			// DefaultConfig.MaxLLMCalls is per-collection; this caps total LLM
			// calls across the whole sweep (0/unset = unbounded).
			sweepBudget := intEnv("CONSOLIDATION_MAX_LLM_CALLS_PER_SWEEP", 0)
			stop := consolidate.StartJanitor(context.Background(), mcp.NewConsolidationRunner(vectorHttp.NewMCPDeps(mcpCfg), sweepBudget), d)
			defer stop()
			log.Printf("consolidation janitor enabled (interval=%s, max_llm_calls_per_sweep=%d)", d, sweepBudget)
		} else {
			log.Printf("CONSOLIDATION_INTERVAL=%q invalid; janitor disabled", v)
		}
	}

	if workspaceIndexWorkerEnabled() {
		stopWorkspaceIndexWorker := vectorHttp.StartWorkspaceIndexWorker(context.Background(), apiCfg, vectorHttp.WorkspaceIndexWorkerOptions{
			Interval:    durationEnv("LEVARA_WORKSPACE_INDEX_WORKER_INTERVAL", 2*time.Second),
			Backoff:     durationEnv("LEVARA_WORKSPACE_INDEX_WORKER_BACKOFF", 5*time.Second),
			MaxAttempts: intEnv("LEVARA_WORKSPACE_INDEX_WORKER_MAX_ATTEMPTS", 3),
		})
		defer stopWorkspaceIndexWorker()
		log.Printf("workspace index worker enabled for %s", apiCfg.WorkspacePath)
	}

	if workspaceWatchEnabled() {
		stopWorkspaceWatcher := vectorHttp.StartWorkspaceWatcher(context.Background(), apiCfg, vectorHttp.WorkspaceWatchOptions{
			Interval:      durationEnv("LEVARA_WORKSPACE_WATCH_INTERVAL", 2*time.Second),
			Debounce:      durationEnv("LEVARA_WORKSPACE_WATCH_DEBOUNCE", 1500*time.Millisecond),
			ChunkStrategy: os.Getenv("LEVARA_WORKSPACE_WATCH_CHUNK_STRATEGY"),
			MinChunkChars: intEnv("LEVARA_WORKSPACE_WATCH_MIN_CHARS", 0),
			MaxChunkChars: intEnv("LEVARA_WORKSPACE_WATCH_MAX_CHARS", 0),
			AsyncIndex:    workspaceWatchAsyncIndexEnabled(),
		})
		defer stopWorkspaceWatcher()
		log.Printf("workspace watcher enabled for %s", apiCfg.WorkspacePath)
		if workspaceWatchAsyncIndexEnabled() {
			log.Printf("workspace watcher async indexing enabled; start LEVARA_WORKSPACE_INDEX_WORKER=1 to process queued jobs")
		}
	}

	// Cache stats endpoint
	api.Get("/cache/stats", func(c *fiber.Ctx) error {
		return c.JSON(llmCache.Stats())
	})
	log.Printf("MCP server registered at POST /mcp (7 tools)")

	// Detailed /health/details with per-dependency probes lives in
	// bootstrap.go.
	registerHealthDetails(app, healthDeps{
		port:           *port,
		grpcPort:       *grpcPort,
		dim:            *dim,
		dbProvider:     string(vectorHttp.GetDBProvider()),
		storageBackend: storageBackend,
		storagePath:    storagePath,
		pgDB:           pgDB,
		neo4jURL:       *neo4jURL,
		neo4jUser:      *neo4jUser,
		neo4jPassword:  *neo4jPassword,
		neo4jDatabase:  *neo4jDatabase,
		embedEndpoint:  embedEndpoint,
		embedModel:     embedModel,
		rerankEndpoint: rerankCfg.Endpoint,
		rerankModel:    rerankCfg.Model,
		llmProvider:    llmProvider,
		colManager:     colManager,
	})

	// gRPC server (v1 + v2) starts in a goroutine. nil when disabled.
	grpcServer := startGRPCServer(*grpcPort, authCfg.JWTSecret, *requireAuth, grpcSvc)

	// Optional LLM proxy on its own port.
	stopProxy := startLLMProxyIfConfigured(*llmProxyPort, *llmUpstream, *dataDir, nodeID, *llmCacheSize, *llmMaxInflight)
	defer stopProxy()

	// Start replica client if joining a primary
	if *joinAddr != "" && replServer != nil && replDB != nil {
		replicaClient := cluster.NewReplicaClient(*joinAddr, nodeID, replDB, colManager)
		go func() {
			if err := replicaClient.Start(context.Background()); err != nil {
				log.Printf("replica start error: %v", err)
			}
		}()
	}

	addr := fmt.Sprintf(":%d", *port)
	mode := "standalone/WAL"
	if !*standalone {
		mode = "raft"
	}
	if *joinAddr != "" {
		mode = "replica"
	}
	log.Printf("Levara listening on HTTP:%d gRPC:%d (dim=%d, shards=%d, mode=%s, node=%s)", *port, *grpcPort, *dim, numShards, mode, nodeID)

	// Graceful shutdown — see bootstrap.go for the full close ordering.
	installGracefulShutdown(app, shards, colManager, pgDB, grpcServer)

	// Embed model keep-alive ticker (Ollama eviction defence).
	startEmbedKeepAlive(embedEndpoint, embedModel, keepaliveDur)

	log.Fatal(app.Listen(addr))
}
