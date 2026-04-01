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
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"path/filepath"

	"github.com/hashicorp/raft"
	_ "github.com/jackc/pgx/v5/stdlib"  // pgx via database/sql (binary protocol, prepared stmts)
	_ "github.com/ncruces/go-sqlite3/driver" // pure-Go SQLite driver (no CGO, ARM64 ready)
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stek0v/cognevra/internal/cluster"
	vectorGrpc "github.com/stek0v/cognevra/internal/grpc"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/llmproxy"
	"github.com/stek0v/cognevra/pkg/observe"
	"github.com/stek0v/cognevra/pkg/storage"
	pb "github.com/stek0v/cognevra/proto/pb"
	"google.golang.org/grpc"

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

	// LLM multi-provider abstraction: supports OpenAI, Ollama, Anthropic via env vars.
	// LLM_PROVIDER: "openai" (default), "ollama", "anthropic"
	// LLM_ENDPOINT: required for openai/ollama (e.g. http://localhost:11434/v1)
	// LLM_API_KEY:  required for anthropic, optional for openai
	var llmProvider llm.Provider
	{
		providerName := os.Getenv("LLM_PROVIDER")
		llmEndpoint := os.Getenv("LLM_ENDPOINT")
		llmAPIKey := os.Getenv("LLM_API_KEY")
		if providerName != "" || llmEndpoint != "" {
			p, err := llm.NewProvider(providerName, llmEndpoint, llmAPIKey)
			if err != nil {
				log.Printf("LLM provider init warning: %v (using legacy HTTP)", err)
			} else {
				llmProvider = p
				log.Printf("LLM provider: %s (model=%s)", p.Name(), os.Getenv("LLM_MODEL"))
			}
		}
	}

	// Langfuse LLM tracing (optional): wrap provider if LANGFUSE_PUBLIC_KEY is set
	if lfPubKey := os.Getenv("LANGFUSE_PUBLIC_KEY"); lfPubKey != "" && llmProvider != nil {
		lfSecKey := os.Getenv("LANGFUSE_SECRET_KEY")
		lfEndpoint := os.Getenv("LANGFUSE_ENDPOINT")
		tracer := observe.NewLangfuseTracer(lfEndpoint, lfPubKey, lfSecKey)
		adapter := llm.NewLangfuseAdapter(tracer)
		llmProvider = llm.NewTracedProvider(llmProvider, adapter)
		log.Printf("Langfuse tracing enabled (endpoint=%s)", tracer.Endpoint())
	}

	// Rate limiting (optional): LLM_RATE_LIMIT_REQUESTS + LLM_RATE_LIMIT_INTERVAL
	if rlReqs := os.Getenv("LLM_RATE_LIMIT_REQUESTS"); rlReqs != "" {
		maxReqs, _ := strconv.Atoi(rlReqs)
		intervalSec, _ := strconv.Atoi(os.Getenv("LLM_RATE_LIMIT_INTERVAL"))
		if intervalSec <= 0 {
			intervalSec = 60
		}
		if maxReqs > 0 && llmProvider != nil {
			llmProvider = llm.NewRateLimiter(llmProvider, maxReqs, time.Duration(intervalSec)*time.Second)
			log.Printf("LLM rate limit: %d requests per %ds", maxReqs, intervalSec)
		}
	}

	// Protected routes: Cognee-compatible API (datasets, upload, cognify, search)
	vectorHttp.RegisterCogneeAPI(api, vectorHttp.APIConfig{
		PostgresDSN:   pgDSN,
		StoragePath:   *dataDir + "/uploads",
		EmbedEndpoint: embedEndpoint,
		EmbedModel:    embedModel,
		Collections:   colManager,
		Neo4jCfg:      vizCfg,
		DB:            pgDB,
		BM25Indexes:   grpcSvc.BM25Indexes(),
		LLMCache:      llmCache,
		LLMProvider:   llmProvider,
		ErrorTracker:  errTracker,
		FileStorage:   fileStore,
		Logger:        srvLog,
	})

	// MCP (Model Context Protocol) server — JSON-RPC 2.0 for AI agent integration
	vectorHttp.RegisterMCPAPI(app, vectorHttp.APIConfig{
		EmbedEndpoint: embedEndpoint,
		EmbedModel:    embedModel,
		Collections:   colManager,
		DB:            pgDB,
		BM25Indexes:   grpcSvc.BM25Indexes(),
		LLMCache:      llmCache,
	})

	// Cache stats endpoint
	api.Get("/cache/stats", func(c *fiber.Ctx) error {
		return c.JSON(llmCache.Stats())
	})
	log.Printf("MCP server registered at POST /mcp (7 tools)")

	// Detailed health endpoint — checks all dependencies (registered after all inits)
	app.Get("/health/details", func(ctx *fiber.Ctx) error {
		services := fiber.Map{}
		services["backend"] = fiber.Map{"status": "connected", "version": "levara-go", "port": *port}

		if pgDB != nil {
			if err := pgDB.Ping(); err == nil {
				services["postgres"] = fiber.Map{"status": "connected"}
			} else {
				services["postgres"] = fiber.Map{"status": "error", "error": err.Error()}
			}
		} else {
			services["postgres"] = fiber.Map{"status": "not_configured"}
		}

		if *neo4jURL != "" {
			// Actually try to connect
			neoCtx, neoCancel := context.WithTimeout(context.Background(), 3*time.Second)
			w, neoErr := graphdb.NewWriter(neoCtx, *neo4jURL, *neo4jUser, *neo4jPassword, *neo4jDatabase)
			if neoErr == nil {
				w.Close(neoCtx)
				services["neo4j"] = fiber.Map{"status": "connected", "url": *neo4jURL}
			} else {
				services["neo4j"] = fiber.Map{"status": "unreachable", "url": *neo4jURL, "error": neoErr.Error()}
			}
			neoCancel()
		} else {
			services["neo4j"] = fiber.Map{"status": "not_configured"}
		}

		if embedEndpoint != "" {
			// Derive base URL from embed endpoint (strip /v1/embeddings or similar suffix)
			embedBase := embedEndpoint
			for _, suffix := range []string{"/v1/embeddings", "/v1/embed", "/api/embed", "/api/embeddings", "/embeddings"} {
				if strings.HasSuffix(embedBase, suffix) {
					embedBase = strings.TrimSuffix(embedBase, suffix)
					break
				}
			}
			if embedBase == "" {
				embedBase = embedEndpoint
			}
			// Try health check on base URL
			embedOk := false
			for _, path := range []string{"/api/tags", "/health", "/v1/models", ""} {
				resp, err := http.Get(embedBase + path)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode == 200 {
						embedOk = true
						break
					}
				}
			}
			if embedOk {
				services["embed"] = fiber.Map{"status": "connected", "endpoint": embedEndpoint, "model": embedModel}
			} else {
				services["embed"] = fiber.Map{"status": "unreachable", "endpoint": embedEndpoint, "model": embedModel}
			}
		} else {
			services["embed"] = fiber.Map{"status": "not_configured"}
		}

		llmEP := os.Getenv("LLM_ENDPOINT")
		llmMD := os.Getenv("LLM_MODEL")
		if llmEP != "" {
			resp, err := http.Get(llmEP + "/models")
			if err == nil {
				resp.Body.Close()
				services["llm"] = fiber.Map{"status": "connected", "endpoint": llmEP, "model": llmMD}
			} else {
				services["llm"] = fiber.Map{"status": "unreachable", "endpoint": llmEP, "model": llmMD}
			}
		} else {
			services["llm"] = fiber.Map{"status": "not_configured"}
		}

		// Rate limiter status
		if rl, ok := llmProvider.(*llm.RateLimiter); ok {
			services["llm_rate_limit"] = fiber.Map{
				"status":           "active",
				"available_tokens": rl.AvailableTokens(),
				"max_requests":     rl.MaxRequests(),
				"interval_seconds": int(rl.Interval().Seconds()),
			}
		}

		if whisperEndpoint := os.Getenv("WHISPER_ENDPOINT"); whisperEndpoint != "" {
			resp, err := http.Get(whisperEndpoint + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					services["whisper"] = fiber.Map{"status": "connected", "endpoint": whisperEndpoint}
				} else {
					services["whisper"] = fiber.Map{"status": "unreachable", "endpoint": whisperEndpoint}
				}
			} else {
				services["whisper"] = fiber.Map{"status": "unreachable", "endpoint": whisperEndpoint}
			}
		} else {
			services["whisper"] = fiber.Map{"status": "not_configured"}
		}

		services["collections"] = fiber.Map{"status": "ready", "count": len(colManager.List()), "dimension": *dim}
		services["grpc"] = fiber.Map{"status": "listening", "port": *grpcPort}

		return ctx.JSON(fiber.Map{"services": services})
	})

	// Start gRPC server (parallel to HTTP)
	if *grpcPort > 0 {
		go func() {
			lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *grpcPort))
			if err != nil {
				log.Fatalf("gRPC listen: %v", err)
			}
			grpcServer := grpc.NewServer(
				grpc.UnaryInterceptor(vectorGrpc.MetricsUnaryInterceptor()),
			)
			pb.RegisterCognevraServiceServer(grpcServer, grpcSvc)
			log.Printf("gRPC server listening on port %d", *grpcPort)
			if err := grpcServer.Serve(lis); err != nil {
				log.Fatalf("gRPC serve: %v", err)
			}
		}()
	}

	// Start LLM proxy (optional) with persistent cache
	if *llmProxyPort > 0 && *llmUpstream != "" {
		cachePath := *dataDir + "/" + nodeID + "/llm_cache.jsonl"
		cache, cacheErr := llmcache.NewPersistent(*llmCacheSize, cachePath)
		if cacheErr != nil {
			log.Printf("LLM cache persist warning: %v (using in-memory)", cacheErr)
			cache = &llmcache.PersistentCache{Cache: llmcache.New(*llmCacheSize, 0)}
		}
		defer cache.Close()
		stop, err := llmproxy.StartBackground(
			fmt.Sprintf(":%d", *llmProxyPort),
			llmproxy.Config{
				UpstreamURL: *llmUpstream,
				Cache:       cache.Cache,
				MaxInFlight: *llmMaxInflight,
			},
		)
		if err != nil {
			log.Fatalf("LLM proxy: %v", err)
		}
		defer stop()
	}

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

	// Graceful shutdown: flush WAL + disk on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		log.Printf("Received %v, shutting down gracefully...", sig)

		for i, shard := range shards {
			if dn, ok := shard.(*cluster.DirectNode); ok {
				if err := dn.DB.Close(); err != nil {
					log.Printf("shard %d close error: %v", i, err)
				}
			}
		}
		if err := colManager.Close(); err != nil {
			log.Printf("collection manager close: %v", err)
		}
		if pgDB != nil {
			pgDB.Close()
		}
		log.Println("All shards flushed and closed")
		app.Shutdown()
	}()

	// Background keep-alive ping for Ollama models (prevents model eviction)
	if embedEndpoint != "" {
		go func() {
			embedClient := embed.NewClient(embedEndpoint, embedModel, 1, 1)
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				_, err := embedClient.EmbedSingle(ctx, "keepalive")
				if err != nil {
					log.Printf("[keepalive] embed ping failed: %v", err)
				}
				cancel()
			}
		}()
		log.Printf("Embed keep-alive started (ping every 10min)")
	}

	log.Fatal(app.Listen(addr))
}
