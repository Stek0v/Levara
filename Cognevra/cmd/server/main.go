// Cognevra server — HTTP + gRPC vector database with HNSW indexing and WAL durability.
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
	"syscall"
	"time"

	"github.com/hashicorp/raft"
	_ "github.com/jackc/pgx/v5/stdlib" // pgx via database/sql (binary protocol, prepared stmts)
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stek0v/cognevra/internal/cluster"
	vectorGrpc "github.com/stek0v/cognevra/internal/grpc"
	"github.com/stek0v/cognevra/internal/store"
	"github.com/stek0v/cognevra/pkg/graphdb"
	"github.com/stek0v/cognevra/pkg/llmcache"
	"github.com/stek0v/cognevra/pkg/llmproxy"
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

	flag.Parse()

	hnswCfg := store.HNSWConfig{
		M:            *hnswM,
		M0:           *hnswM * 2,
		EfSearchMult: *hnswEfMult,
		EfSearchMin:  *hnswEfMin,
		LevelMult:    1.0 / 0.69,
	}

	nodeID := "node1"
	basePort := 9000
	numShards := *numShardsFlag

	if *standalone {
		log.Printf("Cognevra standalone mode (WAL-only, no Raft)")
	} else {
		log.Printf("Cognevra Raft consensus mode")
	}

	var shards []store.ShardHandler

	for i := range numShards {
		dbPath := fmt.Sprintf("%s/%s/shard_%d/meta.bin", *dataDir, nodeID, i)
		db, err := store.NewCognevra(*dim, dbPath, hnswCfg)
		if err != nil {
			log.Fatal(err)
		}

		if *standalone {
			shards = append(shards, &cluster.DirectNode{DB: db})
		} else {
			raftNode, err := cluster.NewRaftNode(i, nodeID, *dataDir+"/"+nodeID, basePort+i, db)
			if err != nil {
				log.Fatal(err)
			}

			if *bootstrap {
				configuration := raft.Configuration{
					Servers: []raft.Server{
						{
							ID:      raft.ServerID(fmt.Sprintf("%s-shard-%d", nodeID, i)),
							Address: raft.ServerAddress(fmt.Sprintf("127.0.0.1:%d", basePort+i)),
						},
					},
				}
				raftNode.Raft.BootstrapCluster(configuration)
			}
			shards = append(shards, raftNode)
		}
	}

	c := store.NewCluster(shards)

	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000,http://127.0.0.1:3000,http://localhost:8080",
		AllowMethods:     "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Api-Key",
		AllowCredentials: true,
	}))
	app.Use(logger.New())

	handler := vectorHttp.NewHandler(c, *dim)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))

	// Root-level health for frontend compatibility
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "cognevra-go"})
	})


	// Cloud API compatibility: /api/datasets → /api/v1/datasets (cloudFetch strips /v1)
	cloudApi := app.Group("/api")
	cloudApi.Get("/datasets", func(c *fiber.Ctx) error {
		return c.Redirect("/api/v1/datasets", 307)
	})
	cloudApi.Get("/datasets/:id/data", func(c *fiber.Ctx) error {
		return c.Redirect("/api/v1/datasets/"+c.Params("id")+"/data", 307)
	})
	cloudApi.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "cognevra-go"})
	})


	api := app.Group("/api/v1")

	// Graph visualization config (used by both public and protected routes)
	vizCfg := vectorHttp.GraphVisualizationConfig{
		Neo4jURL: *neo4jURL, Neo4jUser: *neo4jUser,
		Neo4jPassword: *neo4jPassword, Neo4jDatabase: *neo4jDatabase,
	}

	// Public routes (no auth required)
	api.Get("/info", handler.Info)
	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "cognevra-go"})
	})
	api.Post("/checks/connection", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "connected"})
	})
	api.Get("/visualize", vectorHttp.VisualizeHTML(vizCfg))

	// Initialize CollectionManager for native collections (used by gRPC)
	colManager, err := store.NewCollectionManager(*dim, *dataDir+"/"+nodeID, hnswCfg)
	if err != nil {
		log.Fatalf("Failed to init CollectionManager: %v", err)
	}

	// PostgreSQL connection pool (shared across all HTTP handlers)
	pgDSN := ""
	var pgDB *sql.DB
	if dbHost := os.Getenv("DB_HOST"); dbHost != "" {
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

	// JWT middleware on all protected routes below this point
	// Uses authCfg.JWTSecret which is guaranteed non-empty after RegisterAuthAPI
	api.Use(vectorHttp.JWTMiddleware(authCfg.JWTSecret, *requireAuth))

	// Protected routes: vector ops
	api.Post("/insert", handler.Insert)
	api.Post("/batch_insert", handler.BatchInsert)
	api.Post("/search", handler.Search)
	api.Post("/delete", handler.Delete)
	api.Get("/datasets/:id/graph", vectorHttp.DatasetGraph(vizCfg))

	// Create gRPC service (shared between gRPC server and HTTP handlers for BM25 indexes)
	grpcSvc := vectorGrpc.NewService(colManager, c, *dim)

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
	})

	// MCP (Model Context Protocol) server — JSON-RPC 2.0 for AI agent integration
	vectorHttp.RegisterMCPAPI(app, vectorHttp.APIConfig{
		EmbedEndpoint: embedEndpoint,
		EmbedModel:    embedModel,
		Collections:   colManager,
		DB:            pgDB,
		BM25Indexes:   grpcSvc.BM25Indexes(),
	})
	log.Printf("MCP server registered at POST /mcp (7 tools)")

	// Detailed health endpoint — checks all dependencies (registered after all inits)
	app.Get("/health/details", func(ctx *fiber.Ctx) error {
		services := fiber.Map{}
		services["backend"] = fiber.Map{"status": "connected", "version": "cognevra-go", "port": *port}

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
			resp, err := http.Get(embedEndpoint + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					services["embed"] = fiber.Map{"status": "connected", "endpoint": embedEndpoint, "model": embedModel}
				} else {
					services["embed"] = fiber.Map{"status": "error", "endpoint": embedEndpoint, "code": resp.StatusCode}
				}
			} else {
				services["embed"] = fiber.Map{"status": "unreachable", "endpoint": embedEndpoint}
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

	mode := "standalone/WAL"
	if !*standalone {
		mode = "Raft consensus"
	}
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Cognevra listening on HTTP:%d gRPC:%d (dim=%d, shards=%d, mode=%s)", *port, *grpcPort, *dim, numShards, mode)

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

	log.Fatal(app.Listen(addr))
}
