// mcp.go — Model Context Protocol (MCP) Streamable HTTP server (spec 2025-03-26).
// Implements JSON-RPC 2.0 with session management, SSE streaming, and 15 tools.
// Compatible with Claude Code, Cursor, Cline, and any MCP client.
//
// Transport: Streamable HTTP (preferred)
//   POST /mcp — JSON-RPC requests + notifications
//   GET  /mcp — SSE stream for server-initiated messages
//   DELETE /mcp — terminate session
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
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/internal/metrics"
	"github.com/stek0v/cognevra/pkg/community"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/extract"
	"github.com/stek0v/cognevra/pkg/git"
	"github.com/stek0v/cognevra/pkg/llm"
	"github.com/stek0v/cognevra/pkg/mcp"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/rerank"
	"github.com/stek0v/cognevra/pkg/router"
	"github.com/stek0v/cognevra/pkg/runreg"
	"github.com/stek0v/cognevra/pipeline"
)

// F-4 wave 1b: the canonical type definitions live in pkg/mcp now. Local
// names below are type aliases that preserve every existing call-site inside
// this package unchanged while the handler migration continues in later
// waves. When the handler itself moves to pkg/mcp the aliases can be dropped.
type (
	jsonRPCRequest  = mcp.JSONRPCRequest
	jsonRPCResponse = mcp.JSONRPCResponse
	rpcError        = mcp.RPCError
	contextKey      = mcp.ContextKey
	mcpTool         = mcp.Tool
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
func (h *mcpHandler) Embed(ctx context.Context, text string) ([]float32, error) {
	client := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 1, 1)
	return client.EmbedSingle(ctx, text)
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
// to both RegisterCogneeAPI and RegisterMCPAPI so MCP-initiated runs and
// REST-initiated runs share the same map.
func (h *mcpHandler) Runs() *runreg.Registry { return h.cfg.Runs }

// BaseCognifyConfig implements mcp.Deps: builds an orchestrator.Config
// pre-populated with every deployment-level field the cognify pipeline
// needs. The MCP tool body then overrides per-call fields (Collection,
// DatasetID, Room, Tags, SystemPrompt, SkipGraph, chunking knobs) before
// passing the result to RunPipeline.
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
func (h *mcpHandler) LogHeartbeat(eventType string, payload any) {
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

func (a *searchPipelineAdapter) SearchByTextWithRerank(ctx context.Context, coll, query string, topK int) ([]pipeline.ScoredResult, bool, error) {
	return a.sp.SearchByTextWithRerank(ctx, coll, query, topK)
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
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return nil
	}
	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)
	var rerankClient *rerank.Client
	if doRerank {
		rerankClient = rerank.NewClient(h.cfg.RerankEndpoint, h.cfg.RerankModel, 0, h.cfg.RerankTimeoutMs)
	}
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections, rerankClient)
	return &searchPipelineAdapter{sp: sp, rerankClient: rerankClient}
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

// getOrValidateSession returns the session for the given ID, or nil if invalid.
func (h *mcpHandler) getOrValidateSession(sessionID string) *mcpSession {
	return h.sessions.Get(sessionID)
}

// createSession creates a new MCP session and returns its ID.
func (h *mcpHandler) createSession() string {
	return h.sessions.Create().ID
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

	// Session validation for non-initialize requests
	sessionID := c.Get("Mcp-Session-Id")
	if req.Method != "initialize" && sessionID != "" {
		if h.getOrValidateSession(sessionID) == nil {
			return c.SendStatus(404) // invalid session → client should re-initialize
		}
	}

	// Set session header on all responses
	if sessionID != "" {
		c.Set("Mcp-Session-Id", sessionID)
	}

	switch req.Method {
	case "initialize":
		sid := h.createSession()
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

	// Inject user ID from session for isolation
	toolCtx := context.Background()
	sessionID := c.Get("Mcp-Session-Id")
	sess := h.getOrValidateSession(sessionID)
	if sess != nil && sess.UserID != "" {
		toolCtx = context.WithValue(toolCtx, mcpUserIDKey, sess.UserID)
	}

	result := h.executeTool(toolCtx, sess, params.Name, params.Arguments)
	return c.JSON(jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID, Result: result,
	})
}

func (h *mcpHandler) executeTool(ctx context.Context, sess *mcpSession, name string, args map[string]any) mcpToolResult {
	toolStart := time.Now()
	result := h.executeToolInner(ctx, sess, name, args)
	duration := time.Since(toolStart).Seconds()
	metrics.MCPToolDuration.WithLabelValues(name).Observe(duration)
	status := "ok"
	if result.IsError {
		status = "error"
	}
	metrics.MCPToolRequests.WithLabelValues(name, status).Inc()
	return result
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
		"diary_write", "diary_read":
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

func (h *mcpHandler) toolAnalyzeCommits(ctx context.Context, args map[string]any) mcpToolResult {
	repoPath, _ := args["repo_path"].(string)
	if repoPath == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'repo_path' required"}}, IsError: true}
	}
	since, _ := args["since"].(string)
	limit := 100
	if l, ok := args["limit"].(float64); ok {
		limit = int(l)
	}

	commits, err := git.ParseLog(repoPath, since, limit)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error parsing git log: %s", err.Error())}},
			IsError: true,
		}
	}

	if len(commits) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No commits found."}}}
	}

	text := git.CommitsToText(commits)

	// If orchestrator is available, cognify the commit text
	if h.cfg.EmbedEndpoint != "" {
		runID := uuid.New().String()
		collection := "git_commits"

		status := &runreg.Status{
			RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
		}
		h.cfg.Runs.Store(runID, status)

		pipeCfg := orchestrator.Config{
			ChunkStrategy:  "merged",
			MinChunkChars:  50,
			MaxChunkChars:  2000,
			LLMEndpoint:    os.Getenv("LLM_ENDPOINT"),
			LLMModel:       os.Getenv("LLM_MODEL"),
			LLMConcurrency: 1,
			EmbedEndpoint:  h.cfg.EmbedEndpoint,
			EmbedModel:     h.cfg.EmbedModel,
			Neo4jURL:       h.cfg.Neo4jCfg.Neo4jURL,
			Neo4jUser:      h.cfg.Neo4jCfg.Neo4jUser,
			Neo4jPassword:  h.cfg.Neo4jCfg.Neo4jPassword,
			Neo4jDatabase:  h.cfg.Neo4jCfg.Neo4jDatabase,
			Collection:     collection,
			Collections:    h.cfg.Collections,
			GenerateTriplets: true,
			DatasetID:      runID,
			DB:             h.cfg.DB,
			LLMCache:       h.cfg.LLMCache,
		}

		go func() {
			progressCh := make(chan orchestrator.Progress, 100)
			errCh := make(chan error, 1)
			go func() {
				errCh <- orchestrator.Run(context.Background(), []string{text}, pipeCfg, progressCh)
			}()
			for p := range progressCh {
				status.Stage = p.Stage
				status.Message = p.Message
				status.Chunks = p.ChunksCreated
				status.Entities = p.EntitiesExtracted
				status.Edges = p.EdgesExtracted
				status.ElapsedMs = p.ElapsedMs
			}
			if err := <-errCh; err != nil {
				status.Status = "FAILED"
				status.Message = err.Error()
			} else {
				status.Status = "COMPLETED"
			}
			status.ElapsedMs = time.Since(status.StartedAt).Milliseconds()
		}()

		return mcpToolResult{Content: []mcpContent{{
			Type: "text",
			Text: fmt.Sprintf("Analyzed %d commits. Cognify pipeline started (run_id: %s). Use cognify_status to track.\n\nPreview:\n%s",
				len(commits), runID, truncate(text, 2000)),
		}}}
	}

	return mcpToolResult{Content: []mcpContent{{
		Type: "text",
		Text: fmt.Sprintf("Analyzed %d commits (no embedding service — text only):\n%s", len(commits), truncate(text, 4000)),
	}}}
}

func (h *mcpHandler) toolGitSearch(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'query' required"}}, IsError: true}
	}

	// Search in the git_commits collection
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results (embedding service not configured)"}}}
	}

	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections, nil)

	res, err := sp.SearchByText(ctx, "git_commits", query, 10)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Search error: %s", err.Error())}}, IsError: true}
	}

	if len(res) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No matching commits found."}}}
	}

	var results []map[string]any
	for _, r := range res {
		results = append(results, map[string]any{
			"id": r.ID, "score": r.Score, "metadata": string(r.Metadata),
		})
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
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

func (h *mcpHandler) toolCodify(ctx context.Context, args map[string]any) mcpToolResult {
	code, _ := args["code"].(string)
	filename, _ := args["filename"].(string)
	if code == "" || filename == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'code' and 'filename' required"}}, IsError: true}
	}

	analysis := extract.AnalyzeCode(code, filename)

	// Store entities in graph if DB available
	if h.cfg.DB != nil {
		for _, e := range analysis.Entities {
			props := fmt.Sprintf(`{"file":"%s","line":%d}`, e.File, e.Line)
			if e.Parent != "" {
				props = fmt.Sprintf(`{"file":"%s","line":%d,"parent":"%s"}`, e.File, e.Line, e.Parent)
			}
			q, qargs := QArgs(`INSERT INTO graph_nodes (id, name, type, description, properties)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT(id) DO UPDATE SET name = $2, type = $3, properties = $5`,
				uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.Name+e.Type+e.File)).String(),
				e.Name, e.Type, filename, props)
			h.cfg.DB.ExecContext(ctx, q, qargs...)
		}
		for _, r := range analysis.Relations {
			srcID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Source)).String()
			tgtID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Target)).String()
			edgeID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(r.Source+r.Relationship+r.Target)).String()
			q, qargs := QArgs(`INSERT INTO graph_edges (id, source_id, target_id, relationship_name, properties)
				VALUES ($1, $2, $3, $4, '{}')
				ON CONFLICT(id) DO NOTHING`,
				edgeID, srcID, tgtID, r.Relationship)
			h.cfg.DB.ExecContext(ctx, q, qargs...)
		}
	}

	// Embed code entities into collection if configured
	collection, _ := args["collection"].(string)
	if collection != "" && h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil {
		embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 3)
		var texts []string
		var ids []string
		for _, e := range analysis.Entities {
			texts = append(texts, e.Name+": "+e.Type+" in "+e.File)
			ids = append(ids, uuid.NewSHA1(uuid.NameSpaceOID, []byte(e.Name+e.Type+e.File)).String())
		}
		if len(texts) > 0 {
			if vecs, err := embedClient.EmbedTexts(ctx, texts); err == nil {
				for i, vec := range vecs {
					meta := fmt.Sprintf(`{"name":"%s","type":"%s","file":"%s"}`, analysis.Entities[i].Name, analysis.Entities[i].Type, analysis.Entities[i].File)
					h.cfg.Collections.Insert(collection, ids[i], vec, meta)
				}
			}
		}
	}

	out, _ := json.MarshalIndent(map[string]any{
		"language":  analysis.Language,
		"entities":  len(analysis.Entities),
		"relations": len(analysis.Relations),
		"details":   analysis,
	}, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
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

var sensitiveKeyPatterns = []string{"api_key", "apikey", "password", "passwd", "secret", "token", "credential", "private_key"}

func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, p := range sensitiveKeyPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

func (h *mcpHandler) toolCrossSearch(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["search_query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'search_query' required"}}, IsError: true}
	}

	collectionsRaw, _ := args["collections"].([]any)
	if len(collectionsRaw) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'collections' array required"}}, IsError: true}
	}
	if len(collectionsRaw) > 5 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: max 5 collections per cross-search"}}, IsError: true}
	}

	var collections []string
	for _, c := range collectionsRaw {
		if s, ok := c.(string); ok && s != "" {
			collections = append(collections, s)
		}
	}

	topK := 5
	if tk, ok := args["top_k"].(float64); ok && tk > 0 {
		topK = int(tk)
	}
	includeMemories := true
	if im, ok := args["include_memories"].(bool); ok {
		includeMemories = im
	}

	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results (embedding service not configured)"}}}
	}

	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections, nil)

	type collResult struct {
		Collection string         `json:"collection"`
		Vectors    []map[string]any `json:"vectors,omitempty"`
		Memories   []map[string]any `json:"memories,omitempty"`
	}

	var results []collResult
	for _, coll := range collections {
		cr := collResult{Collection: coll}

		// Vector search
		res, err := sp.SearchByText(ctx, coll, query, topK)
		if err == nil {
			for _, r := range res {
				cr.Vectors = append(cr.Vectors, map[string]any{
					"id": r.ID, "score": r.Score, "metadata": string(r.Metadata),
				})
			}
		}

		// Memory search (SQL LIKE)
		if includeMemories && h.cfg.DB != nil {
			pattern := "%" + query + "%"
			ownerID := ""
			if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
				ownerID = uid
			}
			rows, err := h.cfg.DB.QueryContext(ctx,
				Q(`SELECT key, value, type FROM memories
				 WHERE (key LIKE $1 OR value LIKE $2)
				 AND (collection_name = $3 OR collection_name = '')
				 AND (owner_id = $4 OR owner_id = '')
				 ORDER BY updated_at DESC LIMIT $5`),
				pattern, pattern, coll, ownerID, topK)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var key, value, typ string
					rows.Scan(&key, &value, &typ)
					if isSensitiveKey(key) {
						continue // skip sensitive data in cross-project results
					}
					cr.Memories = append(cr.Memories, map[string]any{
						"key": key, "value": truncate(value, 200), "type": typ,
					})
				}
			}
		}

		results = append(results, cr)
	}

	log.Printf("[cross-project] searched %d collections for query: %s", len(collections), truncate(query, 50))

	out, _ := json.MarshalIndent(map[string]any{
		"results":     results,
		"collections": collections,
		"query":       query,
	}, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolSync(ctx context.Context, args map[string]any) mcpToolResult {
	remoteURL, _ := args["remote_url"].(string)
	if remoteURL == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'remote_url' required (e.g., http://10.23.0.53:8080/api/v1)"}}, IsError: true}
	}
	direction, _ := args["direction"].(string)
	if direction == "" {
		direction = "pull"
	}
	since, _ := args["since"].(string)

	var types []string
	if typesRaw, ok := args["types"].([]any); ok {
		for _, t := range typesRaw {
			if s, ok := t.(string); ok {
				types = append(types, s)
			}
		}
	}

	var collectionNames []string
	if collsRaw, ok := args["collections"].([]any); ok {
		for _, c := range collsRaw {
			if s, ok := c.(string); ok && s != "" {
				collectionNames = append(collectionNames, s)
			}
		}
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}

	// First, get remote manifest
	manifest, err := SyncManifestFromRemote(remoteURL)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: " + err.Error()}}, IsError: true}
	}

	if direction == "pull" {
		result := SyncPull(h.cfg, remoteURL, types, since)
		// Collection sync (pull: fetch text from remote → re-embed locally)
		if containsType(types, "collections") && len(collectionNames) > 0 {
			result["collections_sync"] = syncPullCollections(h.cfg, remoteURL, collectionNames)
		}
		result["remote_manifest"] = manifest
		h.logHeartbeat("sync", map[string]any{"direction": "pull", "remote": remoteURL, "types": types})
		out, _ := json.MarshalIndent(result, "", "  ")
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
	}

	// Push: export local data, POST to remote
	result := syncPush(ctx, h.cfg, remoteURL, types, since)
	if containsType(types, "collections") && len(collectionNames) > 0 {
		result["collections_sync"] = syncPushCollections(ctx, h.cfg, remoteURL, collectionNames)
	}
	result["remote_manifest"] = manifest
	h.logHeartbeat("sync", map[string]any{"direction": "push", "remote": remoteURL, "types": types})
	out, _ := json.MarshalIndent(result, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
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
		query := Q(`SELECT id, key, value, type, owner_id, collection_name, created_at, updated_at FROM memories ORDER BY updated_at`)
		args := []any{}
		if since != "" {
			query = Q(`SELECT id, key, value, type, owner_id, collection_name, created_at, updated_at FROM memories WHERE updated_at > $1 ORDER BY updated_at`)
			args = []any{since}
		}
		rows, err := cfg.DB.QueryContext(ctx, query, args...)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var m syncMemory
				rows.Scan(&m.ID, &m.Key, &m.Value, &m.Type, &m.OwnerID, &m.CollectionName, &m.CreatedAt, &m.UpdatedAt)
				memories = append(memories, m)
			}
		}
		if len(memories) > 0 {
			body, _ := json.Marshal(memories)
			resp, err := client.Post(remoteURL+"/sync/import/memories", "application/json", strings.NewReader(string(body)))
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
		nodeRows, err := cfg.DB.QueryContext(ctx, Q(`SELECT id, name, type, description, properties FROM graph_nodes`))
		if err == nil {
			defer nodeRows.Close()
			for nodeRows.Next() {
				var n syncGraphNode
				nodeRows.Scan(&n.ID, &n.Name, &n.Type, &n.Description, &n.Properties)
				g.Nodes = append(g.Nodes, n)
			}
		}
		edgeRows, err := cfg.DB.QueryContext(ctx, Q(`SELECT id, source_id, target_id, relationship_name, properties FROM graph_edges`))
		if err == nil {
			defer edgeRows.Close()
			for edgeRows.Next() {
				var e syncGraphEdge
				edgeRows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.RelationshipName, &e.Properties)
				g.Edges = append(g.Edges, e)
			}
		}
		if len(g.Nodes) > 0 || len(g.Edges) > 0 {
			body, _ := json.Marshal(g)
			resp, err := client.Post(remoteURL+"/sync/import/graph", "application/json", strings.NewReader(string(body)))
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
		resp, err := client.Get(remoteURL + "/sync/export/collection/" + coll)
		if err != nil {
			results[coll] = map[string]string{"error": err.Error()}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// POST to local import endpoint
		importResp, err := client.Post(
			"http://localhost:"+fmt.Sprintf("%d", 8080)+"/api/v1/sync/import/collection",
			"application/json",
			strings.NewReader(string(body)),
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
		resp, err := client.Post(remoteURL+"/sync/import/collection", "application/json", strings.NewReader(string(body)))
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

func (h *mcpHandler) toolGetProjectContext(ctx context.Context, args map[string]any) mcpToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'collection' required"}}, IsError: true}
	}

	var sb strings.Builder

	// 1. Collection stats
	sb.WriteString("## Collection Stats\n")
	if h.cfg.Collections != nil {
		meta := h.cfg.Collections.GetMeta(collection)
		if meta != nil {
			sb.WriteString(fmt.Sprintf("- Name: %s\n- Records: %d\n- Dimension: %d\n- Metric: %s\n\n",
				meta.Name, meta.RecordCount, meta.EmbeddingDim, meta.DistanceMetric))
		} else {
			sb.WriteString(fmt.Sprintf("- Collection '%s' not found (no vectors indexed yet)\n\n", collection))
		}
	}

	// 2. Memories
	sb.WriteString("## Project Memories\n")
	if h.cfg.DB != nil {
		rows, err := h.cfg.DB.QueryContext(ctx,
			Q(`SELECT key, value, type FROM memories
			 WHERE collection_name = $1 ORDER BY updated_at DESC LIMIT 20`), collection)
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var key, value, typ string
				rows.Scan(&key, &value, &typ)
				sb.WriteString(fmt.Sprintf("- [%s] %s: %s\n", typ, key, truncate(value, 200)))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no memories saved for this collection)\n")
			}
			sb.WriteString("\n")
		}
	}

	// 3. Graph entities (top types)
	sb.WriteString("## Key Entity Types\n")
	if h.cfg.DB != nil {
		rows, err := h.cfg.DB.QueryContext(ctx,
			Q(`SELECT type, COUNT(*) as cnt FROM graph_nodes GROUP BY type ORDER BY cnt DESC LIMIT 10`))
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var typ string
				var cnt int
				rows.Scan(&typ, &cnt)
				sb.WriteString(fmt.Sprintf("- %s: %d entities\n", typ, cnt))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no entities extracted yet)\n")
			}
			sb.WriteString("\n")
		}
	}

	// 4. Recent interactions
	sb.WriteString("## Recent Interactions\n")
	if h.cfg.DB != nil {
		rows, err := h.cfg.DB.QueryContext(ctx,
			Q(`SELECT query, response, created_at FROM interactions
			 ORDER BY created_at DESC LIMIT 5`))
		if err == nil {
			defer rows.Close()
			count := 0
			for rows.Next() {
				var query, response, createdAt string
				rows.Scan(&query, &response, &createdAt)
				sb.WriteString(fmt.Sprintf("- Q: %s\n  A: %s\n", truncate(query, 100), truncate(response, 150)))
				count++
			}
			if count == 0 {
				sb.WriteString("- (no interactions recorded)\n")
			}
		}
	}

	// 5. Related projects (compact summaries)
	if related, ok := args["include_related"].([]any); ok && len(related) > 0 {
		sb.WriteString("\n## Related Projects\n")
		for _, r := range related {
			relColl, ok := r.(string)
			if !ok || relColl == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("\n### %s\n", relColl))
			if h.cfg.Collections != nil {
				if meta := h.cfg.Collections.GetMeta(relColl); meta != nil {
					sb.WriteString(fmt.Sprintf("- Records: %d, Dim: %d\n", meta.RecordCount, meta.EmbeddingDim))
				} else {
					sb.WriteString("- (no vectors)\n")
				}
			}
			if h.cfg.DB != nil {
				rows, err := h.cfg.DB.QueryContext(ctx,
					Q(`SELECT key, value FROM memories WHERE collection_name = $1 ORDER BY updated_at DESC LIMIT 3`), relColl)
				if err == nil {
					defer rows.Close()
					for rows.Next() {
						var key, value string
						rows.Scan(&key, &value)
						sb.WriteString(fmt.Sprintf("- %s: %s\n", key, truncate(value, 100)))
					}
				}
			}
		}
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: sb.String()}}}
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

// Helper to check if string is in list
func strIn(s string, list []string) bool {
	for _, v := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// toolListCommunities returns detected communities from graph_communities table.
func (h *mcpHandler) toolListCommunities(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	limit := 20
	if lim, ok := args["limit"].(float64); ok && lim > 0 {
		limit = int(lim)
	}
	minMembers := 2
	if mm, ok := args["min_members"].(float64); ok {
		minMembers = int(mm)
	}

	query := "SELECT id, level, parent_id, member_count, summary FROM graph_communities WHERE member_count >= ? ORDER BY level ASC, member_count DESC LIMIT ?"
	queryArgs := []any{minMembers, limit}

	if levelVal, ok := args["level"].(float64); ok {
		query = "SELECT id, level, parent_id, member_count, summary FROM graph_communities WHERE member_count >= ? AND level = ? ORDER BY member_count DESC LIMIT ?"
		queryArgs = []any{minMembers, int(levelVal), limit}
	}

	rows, err := h.cfg.DB.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var communities []map[string]any
	for rows.Next() {
		var id, parentID, summary string
		var level, memberCount int
		if rows.Scan(&id, &level, &parentID, &memberCount, &summary) != nil {
			continue
		}
		communities = append(communities, map[string]any{
			"id": id, "level": level, "parent_id": parentID,
			"member_count": memberCount, "summary": summary,
		})
	}

	if communities == nil {
		communities = []map[string]any{}
	}

	out, _ := json.MarshalIndent(communities, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// searchTypesForMode returns allowed search types for a given mode.
// Returns nil (= all allowed) for "full" and "auto".
func searchTypesForMode(mode string) map[string]bool {
	switch mode {
	case "rag":
		return map[string]bool{
			"CHUNKS": true, "HYBRID": true, "CHUNKS_LEXICAL": true,
			"RAG_COMPLETION": true, "SUMMARIES": true, "WEIGHTED_HYBRID": true,
		}
	case "graph":
		return map[string]bool{
			"GRAPH_COMPLETION": true, "GRAPH_COMPLETION_COT": true,
			"GRAPH_COMPLETION_CONTEXT_EXTENSION": true, "GRAPH_SUMMARY_COMPLETION": true,
			"COMMUNITY_LOCAL": true, "COMMUNITY_GLOBAL": true,
			"CYPHER": true, "TRIPLET_COMPLETION": true, "TEMPORAL": true,
		}
	default:
		return nil
	}
}

func defaultTypeForMode(mode string) string {
	switch mode {
	case "rag":
		return "CHUNKS"
	case "graph":
		return "GRAPH_COMPLETION"
	default:
		return "AUTO"
	}
}

// toolCheckDrift reports embedding model drift across collections.
func (h *mcpHandler) toolCheckDrift(ctx context.Context, args map[string]any) mcpToolResult {
	drifted := embed.CheckDrift(h.cfg.Collections, h.cfg.EmbedModel, 0)
	// Try to get actual dim from first collection
	dim := 0
	if h.cfg.Collections != nil {
		for _, name := range h.cfg.Collections.List() {
			m := h.cfg.Collections.GetMeta(name)
			if m.EmbeddingDim > 0 {
				dim = m.EmbeddingDim
				break
			}
		}
	}
	if dim > 0 {
		drifted = embed.CheckDrift(h.cfg.Collections, h.cfg.EmbedModel, dim)
	}

	if drifted == nil {
		drifted = []embed.DriftCheckResult{}
	}
	out, _ := json.MarshalIndent(drifted, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// toolPruneGraph cleans up old superseded graph edges.
func (h *mcpHandler) toolPruneGraph(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"edges_deleted":0}`}}}
	}

	cfg := community.PruneConfig{
		MaxAgeDays:      90,
		KeepSuperseding: true,
		DryRun:          true,
	}
	if days, ok := args["max_age_days"].(float64); ok && days > 0 {
		cfg.MaxAgeDays = int(days)
	}
	if dr, ok := args["dry_run"].(bool); ok {
		cfg.DryRun = dr
	}
	if io, ok := args["include_orphan_nodes"].(bool); ok {
		cfg.IncludeOrphans = io
	}

	result, err := community.PruneGraph(ctx, h.cfg.DB, cfg)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error: %v", err)}},
			IsError: true,
		}
	}

	h.logHeartbeat("prune", result)
	out, _ := json.MarshalIndent(result, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}
