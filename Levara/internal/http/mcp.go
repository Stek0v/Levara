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
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/git"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/orchestrator"
	"github.com/stek0v/cognevra/pkg/router"
	"github.com/stek0v/cognevra/pipeline"
)

// ── JSON-RPC 2.0 types ──

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any         `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── MCP types ──

// mcpUserIDKey is the context key for user isolation in MCP tools.
type contextKey string
const mcpUserIDKey contextKey = "mcp_user_id"

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

// ── Tool definitions ──

var mcpTools = []mcpTool{
	{
		Name:        "cognify",
		Description: "Transform text data into a structured knowledge graph. Extracts entities, relationships, and builds searchable graph.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"data":          map[string]any{"type": "string", "description": "Text data to process into knowledge graph"},
				"collection":    map[string]any{"type": "string", "description": "Target collection name (default: 'default')"},
				"custom_prompt": map[string]any{"type": "string", "description": "Custom LLM prompt for entity extraction"},
			},
			"required": []string{"data"},
		},
	},
	{
		Name:        "search",
		Description: "Search the knowledge graph using various strategies. Use AUTO (default) for intelligent routing that analyzes your query and selects the best strategy automatically.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"search_query": map[string]any{"type": "string", "description": "Natural language search query"},
				"search_type":  map[string]any{"type": "string", "description": "Search strategy: AUTO (intelligent routing), CHUNKS (vector), HYBRID (vector+BM25), RAG_COMPLETION (vector+LLM answer), GRAPH_COMPLETION (graph traversal+LLM), TEMPORAL (date-aware), SUMMARIES, CHUNKS_LEXICAL (BM25), CODING_RULES (code entities)", "default": "AUTO"},
				"top_k":        map[string]any{"type": "integer", "description": "Number of results to return", "default": 10},
				"collection":   map[string]any{"type": "string", "description": "Project collection name to search in. Leave empty to search all."},
			},
			"required": []string{"search_query"},
		},
	},
	{
		Name:        "list_data",
		Description: "List all datasets and their data items in the knowledge base.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "delete",
		Description: "Delete a specific dataset by ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"dataset_id": map[string]any{"type": "string", "description": "UUID of the dataset to delete"},
			},
			"required": []string{"dataset_id"},
		},
	},
	{
		Name:        "prune",
		Description: "Reset all data — removes all datasets, vectors, and graph data.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "cognify_status",
		Description: "Check the status of a running cognify pipeline by run ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id": map[string]any{"type": "string", "description": "Pipeline run ID returned by cognify"},
			},
			"required": []string{"run_id"},
		},
	},
	{
		Name:        "add",
		Description: "Ingest text data into the knowledge base for later cognification.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"data":         map[string]any{"type": "string", "description": "Text content to ingest"},
				"dataset_name": map[string]any{"type": "string", "description": "Dataset name (default: 'default')"},
				"collection":   map[string]any{"type": "string", "description": "Collection to associate with added data."},
			},
			"required": []string{"data"},
		},
	},

	// ── Git Commit Analyzer tools ──
	{
		Name:        "analyze_commits",
		Description: "Analyze git repository commits and build knowledge graph.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"repo_path": map[string]any{"type": "string", "description": "Path to git repository"},
				"since":     map[string]any{"type": "string", "description": "Date filter (e.g. 2024-01-01)"},
				"limit":     map[string]any{"type": "number", "description": "Max commits to analyze"},
			},
			"required": []string{"repo_path"},
		},
	},
	{
		Name:        "git_search",
		Description: "Search through analyzed git commits in the knowledge graph.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Search query about commits"},
			},
			"required": []string{"query"},
		},
	},

	// ── Project Memory tools ──
	{
		Name:        "save_memory",
		Description: "Save project/user memory key-value pair.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key":        map[string]any{"type": "string", "description": "Memory key"},
				"value":      map[string]any{"type": "string", "description": "Memory value"},
				"type":       map[string]any{"type": "string", "description": "Memory type: user, project, feedback"},
				"collection": map[string]any{"type": "string", "description": "Collection name to scope memory to."},
			},
			"required": []string{"key", "value"},
		},
	},
	{
		Name:        "recall_memory",
		Description: "Search memories by query.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":      map[string]any{"type": "string", "description": "Search query for memories"},
				"collection": map[string]any{"type": "string", "description": "Collection name to filter memories."},
			},
			"required": []string{"query"},
		},
	},
	{
		Name:        "list_memories",
		Description: "List all memories, optionally filtered by type.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"type":       map[string]any{"type": "string", "description": "Optional filter: user, project, feedback"},
				"collection": map[string]any{"type": "string", "description": "Collection name to filter memories."},
			},
		},
	},

	// ── Chat History tools ──
	{
		Name:        "save_chat",
		Description: "Save chat session messages.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "Chat session ID"},
				"collection": map[string]any{"type": "string", "description": "Collection to associate with chat session."},
				"messages": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"role":    map[string]any{"type": "string", "description": "Message role: user or assistant"},
							"content": map[string]any{"type": "string", "description": "Message content"},
						},
					},
					"description": "Array of chat messages",
				},
			},
			"required": []string{"session_id", "messages"},
		},
	},
	{
		Name:        "recall_chat",
		Description: "Recall chat history by session ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"session_id": map[string]any{"type": "string", "description": "Chat session ID to recall"},
				"collection": map[string]any{"type": "string", "description": "Collection to filter chat recall."},
			},
			"required": []string{"session_id"},
		},
	},
	{
		Name:        "search_chats",
		Description: "Search across all chat sessions.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":      map[string]any{"type": "string", "description": "Search query across chat history"},
				"collection": map[string]any{"type": "string", "description": "Collection to filter chat search."},
			},
			"required": []string{"query"},
		},
	},
	{
		Name:        "get_project_context",
		Description: "Get full project context: memories, collection stats, key entities, recent interactions. Call at session start for maximum context awareness.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"collection": map[string]any{"type": "string", "description": "Project collection name"},
			},
			"required": []string{"collection"},
		},
	},
}

// RegisterMCPAPI registers MCP Streamable HTTP endpoint (spec 2025-03-26).
// POST /mcp — JSON-RPC requests + notifications
// GET  /mcp — SSE stream for server-initiated messages
// DELETE /mcp — terminate session
func RegisterMCPAPI(app fiber.Router, cfg APIConfig) {
	handler := &mcpHandler{
		cfg:      cfg,
		sessions: make(map[string]*mcpSession),
	}
	app.Post("/mcp", handler.handleRPC)
	app.Get("/mcp", handler.handleSSEStream)
	app.Delete("/mcp", handler.handleDeleteSession)
	go handler.sessionCleanupLoop()
}

// mcpSession tracks a connected MCP client session.
type mcpSession struct {
	id        string
	userID    string    // from Authorization header (JWT), empty for anonymous
	createdAt time.Time
	sseCh     chan []byte // buffered channel for server-initiated SSE messages
}

type mcpHandler struct {
	cfg      APIConfig
	mu       sync.RWMutex
	sessions map[string]*mcpSession
}

// getOrValidateSession returns the session for the given ID, or nil if invalid.
func (h *mcpHandler) getOrValidateSession(sessionID string) *mcpSession {
	if sessionID == "" {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessions[sessionID]
}

// createSession creates a new MCP session and returns its ID.
func (h *mcpHandler) createSession() string {
	id := fmt.Sprintf("mcp-%d-%s", time.Now().UnixNano(), randomHex(8))
	h.mu.Lock()
	h.sessions[id] = &mcpSession{
		id:        id,
		createdAt: time.Now(),
		sseCh:     make(chan []byte, 100),
	}
	h.mu.Unlock()
	return id
}

// deleteSession removes a session.
func (h *mcpHandler) deleteSession(id string) {
	h.mu.Lock()
	if s, ok := h.sessions[id]; ok {
		close(s.sseCh)
		delete(h.sessions, id)
	}
	h.mu.Unlock()
}

// randomHex returns n random hex characters.
func randomHex(n int) string {
	b := make([]byte, n/2+1)
	rand.Read(b)
	return fmt.Sprintf("%x", b)[:n]
}

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
				"tools": mcpTools,
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
	if sess := h.getOrValidateSession(sessionID); sess != nil && sess.userID != "" {
		toolCtx = context.WithValue(toolCtx, mcpUserIDKey, sess.userID)
	}

	result := h.executeTool(toolCtx, params.Name, params.Arguments)
	return c.JSON(jsonRPCResponse{
		JSONRPC: "2.0", ID: req.ID, Result: result,
	})
}

func (h *mcpHandler) executeTool(ctx context.Context, name string, args map[string]any) mcpToolResult {
	switch name {
	case "cognify":
		return h.toolCognify(ctx, args)
	case "search":
		return h.toolSearch(ctx, args)
	case "list_data":
		return h.toolListData(ctx)
	case "delete":
		return h.toolDelete(ctx, args)
	case "prune":
		return h.toolPrune(ctx)
	case "cognify_status":
		return h.toolCognifyStatus(args)
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
	if cp, _ := args["custom_prompt"].(string); cp != "" {
		pipeCfg.SystemPrompt = cp
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

	// Smart routing: AUTO → heuristic router selects best strategy
	var routingInfo *router.Decision
	upperType := strings.ToUpper(searchType)
	if upperType == "AUTO" || upperType == "FEELING_LUCKY" {
		caps := capabilitiesFromConfig(h.cfg)
		d := router.Route(query, caps)
		routingInfo = &d
		searchType = d.SearchType
	}

	// Execute search
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results (embedding service not configured)"}}}
	}

	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections)

	var colls []string
	if collection != "" {
		colls = []string{collection}
	} else {
		colls = h.cfg.Collections.List()
	}
	var results []map[string]any

	for _, coll := range colls {
		res, err := sp.SearchByText(ctx, coll, query, topK)
		if err != nil {
			continue
		}
		for _, r := range res {
			results = append(results, map[string]any{
				"id": r.ID, "score": r.Score, "collection": coll, "metadata": string(r.Metadata),
			})
		}
		if len(results) >= topK {
			break
		}
	}

	// Build response with routing metadata
	response := map[string]any{
		"results":     results,
		"search_type": searchType,
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

func (h *mcpHandler) toolListData(ctx context.Context) mcpToolResult {
	if h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	colls := h.cfg.Collections.List()
	var items []map[string]any
	for _, c := range colls {
		items = append(items, map[string]any{"collection": c, "type": "vector_collection"})
	}

	// Also list datasets from DB
	if h.cfg.DB != nil {
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

	out, _ := json.MarshalIndent(items, "", "  ")
	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: string(out)}}}
}

func (h *mcpHandler) toolDelete(ctx context.Context, args map[string]any) mcpToolResult {
	dsID, _ := args["dataset_id"].(string)
	if dsID == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'dataset_id' required"}}, IsError: true}
	}

	if h.cfg.DB != nil {
		h.cfg.DB.ExecContext(ctx, Q("DELETE FROM datasets WHERE id = $1"), dsID)
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Dataset %s deleted.", dsID)}}}
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

	items := []ingest.Item{{Text: data, DatasetName: datasetName}}
	results, err := ingest.Ingest(items, storagePath)
	if err != nil {
		return mcpToolResult{
			Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Ingest error: %s", err.Error())}},
			IsError: true,
		}
	}

	// Write metadata to PostgreSQL if configured
	dsID := uuid.New().String()
	if h.cfg.DB != nil {
		mw := ingest.NewMetadataWriterFromDB(h.cfg.DB)
		mw.WriteMetadata(context.Background(), results, "" /* ownerID */, dsID, datasetName)
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
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections)

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

	upsertSQL := `INSERT INTO memories (id, key, value, type, owner_id, collection_name, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT(key, owner_id) DO UPDATE SET value = $3, type = $4, collection_name = $6, updated_at = $8`
	q, qargs := QArgs(upsertSQL, id, key, value, memType, ownerID, collectionName, now, now)
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

	// User isolation
	ownerID := ""
	if uid, ok := ctx.Value(mcpUserIDKey).(string); ok {
		ownerID = uid
	}

	// Strategy 1: Vector semantic search (if embed configured)
	if h.cfg.EmbedEndpoint != "" && h.cfg.Collections != nil {
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

	// Strategy 2: Fallback to LIKE search
	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	pattern := "%" + query + "%"
	var rows *sql.Rows
	var err error
	if collectionName != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE (key LIKE $1 OR value LIKE $2) AND collection_name = $3
			 AND (owner_id = $4 OR owner_id = '')
			 ORDER BY updated_at DESC LIMIT 20`), pattern, pattern, collectionName, ownerID)
	} else {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE (key LIKE $1 OR value LIKE $2)
			 AND (owner_id = $3 OR owner_id = '')
			 ORDER BY updated_at DESC LIMIT 20`), pattern, pattern, ownerID)
	}
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}}, IsError: true}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, oid, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &oid, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": oid, "created_at": ca, "updated_at": ua,
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
	var rows *sql.Rows
	var err error

	if filterType != "" && collectionName != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE type = $1 AND collection_name = $2 ORDER BY updated_at DESC LIMIT 100`), filterType, collectionName)
	} else if filterType != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE type = $1 ORDER BY updated_at DESC LIMIT 100`), filterType)
	} else if collectionName != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE collection_name = $1 ORDER BY updated_at DESC LIMIT 100`), collectionName)
	} else {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories ORDER BY updated_at DESC LIMIT 100`))
	}
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}
	defer rows.Close()

	var results []map[string]any
	for rows.Next() {
		var id, key, value, typ, ownerID, ca, ua string
		if err := rows.Scan(&id, &key, &value, &typ, &ownerID, &ca, &ua); err != nil {
			continue
		}
		results = append(results, map[string]any{
			"id": id, "key": key, "value": value, "type": typ,
			"owner_id": ownerID, "created_at": ca, "updated_at": ua,
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
		content = h.resourceMemories(c.Context(), memType, collName)

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

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: sb.String()}}}
}

// ── Session Cleanup ───────────────────────────────────────────────────────

func (h *mcpHandler) sessionCleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		now := time.Now()
		for id, s := range h.sessions {
			if now.Sub(s.createdAt) > time.Hour {
				close(s.sseCh)
				delete(h.sessions, id)
			}
		}
		h.mu.Unlock()
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
			case msg, ok := <-sess.sseCh:
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
			"tools":   len(mcpTools),
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
