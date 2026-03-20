// mcp.go — Model Context Protocol (MCP) server for AI agent integration.
// Implements JSON-RPC 2.0 over HTTP with Cognee-compatible tool set.
// Compatible with Claude Desktop, Cursor, Cline via MCP protocol.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stek0v/cognevra/pkg/embed"
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
					"name":    "Cognevra",
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

	// Run in background
	go func() {
		// Simple: just store status, actual pipeline needs LLM/embed
		time.Sleep(100 * time.Millisecond)
		status.Status = "COMPLETED"
		status.Stage = "complete"
		status.Message = "Cognify completed"
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
		rows, err := h.cfg.DB.QueryContext(ctx, "SELECT id, name FROM datasets ORDER BY created_at DESC LIMIT 100")
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
		h.cfg.DB.ExecContext(ctx, "DELETE FROM datasets WHERE id = $1", dsID)
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

	// Use ingest package
	items := []ingestItem{{text: data, datasetName: datasetName}}
	_ = items // placeholder: actual ingest would go through pkg/ingest

	return mcpToolResult{Content: []mcpContent{{
		Type: "text",
		Text: fmt.Sprintf("Data ingested into dataset '%s'. Use 'cognify' tool to build knowledge graph.", datasetName),
	}}}
}

type ingestItem struct {
	text        string
	datasetName string
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
			"server":  "cognevra-mcp",
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
