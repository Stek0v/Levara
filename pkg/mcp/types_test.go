package mcp

import (
	"encoding/json"
	"testing"
)

// JSON wire-format tests for the MCP envelope types. These lock in the on-the-
// wire field names that external clients (Claude Code, Cursor, Cline) depend
// on — any rename here breaks every MCP client silently.

func TestJSONRPCRequest_FieldNames(t *testing.T) {
	src := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"search"}`),
	}
	raw, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search"}}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

func TestJSONRPCResponse_OmitsErrorWhenNil(t *testing.T) {
	src := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Result:  map[string]string{"k": "v"},
	}
	raw, _ := json.Marshal(src)
	if string(raw) != `{"jsonrpc":"2.0","id":1,"result":{"k":"v"}}` {
		t.Errorf("got %s", raw)
	}
}

func TestJSONRPCResponse_OmitsResultWhenError(t *testing.T) {
	src := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Error:   &RPCError{Code: -32601, Message: "Method not found"},
	}
	raw, _ := json.Marshal(src)
	want := `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`
	if string(raw) != want {
		t.Errorf("got  %s\nwant %s", raw, want)
	}
}

func TestToolResult_OmitsIsErrorWhenFalse(t *testing.T) {
	src := ToolResult{Content: []Content{{Type: "text", Text: "ok"}}}
	raw, _ := json.Marshal(src)
	want := `{"content":[{"type":"text","text":"ok"}]}`
	if string(raw) != want {
		t.Errorf("got %s, want %s", raw, want)
	}
}

func TestToolResult_IncludesIsErrorWhenTrue(t *testing.T) {
	src := ToolResult{
		Content: []Content{{Type: "text", Text: "boom"}},
		IsError: true,
	}
	raw, _ := json.Marshal(src)
	want := `{"content":[{"type":"text","text":"boom"}],"isError":true}`
	if string(raw) != want {
		t.Errorf("got %s, want %s", raw, want)
	}
}

func TestTool_RoundTrip(t *testing.T) {
	src := Tool{
		Name:        "search",
		Description: "Search",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
		},
	}
	raw, _ := json.Marshal(src)
	var got Tool
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "search" || got.Description != "Search" {
		t.Errorf("roundtrip lost fields: %+v", got)
	}
}

func TestUserIDKey_TypedConstant(t *testing.T) {
	// UserIDKey must be of type ContextKey (not plain string) to avoid
	// collisions with other packages stuffing keys into context.WithValue.
	var k any = UserIDKey
	if _, ok := k.(ContextKey); !ok {
		t.Errorf("UserIDKey is %T, want ContextKey", k)
	}
}
