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
	"github.com/stek0v/cognevra/pkg/graphrank"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/mcp"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/rerank"
	"github.com/stek0v/cognevra/pkg/router"
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

func (h *mcpHandler) toolCognify(ctx context.Context, args map[string]any) mcpToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'data' parameter required"}}, IsError: true}
	}

	runID := uuid.New().String()
	collection, _ := args["collection"].(string)
	if collection == "" {
		collection = "default"
	}

	status := &pipelineRunStatus{
		RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
	}
	pipelineRuns.Store(runID, status)

	// Validate that embedding service is configured
	if h.cfg.EmbedEndpoint == "" {
		status.Status = "FAILED"
		status.Message = "Embedding service not configured (EMBED_ENDPOINT)"
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: "Error: embedding service not configured"}},
			IsError: true,
		}
	}

	// Collect texts: from data arg, or from dataset files via DB
	texts := []string{data}

	// Build orchestrator config (mirrors cognifyHandler in api.go)
	pipeCfg := orchestrator.Config{
		ChunkStrategy:    "merged",
		MinChunkChars:    50,
		MaxChunkChars:    2000,
		LLMEndpoint:      os.Getenv("LLM_ENDPOINT"),
		LLMModel:         os.Getenv("LLM_MODEL"),
		LLMProvider:      h.cfg.LLMProvider,
		BM25Indexes:      h.cfg.BM25Indexes,
		LLMConcurrency:   1,
		EmbedEndpoint:    h.cfg.EmbedEndpoint,
		EmbedModel:       h.cfg.EmbedModel,
		Neo4jURL:         h.cfg.Neo4jCfg.Neo4jURL,
		Neo4jUser:        h.cfg.Neo4jCfg.Neo4jUser,
		Neo4jPassword:    h.cfg.Neo4jCfg.Neo4jPassword,
		Neo4jDatabase:    h.cfg.Neo4jCfg.Neo4jDatabase,
		Collection:       collection,
		Collections:      h.cfg.Collections,
		GenerateTriplets: true,
		DatasetID:        runID,
		DB:               h.cfg.DB,
		LLMCache:            h.cfg.LLMCache,
		UseStructuredOutput: func() *bool { b := true; return &b }(),
	}
	// RAG mode: skip graph extraction (chunk+embed only, no LLM needed)
	if mode, _ := args["mode"].(string); mode == "rag" {
		pipeCfg.SkipGraph = true
		pipeCfg.GenerateTriplets = false
	}
	// Custom chunking strategy
	if cs, _ := args["chunk_strategy"].(string); cs != "" {
		pipeCfg.ChunkStrategy = cs
	}
	// Overlap for sliding window chunking
	if oc, ok := args["overlap_chars"].(float64); ok && oc > 0 {
		pipeCfg.OverlapChars = int(oc)
	}
	// Snap to sentence boundary (default true)
	if snap, ok := args["snap_to_sentence"].(bool); ok {
		pipeCfg.SnapToSentence = &snap
	}
	// Parent-child chunking
	if pc, ok := args["parent_child"].(bool); ok && pc {
		pipeCfg.ParentChild = true
	}
	// Document metadata
	if dt, _ := args["document_title"].(string); dt != "" {
		pipeCfg.DocumentTitle = dt
	}
	if di, _ := args["document_id"].(string); di != "" {
		pipeCfg.DocumentID = di
	}
	if cr, ok := args["community_resolution"].(float64); ok && cr > 0 {
		pipeCfg.CommunityResolution = cr
	}
	if dt, ok := args["dedup_threshold"].(float64); ok && dt > 0 {
		pipeCfg.DedupThreshold = dt
	}
	if minC, ok := args["min_chunk_chars"].(float64); ok && minC > 0 {
		pipeCfg.MinChunkChars = int(minC)
	}
	if maxC, ok := args["max_chunk_chars"].(float64); ok && maxC > 0 {
		pipeCfg.MaxChunkChars = int(maxC)
	}
	if cp, _ := args["custom_prompt"].(string); cp != "" {
		pipeCfg.SystemPrompt = cp
	}
	// Optional room + tags propagated to chunk metadata for filtered search.
	if room, _ := args["room"].(string); room != "" {
		pipeCfg.Room = room
	}
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				pipeCfg.Tags = append(pipeCfg.Tags, s)
			}
		}
	}
	// Ontology-guided extraction: append allowed entity types to prompt
	if ontologySuffix := GetOntologyPromptSuffix(collection); ontologySuffix != "" {
		pipeCfg.SystemPrompt += ontologySuffix
	}

	// Run pipeline in background
	go func() {
		progressCh := make(chan orchestrator.Progress, 100)
		errCh := make(chan error, 1)

		go func() {
			errCh <- orchestrator.Run(context.Background(), texts, pipeCfg, progressCh)
		}()

		// Track progress
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

		// Persist pipeline status to data table
		PersistPipelineStatus(h.cfg.DB, runID, collection,
			status.Status, status.Chunks, status.Entities, status.Edges, status.ElapsedMs)

		// Log heartbeat
		h.logHeartbeat("cognify", map[string]any{
			"run_id":     runID,
			"collection": collection,
			"status":     status.Status,
			"chunks":     status.Chunks,
			"entities":   status.Entities,
			"elapsed_ms": status.ElapsedMs,
		})
	}()

	return mcpToolResult{
		Content: []mcpContent{{
			Type: "text",
			Text: fmt.Sprintf("Cognify pipeline started. Run ID: %s. Use cognify_status tool to check progress.", runID),
		}},
	}
}

func (h *mcpHandler) toolSearch(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["search_query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'search_query' required"}}, IsError: true}
	}

	searchType, _ := args["search_type"].(string)
	if searchType == "" {
		searchType = "AUTO"
	}

	topK := 10
	if tk, ok := args["top_k"].(float64); ok {
		topK = int(tk)
	}

	collection, _ := args["collection"].(string)

	// Optional metadata filters (room + tags). When set, we overfetch and post-filter.
	roomFilter, _ := args["room"].(string)
	var tagFilters []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				tagFilters = append(tagFilters, s)
			}
		}
	}
	hasMetaFilter := roomFilter != "" || len(tagFilters) > 0
	doRerank, _ := args["rerank"].(bool)
	doParentChild, _ := args["parent_child"].(bool)
	doMultiQuery, _ := args["multi_query"].(bool)
	doDedup := true // default: dedup enabled
	if dd, ok := args["dedup"].(bool); ok {
		doDedup = dd
	}
	doGraphRerank, _ := args["graph_rerank"].(bool)
	searchMode, _ := args["mode"].(string)
	if searchMode == "" {
		searchMode = "auto"
	}

	// Mode gating: restrict search types based on mode
	if searchMode == "rag" || searchMode == "graph" {
		allowed := searchTypesForMode(searchMode)
		if allowed != nil && !allowed[strings.ToUpper(searchType)] && searchType != "AUTO" && searchType != "" {
			searchType = defaultTypeForMode(searchMode)
		}
	}

	// Smart routing: AUTO → heuristic router selects best strategy
	var routingInfo *router.Decision
	upperType := strings.ToUpper(searchType)
	if upperType == "AUTO" || upperType == "FEELING_LUCKY" {
		caps := capabilitiesFromConfig(h.cfg)
		// Mode-aware: suppress graph capabilities in rag mode
		if searchMode == "rag" {
			caps.HasNeo4j = false
			caps.HasPostgres = false
			caps.HasCommunities = false
		}
		d := router.Route(query, caps)
		routingInfo = &d
		searchType = d.SearchType
	}

	// Map search_type to feature flags (unless already set explicitly via args).
	switch strings.ToUpper(searchType) {
	case "PARENT_CHILD":
		doParentChild = true
	case "MULTI_QUERY":
		doMultiQuery = true
	case "RERANK":
		doRerank = true
	case "GRAPH_RERANK":
		doGraphRerank = true
	case "BASIC", "CHUNKS", "AUTO", "":
		// default vector search — no flag override
	}

	// Execute search
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results (embedding service not configured)"}}}
	}

	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)

	// Build reranker client if requested
	var rerankClient *rerank.Client
	if doRerank {
		rerankClient = rerank.NewClient(h.cfg.RerankEndpoint, h.cfg.RerankModel, 0, h.cfg.RerankTimeoutMs)
	}
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections, rerankClient)

	var colls []string
	if collection != "" {
		colls = []string{collection}
	} else {
		colls = h.cfg.Collections.List()
	}
	// Overfetch when metadata filtering is requested so post-filter still
	// returns enough results.
	fetchK := topK
	if hasMetaFilter {
		fetchK = topK * 3
	}
	var results []map[string]any
	wasReranked := false

	for _, coll := range colls {
		var res []pipeline.ScoredResult
		var err error

		switch {
		case doParentChild:
			res, err = sp.SearchByTextParentChild(ctx, coll, query, fetchK)
		case doMultiQuery && h.cfg.LLMProvider != nil:
			res, err = sp.SearchByTextMultiQuery(ctx, coll, query, fetchK,
				h.cfg.LLMProvider, os.Getenv("LLM_MODEL"), 3)
		case doRerank && rerankClient.Enabled():
			var reranked bool
			res, reranked, err = sp.SearchByTextWithRerank(ctx, coll, query, fetchK)
			if reranked {
				wasReranked = true
			}
		case doGraphRerank && h.cfg.DB != nil:
			res, err = sp.SearchByText(ctx, coll, query, fetchK)
			if err == nil && len(res) > 0 {
				// Convert to graphrank.ScoredResult, rerank, convert back
				grRes := make([]graphrank.ScoredResult, len(res))
				for i, r := range res {
					grRes[i] = graphrank.ScoredResult{ID: r.ID, Score: r.Score, Metadata: r.Metadata}
				}
				queryEntities := extractQueryEntities(ctx, h.cfg.DB, query)
				grReranked := graphrank.RerankWithGraph(ctx, h.cfg.DB, queryEntities, grRes, graphrank.DefaultConfig())
				for i, r := range grReranked {
					res[i] = pipeline.ScoredResult{ID: r.ID, Score: r.Score, Metadata: r.Metadata}
				}
			}
		default:
			res, err = sp.SearchByText(ctx, coll, query, fetchK)
		}
		if err != nil {
			continue
		}

		// Dedup overlapping results (enabled by default)
		if doDedup && len(res) > 1 {
			res = pipeline.DeduplicateResults(res, 0.85)
		}

		for _, r := range res {
			if hasMetaFilter && !mcp.ChunkMetaMatches(r.Metadata, roomFilter, tagFilters) {
				continue
			}
			results = append(results, map[string]any{
				"id": r.ID, "score": r.Score, "collection": coll, "metadata": string(r.Metadata),
			})
			if len(results) >= topK {
				break
			}
		}
		if len(results) >= topK {
			break
		}
	}

	// Build response with routing metadata
	response := map[string]any{
		"results":     results,
		"search_type": searchType,
		"reranked":    wasReranked,
	}
	if routingInfo != nil {
		response["routing"] = map[string]any{
			"selected_type": routingInfo.SearchType,
			"reason":        routingInfo.Reason,
			"confidence":    routingInfo.Confidence,
			"alternatives":  routingInfo.Alternatives,
			"source":        "routed",
		}
	}

	if len(results) == 0 {
		response["results"] = []any{}
	}

	out, _ := json.MarshalIndent(response, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolListData(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	// Optional filters
	var wantTags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				wantTags = append(wantTags, s)
			}
		}
	}
	roomFilter, _ := args["room"].(string)
	hasFilter := len(wantTags) > 0 || roomFilter != ""

	var items []map[string]any
	if !hasFilter {
		colls := h.cfg.Collections.List()
		for _, c := range colls {
			items = append(items, map[string]any{"collection": c, "type": "vector_collection"})
		}
	}

	// Also list datasets / data items from DB
	if h.cfg.DB != nil {
		if hasFilter {
			// Tag/room filter operates on the data table directly.
			var conds []string
			var qargs []any
			pos := 1
			if roomFilter != "" {
				conds = append(conds, fmt.Sprintf("room = $%d", pos))
				qargs = append(qargs, roomFilter)
				pos++
			}
			for _, t := range wantTags {
				// JSON tag list is stored as a string like ["a","b"]; LIKE works on both PG/SQLite.
				conds = append(conds, fmt.Sprintf("tags LIKE $%d", pos))
				qargs = append(qargs, "%\""+t+"\"%")
				pos++
			}
			sqlStr := `SELECT id, name, extension, room, tags FROM data`
			if len(conds) > 0 {
				sqlStr += " WHERE " + strings.Join(conds, " AND ")
			}
			sqlStr += " ORDER BY created_at DESC LIMIT 200"
			rows, err := h.cfg.DB.QueryContext(ctx, Q(sqlStr), qargs...)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var id, name, ext, rm, tg string
					rows.Scan(&id, &name, &ext, &rm, &tg)
					items = append(items, map[string]any{
						"id": id, "name": name, "extension": ext,
						"room": rm, "tags": json.RawMessage(tg),
						"type": "data",
					})
				}
			}
		} else {
			rows, err := h.cfg.DB.QueryContext(ctx, Q("SELECT id, name FROM datasets ORDER BY created_at DESC LIMIT 100"))
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var id, name string
					rows.Scan(&id, &name)
					items = append(items, map[string]any{"id": id, "name": name, "type": "dataset"})
				}
			}
		}
	}

	out, _ := json.MarshalIndent(items, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// toolDelete is a thin shim over mcp.ToolDelete. F-4 wave 3a moved the
// body into pkg/mcp to establish the Deps-interface pattern; this wrapper
// stays until the handler itself migrates.
func (h *mcpHandler) toolDelete(ctx context.Context, args map[string]any) mcpToolResult {
	return mcp.ToolDelete(ctx, h, args)
}

func (h *mcpHandler) toolPrune(ctx context.Context) mcpToolResult {
	if h.cfg.DB != nil {
		h.cfg.DB.ExecContext(ctx, "DELETE FROM dataset_data")
		h.cfg.DB.ExecContext(ctx, "DELETE FROM data")
		h.cfg.DB.ExecContext(ctx, "DELETE FROM datasets")
		h.cfg.DB.ExecContext(ctx, "DELETE FROM graph_nodes")
		h.cfg.DB.ExecContext(ctx, "DELETE FROM graph_edges")
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "All data pruned."}}}
}

func (h *mcpHandler) toolCognifyStatus(args map[string]any) mcpToolResult {
	runID, _ := args["run_id"].(string)
	if runID == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'run_id' required"}}, IsError: true}
	}

	val, ok := pipelineRuns.Load(runID)
	if !ok {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Run %s not found.", runID)}}, IsError: true}
	}

	out, _ := json.MarshalIndent(val, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolAdd(ctx context.Context, args map[string]any) mcpToolResult {
	data, _ := args["data"].(string)
	if data == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'data' required"}}, IsError: true}
	}

	datasetName, _ := args["dataset_name"].(string)
	if datasetName == "" {
		datasetName = "default"
	}

	// Ingest data to disk via pkg/ingest
	storagePath := h.cfg.StoragePath
	if storagePath == "" {
		storagePath = "data/uploads"
	}

	ownerID := "" // MCP tools run without user context for now

	// Optional metadata: tags + room
	var tags []string
	if rawTags, ok := args["tags"].([]any); ok {
		for _, t := range rawTags {
			if s, ok := t.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	room, _ := args["room"].(string)

	items := []ingest.Item{{Text: data, DatasetName: datasetName, OwnerID: ownerID, Tags: tags, Room: room}}
	results, err := ingest.Ingest(items, storagePath)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Ingest error: %s", err.Error())}},
			IsError: true,
		}
	}

	// Write metadata to DB
	dsID := uuid.New().String()
	if h.cfg.DB != nil {
		mw := ingest.NewMetadataWriterFromDB(h.cfg.DB)
		mw.WriteMetadata(context.Background(), results, ownerID, dsID, datasetName)
	}

	return mcpToolResult{Content: []mcpContent{{
		Type: "text",
		Text: fmt.Sprintf("Data ingested into dataset '%s' (dataset_id: %s, items: %d). Use 'cognify' tool to build knowledge graph.", datasetName, dsID, len(results)),
	}}}
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

		status := &pipelineRunStatus{
			RunID: runID, Status: "RUNNING", Stage: "starting", StartedAt: time.Now(),
		}
		pipelineRuns.Store(runID, status)

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

func (h *mcpHandler) toolSaveMemory(ctx context.Context, args map[string]any) mcpToolResult {
	key, _ := args["key"].(string)
	value, _ := args["value"].(string)
	if key == "" || value == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'key' and 'value' required"}}, IsError: true}
	}
	memType, _ := args["type"].(string)
	if memType == "" {
		memType = "project"
	}
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)
	if hall != "" && !mcp.IsValidHall(hall) {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error: invalid hall '%s'. Valid values: %s", hall, strings.Join(mcp.ValidHalls(), ", "))}}, IsError: true}
	}
	pin, _ := args["pin"].(bool)
	pinPriority := 0
	if pp, ok := args["pin_priority"].(float64); ok {
		pinPriority = int(pp)
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)

	// User isolation: extract ownerID from context (set by handleToolCall)
	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}

	pinInt := 0
	if pin {
		pinInt = 1
	}
	upsertSQL := `INSERT INTO memories (id, key, value, type, owner_id, collection_name, room, hall, is_pinned, pin_priority, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT(key, owner_id) DO UPDATE SET value = $3, type = $4, collection_name = $6, room = $7, hall = $8, is_pinned = $9, pin_priority = $10, updated_at = $12`
	q, qargs := QArgs(upsertSQL, id, key, value, memType, ownerID, collectionName, room, hall, pinInt, pinPriority, now, now)
	if _, err := h.cfg.DB.ExecContext(ctx, q, qargs...); err != nil {
		if h.cfg.Logger != nil {
			h.cfg.Logger.Error("save_memory SQL failed", err, map[string]any{"key": key})
		}
	}

	// Vector-index the memory for semantic recall
	if h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil {
		go func() {
			embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 1, 1)
			embedCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			vec, err := embedClient.EmbedSingle(embedCtx, key+" "+value)
			if err == nil {
				memColl := "_memories"
				if collectionName != "" {
					memColl = "_memories_" + collectionName
				}
				meta, _ := json.Marshal(map[string]string{
					"key": key, "value": value, "type": memType,
					"collection": collectionName, "memory_id": id,
				})
				h.cfg.Collections.Insert(memColl, id, vec, meta)
			}
		}()
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Memory saved: %s = %s (type: %s)", key, truncate(value, 100), memType)}}}
}

func (h *mcpHandler) toolRecallMemory(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'query' required"}}, IsError: true}
	}
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)

	// User isolation
	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}

	// SQL fallback path is the source of truth for room/hall filtering since
	// historical vector metadata may not include these fields. Vector path is
	// kept for fast semantic recall when no structural filters are provided.
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	// Strategy 1: Vector semantic search (only when no structural filter — keeps it cheap)
	if room == "" && hall == "" && h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil {
		embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 1, 1)
		vec, err := embedClient.EmbedSingle(ctx, query)
		if err == nil {
			memColl := "_memories"
			if collectionName != "" {
				memColl = "_memories_" + collectionName
			}
			results, err := h.cfg.Collections.Search(memColl, vec, 10)
			if err == nil && len(results) > 0 {
				var items []map[string]string
				for _, r := range results {
					var meta map[string]string
					if err := json.Unmarshal(r.Data, &meta); err == nil {
						items = append(items, meta)
					}
				}
				if len(items) > 0 {
					data, _ := json.MarshalIndent(items, "", "  ")
					return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(data)}}}
				}
			}
		}
	}

	// Strategy 2: SQL LIKE search with room/hall filters
	pattern := "%" + query + "%"
	var conds []string
	var qargs []any
	pos := 1
	conds = append(conds, fmt.Sprintf("(key LIKE $%d OR value LIKE $%d)", pos, pos+1))
	qargs = append(qargs, pattern, pattern)
	pos += 2
	conds = append(conds, fmt.Sprintf("(owner_id = $%d OR owner_id = '')", pos))
	qargs = append(qargs, ownerID)
	pos++
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
		pos++
	}
	if room != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, room)
		pos++
	}
	if hall != "" {
		conds = append(conds, fmt.Sprintf("hall = $%d", pos))
		qargs = append(qargs, hall)
		pos++
	}
	sqlStr := `SELECT id, key, value, type, owner_id, room, hall, created_at, updated_at
			 FROM memories WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY updated_at DESC LIMIT 20`
	rows, err := h.cfg.DB.QueryContext(ctx, Q(sqlStr), qargs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}}, IsError: true}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, oid, rm, hl, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &oid, &rm, &hl, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": oid, "room": rm, "hall": hl,
			"created_at": ca, "updated_at": ua,
		})
	}

	if len(results) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No memories found matching query."}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolListMemories(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	filterType, _ := args["type"].(string)
	collectionName, _ := args["collection"].(string)
	room, _ := args["room"].(string)
	hall, _ := args["hall"].(string)

	var conds []string
	var qargs []any
	pos := 1
	if filterType != "" {
		conds = append(conds, fmt.Sprintf("type = $%d", pos))
		qargs = append(qargs, filterType)
		pos++
	}
	if collectionName != "" {
		conds = append(conds, fmt.Sprintf("collection_name = $%d", pos))
		qargs = append(qargs, collectionName)
		pos++
	}
	if room != "" {
		conds = append(conds, fmt.Sprintf("room = $%d", pos))
		qargs = append(qargs, room)
		pos++
	}
	if hall != "" {
		conds = append(conds, fmt.Sprintf("hall = $%d", pos))
		qargs = append(qargs, hall)
		pos++
	}
	sqlStr := `SELECT id, key, value, type, owner_id, room, hall, is_pinned, pin_priority, created_at, updated_at FROM memories`
	if len(conds) > 0 {
		sqlStr += " WHERE " + strings.Join(conds, " AND ")
	}
	sqlStr += " ORDER BY updated_at DESC LIMIT 100"

	rows, err := h.cfg.DB.QueryContext(ctx, Q(sqlStr), qargs...)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, ownerID, rm, hl, ca, ua string
		var pinned, prio int
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &rm, &hl, &pinned, &prio, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "room": rm, "hall": hl,
			"is_pinned": pinned == 1, "pin_priority": prio,
			"created_at": ca, "updated_at": ua,
		})
	}

	if results == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// ── Chat History handlers ──

func (h *mcpHandler) toolSaveChat(ctx context.Context, args map[string]any) mcpToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'session_id' required"}}, IsError: true}
	}

	messagesRaw, ok := args["messages"].([]any)
	if !ok || len(messagesRaw) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'messages' array required"}}, IsError: true}
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}

	saved := 0
	for _, msgRaw := range messagesRaw {
		msg, ok := msgRaw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		content, _ := msg["content"].(string)
		if role == "" || content == "" {
			continue
		}

		id := uuid.New().String()
		now := time.Now().UTC()

		// Map role to query/response fields
		query := ""
		response := ""
		if role == "user" {
			query = content
		} else {
			response = content
		}

		h.cfg.DB.ExecContext(ctx,
			Q(`INSERT INTO interactions (id, session_id, user_id, query, response, search_type, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`),
			id, sessionID, "", query, response, "chat", now)
		saved++
	}

	return mcpToolResult{Content: []mcpContent{{
		Type: "text",
		Text: fmt.Sprintf("Saved %d messages to session %s", saved, sessionID),
	}}}
}

func (h *mcpHandler) toolRecallChat(ctx context.Context, args map[string]any) mcpToolResult {
	sessionID, _ := args["session_id"].(string)
	if sessionID == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'session_id' required"}}, IsError: true}
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	rows, err := h.cfg.DB.QueryContext(ctx,
		Q(`SELECT id, query, response, created_at FROM interactions
		 WHERE session_id = $1 ORDER BY created_at LIMIT 100`), sessionID)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var messages []map[string]any
	for rows.Next() {
		var id, query, response, ca string
		if err := rows.Scan(&id, &query, &response, &ca); err != nil {
			continue
		}
		if query != "" {
			messages = append(messages, map[string]any{"role": "user", "content": query, "created_at": ca})
		}
		if response != "" {
			messages = append(messages, map[string]any{"role": "assistant", "content": response, "created_at": ca})
		}
	}

	if len(messages) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No messages found for session."}}}
	}
	out, _ := json.MarshalIndent(messages, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolSearchChats(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'query' required"}}, IsError: true}
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	pattern := "%" + query + "%"
	rows, err := h.cfg.DB.QueryContext(ctx,
		Q(`SELECT id, session_id, query, response, created_at FROM interactions
		 WHERE query LIKE $1 OR response LIKE $2
		 ORDER BY created_at DESC LIMIT 20`), pattern, pattern)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, sid, q, r, ca string
		if err := rows.Scan(&id, &sid, &q, &r, &ca); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "session_id": sid, "query": q, "response": r, "created_at": ca,
		})
	}

	if len(results) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No matching chats found."}}}
	}
	out, _ := json.MarshalIndent(results, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

// truncate cuts a string to maxLen and adds "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

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

func (h *mcpHandler) toolAddFeedback(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'query' required"}}, IsError: true}
	}
	rating := 0
	if r, ok := args["rating"].(float64); ok {
		rating = int(r)
	}
	if rating < 1 || rating > 5 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'rating' must be 1-5"}}, IsError: true}
	}
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}

	resultID, _ := args["result_id"].(string)
	collection, _ := args["collection"].(string)
	searchType, _ := args["search_type"].(string)
	comment, _ := args["comment"].(string)
	userID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		userID = uid
	}

	id := uuid.New().String()
	h.cfg.DB.ExecContext(ctx,
		Q(`INSERT INTO search_feedback (id, query, result_id, collection, search_type, rating, comment, user_id)
		   VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`),
		id, query, resultID, collection, searchType, rating, comment, userID)

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Feedback saved: rating=%d for query '%s'", rating, truncate(query, 50))}}}
}

func (h *mcpHandler) toolGetFeedbackStats(ctx context.Context, args map[string]any) mcpToolResult {
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"total":0}`}}}
	}
	collection, _ := args["collection"].(string)

	var total int
	var avgRating float64
	var worstQuery string

	if collection != "" {
		h.cfg.DB.QueryRowContext(ctx,
			Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback WHERE collection = $1`), collection).Scan(&total, &avgRating)
		h.cfg.DB.QueryRowContext(ctx,
			Q(`SELECT COALESCE(query,'') FROM search_feedback WHERE collection = $1 ORDER BY rating ASC LIMIT 1`), collection).Scan(&worstQuery)
	} else {
		h.cfg.DB.QueryRowContext(ctx,
			Q(`SELECT COUNT(*), COALESCE(AVG(rating),0) FROM search_feedback`)).Scan(&total, &avgRating)
		h.cfg.DB.QueryRowContext(ctx,
			Q(`SELECT COALESCE(query,'') FROM search_feedback ORDER BY rating ASC LIMIT 1`)).Scan(&worstQuery)
	}

	out, _ := json.MarshalIndent(map[string]any{
		"total": total, "avg_rating": avgRating, "worst_query": worstQuery, "collection": collection,
	}, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolSetContext(sess *mcpSession, args map[string]any) mcpToolResult {
	collection, _ := args["collection"].(string)
	if collection == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'collection' required"}}, IsError: true}
	}
	if sess == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: no active session (send initialize first)"}}, IsError: true}
	}
	// Validate collection exists (or allow setting for future use)
	exists := h.cfg.Collections != nil && h.cfg.Collections.Has(collection)
	sess.DefaultCollection = collection

	status := "set"
	if !exists {
		status = "set (collection not yet created — will be used when data is added)"
	}
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Context %s: default collection = '%s'", status, collection)}}}
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
