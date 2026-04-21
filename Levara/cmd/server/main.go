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
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"path/filepath"

	"github.com/hashicorp/raft"
	_ "github.com/jackc/pgx/v5/stdlib"  // pgx via database/sql (binary protocol, prepared stmts)
	_ "github.com/ncruces/go-sqlite3/driver" // pure-Go SQLite driver (no CGO, ARM64 ready)
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/gofiber/swagger"
	"github.com/stek0v/cognevra/internal/cluster"
	vectorGrpc "github.com/stek0v/cognevra/internal/grpc"
	"github.com/stek0v/cognevra/internal/metrics"
	"github.com/stek0v/cognevra/internal/store"

	_ "github.com/stek0v/cognevra/docs" // swaggo-generated OpenAPI spec (T13)
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/observe"
	"github.com/stek0v/cognevra/pkg/router"
	"github.com/stek0v/cognevra/pkg/runreg"
	"github.com/stek0v/cognevra/pkg/storage"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"

	vectorHttp "github.com/stek0v/cognevra/internal/http"
)

func main() {
	bootstrap := flag.Bool("bootstrap", false, "Bootstrap the Raft cluster (Leader only)")
	standalone := flag.Bool("standalone", true, "Standalone mode: WAL-only, no Raft consensus (fastest)")
	dim := flag.Int("dim", 128, "Vector dimension size (must match embedding model output)")
	port := flag.Int("port", 8080, "HTTP API port")
	numShardsFlag := flag.Int("shards", 3, "Number of shards")
	dataDir := flag.String("data-dir", "data", "Directory for persistent data storage")
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

	flag.Parse()

	// ---------------------------------------------------------------
	// Structured logging + error tracker (P3.4)
	// ---------------------------------------------------------------
	srvLog := observe.NewLogger("server")
	errTracker := observe.NewErrorTracker(200)

	// ---------------------------------------------------------------
	// Storage backend (P3.5): STORAGE_BACKEND=local|s3
	// ---------------------------------------------------------------
	storagePath := *dataDir + "/uploads"
	fileStore, storeErr := storage.NewFromEnv(storagePath)
	if storeErr != nil {
		log.Fatalf("storage init: %v", storeErr)
	}
	srvLog.Info("storage backend ready", map[string]any{"backend": os.Getenv("STORAGE_BACKEND"), "path": storagePath})

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

	var shards []store.ShardHandler

	for i := range numShards {
		dbPath := fmt.Sprintf("%s/%s/shard_%d/meta.bin", *dataDir, nodeID, i)
		db, err := store.NewLevara(*dim, dbPath, hnswCfg)
		if err != nil {
			log.Fatal(err)
		}

		if *standalone {
			shards = append(shards, &cluster.DirectNode{DB: db}) // Repl set after replServer created
		} else {
			raftNode, err := cluster.NewRaftNode(i, nodeID, *dataDir+"/"+nodeID, basePort+i, db,
				cluster.WithBindAddr(*raftAddr))
			if err != nil {
				log.Fatal(err)
			}

			if *bootstrap {
				configuration := raft.Configuration{
					Servers: []raft.Server{
						{
							ID:      raft.ServerID(fmt.Sprintf("%s-shard-%d", nodeID, i)),
							Address: raft.ServerAddress(fmt.Sprintf("%s:%d", *raftAddr, basePort+i)),
						},
					},
				}
				raftNode.Raft.BootstrapCluster(configuration)
			}
			shards = append(shards, raftNode)
		}
	}

	c := store.NewCluster(shards)

	// ---------------------------------------------------------------
	// Replication setup
	// ---------------------------------------------------------------
	// Get a reference DB for replication (shard 0's underlying DB)
	var replDB *store.Levara
	if len(shards) > 0 {
		switch s := shards[0].(type) {
		case *cluster.DirectNode:
			replDB = s.DB
		case *cluster.RaftNode:
			replDB = s.DB
		}
	}

	var replServer *cluster.ReplicationServer
	if replDB != nil {
		replServer = cluster.NewReplicationServer(nodeID, nil, replDB)
		if *joinAddr != "" {
			replServer.SetRole("replica")
			replServer.SetPrimaryAddr(*joinAddr)
			log.Printf("Levara replica mode — joining primary at %s", *joinAddr)
		} else {
			replServer.SetRole("primary")
			log.Printf("Levara primary mode — accepting replicas")
		}
		// Wire replication to all DirectNode shards
		for _, shard := range shards {
			if dn, ok := shard.(*cluster.DirectNode); ok {
				dn.Repl = replServer
			}
		}
	}

	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000,http://localhost:3001,http://127.0.0.1:3000,http://127.0.0.1:3001,http://localhost:8080,http://localhost:8081",
		AllowMethods:     "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Api-Key",
		AllowCredentials: true,
	}))
	app.Use(logger.New())

	handler := vectorHttp.NewHandler(c, *dim)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	// Swagger UI (T13). Public in dev so anyone can explore; in prod we
	// could gate behind a JWT admin middleware, but for now we let
	// operators flip it off entirely by not setting ENV=dev. Regenerate
	// docs/swagger.{json,yaml} via `make swag`.
	if strings.EqualFold(os.Getenv("ENV"), "dev") || os.Getenv("ENV") == "" {
		app.Get("/swagger/*", swagger.HandlerDefault)
	}

	// Root-level health for frontend compatibility
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "levara-go"})
	})


	// ---------------------------------------------------------------
	// Cluster replication endpoints
	// ---------------------------------------------------------------
	if replServer != nil {
		app.Get("/cluster/wal/stream", adaptor.HTTPHandlerFunc(replServer.HandleStreamWAL))
		app.Get("/cluster/snapshot", adaptor.HTTPHandlerFunc(replServer.HandleSnapshot))
		app.Get("/cluster/state", adaptor.HTTPHandlerFunc(replServer.HandleClusterState))
	}

	// Cloud API compatibility: /api/datasets → /api/v1/datasets (cloudFetch strips /v1)
	cloudApi := app.Group("/api")
	cloudApi.Get("/datasets", func(c *fiber.Ctx) error {
		return c.Redirect("/api/v1/datasets", 307)
	})
	cloudApi.Get("/datasets/:id/data", func(c *fiber.Ctx) error {
		return c.Redirect("/api/v1/datasets/"+c.Params("id")+"/data", 307)
	})
	cloudApi.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "levara-go"})
	})


	api := app.Group("/api/v1")

	// Graph visualization config (used by both public and protected routes)
	vizCfg := vectorHttp.GraphVisualizationConfig{
		Neo4jURL: *neo4jURL, Neo4jUser: *neo4jUser,
		Neo4jPassword: *neo4jPassword, Neo4jDatabase: *neo4jDatabase,
		// DB set after pgDB init below
	}

	// Public routes (no auth required)
	api.Get("/info", handler.Info)
	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "levara-go"})
	})
	api.Post("/checks/connection", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "connected"})
	})
	// api.Get("/visualize", ...) — moved below after DB init to include cfg.DB

	// Error tracking endpoint (P3.4 observability)
	api.Get("/errors", func(c *fiber.Ctx) error {
		limit := c.QueryInt("limit", 50)
		return c.JSON(fiber.Map{
			"errors": errTracker.Recent(limit),
			"total":  errTracker.Count(),
		})
	})
	api.Delete("/errors", func(c *fiber.Ctx) error {
		errTracker.Clear()
		return c.JSON(fiber.Map{"cleared": true})
	})

	// Initialize CollectionManager for native collections (used by gRPC)
	colManager, err := store.NewCollectionManager(*dim, *dataDir+"/"+nodeID, hnswCfg)
	if err != nil {
		log.Fatalf("Failed to init CollectionManager: %v", err)
	}

	// Database connection pool (shared across all HTTP handlers)
	// DB_PROVIDER: "sqlite" (embedded, no external server) or "postgres" (default)
	pgDSN := ""
	var pgDB *sql.DB
	dbProvider := os.Getenv("DB_PROVIDER")

	if dbProvider == "sqlite" {
		// SQLite mode: pure-Go, no CGO, ARM64 ready (Raspberry Pi)
		vectorHttp.SetDBProvider(vectorHttp.DBSQLite)
		ingest.SetSQLiteMode(true)
		dbPath := os.Getenv("DB_PATH")
		if dbPath == "" {
			dbPath = filepath.Join(*dataDir, "levara.db")
		}
		// Ensure parent directory exists
		os.MkdirAll(filepath.Dir(dbPath), 0755)

		var dbErr error
		// ncruces/go-sqlite3: connection string with pragma query params
		// ncruces/go-sqlite3: pragma params become part of filename — this is by design.
		// Use simple WAL pragma only to keep filename manageable.
		// ncruces/go-sqlite3: use file: URI with pragmas (file: prefix prevents
		// pragma params from becoming part of the literal filename on disk)
		dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
		pgDB, dbErr = sql.Open("sqlite3", dsn)
		log.Printf("SQLite DSN: %s", dsn)
		if dbErr != nil {
			log.Printf("SQLite init warning: %v (running without DB)", dbErr)
		} else {
			// Allow multiple connections — ncruces driver may close connections
			// after use; pool needs room to create replacements
			pgDB.SetMaxOpenConns(4)
			pgDB.SetMaxIdleConns(4)
			pgDB.SetConnMaxLifetime(0)
			pgDB.SetConnMaxIdleTime(0)
			if err := pgDB.Ping(); err != nil {
				log.Printf("SQLite ping warning: %v (running without DB)", err)
				pgDB.Close()
				pgDB = nil
			} else {
				log.Printf("SQLite database ready: %s", dbPath)
				if err := vectorHttp.MigrateSchema(pgDB); err != nil {
					log.Printf("Schema migration warning: %v", err)
				}
			}
		}
	} else if dbHost := os.Getenv("DB_HOST"); dbHost != "" {
		// PostgreSQL mode (default)
		vectorHttp.SetDBProvider(vectorHttp.DBPostgres)
		dbUser := os.Getenv("DB_USERNAME"); if dbUser == "" { dbUser = "cognee" }
		dbPass := os.Getenv("DB_PASSWORD"); if dbPass == "" { dbPass = "cognee" }
		dbName := os.Getenv("DB_NAME"); if dbName == "" { dbName = "cognee_db" }
		dbPort := os.Getenv("DB_PORT"); if dbPort == "" { dbPort = "5432" }
		pgDSN = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", dbUser, dbPass, dbHost, dbPort, dbName)

		var dbErr error
		pgDB, dbErr = sql.Open("pgx", pgDSN)
		if dbErr != nil {
			log.Printf("PostgreSQL pool init warning: %v (running without DB)", dbErr)
		} else {
			pgDB.SetMaxOpenConns(25)
			pgDB.SetMaxIdleConns(10)
			pgDB.SetConnMaxLifetime(5 * time.Minute)
			if err := pgDB.Ping(); err != nil {
				log.Printf("PostgreSQL ping warning: %v (running without DB)", err)
				pgDB.Close()
				pgDB = nil
			} else {
				log.Printf("PostgreSQL connection pool ready (max_open=25, max_idle=10)")
				if err := vectorHttp.MigrateSchema(pgDB); err != nil {
					log.Printf("Schema migration warning: %v", err)
				}
			}
		}
	}
	vizCfg.DB = pgDB // PostgreSQL/SQLite fallback for graph visualization
	api.Get("/visualize", vectorHttp.VisualizeHTML(&vizCfg))
	if pgDB != nil {
		log.Printf("Graph visualization: SQL fallback enabled")
	}
	embedEndpoint := os.Getenv("EMBEDDING_ENDPOINT")
	embedModel := os.Getenv("EMBEDDING_MODEL"); if embedModel == "" { embedModel = "text-embedding-3-small" }

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
	api.Use(vectorHttp.UserRateLimiter(vectorHttp.RateLimitConfig{}))

	// Per-endpoint Prometheus instrumentation with bounded user_id
	// cardinality (T17 / D14). UserBucket promotes the top-50 active users
	// to real labels and buckets the long tail into "other"; refreshed
	// every minute so a burst can't permanently pin a user.
	userBucket := metrics.NewUserBucket(50, time.Minute)
	defer userBucket.Stop()
	api.Use(vectorHttp.PromInstrumentationMiddleware("api", userBucket))

	// Tenant isolation middleware (resolves tenant from user or X-Tenant-Id header)
	api.Use(vectorHttp.TenantMiddleware(pgDB))

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

	// Shared search-strategy registry (T5) — owned by main so tests can
	// substitute strategies without touching NewDefaultStrategyRegistry.
	searchStrategies := vectorHttp.NewDefaultStrategyRegistry()

	// Protected routes: Cognee-compatible API (datasets, upload, cognify, search)
	vectorHttp.RegisterCogneeAPI(api, vectorHttp.APIConfig{
		PostgresDSN:      pgDSN,
		StoragePath:      *dataDir + "/uploads",
		EmbedEndpoint:    embedEndpoint,
		EmbedModel:       embedModel,
		EmbedClient:      sharedEmbed,
		Collections:      colManager,
		Neo4jCfg:         vizCfg,
		DB:               pgDB,
		BM25Indexes:      grpcSvc.BM25Indexes(),
		LLMCache:         llmCache,
		LLMProvider:      llmProvider,
		ErrorTracker:     errTracker,
		FileStorage:      fileStore,
		Logger:           srvLog,
		AdaptiveWeights:  adaptiveWeights,
		Runs:             runs,
		SearchStrategies: searchStrategies,
	})

	// MCP (Model Context Protocol) server — JSON-RPC 2.0 for AI agent integration
	vectorHttp.RegisterMCPAPI(app, vectorHttp.APIConfig{
		EmbedEndpoint:  embedEndpoint,
		EmbedModel:     embedModel,
		EmbedClient:    sharedEmbed,
		Collections:    colManager,
		DB:             pgDB,
		BM25Indexes:    grpcSvc.BM25Indexes(),
		LLMCache:       llmCache,
		RerankEndpoint: os.Getenv("RERANK_ENDPOINT"),
		RerankModel:    os.Getenv("RERANK_MODEL"),
		Runs:           runs,
	})

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
		pgDB:           pgDB,
		neo4jURL:       *neo4jURL,
		neo4jUser:      *neo4jUser,
		neo4jPassword:  *neo4jPassword,
		neo4jDatabase:  *neo4jDatabase,
		embedEndpoint:  embedEndpoint,
		embedModel:     embedModel,
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
	startEmbedKeepAlive(embedEndpoint, embedModel)

	log.Fatal(app.Listen(addr))
}
