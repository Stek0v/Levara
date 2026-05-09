// bootstrap.go — main() helpers, split out of main.go (ARCH-1).
//
// main() stays linear and readable as a sequencing scaffold; the chunky
// wiring blocks live here. Each helper takes its dependencies explicitly
// so the call sites in main read like a recipe.
package main

import (
	"context"
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

	"github.com/gofiber/fiber/v2"
	"google.golang.org/grpc"

	"github.com/stek0v/levara/internal/cluster"
	vectorGrpc "github.com/stek0v/levara/internal/grpc"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/embed"
	"github.com/stek0v/levara/pkg/graphdb"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/llmcache"
	"github.com/stek0v/levara/pkg/llmproxy"
	"github.com/stek0v/levara/pkg/observe"
	pb "github.com/stek0v/levara/proto/pb"
	pbv2 "github.com/stek0v/levara/proto/pb/v2"
)

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
func startGRPCServer(port int, jwtSecret string, requireAuth bool, svc *vectorGrpc.Service) *grpc.Server {
	if port <= 0 {
		return nil
	}
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
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
		log.Printf("gRPC server listening on port %d", port)
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

// startEmbedKeepAlive pings the embedding endpoint every 10 minutes so
// Ollama / vLLM doesn't evict the model from VRAM during quiet periods.
// No-op when embedEndpoint is empty.
func startEmbedKeepAlive(embedEndpoint, embedModel string) {
	if embedEndpoint == "" {
		return
	}
	go func() {
		client := embed.NewClient(embedEndpoint, embedModel, 1, 1)
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if _, err := client.EmbedSingle(ctx, "keepalive"); err != nil {
				log.Printf("[keepalive] embed ping failed: %v", err)
			}
			cancel()
		}
	}()
	log.Printf("Embed keep-alive started (ping every 10min)")
}

// installGracefulShutdown blocks-on-signal in a background goroutine and
// flushes shards + collection manager + DB pool before asking Fiber to
// shut down. Decoupled from main so the signal handling order is one
// place to audit.
func installGracefulShutdown(app *fiber.App, shards []store.ShardHandler, colManager *store.CollectionManager, pgDB closer, grpcServer *grpc.Server) {
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
		if grpcServer != nil {
			grpcServer.GracefulStop()
		}
		log.Println("All shards flushed and closed")
		_ = app.Shutdown()
	}()
}

// closer is the minimum interface graceful shutdown needs from pgDB —
// keeps the helper from importing database/sql just for *sql.DB.
type closer interface{ Close() error }

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

		if deps.pgDB != nil {
			if err := deps.pgDB.Ping(); err == nil {
				services["postgres"] = fiber.Map{"status": "connected"}
			} else {
				services["postgres"] = fiber.Map{"status": "error", "error": err.Error()}
			}
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

		services["collections"] = fiber.Map{"status": "ready", "count": len(deps.colManager.List()), "dimension": deps.dim}
		services["grpc"] = fiber.Map{"status": "listening", "port": deps.grpcPort}

		return ctx.JSON(fiber.Map{"services": services})
	})
}

// healthDeps is the bundle of shared deps the verbose health endpoint
// needs. Defined here so the registration site is one parameter rather
// than a long positional list.
type healthDeps struct {
	port           int
	grpcPort       int
	dim            int
	pgDB           interface{ Ping() error }
	neo4jURL       string
	neo4jUser      string
	neo4jPassword  string
	neo4jDatabase  string
	embedEndpoint  string
	embedModel     string
	llmProvider    llm.Provider
	colManager     *store.CollectionManager
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
