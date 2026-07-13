// bootstrap.go — main() helpers, split out of main.go (ARCH-1).
//
// main() stays linear and readable as a sequencing scaffold; the chunky
// wiring blocks live here. Each helper takes its dependencies explicitly
// so the call sites in main read like a recipe.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/swagger"
	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"

	"github.com/stek0v/levara/internal/cluster"
	vectorGrpc "github.com/stek0v/levara/internal/grpc"
	vectorHttp "github.com/stek0v/levara/internal/http"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
	"github.com/stek0v/levara/pkg/llmproxy"
	"github.com/stek0v/levara/pkg/observe"
	"github.com/stek0v/levara/pkg/profile"
	"github.com/stek0v/levara/pkg/storage"
	pb "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
)

func initStorageBackend(dataDir string, srvLog *observe.Logger) (storage.Storage, string, string) {
	storagePath := dataDir + "/uploads"
	fileStore, err := storage.NewFromEnv(storagePath)
	if err != nil {
		log.Fatalf("storage init: %v", err)
	}
	storageBackend := strings.ToLower(os.Getenv("STORAGE_BACKEND"))
	if srvLog != nil {
		srvLog.Info("storage backend ready", map[string]any{"backend": storageBackend, "path": storagePath})
		if storageBackend == "s3" {
			srvLog.Info("S3 storage enabled for upload hot-path", map[string]any{
				"hot_path":     "/api/v1/add -> ingest + mirror to cfg.FileStorage",
				"location_uri": "storage://<key> for non-local backend",
				"storage_path": storagePath,
			})
		}
	}
	return fileStore, storageBackend, storagePath
}

type sqlRuntime struct {
	DSN string
	DB  *sql.DB
}

func initSQLRuntime(dataDir string, pgURL string) sqlRuntime {
	dbProvider := os.Getenv("DB_PROVIDER")
	if dbProvider == "sqlite" {
		return initSQLiteRuntime(dataDir)
	}
	// pg-url flag takes precedence over env vars.
	if pgURL != "" {
		return initPostgresRuntime(pgURL)
	}
	if os.Getenv("DB_HOST") != "" {
		return initPostgresRuntime("")
	}
	return sqlRuntime{}
}

type vectorRuntime struct {
	Shards      []store.ShardHandler
	Cluster     *store.Cluster
	Replication *cluster.ReplicationServer
}

type vectorRuntimeConfig struct {
	Dim        int
	DataDir    string
	NodeID     string
	NumShards  int
	Standalone bool
	Bootstrap  bool
	RaftAddr   string
	RaftPort   int
	JoinAddr   string
	HNSW       store.HNSWConfig
}

func initVectorRuntime(cfg vectorRuntimeConfig) vectorRuntime {
	var shards []store.ShardHandler
	for i := range cfg.NumShards {
		dbPath := fmt.Sprintf("%s/%s/shard_%d/meta.bin", cfg.DataDir, cfg.NodeID, i)
		db, err := store.NewLevara(cfg.Dim, dbPath, cfg.HNSW)
		if err != nil {
			log.Fatal(err)
		}

		if cfg.Standalone {
			shards = append(shards, &cluster.DirectNode{DB: db})
			continue
		}
		raftNode, err := cluster.NewRaftNode(i, cfg.NodeID, cfg.DataDir+"/"+cfg.NodeID, cfg.RaftPort+i, db,
			cluster.WithBindAddr(cfg.RaftAddr))
		if err != nil {
			log.Fatal(err)
		}
		if cfg.Bootstrap {
			configuration := raft.Configuration{
				Servers: []raft.Server{{
					ID:      raft.ServerID(fmt.Sprintf("%s-shard-%d", cfg.NodeID, i)),
					Address: raft.ServerAddress(fmt.Sprintf("%s:%d", cfg.RaftAddr, cfg.RaftPort+i)),
				}},
			}
			raftNode.Raft.BootstrapCluster(configuration)
		}
		shards = append(shards, raftNode)
	}

	replServer := initReplicationServer(cfg.NodeID, cfg.JoinAddr, shards)
	return vectorRuntime{
		Shards:      shards,
		Cluster:     store.NewCluster(shards),
		Replication: replServer,
	}
}

func initReplicationServer(nodeID, joinAddr string, shards []store.ShardHandler) *cluster.ReplicationServer {
	replDB := replicationDB(shards)
	if replDB == nil {
		return nil
	}
	replServer := cluster.NewReplicationServer(nodeID, nil, replDB)
	if joinAddr != "" {
		replServer.SetRole("replica")
		replServer.SetPrimaryAddr(joinAddr)
		log.Printf("Levara replica mode — joining primary at %s", joinAddr)
	} else {
		replServer.SetRole("primary")
		log.Printf("Levara primary mode — accepting replicas")
	}
	for _, shard := range shards {
		if dn, ok := shard.(*cluster.DirectNode); ok {
			dn.Repl = replServer
		}
	}
	return replServer
}

func replicationDB(shards []store.ShardHandler) *store.Levara {
	if len(shards) == 0 {
		return nil
	}
	switch s := shards[0].(type) {
	case *cluster.DirectNode:
		return s.DB
	case *cluster.RaftNode:
		return s.DB
	default:
		return nil
	}
}

type httpRuntime struct {
	App            *fiber.App
	API            fiber.Router
	VectorHandler  *vectorHttp.Handler
	VersionHandler fiber.Handler
}

func initHTTPRuntime(clusterStore *store.Cluster, dim int, replServer *cluster.ReplicationServer, errTracker *observe.ErrorTracker) httpRuntime {
	app := fiber.New()
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000,http://localhost:3001,http://127.0.0.1:3000,http://127.0.0.1:3001,http://localhost:8080,http://localhost:8081",
		AllowMethods:     "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization,X-Api-Key,X-Trace-ID",
		AllowCredentials: true,
	}))
	if httpAccessLogEnabled() {
		app.Use(logger.New())
	}

	handler := vectorHttp.NewHandler(clusterStore, dim)
	app.Get("/metrics", adaptor.HTTPHandler(promhttp.Handler()))
	if strings.EqualFold(os.Getenv("ENV"), "dev") || os.Getenv("ENV") == "" {
		app.Get("/swagger/*", swagger.HandlerDefault)
	}
	app.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "levara-go"})
	})
	versionHandler := func(c *fiber.Ctx) error { return c.JSON(versionPayload()) }
	app.Get("/version", versionHandler)

	if replServer != nil {
		app.Get("/cluster/wal/stream", adaptor.HTTPHandlerFunc(replServer.HandleStreamWAL))
		app.Get("/cluster/snapshot", adaptor.HTTPHandlerFunc(replServer.HandleSnapshot))
		app.Get("/cluster/state", adaptor.HTTPHandlerFunc(replServer.HandleClusterState))
	}

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
	api.Get("/info", handler.Info)
	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ready", "health": "healthy", "version": "levara-go"})
	})
	api.Get("/version", versionHandler)
	api.Post("/checks/connection", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "connected"})
	})
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

	return httpRuntime{
		App:            app,
		API:            api,
		VectorHandler:  handler,
		VersionHandler: versionHandler,
	}
}

func httpAccessLogEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("LEVARA_HTTP_ACCESS_LOG")))
	return raw != "0" && raw != "false" && raw != "off" && raw != "no"
}

func initSQLiteRuntime(dataDir string) sqlRuntime {
	vectorHttp.SetDBProvider(vectorHttp.DBSQLite)
	ingest.SetSQLiteMode(true)
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(dataDir, "levara.db")
	}
	_ = os.MkdirAll(filepath.Dir(dbPath), 0755)

	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite3", dsn)
	log.Printf("SQLite DSN: %s", dsn)
	if err != nil {
		log.Printf("SQLite init warning: %v (running without DB)", err)
		return sqlRuntime{}
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.Ping(); err != nil {
		log.Printf("SQLite ping warning: %v (running without DB)", err)
		db.Close()
		return sqlRuntime{}
	}
	log.Printf("SQLite database ready: %s", dbPath)
	if err := vectorHttp.MigrateSchema(db); err != nil {
		log.Printf("Schema migration warning: %v", err)
	}
	return sqlRuntime{DB: db}
}

func initPostgresRuntime(pgURL string) sqlRuntime {
	vectorHttp.SetDBProvider(vectorHttp.DBPostgres)

	// pg-url flag takes precedence; fall back to component env vars.
	var dsn string
	if pgURL != "" {
		dsn = pgURL
		log.Printf("PostgreSQL DSN from --pg-url")
	} else {
		dbUser := os.Getenv("DB_USERNAME")
		if dbUser == "" {
			dbUser = "levara"
		}
		dbPass := os.Getenv("DB_PASSWORD")
		if dbPass == "" {
			dbPass = "levara"
		}
		dbName := os.Getenv("DB_NAME")
		if dbName == "" {
			dbName = "levara_db"
		}
		dbPort := os.Getenv("DB_PORT")
		if dbPort == "" {
			dbPort = "5432"
		}
		dbHost := os.Getenv("DB_HOST")
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", dbUser, dbPass, dbHost, dbPort, dbName)
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		log.Printf("PostgreSQL pool init warning: %v (running without DB)", err)
		return sqlRuntime{DSN: dsn}
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		log.Printf("PostgreSQL ping warning: %v (running without DB)", err)
		db.Close()
		return sqlRuntime{DSN: dsn}
	}
	log.Printf("PostgreSQL connection pool ready (max_open=25, max_idle=10)")
	if err := vectorHttp.MigrateSchema(db); err != nil {
		log.Printf("Schema migration warning: %v", err)
	}
	return sqlRuntime{DSN: dsn, DB: db}
}

// initLLMProvider wires the multi-provider abstraction from env vars
// (LLM_PROVIDER, LLM_ENDPOINT, LLM_API_KEY) and optionally wraps it with
// Langfuse tracing and an outbound rate limiter. Returns nil when no
// LLM_* env is set — callers handle nil gracefully.
func initLLMProvider() llm.Provider {
	providerName := os.Getenv("LLM_PROVIDER")
	llmEndpoint := os.Getenv("LLM_ENDPOINT")
	llmAPIKey := os.Getenv("LLM_API_KEY")
	if providerName == "" && llmEndpoint == "" {
		return nil
	}
	p, err := llm.NewProvider(providerName, llmEndpoint, llmAPIKey)
	if err != nil {
		log.Printf("LLM provider init warning: %v (using legacy HTTP)", err)
		return nil
	}
	log.Printf("LLM provider: %s (model=%s)", p.Name(), os.Getenv("LLM_MODEL"))
	provider := llm.Provider(p)

	// Langfuse tracing wrapper.
	if lfPubKey := os.Getenv("LANGFUSE_PUBLIC_KEY"); lfPubKey != "" {
		lfSecKey := os.Getenv("LANGFUSE_SECRET_KEY")
		lfEndpoint := os.Getenv("LANGFUSE_ENDPOINT")
		tracer := observe.NewLangfuseTracer(lfEndpoint, lfPubKey, lfSecKey)
		adapter := llm.NewLangfuseAdapter(tracer)
		provider = llm.NewTracedProvider(provider, adapter)
		log.Printf("Langfuse tracing enabled (endpoint=%s)", tracer.Endpoint())
	}

	// Outbound rate limiter (LLM_RATE_LIMIT_REQUESTS / _INTERVAL).
	if rlReqs := os.Getenv("LLM_RATE_LIMIT_REQUESTS"); rlReqs != "" {
		maxReqs, _ := strconv.Atoi(rlReqs)
		intervalSec, _ := strconv.Atoi(os.Getenv("LLM_RATE_LIMIT_INTERVAL"))
		if intervalSec <= 0 {
			intervalSec = 60
		}
		if maxReqs > 0 {
			provider = llm.NewRateLimiter(provider, maxReqs, time.Duration(intervalSec)*time.Second)
			log.Printf("LLM rate limit: %d requests per %ds", maxReqs, intervalSec)
		}
	}
	return provider
}

// startGRPCServer fires up the gRPC listener with the full interceptor
// chain (auth → ratelimit → metrics) and registers both v1 and v2
// services on the same port. Runs the server in a goroutine — the
// returned *grpc.Server lets the caller GracefulStop on shutdown.
func startGRPCServer(host string, port int, jwtSecret string, requireAuth bool, svc *vectorGrpc.Service) *grpc.Server {
	if port <= 0 {
		return nil
	}
	lis, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		log.Fatalf("gRPC listen: %v", err)
	}
	// Per-peer token bucket (T2): 100 req/min default, burst=20.
	// idleTTL=30min evicts dormant buckets.
	grpcLimiters := vectorGrpc.NewPeerLimiters(100, 20, 30*time.Minute)
	// JWT auth (T19): same secret as HTTP. requireAuth flag honours dev
	// deployments that haven't rolled out tokens yet (permissive). Auth
	// runs first in the chain so a rejected call never burns a legit
	// user's rate-limit budget.
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			vectorGrpc.UnaryAuthInterceptor(jwtSecret, requireAuth),
			vectorGrpc.UnaryRateLimitInterceptor(grpcLimiters),
			vectorGrpc.MetricsUnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			vectorGrpc.StreamAuthInterceptor(jwtSecret, requireAuth),
			vectorGrpc.StreamRateLimitInterceptor(grpcLimiters),
			vectorGrpc.MetricsStreamInterceptor(),
		),
	)
	pb.RegisterLevaraServiceServer(srv, svc)
	// v2 (T10): coexists with v1 on the same listener; gRPC dispatches by
	// fully qualified method name so old clients keep working.
	pbv2.RegisterLevaraServiceV2Server(srv, vectorGrpc.NewServiceV2(svc))
	go func() {
		log.Printf("gRPC server listening on %s", lis.Addr())
		if err := srv.Serve(lis); err != nil {
			log.Fatalf("gRPC serve: %v", err)
		}
	}()
	return srv
}

// startLLMProxyIfConfigured runs the optional LLM proxy on llmProxyPort
// when an upstream URL is configured. Returns the stop function so the
// caller can defer cleanup.
func startLLMProxyIfConfigured(llmProxyPort int, llmUpstream, dataDir, nodeID string, llmCacheSize, llmMaxInflight int) func() {
	if llmProxyPort <= 0 || llmUpstream == "" {
		return func() {}
	}
	cachePath := dataDir + "/" + nodeID + "/llm_cache.jsonl"
	cache, cacheErr := llmcache.NewPersistent(llmCacheSize, cachePath)
	if cacheErr != nil {
		log.Printf("LLM cache persist warning: %v (using in-memory)", cacheErr)
		cache = &llmcache.PersistentCache{Cache: llmcache.New(llmCacheSize, 0)}
	}
	stop, err := llmproxy.StartBackground(
		fmt.Sprintf(":%d", llmProxyPort),
		llmproxy.Config{
			UpstreamURL: llmUpstream,
			Cache:       cache.Cache,
			MaxInFlight: llmMaxInflight,
		},
	)
	if err != nil {
		log.Fatalf("LLM proxy: %v", err)
	}
	return func() {
		stop()
		cache.Close()
	}
}

// startEmbedKeepAlive pings the embedding endpoint on a configurable interval
// so Ollama / vLLM doesn't evict the model from VRAM during quiet periods.
// No-op when embedEndpoint is empty or interval is <= 0.
func startEmbedKeepAlive(embedEndpoint, embedModel string, interval time.Duration) {
	if embedEndpoint == "" || interval <= 0 {
		return
	}
	go func() {
		client := embed.NewClient(embedEndpoint, embedModel, 1, 1)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := client.EmbedSingle(ctx, "keepalive"); err != nil {
				log.Printf("[keepalive] embed ping failed: %v", err)
			}
			cancel()
		}
	}()
	log.Printf("Embed keep-alive started (ping every %v)", interval)
}

// installGracefulShutdown stops accepting traffic first, then closes backing
// services after in-flight HTTP requests have drained. The returned channel is
// closed only after all cleanup finishes so main does not exit early.
func installGracefulShutdown(app *fiber.App, shards []store.ShardHandler, colManager *store.CollectionManager, pgDB *sql.DB, grpcServer *grpc.Server) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		defer signal.Stop(sigCh)
		sig := <-sigCh
		log.Printf("Received %v, shutting down gracefully...", sig)
		if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
			log.Printf("HTTP shutdown: %v", err)
		}
		if grpcServer != nil {
			grpcDone := make(chan struct{})
			go func() {
				grpcServer.GracefulStop()
				close(grpcDone)
			}()
			select {
			case <-grpcDone:
			case <-time.After(10 * time.Second):
				log.Printf("gRPC graceful shutdown timed out; forcing stop")
				grpcServer.Stop()
			}
		}

		for i, shard := range shards {
			if dn, ok := shard.(*cluster.DirectNode); ok {
				if err := dn.DB.Close(); err != nil {
					log.Printf("shard %d close error: %v", i, err)
				}
			}
		}
		if colManager != nil {
			if err := colManager.Close(); err != nil {
				log.Printf("collection manager close: %v", err)
			}
		}
		if pgDB != nil {
			if err := pgDB.Close(); err != nil {
				log.Printf("pgDB close: %v", err)
			}
		}
		log.Println("All shards flushed and closed")
	}()
	return done
}

// registerHealthDetails wires the verbose /health/details endpoint that
// probes every dependency (Postgres, Neo4j, embed service, LLM, Whisper)
// and reports status + endpoint info per service.
//
// Big block (~110 lines) — pulled out of main verbatim to make the
// linear init flow scannable. Probe logic itself is unchanged.
func registerHealthDetails(app *fiber.App, deps healthDeps) {
	app.Get("/health/details", func(ctx *fiber.Ctx) error {
		services := fiber.Map{}
		services["backend"] = fiber.Map{"status": "connected", "version": "levara-go", "port": deps.port}
		services["database"] = fiber.Map{"status": "not_configured", "provider": deps.dbProvider}

		if deps.pgDB != nil {
			if err := deps.pgDB.Ping(); err == nil {
				services["database"] = fiber.Map{"status": "connected", "provider": deps.dbProvider}
				if strings.EqualFold(deps.dbProvider, "postgres") {
					services["postgres"] = fiber.Map{"status": "connected"}
				} else {
					services["postgres"] = fiber.Map{"status": "not_configured"}
				}
			} else {
				services["database"] = fiber.Map{"status": "error", "provider": deps.dbProvider, "error": err.Error()}
				if strings.EqualFold(deps.dbProvider, "postgres") {
					services["postgres"] = fiber.Map{"status": "error", "error": err.Error()}
				} else {
					services["postgres"] = fiber.Map{"status": "not_configured"}
				}
			}
		} else if strings.EqualFold(deps.dbProvider, "postgres") {
			services["database"] = fiber.Map{"status": "unavailable", "provider": deps.dbProvider, "error": "connection not initialized"}
			services["postgres"] = fiber.Map{"status": "unavailable", "error": "connection not initialized"}
		} else {
			services["postgres"] = fiber.Map{"status": "not_configured"}
		}

		if deps.neo4jURL != "" {
			neoCtx, neoCancel := context.WithTimeout(context.Background(), 3*time.Second)
			w, neoErr := graphdb.NewWriter(neoCtx, deps.neo4jURL, deps.neo4jUser, deps.neo4jPassword, deps.neo4jDatabase)
			if neoErr == nil {
				w.Close(neoCtx)
				services["neo4j"] = fiber.Map{"status": "connected", "url": deps.neo4jURL}
			} else {
				services["neo4j"] = fiber.Map{"status": "unreachable", "url": deps.neo4jURL, "error": neoErr.Error()}
			}
			neoCancel()
		} else {
			services["neo4j"] = fiber.Map{"status": "not_configured"}
		}

		if deps.embedEndpoint != "" {
			services["embed"] = probeEmbedService(deps.embedEndpoint, deps.embedModel)
		} else {
			services["embed"] = fiber.Map{"status": "not_configured"}
		}

		if deps.rerankEndpoint != "" {
			services["rerank"] = fiber.Map{"status": "configured", "endpoint": deps.rerankEndpoint, "model": deps.rerankModel}
		} else {
			services["rerank"] = fiber.Map{"status": "not_configured"}
		}

		llmEP := os.Getenv("LLM_ENDPOINT")
		llmMD := os.Getenv("LLM_MODEL")
		if llmEP != "" {
			resp, err := healthHTTPGet(llmEP + "/models")
			if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
				resp.Body.Close()
				services["llm"] = fiber.Map{"status": "connected", "endpoint": llmEP, "model": llmMD}
			} else {
				if resp != nil {
					resp.Body.Close()
				}
				services["llm"] = fiber.Map{"status": "unreachable", "endpoint": llmEP, "model": llmMD}
			}
		} else {
			services["llm"] = fiber.Map{"status": "not_configured"}
		}

		// Outbound LLM rate-limit telemetry — skips when no rate-limit
		// wrapper was applied (initLLMProvider returns the bare provider).
		if rl, ok := deps.llmProvider.(*llm.RateLimiter); ok {
			services["llm_rate_limit"] = fiber.Map{
				"status":           "active",
				"available_tokens": rl.AvailableTokens(),
				"max_requests":     rl.MaxRequests(),
				"interval_seconds": int(rl.Interval().Seconds()),
			}
		}

		if whisperEndpoint := os.Getenv("WHISPER_ENDPOINT"); whisperEndpoint != "" {
			resp, err := healthHTTPGet(whisperEndpoint + "/health")
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

		storageBackend := deps.storageBackend
		if storageBackend == "" {
			storageBackend = "local"
		}
		services["storage"] = fiber.Map{"status": "ready", "backend": storageBackend, "path": deps.storagePath}
		ocrBackend := strings.ToLower(strings.TrimSpace(os.Getenv("OCR_BACKEND")))
		if ocrBackend == "" && strings.EqualFold(os.Getenv("TESSERACT_ENABLED"), "true") {
			ocrBackend = "tesseract"
		}
		if ocrBackend == "tesseract" || ocrBackend == "tesseract-cli" {
			services["ocr"] = fiber.Map{"status": "configured", "backend": "tesseract-cli", "binary": os.Getenv("TESSERACT_BINARY"), "lang": os.Getenv("TESSERACT_LANG")}
		} else if ocrBackend == "gosseract" || ocrBackend == "tesseract-cgo" {
			services["ocr"] = fiber.Map{"status": "configured", "backend": "gosseract", "lang": os.Getenv("TESSERACT_LANG")}
		} else if visionEndpoint := os.Getenv("VISION_ENDPOINT"); visionEndpoint != "" {
			services["ocr"] = fiber.Map{"status": "configured", "backend": "remote", "endpoint": visionEndpoint}
		} else if visionModel := os.Getenv("VISION_MODEL"); visionModel != "" {
			services["ocr"] = fiber.Map{"status": "configured", "backend": "ollama", "model": visionModel}
		} else {
			services["ocr"] = fiber.Map{"status": "not_configured", "backend": "vision"}
		}
		if deps.colManager != nil {
			services["collections"] = fiber.Map{"status": "ready", "count": len(deps.colManager.List()), "dimension": deps.dim}
		} else {
			services["collections"] = fiber.Map{"status": "unavailable", "count": 0, "dimension": deps.dim}
		}
		if deps.grpcPort > 0 {
			services["grpc"] = fiber.Map{"status": "listening", "port": deps.grpcPort}
		} else {
			services["grpc"] = fiber.Map{"status": "disabled", "port": 0}
		}

		return ctx.JSON(fiber.Map{"services": services})
	})
}

var healthProbeClient = &http.Client{Timeout: 3 * time.Second}

func healthHTTPGet(rawURL string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	return healthProbeClient.Do(req)
}

// healthDeps is the bundle of shared deps the verbose health endpoint
// needs. Defined here so the registration site is one parameter rather
// than a long positional list.
type healthDeps struct {
	port           int
	grpcPort       int
	dim            int
	dbProvider     string
	storageBackend string
	storagePath    string
	pgDB           *sql.DB
	neo4jURL       string
	neo4jUser      string
	neo4jPassword  string
	neo4jDatabase  string
	embedEndpoint  string
	embedModel     string
	rerankEndpoint string
	rerankModel    string
	llmProvider    llm.Provider
	colManager     *store.CollectionManager
}

// initMCPAuditSink resolves the -mcp-audit-log flag into an audit.Sink.
// "" → JSONL on stderr (zero-config default). "-" → disabled (nil).
// Anything else is treated as a directory for daily-rolled, gzipped logs.
func initMCPAuditSink(path string, log *observe.Logger) audit.Sink {
	switch path {
	case "-":
		return nil
	case "":
		return audit.NewLogger(nil) // stderr
	}
	fl, err := audit.NewFileLogger(path, 30)
	if err != nil {
		log.Warn("mcp_audit_init_failed", map[string]any{"path": path, "err": err.Error()})
		return audit.NewLogger(nil)
	}
	log.Info("mcp_audit_log_ready", map[string]any{"path": path, "retention_days": 30})
	return audit.NewAsyncSink(fl, 65536)
}

// initWorkspaceAuditExporter constructs the optional enterprise audit-export
// adapter that workspace handlers mirror sanitized events into (Phase 4A). It
// is enabled by LEVARA_WORKSPACE_AUDIT_EXPORT (truthy) — the same flag profile
// validation reads via auditSinkConfigured, so "export configured" stays a
// single source of truth. Events are written as daily-rolled, gzipped JSONL
// under LEVARA_WORKSPACE_AUDIT_EXPORT_DIR, defaulting to <dataDir>/audit/
// workspace; retention is LEVARA_WORKSPACE_AUDIT_RETENTION_DAYS (default 30).
//
// Returns nil when disabled or on init failure — a broken audit sink must never
// abort startup, matching the "audit failures are observable but do not break
// core operations" acceptance. The returned *audit.AsyncExporter is both the
// EventSink to wire into APIConfig.WorkspaceAuditSink and the closer to drain
// on shutdown.
func initWorkspaceAuditExporter(dataDir string, log *observe.Logger) *audit.AsyncExporter {
	if !truthyEnv("LEVARA_WORKSPACE_AUDIT_EXPORT") {
		return nil
	}
	dir := strings.TrimSpace(os.Getenv("LEVARA_WORKSPACE_AUDIT_EXPORT_DIR"))
	if dir == "" {
		dir = filepath.Join(dataDir, "audit", "workspace")
	}
	retention := intEnv("LEVARA_WORKSPACE_AUDIT_RETENTION_DAYS", 30)
	exp, err := audit.NewJSONLExporter(dir, retention, audit.ExportConfig{})
	if err != nil {
		log.Warn("workspace_audit_export_init_failed", map[string]any{"dir": dir, "err": err.Error()})
		return nil
	}
	log.Info("workspace_audit_export_ready", map[string]any{"dir": dir, "retention_days": retention})
	return exp
}

// evaluateRuntimeProfile is the pure decision core for profile validation: it
// returns the findings to log and whether startup must fail fast. In strict
// mode the profile requirements are error-level and any of them is fatal; the
// default warn-only mode is never fatal. Kept side-effect-free so it can be
// unit-tested without a logger or a running server.
func evaluateRuntimeProfile(cfg profile.Config, strict bool) (findings []profile.Finding, fatal bool) {
	if strict {
		findings = profile.ValidateStrict(cfg)
		return findings, profile.HasError(findings)
	}
	return profile.Validate(cfg), false
}

// enforceRuntimeProfile logs each finding (error-level findings via Error,
// warnings via Warn) and reports whether strict mode demands a fail-fast exit.
// The caller owns the os.Exit so this stays testable.
func enforceRuntimeProfile(log *observe.Logger, cfg profile.Config, strict bool) bool {
	findings, fatal := evaluateRuntimeProfile(cfg, strict)
	for _, finding := range findings {
		fields := map[string]any{
			"profile": profile.Normalize(cfg.Profile),
			"code":    finding.Code,
			"message": finding.Message,
			"strict":  strict,
		}
		if finding.Level == profile.LevelError {
			log.Error("runtime_profile_error", nil, fields)
		} else {
			log.Warn("runtime_profile_warning", fields)
		}
	}
	return fatal
}

func runtimeDBProvider(db *sql.DB) string {
	if db == nil {
		return ""
	}
	if os.Getenv("DB_PROVIDER") == "sqlite" {
		return "sqlite"
	}
	return "postgres"
}

// buildRuntimeProfileConfig assembles the profile.Config validated at startup
// from the resolved runtime facts plus the relevant env vars. Phase 3B moved
// this off the main() call path so the env→profile-fact mapping is a single
// named, unit-testable seam (the "ProfileConfig" group) instead of a literal
// buried in startup wiring. It takes the already-resolved DB handle, the auth
// flag, and the MCP audit path so the env reads stay co-located here.
func buildRuntimeProfileConfig(db *sql.DB, requireAuth bool, mcpAuditPath string) profile.Config {
	cfg := profileConfigEnvFacts(requireAuth, mcpAuditPath)
	cfg.DBProvider = runtimeDBProvider(db)
	cfg.HasDB = db != nil
	return cfg
}

// profileConfigEnvFacts fills the profile.Config fields derived purely from env
// vars plus the auth flag and the MCP audit path. It is the single env→profile
// mapping shared by the live-startup path (buildRuntimeProfileConfig) and the
// -config-check dry run (buildConfigCheckProfileConfig); the two differ only in
// how the DB-derived fields (DBProvider, HasDB) are resolved, which the callers
// set afterwards.
func profileConfigEnvFacts(requireAuth bool, mcpAuditPath string) profile.Config {
	return profile.Config{
		Profile:             os.Getenv("LEVARA_PROFILE"),
		RequireAuth:         requireAuth,
		JWTSecretSet:        strings.TrimSpace(os.Getenv("JWT_SECRET")) != "",
		SyncEnabled:         strings.TrimSpace(os.Getenv("LEVARA_SYNC_REMOTE_URL")) != "",
		SyncTokenSet:        strings.TrimSpace(os.Getenv("LEVARA_TOKEN")) != "",
		TenantEnforced:      truthyEnv("LEVARA_TENANT_ENFORCED"),
		AuditSinkSet:        auditSinkConfigured(mcpAuditPath),
		SSOBridgeConfigured: ssoBridgeConfigured(),
	}
}

// configCheckDBProvider derives the DB provider for the -config-check dry run
// from env alone (no live handle): "sqlite" when explicitly selected, otherwise
// "postgres" — the bootstrap default that initSQLRuntime would resolve.
func configCheckDBProvider() string {
	if os.Getenv("DB_PROVIDER") == "sqlite" {
		return "sqlite"
	}
	return "postgres"
}

// buildConfigCheckProfileConfig assembles the profile.Config for the
// -config-check dry run from env + flags only. Unlike buildRuntimeProfileConfig
// it never opens a database: it validates the *declared* configuration
// (DBProvider from DB_PROVIDER, HasDB assumed true) so an operator can verify a
// profile preset before any service — DB, embedder, listeners — is brought up.
func buildConfigCheckProfileConfig(requireAuth bool, mcpAuditPath string) profile.Config {
	cfg := profileConfigEnvFacts(requireAuth, mcpAuditPath)
	cfg.DBProvider = configCheckDBProvider()
	cfg.HasDB = true
	return cfg
}

// runConfigCheck implements the -config-check dry run: it resolves the runtime
// profile config from env + flags, evaluates it (strict or warn-only), writes a
// human-readable report to w, and returns a process exit code — 0 when the
// configuration is acceptable, 1 when strict mode finds a fatal error. It opens
// no listeners, no DB connection, and makes no network calls, so it is safe to
// run anywhere, including CI and the `make profile-smoke` target.
func runConfigCheck(w io.Writer, requireAuth bool, mcpAuditPath string, strict bool) int {
	cfg := buildConfigCheckProfileConfig(requireAuth, mcpAuditPath)
	findings, fatal := evaluateRuntimeProfile(cfg, strict)
	fmt.Fprintf(w, "profile: %s (strict=%v, db_provider=%s, require_auth=%v)\n",
		profile.Normalize(cfg.Profile), strict, cfg.DBProvider, cfg.RequireAuth)
	for _, f := range findings {
		fmt.Fprintf(w, "  [%s] %s: %s\n", f.Level, f.Code, f.Message)
	}
	if fatal {
		fmt.Fprintln(w, "config-check: FAIL (strict-mode profile errors)")
		return 1
	}
	fmt.Fprintln(w, "config-check: OK")
	return 0
}

// ssoBridgeConfigured reports whether an enterprise identity bridge (OIDC/SAML)
// is wired, gated by LEVARA_SSO_BRIDGE. It satisfies the enterprise auth
// requirement in profile validation in lieu of local required auth — an
// SSO-fronted deployment authenticates at the bridge. The flag only feeds
// validation today; no bridge is instantiated until the protocol adapters land.
func ssoBridgeConfigured() bool {
	return truthyEnv("LEVARA_SSO_BRIDGE")
}

// auditSinkConfigured mirrors initMCPAuditSink's enabling rule for profile
// validation: an audit sink is considered present unless the MCP audit path is
// explicitly disabled ("-"), with the default ("") path counting only when
// workspace audit export is independently turned on.
func auditSinkConfigured(mcpAuditPath string) bool {
	return mcpAuditPath != "-" && (mcpAuditPath != "" || truthyEnv("LEVARA_WORKSPACE_AUDIT_EXPORT"))
}

func truthyEnv(key string) bool {
	v := strings.TrimSpace(os.Getenv(key))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// probeEmbedService tries a handful of well-known health paths under the
// embed endpoint's base URL. Ollama uses /api/tags; vLLM and OpenAI use
// /v1/models; some deployments expose /health. We accept any 200.
func probeEmbedService(embedEndpoint, embedModel string) fiber.Map {
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
	for _, path := range []string{"/api/tags", "/health", "/v1/models", ""} {
		resp, err := http.Get(embedBase + path)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return fiber.Map{"status": "connected", "endpoint": embedEndpoint, "model": embedModel}
			}
		}
	}
	return fiber.Map{"status": "unreachable", "endpoint": embedEndpoint, "model": embedModel}
}
