// mcp.go — Model Context Protocol (MCP) Streamable HTTP server (spec 2025-03-26).
// Implements JSON-RPC 2.0 with session management, SSE streaming, and 15 tools.
// Compatible with Claude Code, Cursor, Cline, and any MCP client.
//
// Transport: Streamable HTTP (preferred)
//
//	POST /mcp — JSON-RPC requests + notifications
//	GET  /mcp — SSE stream for server-initiated messages
//	DELETE /mcp — terminate session
//
// Session management via Mcp-Session-Id header.
package http

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/metrics"
	"github.com/stek0v/levara/pipeline"
	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/mcp"
	"github.com/stek0v/levara/pkg/orchestrator"
	"github.com/stek0v/levara/pkg/rerank"
	"github.com/stek0v/levara/pkg/router"
	"github.com/stek0v/levara/pkg/runreg"
)

// F-4 wave 1b: the canonical type definitions live in pkg/mcp now. Local
// names below are type aliases that preserve every existing call-site inside
// this package unchanged while the handler migration continues in later
// waves. When the handler itself moves to pkg/mcp the aliases can be dropped.
type (
	jsonRPCRequest  = mcp.JSONRPCRequest
	jsonRPCResponse = mcp.JSONRPCResponse
	rpcError        = mcp.RPCError
	mcpContent      = mcp.Content
	mcpToolResult   = mcp.ToolResult
)

const mcpUserIDKey = mcp.UserIDKey

// ── Tool definitions ──

// mcpTools moved to pkg/mcp.ToolDescriptors() during F-4.

// RegisterMCPAPI registers MCP Streamable HTTP endpoint (spec 2025-03-26).
// POST /mcp — JSON-RPC requests + notifications
// GET  /mcp — SSE stream for server-initiated messages
// DELETE /mcp — terminate session
func RegisterMCPAPI(app fiber.Router, cfg APIConfig) {
	store := mcp.NewSessionStore()
	store.OnCountChange = func(n int) {
		metrics.MCPSessionsActive.Set(float64(n))
	}
	handler := &mcpHandler{
		cfg:      cfg,
		sessions: store,
	}
	app.Post("/mcp", handler.handleRPC)
	app.Get("/mcp", handler.handleSSEStream)
	app.Delete("/mcp", handler.handleDeleteSession)
	go handler.sessionCleanupLoop()
}

// NewMCPDeps builds a server-scoped mcp.Deps from an APIConfig, independent of
// any HTTP/SSE session. Used by background workers (e.g. the consolidation
// janitor) that must call MCP tool adapters outside a request lifecycle.
func NewMCPDeps(cfg APIConfig) mcp.Deps {
	return &mcpHandler{cfg: cfg, sessions: mcp.NewSessionStore()}
}

// mcpSession is a type alias for the canonical mcp.Session — all session
// state and lifecycle now lives in pkg/mcp (F-4 wave 2). See pkg/mcp/session.go.
type mcpSession = mcp.Session

type mcpHandler struct {
	cfg      APIConfig
	sessions *mcp.SessionStore
}

// DB implements mcp.Deps: exposes the shared *sql.DB to tool functions
// that have migrated into pkg/mcp. May return nil when no PostgresDSN
// is configured.
func (h *mcpHandler) DB() *sql.DB { return h.cfg.DB }

// Q implements mcp.Deps: forwards to the package-level Q() so tools in
// pkg/mcp stay agnostic of internal/http's sqlcompat state.
func (h *mcpHandler) Q(query string) string { return Q(query) }

// HasCollections implements mcp.Deps: true iff a vector-collection
// manager is configured on this handler's APIConfig.
func (h *mcpHandler) HasCollections() bool { return h.cfg.Collections != nil }

// ListCollections implements mcp.Deps: returns the registered
// collection names, or nil if no manager is configured.
func (h *mcpHandler) ListCollections() []string {
	if h.cfg.Collections == nil {
		return nil
	}
	return h.cfg.Collections.List()
}

// StoragePath implements mcp.Deps: returns the on-disk directory for
// ingested files. Empty string is returned as-is; the tool layer
// applies the legacy "data/uploads" default.
func (h *mcpHandler) StoragePath() string { return h.cfg.StoragePath }

// CollectionExists implements mcp.Deps: true iff a collection with
// the given name is registered in the CollectionManager. Always false
// when no manager is configured.
func (h *mcpHandler) CollectionExists(name string) bool {
	return h.cfg.Collections != nil && h.cfg.Collections.Has(name)
}

// EmbedAvailable implements mcp.Deps: true iff both the embed service
// URL and the CollectionManager are configured. Memory tools gate
// their vector-index path on this check.
func (h *mcpHandler) EmbedAvailable() bool {
	return h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil
}

// Embed implements mcp.Deps: single-text embedding via the configured
// embed service. Batch + concurrency are 1 since MCP tool calls drive
// one vector at a time.
//
// Nil-guard: callers are supposed to check EmbedAvailable() first, but a
// misconfigured APIConfig (e.g. EmbedEndpoint set without wiring
// EmbedClient in main) would otherwise nil-panic here. Fail closed with
// an error so the tool layer can surface a proper failure instead.
func (h *mcpHandler) Embed(ctx context.Context, text string) ([]float32, error) {
	if h.cfg.EmbedClient == nil {
		return nil, fmt.Errorf("embed client not configured")
	}
	return h.cfg.EmbedClient.EmbedSingle(ctx, text)
}

// EmbedBatch implements mcp.Deps: reuses the shared embedding client for
// a multi-text call. Used by tool_codify to embed entity descriptions in
// one round-trip instead of constructing a fresh client per invocation.
func (h *mcpHandler) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if h.cfg.EmbedClient == nil {
		return nil, fmt.Errorf("embed client not configured")
	}
	return h.cfg.EmbedClient.EmbedTexts(ctx, texts)
}

// CollectionInsert implements mcp.Deps: forwards to the shared
// CollectionManager. Callers are expected to have guarded on
// EmbedAvailable(); we still return an error rather than panicking
// to keep the surface honest.
func (h *mcpHandler) CollectionInsert(collection, id string, vec []float32, meta any) error {
	if h.cfg.Collections == nil {
		return fmt.Errorf("collections not configured")
	}
	return h.cfg.Collections.Insert(collection, id, vec, meta)
}

// CollectionDelete implements mcp.Deps: tombstones a record by id in the
// given collection (same path as reconcile_memory's orphan-vector cleanup).
// Used by delete_memory to drop a memory's vector sidecar entry so it stops
// surfacing in recall. Returns an error only when collections are
// unconfigured — a missing id resolves to a no-op inside the manager.
func (h *mcpHandler) CollectionDelete(collection, id string) error {
	if h.cfg.Collections == nil {
		return fmt.Errorf("collections not configured")
	}
	return h.cfg.Collections.Delete(collection, id)
}

// CollectionHasRecord implements mcp.Deps: synchronous by-id membership
// check (CollectionManager.HasRecord). Reflects the write the moment
// CollectionInsert returns, so the memory write path can verify the
// vector actually landed without racing the async HNSW indexer.
func (h *mcpHandler) CollectionHasRecord(collection, id string) bool {
	if h.cfg.Collections == nil {
		return false
	}
	return h.cfg.Collections.HasRecord(collection, id)
}

// CollectionSearch implements mcp.Deps: forwards to the shared
// CollectionManager and adapts the internal VectroRecord type to
// pkg/mcp.SearchResult so tool bodies don't need to import
// internal/store.
func (h *mcpHandler) CollectionSearch(collection string, query []float32, topK int) ([]mcp.SearchResult, error) {
	if h.cfg.Collections == nil {
		return nil, fmt.Errorf("collections not configured")
	}
	records, err := h.cfg.Collections.Search(collection, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.SearchResult, 0, len(records))
	for _, r := range records {
		out = append(out, mcp.SearchResult{
			ID:    r.ID,
			Score: r.Score,
			Data:  []byte(r.Data),
		})
	}
	return out, nil
}

// Runs implements mcp.Deps: returns the shared pipeline-run registry.
// Configured in cmd/server/main.go; a single *runreg.Registry is handed
// to both RegisterAPI and RegisterMCPAPI so MCP-initiated runs and
// REST-initiated runs share the same map.
func (h *mcpHandler) Runs() *runreg.Registry { return h.cfg.Runs }

// BaseCognifyConfig implements mcp.Deps: builds an orchestrator.Config
// pre-populated with every deployment-level field the cognify pipeline
// needs. The MCP tool body then overrides per-call fields (Collection,
// DatasetID, Room, Tags, SystemPrompt, SkipGraph, chunking knobs) before
// passing the result to RunPipeline.
// EmbedEndpoint returns the deployment-wide embed URL. Narrow accessor
// added in T6 so tools that only need this single setting don't have to
// materialise the full orchestrator.Config.
func (h *mcpHandler) EmbedEndpoint() string { return h.cfg.EmbedEndpoint }

// EmbedModel returns the deployment-wide embed model name (T6).
func (h *mcpHandler) EmbedModel() string { return h.cfg.EmbedModel }

func (h *mcpHandler) BaseCognifyConfig() orchestrator.Config {
	return orchestrator.Config{
		ChunkStrategy:  "merged",
		MinChunkChars:  50,
		MaxChunkChars:  2000,
		LLMEndpoint:    os.Getenv("LLM_ENDPOINT"),
		LLMModel:       os.Getenv("LLM_MODEL"),
		LLMProvider:    h.cfg.LLMProvider,
		BM25Indexes:    h.cfg.BM25Indexes,
		LLMConcurrency: 1,
		EmbedEndpoint:  h.cfg.EmbedEndpoint,
		EmbedModel:     h.cfg.EmbedModel,
		EmbedClient:    h.cfg.EmbedClient, // BL-1: reuse shared TCP pool inside pipeline
		Neo4jURL:       h.cfg.Neo4jCfg.Neo4jURL,
		Neo4jUser:      h.cfg.Neo4jCfg.Neo4jUser,
		Neo4jPassword:  h.cfg.Neo4jCfg.Neo4jPassword,
		Neo4jDatabase:  h.cfg.Neo4jCfg.Neo4jDatabase,
		Collections:    h.cfg.Collections,
		DB:             h.cfg.DB,
		LLMCache:       h.cfg.LLMCache,
	}
}

// OntologyPromptSuffix implements mcp.Deps: forwards to the package-level
// helper in ontologies.go. Empty string when the collection has no
// ontology configured — tool code concatenates unconditionally.
func (h *mcpHandler) OntologyPromptSuffix(collection string) string {
	return GetOntologyPromptSuffix(collection)
}

// PersistPipelineStatus implements mcp.Deps: forwards to the package-level
// helper in api.go so REST and MCP share the same skip-if-done logic.
// DB may be nil — the helper no-ops in that case.
func (h *mcpHandler) PersistPipelineStatus(datasetID, collection, status string, chunks, entities, edges int, elapsedMs int64) {
	PersistPipelineStatus(h.cfg.DB, datasetID, collection, status, chunks, entities, edges, elapsedMs)
}

// LogHeartbeat implements mcp.Deps: forwards to the handler's own
// heartbeat logger (in mcp_doctor.go). Defined on *mcpHandler to reach
// the DB through cfg.
//
// The "memory_index_divergence" event type is the SQL↔vector consistency
// signal emitted by the memory write path (pkg/mcp stays free of an
// internal/metrics import — this is the seam). We mirror it to Prometheus
// here so the divergence is both queryable (heartbeats table) and alertable.
func (h *mcpHandler) LogHeartbeat(eventType string, payload any) {
	if eventType == "memory_index_divergence" {
		reason := "unknown"
		if m, ok := payload.(map[string]any); ok {
			if r, ok := m["reason"].(string); ok && r != "" {
				reason = r
			}
		}
		metrics.MemoryIndexDivergence.WithLabelValues(reason).Inc()
	}
	h.logHeartbeat(eventType, payload)
}

// RunPipeline implements mcp.Deps: production wiring simply delegates to
// orchestrator.Run. The seam exists so tests in pkg/mcp can exercise the
// cognify goroutine's post-run bookkeeping without spinning up the real
// LLM + embed stack.
func (h *mcpHandler) RunPipeline(ctx context.Context, texts []string, cfg orchestrator.Config, progress chan<- orchestrator.Progress) error {
	return orchestrator.Run(ctx, texts, cfg, progress)
}

// searchPipelineAdapter wraps the concrete *pipeline.SearchPipeline
// plus its optional *rerank.Client into the mcp.SearchPipeline seam.
// Production NewSearchPipeline returns one of these; tests bypass it.
type searchPipelineAdapter struct {
	sp           *pipeline.SearchPipeline
	rerankClient *rerank.Client
	rerankCfg    pipeline.ApplyRerankConfig
}

func (a *searchPipelineAdapter) SearchByText(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
	return a.sp.SearchByText(ctx, coll, query, topK)
}

func (a *searchPipelineAdapter) SearchByTextParentChild(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, error) {
	return a.sp.SearchByTextParentChild(ctx, coll, query, topK)
}

func (a *searchPipelineAdapter) SearchByTextMultiQuery(ctx context.Context, coll, query string, topK int, provider llm.Provider, model string, n int) ([]pipeline.ScoredResult, error) {
	return a.sp.SearchByTextMultiQuery(ctx, coll, query, topK, provider, model, n)
}

func (a *searchPipelineAdapter) ApplyRerank(ctx context.Context, query string, in []pipeline.ScoredResult, topK int) (bool, []pipeline.ScoredResult) {
	return pipeline.ApplyRerankToScored(ctx, a.rerankCfg, a.rerankClient, query, in, topK)
}

func (a *searchPipelineAdapter) RerankEnabled() bool {
	return a.rerankClient != nil && a.rerankClient.Enabled()
}

// NewSearchPipeline implements mcp.Deps: builds embed + rerank clients
// and the *pipeline.SearchPipeline, wraps them in the adapter. Returns
// nil when the embed service or collection manager is unconfigured —
// tool code treats nil as "no results (embedding service not
// configured)".
func (h *mcpHandler) NewSearchPipeline(doRerank bool) mcp.SearchPipeline {
	// EmbedClient nil-check as well as endpoint — misconfigured APIConfig
	// (endpoint set but client forgotten in main wiring) should return nil
	// rather than yield a pipeline that'll nil-panic on first .SearchByText.
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil || h.cfg.EmbedClient == nil {
		return nil
	}
	embedClient := h.cfg.EmbedClient
	var rerankClient *rerank.Client
	if doRerank {
		rerankClient = rerank.NewClient(h.cfg.RerankEndpoint, h.cfg.RerankModel, 0, h.cfg.RerankTimeoutMs)
	}
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections, rerankClient)
	return &searchPipelineAdapter{
		sp:           sp,
		rerankClient: rerankClient,
		rerankCfg: pipeline.ApplyRerankConfig{
			BudgetMs:          h.cfg.RerankBudgetMs,
			ScoreGapThreshold: h.cfg.RerankScoreGapThreshold,
		},
	}
}

// LLMProvider implements mcp.Deps: forwards the cfg field for the
// multi-query search branch. Nil is acceptable — callers gate on it.
func (h *mcpHandler) LLMProvider() llm.Provider { return h.cfg.LLMProvider }

// LLMModel implements mcp.Deps: returns the active LLM model name
// from the environment (matching REST's cognify). Empty when unset.
func (h *mcpHandler) LLMModel() string { return os.Getenv("LLM_MODEL") }

// SearchCapabilities implements mcp.Deps: forwards to the package-level
// capabilitiesFromConfig helper in api.go. Potentially issues a DB
// query to detect communities; kept as a separate method so tool code
// can cache the result when it uses it multiple times.
func (h *mcpHandler) SearchCapabilities() router.Capabilities {
	return capabilitiesFromConfig(h.cfg)
}

// AllowedDatasetIDs implements mcp.Deps: resolves the caller's dataset/project
// scopes from the per-call MCP context. Nil means no filtering, matching REST.
func (h *mcpHandler) AllowedDatasetIDs(ctx context.Context) []string {
	userID, _ := ctx.Value(mcpUserIDKey).(string)
	return GetAllowedDatasetIDs(h.cfg.DB, ctx, userID)
}

// ListLexicalCollections implements mcp.Deps: returns collections with a BM25
// index, independent of whether the vector CollectionManager is configured.
func (h *mcpHandler) ListLexicalCollections() []string {
	if h.cfg.BM25Indexes == nil {
		return nil
	}
	names := make([]string, 0, len(h.cfg.BM25Indexes))
	for name := range h.cfg.BM25Indexes {
		names = append(names, name)
	}
	return names
}

// LexicalSearch implements mcp.Deps: forwards to the shared BM25 index map.
func (h *mcpHandler) LexicalSearch(collection, query string, topK int) ([]mcp.LexicalResult, error) {
	if h.cfg.BM25Indexes == nil {
		return nil, nil
	}
	idx := h.cfg.BM25Indexes[collection]
	if idx == nil {
		return nil, nil
	}
	results := idx.Search(query, topK)
	out := make([]mcp.LexicalResult, 0, len(results))
	for _, r := range results {
		out = append(out, mcp.LexicalResult{
			ID:       r.ID,
			Score:    r.Score,
			Metadata: []byte(r.Metadata),
		})
	}
	return out, nil
}

// DoSync implements mcp.Deps: orchestrates a bidirectional sync operation
// with a remote Levara instance, wrapping all the internal/http sync helpers
// (SyncManifestFromRemote, SyncPull, syncPush, syncPullCollections,
// syncPushCollections) so pkg/mcp doesn't need to know about APIConfig or
// *store.CollectionManager. Added in F-4 wave 3q for toolSync.
func (h *mcpHandler) DoSync(ctx context.Context, remoteURL, direction string, types []string, since string, collections []string) (map[string]any, map[string]any, error) {
	rawManifest, err := SyncManifestFromRemote(remoteURL, h.cfg.SyncToken)
	if err != nil {
		return nil, nil, err
	}
	// Convert *syncManifest to map[string]any by round-tripping through JSON
	// so the tool body stays type-clean (no internal/http types in pkg/mcp).
	var manifest map[string]any
	if rawManifest != nil {
		if b, err := json.Marshal(rawManifest); err == nil {
			json.Unmarshal(b, &manifest)
		}
	}

	// Version skew check (warn-and-continue): a binary mismatch between the
	// two instances can mean incompatible schema/protocol. We don't block the
	// sync — only surface a warning so the operator can investigate.
	var versionWarning string
	if rawManifest != nil && rawManifest.Version != "" && h.cfg.Version != "" &&
		rawManifest.Version != h.cfg.Version {
		versionWarning = fmt.Sprintf("Levara version mismatch: local=%s remote=%s — proceeding with sync",
			h.cfg.Version, rawManifest.Version)
		log.Printf("[sync] %s", versionWarning)
	}

	var result map[string]any
	if direction == "pull" {
		result = SyncPull(h.cfg, remoteURL, types, since)
		if containsType(types, "collections") && len(collections) > 0 {
			result["collections_sync"] = syncPullCollections(h.cfg, remoteURL, collections)
		}
	} else {
		result = syncPush(ctx, h.cfg, remoteURL, types, since)
		if containsType(types, "collections") && len(collections) > 0 {
			result["collections_sync"] = syncPushCollections(ctx, h.cfg, remoteURL, collections)
		}
	}
	if versionWarning != "" {
		if result == nil {
			result = map[string]any{}
		}
		result["version_warning"] = versionWarning
	}
	return result, manifest, nil
}

// CollectionMeta implements mcp.Deps: returns observable metadata for the
// named collection without exposing the internal/store.CollectionMeta pointer
// to pkg/mcp. Returns zero CollectionInfo when the collection manager is nil
// or the collection doesn't exist. Added in F-4 wave 3o for
// toolGetProjectContext and toolCheckDrift.
func (h *mcpHandler) CollectionMeta(name string) mcp.CollectionInfo {
	if h.cfg.Collections == nil {
		return mcp.CollectionInfo{}
	}
	m := h.cfg.Collections.GetMeta(name)
	if m == nil {
		return mcp.CollectionInfo{}
	}
	return mcp.CollectionInfo{
		Name:       m.Name,
		Records:    m.RecordCount,
		Dim:        m.EmbeddingDim,
		Metric:     m.DistanceMetric,
		EmbedModel: m.EmbeddingModel,
	}
}

// getOrValidateSession returns the session for the given ID, or nil if invalid.
func (h *mcpHandler) getOrValidateSession(sessionID string) *mcpSession {
	return h.sessions.Get(sessionID)
}

// createSession creates a new MCP session and returns its ID.
func (h *mcpHandler) createSession(userID string) string {
	sess := h.sessions.Create()
	sess.UserID = userID
	return sess.ID
}

// adoptSession re-establishes a session under a client-supplied id (e.g. one
// replayed after a backend restart wiped the in-memory store), binding its
// owner to userID when known. Returns the session — existing or freshly
// adopted. See SessionStore.Adopt for the lifecycle rationale.
func (h *mcpHandler) adoptSession(sessionID, userID string) *mcpSession {
	sess := h.sessions.Adopt(sessionID)
	if userID != "" {
		sess.UserID = userID
	}
	return sess
}

func (h *mcpHandler) authenticateMCPRequest(c *fiber.Ctx) (string, error) {
	if apiKey := firstNonEmpty(c.Get("X-API-Key"), c.Get("X-Api-Key")); apiKey != "" {
		if h.cfg.DB == nil {
			return "", fmt.Errorf("database required for API key auth")
		}
		id := verifyAPIKey(h.cfg.DB, apiKey)
		if !id.Valid() {
			return "", fmt.Errorf("invalid API key")
		}
		return id.UserID, nil
	}

	token := bearerToken(c.Get("Authorization"))
	if token == "" {
		token = c.Cookies("auth_token")
	}
	if token == "" {
		if h.cfg.RequireAuth {
			return "", fmt.Errorf("authorization required")
		}
		return "", nil
	}
	if h.cfg.JWTSecret == "" {
		return "", fmt.Errorf("JWT secret not configured")
	}
	payload, ok := verifyJWT(token, h.cfg.JWTSecret)
	if !ok {
		return "", fmt.Errorf("invalid token")
	}
	return payload.Sub, nil
}

func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	token := strings.TrimPrefix(header, "Bearer ")
	if token == "null" || token == "undefined" {
		return ""
	}
	return token
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// deleteSession removes a session.
func (h *mcpHandler) deleteSession(id string) {
	h.sessions.Delete(id)
}

// randomHex moved to pkg/mcp.RandomHex.

func (h *mcpHandler) handleRPC(c *fiber.Ctx) error {
	var req jsonRPCRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		})
	}

	// Notifications (no "id") → 202 Accepted, no body
	if req.ID == nil || string(req.ID) == "null" {
		// Handle known notifications silently
		switch req.Method {
		case "notifications/initialized", "notifications/cancelled":
			// acknowledged
		}
		return c.SendStatus(202)
	}

	// Auth + session gate for non-initialize requests. Authenticate FIRST,
	// unconditionally — a request that omits the session header is no more
	// trusted than one replaying a stale id, so both must clear the same
	// gate (otherwise an anonymous caller under require-auth could reach
	// tools/list and tools/call simply by NOT sending a session id). When
	// auth fails we 404 (client should re-initialize). `ping` is a transport
	// liveness probe and stays unauthenticated like `initialize`.
	//
	// A session-id the store doesn't recognise almost always means the
	// in-memory store was reset (a backend restart) and the client is
	// replaying a now-stale id. Rather than 404 (which the Claude Code MCP
	// client surfaces as a hang mid tool-call), we adopt the replayed id and
	// rebind its owner from the just-verified identity, making restarts
	// transparent.
	sessionID := c.Get("Mcp-Session-Id")
	if req.Method != "initialize" && req.Method != "ping" {
		userID, authErr := h.authenticateMCPRequest(c)
		if authErr != nil {
			return c.SendStatus(404) // can't establish an owner → client should re-initialize
		}
		if sessionID != "" && h.getOrValidateSession(sessionID) == nil {
			h.adoptSession(sessionID, userID)
		}
	}

	// Set session header on all responses
	if sessionID != "" {
		c.Set("Mcp-Session-Id", sessionID)
	}

	switch req.Method {
	case "initialize":
		userID, authErr := h.authenticateMCPRequest(c)
		if authErr != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32001, Message: authErr.Error()},
			})
		}
		sid := h.createSession(userID)
		c.Set("Mcp-Session-Id", sid)
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2025-03-26",
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{"subscribe": false, "listChanged": false},
				},
				"serverInfo": map[string]any{
					"name":    "levara",
					"version": "1.0.0",
				},
				"instructions": "Call the `levara_instructions` tool for the versioned agent contract (memory model, when-to-save rules, observability toolkit, anti-patterns). Contract revision: " + mcp.AgentContractVersion + ".",
			},
		})

	case "ping":
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})

	case "tools/list":
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": mcp.ToolDescriptors(),
			},
		})

	case "tools/call":
		return h.handleToolCall(c, req)

	case "resources/list":
		return h.handleResourcesList(c, req)

	case "resources/read":
		return h.handleResourcesRead(c, req)

	default:
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: fmt.Sprintf("Method not found: %s", req.Method)},
		})
	}
}

// resolveCollection is a thin shim around mcp.ResolveCollection kept here so
// existing call-sites inside this package don't need to change. The real
// logic lives in pkg/mcp/session.go now (F-4 wave).
func (h *mcpHandler) resolveCollection(sess *mcpSession, args map[string]any, forWrite bool) string {
	return mcp.ResolveCollection(sess, args, forWrite)
}

func (h *mcpHandler) handleToolCall(c *fiber.Ctx, req jsonRPCRequest) error {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32602, Message: "Invalid params"},
		})
	}

	// Resolve the owner for isolation. Prefer the request's own JWT/API-key —
	// it's authoritative and survives a session-store reset — and fall back to
	// the session binding only when the request carries no identity. This also
	// closes the owner_id='' footgun where a client that dropped its
	// Mcp-Session-Id would otherwise write records with no owner.
	toolCtx := context.Background()
	sessionID := c.Get("Mcp-Session-Id")
	sess := h.getOrValidateSession(sessionID)
	userID := ""
	if uid, err := h.authenticateMCPRequest(c); err == nil && uid != "" {
		userID = uid
	} else if sess != nil {
		userID = sess.UserID
	}
	if userID != "" {
		toolCtx = context.WithValue(toolCtx, mcpUserIDKey, userID)
		// Keep the session's owner in sync so audit/set_context agree with the
		// identity the tool actually ran as.
		if sess != nil && sess.UserID == "" {
			sess.UserID = userID
		}
	}

	result := h.executeTool(toolCtx, sess, params.Name, params.Arguments)
	return c.JSON(jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID, Result: result,
	})
}

func (h *mcpHandler) executeTool(ctx context.Context, sess *mcpSession, name string, args map[string]any) mcpToolResult {
	toolStart := time.Now()
	result := h.executeToolInner(ctx, sess, name, args)
	h.auditWorkspaceTool(ctx, name, args, result)
	duration := time.Since(toolStart)
	metrics.MCPToolDuration.WithLabelValues(name).Observe(duration.Seconds())
	status := "ok"
	if result.IsError {
		status = "error"
	}
	metrics.MCPToolRequests.WithLabelValues(name, status).Inc()
	h.recordMCPAudit(ctx, sess, name, args, result, duration)
	return result
}

// recordMCPAudit emits the per-call audit entry (P3.3) plus the
// outcome-labelled Prometheus counters. Both the JSONL writer and the
// new metrics live behind APIConfig so they can be disabled in tests
// without changing call-sites.
func (h *mcpHandler) recordMCPAudit(ctx context.Context, sess *mcpSession, name string, args map[string]any, result mcpToolResult, duration time.Duration) {
	outcome := classifyOutcome(result)
	agentID := ""
	sessionID := ""
	if sess != nil {
		agentID = sess.UserID
		sessionID = sess.ID
	}
	if agentID == "" {
		if userID, _ := ctx.Value(mcpUserIDKey).(string); userID != "" {
			agentID = userID
		}
	}
	agentBucket := "unknown"
	if h.cfg.MCPAgentBucket != nil {
		h.cfg.MCPAgentBucket.Observe(agentID)
		agentBucket = h.cfg.MCPAgentBucket.Label(agentID)
	}

	resultSize := resultPayloadSize(result)
	metrics.MCPToolCalls.WithLabelValues(name, agentBucket, string(outcome)).Inc()
	metrics.MCPToolLatencyMS.WithLabelValues(name, string(outcome)).Observe(float64(duration.Milliseconds()))
	metrics.MCPToolResultBytes.WithLabelValues(name).Observe(float64(resultSize))

	if h.cfg.MCPAudit == nil {
		return
	}
	entry := audit.Entry{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		SessionID:  sessionID,
		AgentID:    agentID,
		Tool:       name,
		Args:       audit.SanitizeArgs(args),
		LatencyMS:  duration.Milliseconds(),
		Outcome:    outcome,
		ResultSize: resultSize,
	}
	if outcome != audit.OutcomeOK && len(result.Content) > 0 {
		entry.ErrorMessage = truncateAuditField(result.Content[0].Text)
	}
	h.cfg.MCPAudit.Log(entry)
}

func classifyOutcome(r mcpToolResult) audit.Outcome {
	if !r.IsError {
		return audit.OutcomeOK
	}
	if len(r.Content) == 0 {
		return audit.OutcomeServerError
	}
	low := strings.ToLower(r.Content[0].Text)
	switch {
	case strings.Contains(low, "timeout"), strings.Contains(low, "deadline exceeded"):
		return audit.OutcomeTimeout
	case strings.Contains(low, "unauthorized"), strings.Contains(low, "forbidden"), strings.Contains(low, "permission denied"):
		return audit.OutcomeUnauthorized
	case strings.Contains(low, "rate limit"), strings.Contains(low, "too many requests"):
		return audit.OutcomeRateLimited
	case strings.Contains(low, "invalid"), strings.Contains(low, "missing"), strings.Contains(low, "bad request"):
		return audit.OutcomeClientError
	default:
		return audit.OutcomeServerError
	}
}

func resultPayloadSize(r mcpToolResult) int {
	var n int
	for _, c := range r.Content {
		n += len(c.Text)
	}
	return n
}

func truncateAuditField(s string) string {
	const lim = 256
	if len(s) <= lim {
		return s
	}
	return s[:lim] + "…"
}

func (h *mcpHandler) executeToolInner(ctx context.Context, sess *mcpSession, name string, args map[string]any) mcpToolResult {

	// Inject session default collection into args if not explicitly set (for collection-aware tools)
	switch name {
	case "cognify", "add", "save_chat":
		if _, ok := args["collection"]; !ok || args["collection"] == "" {
			args["collection"] = h.resolveCollection(sess, args, true)
		}
	case "save_memory", "recall_memory", "list_memories",
		"wake_up", "pin_memory", "unpin_memory",
		"diary_write", "diary_read", "consolidate":
		// Memory tools: only inject session default, NOT "default" fallback.
		// Empty collection → global _memories (backward compatible with Pi data).
		if _, ok := args["collection"]; !ok || args["collection"] == "" {
			if sess != nil && sess.DefaultCollection != "" {
				args["collection"] = sess.DefaultCollection
			}
			// else: leave empty → _memories (global, no suffix)
		}
	case "search", "recall_chat", "search_chats", "get_project_context":
		if _, ok := args["collection"]; !ok || args["collection"] == "" {
			if resolved := h.resolveCollection(sess, args, false); resolved != "" {
				args["collection"] = resolved
			}
		}
	}

	switch name {
	case "cognify":
		return h.toolCognify(ctx, args)
	case "search":
		return h.toolSearch(ctx, args)
	case "workspace_access_check":
		return h.toolWorkspaceAccessCheck(ctx, args)
	case "workspace_context":
		return h.toolWorkspaceContext(ctx, args)
	case "workspace_audit_log":
		return h.toolWorkspaceAuditLog(ctx, args)
	case "workspace_ops_status":
		return h.toolWorkspaceOpsStatus(ctx, args)
	case "workspace_context_artifacts":
		return h.toolWorkspaceContextArtifacts(ctx, args)
	case "workspace_reindex_artifacts":
		return h.toolWorkspaceReindexArtifacts(ctx, args)
	case "workspace_conflicts":
		return h.toolWorkspaceConflicts(ctx, args)
	case "workspace_search":
		return h.toolWorkspaceSearch(ctx, args)
	case "workspace_index":
		return h.toolWorkspaceIndex(ctx, args)
	case "workspace_read":
		return h.toolWorkspaceRead(ctx, args)
	case "workspace_write":
		return h.toolWorkspaceWrite(ctx, args)
	case "workspace_reindex_paths":
		return h.toolWorkspaceReindex(ctx, args)
	case "workspace_reconcile":
		return h.toolWorkspaceReconcile(ctx, args)
	case "workspace_index_jobs":
		return h.toolWorkspaceIndexJobs(ctx, args)
	case "workspace_enqueue_index_job":
		return h.toolWorkspaceEnqueueIndexJob(ctx, args)
	case "workspace_retry_index_job":
		return h.toolWorkspaceRetryIndexJob(ctx, args)
	case "workspace_watch_status":
		return h.toolWorkspaceWatchStatus(ctx, args)
	case "workspace_run_start":
		return h.toolWorkspaceRunStart(ctx, args)
	case "workspace_run_get":
		return h.toolWorkspaceRunGet(ctx, args)
	case "workspace_commit":
		return h.toolWorkspaceCommit(ctx, args)
	case "workspace_log":
		return h.toolWorkspaceLog(ctx, args)
	case "workspace_revert":
		return h.toolWorkspaceRevert(ctx, args)
	case "workspace_delete":
		return h.toolWorkspaceDelete(ctx, args)
	case "workspace_gc":
		return h.toolWorkspaceGC(ctx, args)
	case "workspace_manifest":
		return h.toolWorkspaceManifest(ctx, args)
	case "list_data":
		return h.toolListData(ctx, args)
	case "delete":
		return h.toolDelete(ctx, args)
	case "prune":
		return h.toolPrune(ctx)
	case "cognify_status":
		return h.toolCognifyStatus(args)
	case "list_communities":
		return h.toolListCommunities(ctx, args)
	case "check_drift":
		return h.toolCheckDrift(ctx, args)
	case "prune_graph":
		return h.toolPruneGraph(ctx, args)
	case "add":
		return h.toolAdd(ctx, args)
	case "analyze_commits":
		return h.toolAnalyzeCommits(ctx, args)
	case "git_search":
		return h.toolGitSearch(ctx, args)
	case "save_memory":
		return h.toolSaveMemory(ctx, args)
	case "recall_memory":
		return h.toolRecallMemory(ctx, args)
	case "list_memories":
		return h.toolListMemories(ctx, args)
	case "consolidate":
		return h.toolConsolidate(ctx, args)
	case "consolidation_revert":
		return h.toolConsolidationRevert(ctx, args)
	case "save_chat":
		return h.toolSaveChat(ctx, args)
	case "recall_chat":
		return h.toolRecallChat(ctx, args)
	case "search_chats":
		return h.toolSearchChats(ctx, args)
	case "get_project_context":
		return h.toolGetProjectContext(ctx, args)
	case "set_context":
		return h.toolSetContext(sess, args)
	case "cross_search":
		return h.toolCrossSearch(ctx, args)
	case "sync":
		return h.toolSync(ctx, args)
	case "add_feedback":
		return h.toolAddFeedback(ctx, args)
	case "get_feedback_stats":
		return h.toolGetFeedbackStats(ctx, args)
	case "codify":
		return h.toolCodify(ctx, args)
	case "wake_up":
		return h.toolWakeUp(ctx, args)
	case "pin_memory":
		return h.toolPinMemory(ctx, args)
	case "unpin_memory":
		return h.toolUnpinMemory(ctx, args)
	case "delete_memory":
		return h.toolDeleteMemory(ctx, args)
	case "query_entity":
		return h.toolQueryEntity(ctx, args)
	case "diary_write":
		return h.toolDiaryWrite(ctx, args)
	case "diary_read":
		return h.toolDiaryRead(ctx, args)
	case "doctor":
		return h.toolDoctor(ctx, args)
	case "heartbeat":
		return h.toolHeartbeat(ctx, args)
	case "runtime_stats":
		return h.toolRuntimeStats(ctx, args)
	case "ingestion_status":
		return h.toolIngestionStatus(ctx, args)
	case "recent_errors":
		return h.toolRecentErrors(ctx, args)
	case "reconcile_memory":
		return h.toolReconcileMemory(ctx, args)
	case "sync_status":
		return h.toolSyncStatus(ctx, args)
	case "levara_instructions":
		return mcp.ToolLevaraInstructions(ctx, h, args)
	default:
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Unknown tool: %s", name)}},
			IsError: true,
		}
	}
}

// toolCognify is a thin shim over mcp.ToolCognify. F-4 wave 3j moved the
// body (argument parsing, pipeline goroutine, post-run bookkeeping) into
// pkg/mcp — *mcpHandler satisfies the enlarged Deps interface via the
// wave-3j forwarders (Runs, BaseCognifyConfig, OntologyPromptSuffix,
// PersistPipelineStatus, LogHeartbeat, RunPipeline) defined above.
func (h *mcpHandler) toolCognify(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolCognify(ctx, h, args)
}

// toolSearch is a thin shim over mcp.ToolSearch. F-4 wave 3k moved the
// body (arg parsing, mode gating, AUTO routing, pipeline dispatch,
// dedup, metadata filter, topK cap, response marshal) into pkg/mcp.
// *mcpHandler satisfies the enlarged Deps interface via the wave-3k
// forwarders (NewSearchPipeline, LLMProvider, LLMModel,
// SearchCapabilities) defined above.
func (h *mcpHandler) toolSearch(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolSearch(ctx, h, args)
}

// toolListData is a thin shim over mcp.ToolListData. F-4 wave 3c moved
// the body into pkg/mcp; the filter parsing and SQL live in
// pkg/mcp/deps.go's listDataFiltered / listDataUnfiltered helpers.
func (h *mcpHandler) toolListData(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolListData(ctx, h, args)
}

// toolDelete is a thin shim over mcp.ToolDelete. F-4 wave 3a moved the
// body into pkg/mcp to establish the Deps-interface pattern; this wrapper
// stays until the handler itself migrates.
func (h *mcpHandler) toolDelete(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDelete(ctx, h, args)
}

// toolPrune is a thin shim over mcp.ToolPrune. F-4 wave 3b moved the
// body into pkg/mcp; the full table list lives in pkg/mcp/deps.go's
// pruneTables.
func (h *mcpHandler) toolPrune(ctx context.Context) mcpToolResult {
	return mcp.ToolPrune(ctx, h)
}

// toolCognifyStatus is a thin shim over mcp.ToolCognifyStatus. F-4 wave 3j.
func (h *mcpHandler) toolCognifyStatus(args map[string]any) mcpToolResult {
	return mcp.ToolCognifyStatus(h, args)
}

// toolAdd is a thin shim over mcp.ToolAdd. F-4 wave 3d moved the body
// into pkg/mcp; the ingest + metadata-write orchestration lives there.
func (h *mcpHandler) toolAdd(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolAdd(ctx, h, args)
}

// ── Git Commit Analyzer handlers ──

// toolAnalyzeCommits is a thin shim over mcp.ToolAnalyzeCommits. F-4
// wave 3m moved the body (git.ParseLog + optional cognify pipeline
// goroutine) into pkg/mcp. Reuses Runs/BaseCognifyConfig/LogHeartbeat
// from wave 3j; no new Deps methods.
func (h *mcpHandler) toolAnalyzeCommits(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolAnalyzeCommits(ctx, h, args)
}

// toolGitSearch is a thin shim over mcp.ToolGitSearch. F-4 wave 3m.
// Reuses NewSearchPipeline from wave 3k; hardcoded topK=10 against
// the git_commits collection lives in pkg/mcp.
func (h *mcpHandler) toolGitSearch(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolGitSearch(ctx, h, args)
}

// ── Project Memory handlers ──

// toolSaveMemory / toolRecallMemory are thin shims over pkg/mcp (F-4 wave 3i).
// First AI-seam tools: vector indexing (save) + semantic recall with SQL
// fallback (recall), both gated on EmbedAvailable() via Deps.
func (h *mcpHandler) toolSaveMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolSaveMemory(ctx, h, args)
}

func (h *mcpHandler) toolRecallMemory(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolRecallMemory(ctx, h, args)
}

// toolListMemories is a thin shim over mcp.ToolListMemories (F-4 wave 3f).
func (h *mcpHandler) toolListMemories(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolListMemories(ctx, h, args)
}

func (h *mcpHandler) toolConsolidate(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolConsolidate(ctx, h, args)
}

func (h *mcpHandler) toolConsolidationRevert(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolConsolidationRevert(ctx, h, args)
}

// ── Chat History handlers ──

// toolSaveChat / toolRecallChat / toolSearchChats are thin shims over
// their pkg/mcp counterparts (F-4 wave 3g).
func (h *mcpHandler) toolSaveChat(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolSaveChat(ctx, h, args)
}

func (h *mcpHandler) toolRecallChat(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolRecallChat(ctx, h, args)
}

func (h *mcpHandler) toolSearchChats(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolSearchChats(ctx, h, args)
}

// truncate cuts a string to maxLen and adds "..." if truncated.
// truncate is a shim over mcp.Truncate kept so the surviving in-http
// tool bodies (analyzeCommits, saveMemory, crossSearch, ...) don't need
// to be edited in this wave. Removed once those tools migrate too.
func truncate(s string, maxLen int) string { return mcp.Truncate(s, maxLen) }

// ── MCP Resources API ──────────────────────────────────────────────────────

func (h *mcpHandler) handleResourcesList(c *fiber.Ctx, req jsonRPCRequest) error {
	resources := []map[string]any{
		{
			"uri":         "levara://collections",
			"name":        "Collections",
			"description": "List of all knowledge collections with record counts and dimensions",
			"mimeType":    "application/json",
		},
		{
			"uri":         "levara://memories/project",
			"name":        "Project Memories",
			"description": "Project-level stored memories (tech stack, decisions, conventions)",
			"mimeType":    "application/json",
		},
		{
			"uri":         "levara://memories/user",
			"name":        "User Memories",
			"description": "User-level stored preferences and settings",
			"mimeType":    "application/json",
		},
		{
			"uri":         "levara://memories/feedback",
			"name":        "Feedback Memories",
			"description": "Stored feedback and corrections",
			"mimeType":    "application/json",
		},
	}

	// Add per-collection resources dynamically
	if h.cfg.Collections != nil {
		for _, name := range h.cfg.Collections.List() {
			resources = append(resources, map[string]any{
				"uri":         fmt.Sprintf("levara://collections/%s", name),
				"name":        fmt.Sprintf("Collection: %s", name),
				"description": fmt.Sprintf("Knowledge collection '%s' — vectors, entities, triplets", name),
				"mimeType":    "application/json",
			})
		}
	}

	return c.JSON(jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{"resources": resources},
	})
}

func (h *mcpHandler) handleResourcesRead(c *fiber.Ctx, req jsonRPCRequest) error {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32602, Message: "Invalid params: uri required"}})
	}

	uri := params.URI
	var content string
	var mimeType = "application/json"

	switch {
	case uri == "levara://collections":
		content = h.resourceCollections()

	case strings.HasPrefix(uri, "levara://collections/"):
		name := strings.TrimPrefix(uri, "levara://collections/")
		content = h.resourceCollectionDetail(name)

	case strings.HasPrefix(uri, "levara://memories/"):
		parts := strings.TrimPrefix(uri, "levara://memories/")
		// parts can be "project" or "project/collectionName"
		segments := strings.SplitN(parts, "/", 2)
		memType := segments[0]
		collName := ""
		if len(segments) > 1 {
			collName = segments[1]
		}
		content = h.resourceMemories(context.Background(), memType, collName)

	default:
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32602, Message: fmt.Sprintf("Unknown resource URI: %s", uri)}})
	}

	return c.JSON(jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID,
		Result: map[string]any{
			"contents": []map[string]any{
				{"uri": uri, "mimeType": mimeType, "text": content},
			},
		},
	})
}

func (h *mcpHandler) resourceCollections() string {
	if h.cfg.Collections == nil {
		return "[]"
	}
	var colls []map[string]any
	for _, name := range h.cfg.Collections.List() {
		meta := h.cfg.Collections.GetMeta(name)
		entry := map[string]any{"name": name}
		if meta != nil {
			entry["record_count"] = meta.RecordCount
			entry["embedding_dim"] = meta.EmbeddingDim
			entry["distance_metric"] = meta.DistanceMetric
		}
		colls = append(colls, entry)
	}
	data, _ := json.Marshal(colls)
	return string(data)
}

func (h *mcpHandler) resourceCollectionDetail(name string) string {
	if h.cfg.Collections == nil {
		return "{}"
	}
	meta := h.cfg.Collections.GetMeta(name)
	if meta == nil {
		return fmt.Sprintf("{\"error\": \"collection '%s' not found\"}", name)
	}
	data, _ := json.Marshal(meta)
	return string(data)
}

func (h *mcpHandler) resourceMemories(ctx context.Context, memType, collName string) string {
	if h.cfg.DB == nil {
		return "[]"
	}
	var rows *sql.Rows
	var err error
	if collName != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT key, value, type, collection_name, updated_at FROM memories
			 WHERE type = $1 AND collection_name = $2 ORDER BY updated_at DESC LIMIT 50`),
			memType, collName)
	} else {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT key, value, type, collection_name, updated_at FROM memories
			 WHERE type = $1 ORDER BY updated_at DESC LIMIT 50`), memType)
	}
	if err != nil {
		return "[]"
	}
	defer rows.Close()

	var items []map[string]string
	for rows.Next() {
		var key, value, typ, coll, updated string
		rows.Scan(&key, &value, &typ, &coll, &updated)
		items = append(items, map[string]string{
			"key": key, "value": value, "type": typ, "collection": coll, "updated_at": updated,
		})
	}
	data, _ := json.Marshal(items)
	return string(data)
}

// ── Tool: get_project_context ─────────────────────────────────────────────

// ── Cross-Project Tools ──

// toolCodify is a thin shim over mcp.ToolCodify. F-4 wave 3p moved the
// body (extract.AnalyzeCode + graph upserts + optional embed) into
// pkg/mcp. The pre-refactor QArgs call for ON CONFLICT parameter reuse
// was replaced by excluded.* syntax (SQLite 3.24+ / PostgreSQL 9.5+) so
// no QArgs method is needed on Deps.
func (h *mcpHandler) toolCodify(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolCodify(ctx, h, args)
}

// toolAddFeedback / toolGetFeedbackStats / toolSetContext are thin
// shims over their pkg/mcp counterparts. F-4 wave 3e moved the bodies.
func (h *mcpHandler) toolAddFeedback(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolAddFeedback(ctx, h, args)
}

func (h *mcpHandler) toolGetFeedbackStats(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolGetFeedbackStats(ctx, h, args)
}

func (h *mcpHandler) toolSetContext(sess *mcpSession, args map[string]any) mcpToolResult {
	return mcp.ToolSetContext(sess, h, args)
}

// toolCrossSearch is a thin shim over mcp.ToolCrossSearch. F-4 wave 3l
// moved the body (plus the sensitiveKeyPatterns list and
// isSensitiveKey helper) into pkg/mcp. No Deps growth — reuses
// NewSearchPipeline + DB + Q + extractOwnerID added in earlier waves.
func (h *mcpHandler) toolCrossSearch(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolCrossSearch(ctx, h, args)
}

// toolSync is a thin shim over mcp.ToolSync. F-4 wave 3q moved the body
// (arg parsing, direction gate, manifest fetch, SyncPull/Push, heartbeat)
// into pkg/mcp. DoSync wraps all the internal/http sync helpers so
// pkg/mcp stays free of APIConfig and *store.CollectionManager.
func (h *mcpHandler) toolSync(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolSync(ctx, h, args)
}

func syncPush(ctx context.Context, cfg APIConfig, remoteURL string, types []string, since string) map[string]any {
	results := map[string]any{}
	client := &http.Client{Timeout: 30 * time.Second}

	shouldSync := func(t string) bool {
		if len(types) == 0 {
			return true
		}
		for _, tt := range types {
			if tt == t {
				return true
			}
		}
		return false
	}

	if shouldSync("memories") && cfg.DB != nil {
		var memories []syncMemory
		query := Q(`SELECT id, key, value, type, owner_id, collection_name,
			 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
			 created_at, updated_at FROM memories ORDER BY updated_at`)
		args := []any{}
		if since != "" {
			query = Q(`SELECT id, key, value, type, owner_id, collection_name,
				 COALESCE(room,''), COALESCE(hall,''), is_pinned, pin_priority,
				 created_at, updated_at FROM memories WHERE updated_at > $1 ORDER BY updated_at`)
			args = []any{since}
		}
		rows, err := cfg.DB.QueryContext(ctx, query, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var m syncMemory
				rows.Scan(&m.ID, &m.Key, &m.Value, &m.Type, &m.OwnerID, &m.CollectionName,
					&m.Room, &m.Hall, &m.IsPinned, &m.PinPriority, &m.CreatedAt, &m.UpdatedAt)
				memories = append(memories, m)
			}
		}
		if len(memories) > 0 {
			body, _ := json.Marshal(memories)
			resp, err := syncAuthPost(client, remoteURL+"/sync/import/memories", "application/json", string(body), cfg.SyncToken)
			if err != nil {
				results["memories_error"] = err.Error()
			} else {
				defer resp.Body.Close()
				var r map[string]any
				json.NewDecoder(resp.Body).Decode(&r)
				results["memories"] = r
			}
		} else {
			results["memories"] = "no data to push"
		}
	}

	if shouldSync("graph") && cfg.DB != nil {
		var g syncGraph
		nodeRows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, name, type, COALESCE(description,''), COALESCE(properties,'{}'), COALESCE(dataset_id,'') FROM graph_nodes`))
		if err == nil {
			defer nodeRows.Close()
			for nodeRows.Next() {
				var n syncGraphNode
				nodeRows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Properties, &n.DatasetID)
				g.Nodes = append(g.Nodes, n)
			}
		}
		edgeRows, err := cfg.DB.QueryContext(ctx,
			Q(`SELECT id, source_id, target_id, relationship_name, COALESCE(properties,'{}'),
				  COALESCE(valid_from,''), COALESCE(valid_until,''), COALESCE(superseded_by,''),
				  COALESCE(confidence,1.0), COALESCE(dataset_id,'')
			   FROM graph_edges`))
		if err == nil {
			defer edgeRows.Close()
			for edgeRows.Next() {
				var e syncGraphEdge
				edgeRows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.RelationshipName, &e.Properties,
					&e.ValidFrom, &e.ValidUntil, &e.SupersededBy, &e.Confidence, &e.DatasetID)
				g.Edges = append(g.Edges, e)
			}
		}
		if len(g.Nodes) > 0 || len(g.Edges) > 0 {
			body, _ := json.Marshal(g)
			resp, err := syncAuthPost(client, remoteURL+"/sync/import/graph", "application/json", string(body), cfg.SyncToken)
			if err != nil {
				results["graph_error"] = err.Error()
			} else {
				defer resp.Body.Close()
				var r map[string]any
				json.NewDecoder(resp.Body).Decode(&r)
				results["graph"] = r
			}
		} else {
			results["graph"] = "no data to push"
		}
	}

	return results
}

func containsType(types []string, t string) bool {
	for _, tt := range types {
		if tt == t {
			return true
		}
	}
	return false
}

func syncPullCollections(cfg APIConfig, remoteURL string, collections []string) map[string]any {
	client := &http.Client{Timeout: 120 * time.Second}
	results := map[string]any{}

	for _, coll := range collections {
		resp, err := syncAuthGet(client, remoteURL+"/sync/export/collection/"+coll, cfg.SyncToken)
		if err != nil {
			results[coll] = map[string]string{"error": err.Error()}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// POST to local import endpoint
		importResp, err := syncAuthPost(
			client,
			"http://localhost:"+fmt.Sprintf("%d", 8080)+"/api/v1/sync/import/collection",
			"application/json",
			string(body),
			cfg.SyncToken,
		)
		if err != nil {
			// Fallback: import directly in-process if local HTTP fails
			var export syncCollectionExport
			if json.Unmarshal(body, &export) == nil {
				results[coll] = map[string]any{
					"records": len(export.Records),
					"status":  "fetched, needs local import via /sync/import/collection",
					"source":  fmt.Sprintf("%s (dim=%d)", export.SourceModel, export.SourceDim),
				}
			} else {
				results[coll] = map[string]string{"error": "parse error"}
			}
			continue
		}
		defer importResp.Body.Close()
		var r map[string]any
		json.NewDecoder(importResp.Body).Decode(&r)
		results[coll] = r
	}

	return results
}

func syncPushCollections(ctx context.Context, cfg APIConfig, remoteURL string, collections []string) map[string]any {
	client := &http.Client{Timeout: 120 * time.Second}
	results := map[string]any{}

	for _, coll := range collections {
		if cfg.Collections == nil || !cfg.Collections.Has(coll) {
			results[coll] = map[string]string{"error": "collection not found locally"}
			continue
		}

		ids, _, metas, err := cfg.Collections.AllRecords(coll)
		if err != nil {
			results[coll] = map[string]string{"error": err.Error()}
			continue
		}

		meta := cfg.Collections.GetMeta(coll)
		export := syncCollectionExport{Collection: coll}
		if meta != nil {
			export.SourceModel = meta.EmbeddingModel
			export.SourceDim = meta.EmbeddingDim
		}
		for i, id := range ids {
			export.Records = append(export.Records, syncCollectionRecord{
				ID:       id,
				Text:     textFromMetadata(metas[i]),
				Metadata: json.RawMessage(metas[i]),
			})
		}

		body, _ := json.Marshal(export)
		resp, err := syncAuthPost(client, remoteURL+"/sync/import/collection", "application/json", string(body), cfg.SyncToken)
		if err != nil {
			results[coll] = map[string]string{"error": err.Error()}
			continue
		}
		defer resp.Body.Close()
		var r map[string]any
		json.NewDecoder(resp.Body).Decode(&r)
		results[coll] = r
	}

	return results
}

// toolGetProjectContext is a thin shim over mcp.ToolGetProjectContext.
// F-4 wave 3o moved the body (collection stats + memories + graph
// entities + interactions + related projects) into pkg/mcp. Uses the
// new CollectionMeta Deps method for vector stats so pkg/mcp stays free
// of internal/store pointers.
func (h *mcpHandler) toolGetProjectContext(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolGetProjectContext(ctx, h, args)
}

// ── Session Cleanup ───────────────────────────────────────────────────────

func (h *mcpHandler) sessionCleanupLoop() {
	// Update data metrics on startup
	h.updateDataMetrics()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		// SessionStore.CleanupIdle fires OnCountChange which updates the
		// MCPSessionsActive gauge for us — no explicit metrics.Set here.
		h.sessions.CleanupIdle(time.Hour)
		h.updateDataMetrics()
	}
}

func (h *mcpHandler) updateDataMetrics() {
	// Collection records
	totalVectors := 0
	if h.cfg.Collections != nil {
		for _, meta := range h.cfg.Collections.ListWithMeta() {
			metrics.CollectionRecords.WithLabelValues(meta.Name).Set(float64(meta.RecordCount))
			totalVectors += meta.RecordCount
		}
	}
	metrics.TotalVectors.Set(float64(totalVectors))

	// Memories count
	if h.cfg.DB != nil {
		var count int
		h.cfg.DB.QueryRow(Q(`SELECT COUNT(*) FROM memories`)).Scan(&count)
		metrics.MemoriesTotal.Set(float64(count))
	}
}

// handleSSEStream implements GET /mcp for server-initiated SSE messages.
// Clients open this to receive notifications (e.g. tools/list_changed, progress).
func (h *mcpHandler) handleSSEStream(c *fiber.Ctx) error {
	sessionID := c.Get("Mcp-Session-Id")
	if sessionID == "" {
		return c.SendStatus(400)
	}
	sess := h.getOrValidateSession(sessionID)
	if sess == nil {
		return c.SendStatus(404)
	}

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("Mcp-Session-Id", sessionID)

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		// Send initial keepalive
		fmt.Fprintf(w, ": keepalive\n\n")
		w.Flush()

		for {
			select {
			case msg, ok := <-sess.SSECh:
				if !ok {
					return // session closed
				}
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
				w.Flush()
			case <-time.After(30 * time.Second):
				// Send keepalive comment every 30s
				fmt.Fprintf(w, ": keepalive\n\n")
				w.Flush()
			}
		}
	})
	return nil
}

// handleDeleteSession implements DELETE /mcp to terminate a session.
func (h *mcpHandler) handleDeleteSession(c *fiber.Ctx) error {
	sessionID := c.Get("Mcp-Session-Id")
	if sessionID == "" {
		return c.SendStatus(400)
	}
	h.deleteSession(sessionID)
	return c.SendStatus(204)
}

// MCPHealthHandler returns MCP-specific health info.
func MCPHealthHandler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status":  "ok",
			"server":  "levara-mcp",
			"version": "1.0.0",
			"tools":   len(mcp.ToolDescriptors()),
		})
	}
}

// toolListCommunities is a thin shim over mcp.ToolListCommunities. F-4
// wave 3n moved the body (SQL query + JSON marshal) into pkg/mcp. No
// new Deps methods — DB() and Q() were already in the interface.
func (h *mcpHandler) toolListCommunities(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolListCommunities(ctx, h, args)
}

// toolCheckDrift is a thin shim over mcp.ToolCheckDrift. F-4 wave 3o
// moved the body into pkg/mcp using CollectionMeta so pkg/mcp stays free
// of the *store.CollectionManager pointer. The embedded drift logic
// (iterate → skip empty/"_" → compare model+dim) is inlined there.
func (h *mcpHandler) toolCheckDrift(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolCheckDrift(ctx, h, args)
}

// toolPruneGraph is a thin shim over mcp.ToolPruneGraph. F-4 wave 3n
// moved the body (community.PruneGraph + LogHeartbeat) into pkg/mcp.
// No new Deps methods — DB() and LogHeartbeat() already in interface.
func (h *mcpHandler) toolPruneGraph(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolPruneGraph(ctx, h, args)
}
