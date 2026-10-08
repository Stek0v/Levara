package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPConsolidationVerifiedOwner(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		f.cfg.JWTSecret = "consolidation-scope-test-secret"
		h := &mcpHandler{cfg: f.cfg, sessions: mcp.NewSessionStore()}
		f.app.Post("/mcp", h.handleRPC)
		f.app.Post(latestMCPPath, h.handleLatestRPC)
		sid := h.createSession("peer")
		for _, row := range []struct{ id, owner, collection string }{
			{"mine", "peer", "main"}, {"foreign", "foreign", "main"}, {"shared", "", "main"}, {"other", "peer", "other"},
		} {
			f.exec(`INSERT INTO memories(id,key,value,owner_id,collection_name,room,hall) VALUES($1,$2,$3,$4,$5,'memory','fact')`, row.id, row.id, "SECRET_"+row.id, row.owner, row.collection)
		}
		for _, stateless := range []bool{false, true} {
			t.Run(fmt.Sprintf("stateless=%t", stateless), func(t *testing.T) {
				for _, shared := range []bool{false, true} {
					args := map[string]any{"collection": "main", "wait": true, "dry_run": true, "owner_id": "foreign", "actor_id": "foreign", "trusted_local": true}
					if shared {
						args["shared"] = true
					}
					encoded, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"consolidate","arguments":%s}}`, encoded)
					headers := map[string]string{"Authorization": "Bearer " + createJWT("peer", "peer@test.invalid", f.cfg.JWTSecret), "Mcp-Session-Id": sid, "X-Tenant-Id": "a", "X-Test-User": "peer"}
					var envelope struct {
						Result mcp.ToolResult `json:"result"`
						Error  any            `json:"error"`
					}
					if stateless {
						body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"consolidate","arguments":%s,%s}}`, encoded, latestMCPMetaParams())
						for k, v := range latestMCPHeaders("tools/call") {
							headers[k] = v
						}
						headers["Mcp-Name"] = "consolidate"
						resp := latestMCPPost(t, f.app, body, headers)
						defer resp.Body.Close()
						if resp.StatusCode != http.StatusOK {
							t.Fatalf("status=%d", resp.StatusCode)
						}
						if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
							t.Fatal(err)
						}
					} else {
						status, raw := postRPC(t, f.app, body, headers)
						if status != http.StatusOK {
							t.Fatalf("status=%d body=%s", status, raw)
						}
						if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
							t.Fatal(err)
						}
					}
					got := envelope.Result
					if envelope.Error != nil || len(got.Content) == 0 {
						t.Fatalf("RPC=%+v", envelope)
					}
					if shared {
						if !got.IsError {
							t.Fatalf("ordinary caller selected shared namespace: %+v", got)
						}
					} else if got.IsError || !strings.Contains(got.Content[0].Text, "candidates=1 ") {
						t.Fatalf("forged args broadened exact owner namespace: %+v", got)
					}
				}
			})
		}
	})
}
