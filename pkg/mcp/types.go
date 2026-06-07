// Package mcp holds Model Context Protocol (MCP) types and helpers shared
// between the HTTP handler in internal/http and any future SDK consumers.
//
// This package is the foundation of the F-4 split: pure types and stateless
// helpers live here so the eventual handler/tools migration can lean on them
// without circular imports. See ADR-002 (forthcoming) for the full plan.
package mcp

import "encoding/json"

// ── JSON-RPC 2.0 wire types ──
//
// MCP rides on JSON-RPC 2.0 over HTTP (spec 2025-03-26). These types capture
// the request/response/error envelope; the rest is method+params dispatch.

// JSONRPCRequest is the inbound JSON-RPC 2.0 request envelope.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse is the outbound JSON-RPC 2.0 response envelope.
// Either Result OR Error is set, never both.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is the JSON-RPC 2.0 error object. Codes follow the spec
// (-32700 parse, -32600 invalid request, -32601 method not found, ...).
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ── MCP-specific types ──

// Tool describes one MCP tool exposed to clients via tools/list.
// InputSchema and OutputSchema are JSON Schema documents (draft 2020-12)
// describing the tool's parameters and its return shape respectively.
//
// OutputSchema is optional — omitempty keeps the wire format
// backward-compatible with pre-T14 clients that never saw the field. New
// tools should populate it; old tools are grandfathered in without one
// until they get audited (tools_test.go will eventually enforce its
// presence once every tool has been annotated).
type Tool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Group        string         `json:"group,omitempty"`  // optional — e.g. "memory", "graph", "search"
	Status       string         `json:"status,omitempty"` // empty => "canonical"
}

// Content is one chunk of MCP tool output. Today we only emit "text"; future
// types include "image" and "resource". Multiple content items can be
// returned from a single tool call.
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ToolResult is what a tool returns. IsError=true signals that Content holds
// an error message (e.g. validation failure) rather than data — distinct from
// JSON-RPC level errors which use the RPCError envelope.
type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"isError,omitempty"`
}

// ── Context keys ──

// ContextKey is the type used for MCP context.WithValue keys to avoid
// collisions with other packages.
type ContextKey string

// UserIDKey identifies the per-call user ID (extracted from auth) for
// scoping memories, diaries, and rate limits in tool implementations.
const UserIDKey ContextKey = "mcp_user_id"
