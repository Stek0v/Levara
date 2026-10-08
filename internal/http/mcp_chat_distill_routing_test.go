package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/chatimport"
	"github.com/stek0v/levara/pkg/llm"
	"github.com/stek0v/levara/pkg/mcp"
)

type routingDistillProvider struct {
	key   string
	calls atomic.Int32
}

func (*routingDistillProvider) Name() string { return "local-routing-fixture" }
func (p *routingDistillProvider) ChatCompletion(context.Context, llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.calls.Add(1)
	raw, err := json.Marshal([]map[string]string{{"key": p.key, "value": "Routing fixture proposal"}})
	return &llm.CompletionResponse{Content: string(raw)}, err
}

func TestMCPChatDistillCollectionRouting(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		h := asyncAuthorityHandler(f)
		app := asyncAuthorityApp(h, nil)
		ctx := context.Background()
		now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
		if err := chatimport.EnsureSchema(ctx, f.db, Q); err != nil {
			t.Fatal(err)
		}
		scope := chatimport.ImportScope{OwnerID: "peer", TenantID: "a"}
		runID, err := chatimport.StartScopedRun(ctx, f.db, Q, scope, chatimport.RunInfo{ID: "routing-import", Platform: chatimport.PlatformCodex, StartedAt: now.Format(time.RFC3339)})
		if err != nil {
			t.Fatal(err)
		}
		var warnings []string
		conv := &chatimport.Conversation{Platform: chatimport.PlatformCodex, SessionID: "routing-transcript", Title: "Actual imported routing input", Messages: []chatimport.Message{{ExternalID: "user", Ordinal: 0, Role: "user", Kind: chatimport.KindText, Content: "Record this proposal"}, {ExternalID: "assistant", Ordinal: 1, Role: "assistant", Kind: chatimport.KindText, Content: "A proposal to route"}}}
		_, inserted, err := chatimport.InsertScopedConversation(ctx, f.db, Q, scope, runID, conv, &warnings, now)
		if err != nil || inserted != 2 {
			t.Fatalf("actual import rows=%d error=%v", inserted, err)
		}
		for _, scenario := range []struct {
			name, path, sessionCollection string
			collection                    *string
			expected                      string
		}{
			{"legacy-omitted", "/mcp", "session-default", nil, "session-default"},
			{"legacy-empty", "/mcp", "session-default", routingString(""), "session-default"},
			{"legacy-explicit", "/mcp", "session-default", routingString("explicit"), "explicit"},
			{"legacy-no-default", "/mcp", "", nil, ""},
			{"latest-explicit", latestMCPPath, "", routingString("latest-explicit"), "latest-explicit"},
			{"latest-no-default", latestMCPPath, "", nil, ""},
			{"latest-empty", latestMCPPath, "", routingString(""), ""},
			{"latest-legacy-sid", latestMCPPath, "", nil, ""},
		} {
			t.Run(scenario.name, func(t *testing.T) {
				provider := &routingDistillProvider{key: scenario.name}
				h.cfg.LLMProvider = provider
				session := ""
				if scenario.path == "/mcp" || scenario.name == "latest-legacy-sid" {
					session = h.createSession("peer")
				}
				if scenario.name == "latest-legacy-sid" {
					selected := routingDistillRPC(t, app, h, "/mcp", session, "set_context", map[string]any{"collection": "legacy-only-default"})
					if selected.IsError {
						t.Fatalf("legacy context control failed: %s", diaryScopeRaw(selected))
					}
				}
				if scenario.sessionCollection != "" {
					selected := routingDistillRPC(t, app, h, scenario.path, session, "set_context", map[string]any{"collection": scenario.sessionCollection})
					if selected.IsError {
						t.Fatalf("actual set_context failed: %s", diaryScopeRaw(selected))
					}
				}
				args := map[string]any{"platform": "codex", "session_id": conv.SessionID}
				if scenario.collection != nil {
					args["collection"] = *scenario.collection
				}
				before := diaryScopeSnapshot(t, f)
				result := routingDistillRPC(t, app, h, scenario.path, session, "chat_distill", args)
				if result.IsError || provider.calls.Load() != 1 {
					t.Fatalf("distill result=%s provider calls=%d", diaryScopeRaw(result), provider.calls.Load())
				}
				var collection, owner, status, task, receipts, value string
				if err := f.db.QueryRow(Q("SELECT collection_name,owner_id,verification_status,source_task_id,source_receipt_ids,value FROM memories WHERE key=$1"), scenario.name).Scan(&collection, &owner, &status, &task, &receipts, &value); err != nil {
					t.Fatal(err)
				}
				if collection != scenario.expected || owner != "peer" {
					t.Errorf("persisted route owner=%q collection=%q want peer/%q", owner, collection, scenario.expected)
				}
				if status != "unverified" || task != "" || receipts != "[]" || !strings.Contains(value, "Actual imported routing input") {
					t.Errorf("evidence/provenance changed: %q/%q/%q value=%q", status, task, receipts, value)
				}
				after := diaryScopeSnapshot(t, f)
				for id, row := range before {
					if after[id] != row {
						t.Errorf("routing changed previous control row %s", id)
					}
				}
			})
		}
	})
}

func routingString(s string) *string { return &s }

// Keep a real legacy session through set_context and distill calls. Latest
// remains stateless and uses explicit collection, as its discovery advertises.
func routingDistillRPC(t *testing.T, app *fiber.App, h *mcpHandler, path, session, tool string, args map[string]any) mcp.ToolResult {
	t.Helper()
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, encoded)
	headers := map[string]string{"Authorization": "Bearer " + createJWT("peer", "peer@test.invalid", h.cfg.JWTSecret), "X-Tenant-Id": "a"}
	if session != "" {
		headers["Mcp-Session-Id"] = session
	}
	var envelope struct {
		Result mcp.ToolResult `json:"result"`
		Error  any            `json:"error"`
	}
	if path == latestMCPPath {
		body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s,%s}}`, tool, encoded, latestMCPMetaParams())
		for key, value := range latestMCPHeaders("tools/call") {
			headers[key] = value
		}
		headers["Mcp-Name"] = tool
		resp := latestMCPPost(t, app, body, headers)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("latest status=%d", resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
	} else {
		headers["Mcp-Session-Id"] = session
		status, raw := postRPC(t, app, body, headers)
		if status != http.StatusOK {
			t.Fatalf("legacy status=%d body=%s", status, raw)
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatal(err)
		}
	}
	if envelope.Error != nil || len(envelope.Result.Content) == 0 {
		t.Fatalf("RPC envelope=%+v", envelope)
	}
	return envelope.Result
}
