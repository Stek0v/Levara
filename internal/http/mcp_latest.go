package http

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/gofiber/fiber/v2"

	"github.com/stek0v/levara/pkg/mcp"
)

const latestMCPPath = "/mcp/2026-07-28"

// handleLatestRPC implements MCP 2026-07-28. Unlike the legacy handler it
// creates no session and each request carries its own protocol metadata.
func (h *mcpHandler) handleLatestRPC(c *fiber.Ctx) error {
	if !validMCPOrigin(c) {
		return c.SendStatus(fiber.StatusForbidden)
	}
	if !acceptsMCPResponse(c.Get("Accept")) {
		return c.Status(fiber.StatusNotAcceptable).SendString("Accept must include application/json and text/event-stream")
	}

	var req jsonRPCRequest
	if err := c.BodyParser(&req); err != nil {
		return latestMCPError(c, req.ID, fiber.StatusBadRequest, -32700, "Parse error", nil)
	}
	if err := validateLatestMCPRequest(c, req); err != nil {
		return latestMCPError(c, req.ID, fiber.StatusBadRequest, err.Code, err.Message, err.Data)
	}
	if req.ID == nil || string(req.ID) == "null" {
		return c.SendStatus(fiber.StatusAccepted)
	}

	// Auth gate. The stateless transport must enforce RequireAuth as strictly
	// as the legacy session transport (mcp.go authenticates every
	// non-initialize/non-ping request); without this gate an unauthenticated
	// caller could reach tools/call and resources/* under -require-auth
	// because handleToolCallWithSession tolerates auth errors (it falls back
	// to an empty actor) and APIKeyAllows("") is permissive. server/discover
	// and tools/list stay public for capability negotiation; tools/call,
	// resources/list and resources/read fail closed with 404 — matching the
	// legacy transport's "can't establish an owner" signal.
	switch req.Method {
	case "tools/call", "resources/list", "resources/read":
		if _, authErr := h.authenticateMCPRequest(c); authErr != nil {
			return c.SendStatus(fiber.StatusNotFound)
		}
	}

	switch req.Method {
	case "server/discover":
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{mcp.LatestProtocolVersion},
			"capabilities":      map[string]any{"tools": map[string]any{}, "resources": map[string]any{}},
			"_meta":             map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "levara", "version": "1.0.0"}},
			"instructions":      "Pass collection explicitly to every collection-aware tool call.",
		}})
	case "tools/list":
		return c.JSON(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": configuredLatestMCPToolDescriptors()}})
	case "tools/call":
		// set_context is session-scoped and hidden from the stateless
		// tools/list; reject it here instead of letting dispatch return a
		// misleading session error (finding M3, 2026-09-03 review).
		if toolNameFromParams(req) == "set_context" {
			return latestMCPError(c, req.ID, fiber.StatusBadRequest, -32602,
				"set_context requires the legacy session transport; pass collection explicitly on each call", nil)
		}
		return h.handleToolCallWithSession(c, req, nil)
	case "resources/list":
		return h.handleResourcesList(c, req)
	case "resources/read":
		return h.handleResourcesRead(c, req)
	default:
		return latestMCPError(c, req.ID, fiber.StatusNotFound, -32601, "Method not found: "+req.Method, nil)
	}
}

func toolNameFromParams(req jsonRPCRequest) string {
	var params struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(req.Params, &params) != nil {
		return ""
	}
	return params.Name
}

func configuredLatestMCPToolDescriptors() []mcp.Tool {
	tools := configuredMCPToolDescriptors()
	for i := range tools {
		if tools[i].Name == "set_context" {
			return append(tools[:i:i], tools[i+1:]...)
		}
	}
	return tools
}

func acceptsMCPResponse(accept string) bool {
	accept = strings.ToLower(accept)
	return strings.Contains(accept, "application/json") && strings.Contains(accept, "text/event-stream")
}

func validMCPOrigin(c *fiber.Ctx) bool {
	origin := c.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	host := strings.ToLower(c.Hostname())
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		(host == "localhost" || host == "127.0.0.1" || host == "::1") &&
		strings.EqualFold(u.Hostname(), host)
}

func validateLatestMCPRequest(c *fiber.Ctx, req jsonRPCRequest) *rpcError {
	if req.JSONRPC != "2.0" {
		return &rpcError{Code: -32600, Message: "Invalid JSON-RPC version"}
	}
	var params map[string]any
	if json.Unmarshal(req.Params, &params) != nil {
		return &rpcError{Code: -32020, Message: "Header mismatch: params must contain _meta"}
	}
	meta, _ := params["_meta"].(map[string]any)
	bodyVersion, _ := meta["io.modelcontextprotocol/protocolVersion"].(string)
	headerVersion := c.Get("MCP-Protocol-Version")
	if headerVersion == "" || bodyVersion == "" || headerVersion != bodyVersion {
		return &rpcError{Code: -32020, Message: "Header mismatch: MCP-Protocol-Version must match _meta"}
	}
	if bodyVersion != mcp.LatestProtocolVersion {
		return &rpcError{Code: -32022, Message: "Unsupported protocol version", Data: map[string]any{"supported": []string{mcp.LatestProtocolVersion}, "requested": bodyVersion}}
	}
	if _, ok := meta["io.modelcontextprotocol/clientInfo"].(map[string]any); !ok {
		return &rpcError{Code: -32020, Message: "Header mismatch: _meta must include clientInfo"}
	}
	if _, ok := meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any); !ok {
		return &rpcError{Code: -32020, Message: "Header mismatch: _meta must include clientCapabilities"}
	}
	if c.Get("Mcp-Method") != req.Method {
		return &rpcError{Code: -32020, Message: "Header mismatch: Mcp-Method must match method"}
	}
	var expectedName string
	nameRequired := false
	switch req.Method {
	case "tools/call":
		expectedName, _ = params["name"].(string)
		nameRequired = true
	case "resources/read":
		expectedName, _ = params["uri"].(string)
		nameRequired = true
	}
	if nameRequired {
		name, err := decodeMCPHeaderValue(c.Get("Mcp-Name"))
		if expectedName == "" || err != nil || name != expectedName {
			return &rpcError{Code: -32020, Message: "Header mismatch: Mcp-Name must match request parameters"}
		}
	}
	return nil
}

func decodeMCPHeaderValue(value string) (string, error) {
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(strings.TrimPrefix(value, "=?base64?"), "?="))
		return string(decoded), err
	}
	return value, nil
}

func latestMCPError(c *fiber.Ctx, id json.RawMessage, status, code int, message string, data any) error {
	return c.Status(status).JSON(jsonRPCResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}})
}
