package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGitCLIResponseFailuresAndSuccess(t *testing.T) {
	for _, command := range []struct {
		name      string
		args      []string
		tool      string
		arguments map[string]any
	}{
		{"analyze", []string{"git", "analyze", "--repo=/repo path", "--since=2026-10-01", "--limit=7"}, "analyze_commits", map[string]any{"repo_path": "/repo path", "since": "2026-10-01", "limit": float64(7)}},
		{"search", []string{"git", "search", "access", "control"}, "git_search", map[string]any{"query": "access control"}},
	} {
		t.Run(command.name, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				status  int
				body    string
				want    string
				success bool
			}{
				{"text", 200, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first\nsecond"},{"type":"text","text":"ignored"}]}}`, "first\nsecond\n", true},
				{"empty_text", 200, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""}]}}`, "\n", true},
				{"http_denied", 403, "denied", "server error 403: denied", false},
				{"http_bad_gateway", 502, "upstream failed", "server error 502", false},
				{"http_empty", 204, "", "invalid Git MCP response", false},
				{"rpc_error", 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"repository denied"}}`, "repository denied", false},
				{"tool_error", 200, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"git failed"}]}}`, "MCP tool error: git failed", false},
				{"invalid_json", 200, "not JSON", "invalid Git MCP response", false},
				{"missing_result", 200, `{"jsonrpc":"2.0","id":1}`, "invalid Git MCP response", false},
				{"null_result", 200, `{"jsonrpc":"2.0","id":1,"result":null}`, "invalid Git MCP response", false},
				{"missing_content", 200, `{"jsonrpc":"2.0","id":1,"result":{}}`, "invalid Git MCP response", false},
				{"empty_content", 200, `{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`, "invalid Git MCP response", false},
				{"missing_text", 200, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text"}]}}`, "invalid Git MCP response", false},
				{"non_text", 200, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"image","text":"image"}]}}`, "invalid Git MCP response", false},
				{"invalid_tool_flag", 200, `{"jsonrpc":"2.0","id":1,"result":{"isError":"false","content":[{"type":"text","text":"looks successful"}]}}`, "invalid Git MCP response", false},
				{"wrong_id", 200, `{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"other request"}]}}`, "invalid Git MCP response", false},
				{"wrong_version", 200, `{"jsonrpc":"1.0","id":1,"result":{"content":[{"type":"text","text":"legacy"}]}}`, "invalid Git MCP response", false},
				{"truncated_body", 200, "short", "read response", false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						calls.Add(1)
						if r.Method != http.MethodPost || r.URL.Path != "/mcp" || r.Header.Get("Authorization") != "Bearer isolated-cli-token" || r.Header.Get("Content-Type") != "application/json" {
							t.Errorf("unexpected request: %s %s headers=%v", r.Method, r.URL, r.Header)
						}
						var request struct {
							JSONRPC string `json:"jsonrpc"`
							ID      int    `json:"id"`
							Method  string `json:"method"`
							Params  struct {
								Name      string         `json:"name"`
								Arguments map[string]any `json:"arguments"`
							} `json:"params"`
						}
						if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.JSONRPC != "2.0" || request.ID != 1 || request.Method != "tools/call" || request.Params.Name != command.tool || !reflect.DeepEqual(request.Params.Arguments, command.arguments) {
							t.Errorf("request=%+v decode=%v", request, err)
						}
						if tc.name == "truncated_body" {
							w.Header().Set("Content-Length", "1000")
						}
						w.WriteHeader(tc.status)
						_, _ = fmt.Fprint(w, tc.body)
					}))
					defer server.Close()
					out, err := runCLICommand(t, server.URL, command.args...)
					if (err == nil) != tc.success || !strings.Contains(out, tc.want) || calls.Load() != 1 {
						t.Fatalf("success=%v err=%v calls=%d output=%q want=%q", tc.success, err, calls.Load(), out, tc.want)
					}
					if tc.success && out != tc.want {
						t.Fatalf("successful output=%q want=%q", out, tc.want)
					}
				})
			}
		})
	}
}

func TestGitCLIRejectsRedirectAndTransportFailure(t *testing.T) {
	for _, args := range [][]string{{"git", "analyze"}, {"git", "search", "query"}} {
		t.Run(args[1], func(t *testing.T) {
			var redirected atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirected.Add(1)
				_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"redirected"}]}}`)
			}))
			defer target.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
			}))
			out, err := runCLICommand(t, server.URL, args...)
			server.Close()
			if err == nil || !strings.Contains(out, "server error 307") || redirected.Load() != 0 {
				t.Fatalf("redirect err=%v calls=%d output=%s", err, redirected.Load(), out)
			}
			out, err = runCLICommand(t, server.URL, args...)
			if err == nil || !strings.Contains(out, "connection failed") {
				t.Fatalf("transport err=%v output=%s", err, out)
			}
		})
	}
}
