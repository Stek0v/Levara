package http

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/stek0v/levara/pkg/audit"
	"github.com/stek0v/levara/pkg/mcp"
)

// captureSink retains every Entry passed through Log so tests can
// assert on the side-effects of executeTool without parsing JSON
// lines from a buffer.
type captureSink struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func (s *captureSink) Log(e audit.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func TestRecordMCPAuditOK(t *testing.T) {
	sink := &captureSink{}
	h := &mcpHandler{cfg: APIConfig{MCPAudit: sink}}
	ctx := context.WithValue(context.Background(), mcpUserIDKey, "alice")
	sess := &mcp.Session{ID: "s1", UserID: "alice"}
	result := mcpToolResult{Content: []mcpContent{{Type: "text", Text: `{"ok":true}`}}}

	h.recordMCPAudit(ctx, sess, "search", map[string]any{"query": "x"}, result, 12345678)

	if len(sink.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(sink.entries))
	}
	e := sink.entries[0]
	if e.Tool != "search" || e.AgentID != "alice" || e.SessionID != "s1" {
		t.Errorf("entry fields wrong: %+v", e)
	}
	if e.Outcome != audit.OutcomeOK {
		t.Errorf("outcome=%q, want ok", e.Outcome)
	}
	if e.ResultSize != len(`{"ok":true}`) {
		t.Errorf("result_size=%d", e.ResultSize)
	}
	if e.ErrorMessage != "" {
		t.Errorf("error_message must be empty on ok, got %q", e.ErrorMessage)
	}
}

func TestRecordMCPAuditClassifiesErrors(t *testing.T) {
	cases := []struct {
		msg  string
		want audit.Outcome
	}{
		{"context deadline exceeded", audit.OutcomeTimeout},
		{"unauthorized: bad token", audit.OutcomeUnauthorized},
		{"rate limit exceeded", audit.OutcomeRateLimited},
		{"invalid collection name", audit.OutcomeClientError},
		{"internal write failure", audit.OutcomeServerError},
	}
	for _, tc := range cases {
		sink := &captureSink{}
		h := &mcpHandler{cfg: APIConfig{MCPAudit: sink}}
		result := mcpToolResult{IsError: true, Content: []mcpContent{{Type: "text", Text: tc.msg}}}
		h.recordMCPAudit(context.Background(), nil, "search", nil, result, 1000)
		if len(sink.entries) != 1 {
			t.Fatalf("%q: expected 1 entry", tc.msg)
		}
		if sink.entries[0].Outcome != tc.want {
			t.Errorf("%q: outcome=%q, want %q", tc.msg, sink.entries[0].Outcome, tc.want)
		}
		if !strings.Contains(sink.entries[0].ErrorMessage, tc.msg) {
			t.Errorf("%q: error_message missing: %q", tc.msg, sink.entries[0].ErrorMessage)
		}
	}
}

func TestRecordMCPAuditSanitizesArgs(t *testing.T) {
	var buf bytes.Buffer
	h := &mcpHandler{cfg: APIConfig{MCPAudit: audit.NewLogger(&buf)}}
	args := map[string]any{
		"query":    "hello",
		"password": "hunter2",
		"vector":   []float64{0, 1, 0},
	}
	h.recordMCPAudit(context.Background(), nil, "search", args, mcpToolResult{}, 0)
	var got audit.Entry
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, buf.String())
	}
	if _, exists := got.Args["password"]; exists {
		t.Errorf("password leaked into audit args: %+v", got.Args)
	}
	if v, ok := got.Args["vector"].(string); !ok || !strings.HasPrefix(v, "<vector len=3") {
		t.Errorf("vector not summarized: %v", got.Args["vector"])
	}
}
