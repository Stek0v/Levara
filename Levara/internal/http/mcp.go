// mcp.go — Model Context Protocol (MCP) server for AI agent integration.
// Implements JSON-RPC 2.0 over HTTP with Cognee-compatible tool set.
// Compatible with Claude Desktop, Cursor, Cline via MCP protocol.
package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/embed"
	"github.com/stek0v/cognevra/pkg/git"
	"github.com/stek0v/cognevra/pkg/ingest"
	"github.com/stek0v/cognevra/pkg/orchestrator"
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
		Description: "Search the knowledge graph using various strategies: CHUNKS (vector), RAG_COMPLETION (vector+LLM), GRAPH_COMPLETION, HYBRID (vector+BM25), TEMPORAL, SUMMARIES, CHUNKS_LEXICAL (BM25).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"search_query": map[string]any{"type": "string", "description": "Natural language search query"},
				"search_type":  map[string]any{"type": "string", "description": "Search strategy: CHUNKS, RAG_COMPLETION, GRAPH_COMPLETION, HYBRID, TEMPORAL, SUMMARIES, CHUNKS_LEXICAL", "default": "CHUNKS"},
				"top_k":        map[string]any{"type": "integer", "description": "Number of results to return", "default": 10},
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
				"key":   map[string]any{"type": "string", "description": "Memory key"},
				"value": map[string]any{"type": "string", "description": "Memory value"},
				"type":  map[string]any{"type": "string", "description": "Memory type: user, project, feedback"},
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
				"query": map[string]any{"type": "string", "description": "Search query for memories"},
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
				"type": map[string]any{"type": "string", "description": "Optional filter: user, project, feedback"},
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
				"query": map[string]any{"type": "string", "description": "Search query across chat history"},
			},
			"required": []string{"query"},
		},
	},
}

// RegisterMCPAPI registers MCP JSON-RPC endpoint.
func RegisterMCPAPI(app fiber.Router, cfg APIConfig) {
	handler := &mcpHandler{cfg: cfg}
	app.Post("/mcp", handler.handleRPC)
	app.Get("/mcp", handler.handleSSE) // SSE transport placeholder
}

type mcpHandler struct {
	cfg APIConfig
}

func (h *mcpHandler) handleRPC(c *fiber.Ctx) error {
	var req jsonRPCRequest
	if err := c.BodyParser(&req); err != nil {
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32700, Message: "Parse error"},
		})
	}

	switch req.Method {
	case "initialize":
		return c.JSON(jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
				"serverInfo": map[string]any{
					"name":    "Levara",
					"version": "1.0.0",
				},
			},
		})

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

	case "notifications/initialized":
		// Client acknowledgment, no response needed
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}})

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

	result := h.executeTool(c.Context(), params.Name, params.Arguments)
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
		searchType = "CHUNKS"
	}

	topK := 10
	if tk, ok := args["top_k"].(float64); ok {
		topK = int(tk)
	}

	// Execute search
	if h.cfg.EmbedEndpoint == "" || h.cfg.Collections == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results (embedding service not configured)"}}}
	}

	embedClient := embed.NewClient(h.cfg.EmbedEndpoint, h.cfg.EmbedModel, 16, 1)
	sp := pipeline.NewSearchPipeline(embedClient, h.cfg.Collections)

	colls := h.cfg.Collections.List()
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

	if len(results) == 0 {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "No results found."}}}
	}

	out, _ := json.MarshalIndent(results, "", "  ")
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

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: database not configured"}}, IsError: true}
	}

	id := uuid.New().String()
	now := time.Now().UTC().Format(time.RFC3339)
	ownerID := ""

	if activeDBProvider == DBSQLite {
		h.cfg.DB.ExecContext(ctx,
			Q(`INSERT INTO memories (id, key, value, type, owner_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT(key, owner_id) DO UPDATE SET value = $3, type = $4, updated_at = $7`),
			id, key, value, memType, ownerID, now, now)
	} else {
		h.cfg.DB.ExecContext(ctx,
			Q(`INSERT INTO memories (id, key, value, type, owner_id, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 ON CONFLICT(key, owner_id) DO UPDATE SET value = EXCLUDED.value, type = EXCLUDED.type, updated_at = EXCLUDED.updated_at`),
			id, key, value, memType, ownerID, now, now)
	}

	return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Memory saved: %s = %s (type: %s)", key, truncate(value, 100), memType)}}}
}

func (h *mcpHandler) toolRecallMemory(ctx context.Context, args map[string]any) mcpToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "Error: 'query' required"}}, IsError: true}
	}

	if h.cfg.DB == nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: "[]"}}}
	}

	// Search memories by key and value LIKE match
	pattern := "%" + query + "%"
	rows, err := h.cfg.DB.QueryContext(ctx,
		Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
		 FROM memories WHERE key LIKE $1 OR value LIKE $2
		 ORDER BY updated_at DESC LIMIT 20`), pattern, pattern)
	if err != nil {
		return mcpToolResult{Content: []mcpContent{{Type: "text", Text: fmt.Sprintf("Error: %s", err.Error())}}, IsError: true}
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
	var rows *sql.Rows
	var err error

	if filterType != "" {
		rows, err = h.cfg.DB.QueryContext(ctx,
			Q(`SELECT id, key, value, type, owner_id, created_at, updated_at
			 FROM memories WHERE type = $1 ORDER BY updated_at DESC LIMIT 100`), filterType)
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

// handleSSE is a placeholder for SSE transport.
func (h *mcpHandler) handleSSE(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status":  "MCP SSE transport available",
		"message": "Use POST /mcp for JSON-RPC requests",
		"tools":   len(mcpTools),
	})
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
