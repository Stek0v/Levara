package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	accesspkg "github.com/stek0v/levara/pkg/access"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPMemoryPinCollectionScope(t *testing.T) {
	for _, tool := range []string{"pin_memory", "unpin_memory"} {
		for _, tc := range []struct {
			name       string
			stateless  bool
			defaultSet bool
			selector   map[string]any
			selected   string
			wantError  bool
		}{
			{"legacy-default", false, true, nil, "one", false},
			{"legacy-empty-default", false, true, map[string]any{"collection": ""}, "one", false},
			{"legacy-override", false, true, map[string]any{"collection": "two"}, "two", false},
			{"legacy-no-default", false, false, nil, "*", false},
			{"stateless-explicit", true, true, map[string]any{"collection": "two"}, "two", false},
			{"stateless-omitted", true, true, nil, "*", false},
			{"stateless-empty", true, true, map[string]any{"collection": ""}, "*", false},
			{"stateless-unknown", true, true, map[string]any{"collection": "missing"}, "missing", tool == "pin_memory"},
			{"legacy-invalid-null", false, true, map[string]any{"collection": nil}, "", true},
			{"stateless-invalid-object", true, true, map[string]any{"collection": map[string]any{}}, "", true},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				t.Setenv("LEVARA_MCP_TOOLSET", "full")
				t.Setenv("LEVARA_TENANT_ENFORCED", "0")
				db := newMCPMemoryBehaviorDB(t)
				if err := accesspkg.EnsureIdentitySchema(t.Context(), db, Q); err != nil {
					t.Fatal(err)
				}
				if err := accesspkg.EnsureBrowserSessionSchema(t.Context(), db, Q); err != nil {
					t.Fatal(err)
				}
				for _, query := range []string{
					`INSERT INTO principals(id) VALUES ('alice'),('bob')`,
					`INSERT INTO users(id,email,hashed_password) VALUES ('alice','alice@example.test','unused'),('bob','bob@example.test','unused')`,
				} {
					if _, err := db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				const secret = "pin-scope-test-secret"
				const oldTime = "2026-01-01T00:00:00Z"
				app, h := mcpAdoptApp(t, APIConfig{DB: db, JWTSecret: secret, RequireAuth: true})
				app.Post(latestMCPPath, h.handleLatestRPC)
				sid := h.createSession("alice")
				token := createJWT("alice", "alice@example.com", secret)
				call := func(name string, args map[string]any, stateless bool) mcp.ToolResult {
					t.Helper()
					encoded, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, encoded)
					headers := map[string]string{"Authorization": "Bearer " + token, "Mcp-Session-Id": sid}
					var payload struct {
						Result mcp.ToolResult `json:"result"`
						Error  any            `json:"error"`
					}
					if stateless {
						body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s,%s}}`, name, encoded, latestMCPMetaParams())
						for key, value := range latestMCPHeaders("tools/call") {
							headers[key] = value
						}
						headers["Mcp-Name"] = name
						resp := latestMCPPost(t, app, body, headers)
						defer resp.Body.Close()
						if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") != "" {
							t.Fatalf("stateless status=%d session=%q", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"))
						}
						if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
							t.Fatal(err)
						}
					} else {
						status, response := postRPC(t, app, body, headers)
						if status != http.StatusOK {
							t.Fatalf("legacy status=%d response=%s", status, response)
						}
						if err := json.Unmarshal([]byte(response), &payload); err != nil {
							t.Fatal(err)
						}
					}
					if payload.Error != nil || len(payload.Result.Content) == 0 {
						t.Fatalf("invalid RPC response: %+v", payload)
					}
					return payload.Result
				}
				if tc.defaultSet {
					if got := call("set_context", map[string]any{"collection": "one"}, false); got.IsError {
						t.Fatalf("set_context: %+v", got)
					}
				}
				initialPin, initialPriority := tool == "unpin_memory", 0
				if initialPin {
					initialPriority = 8
				}
				for _, row := range []struct{ id, key, owner, collection string }{
					{"own-one", "dup", "alice", "one"}, {"own-two", "dup", "alice", "two"},
					{"shared-one", "dup", "", "one"}, {"shared-two", "dup", "", "two"},
					{"foreign", "dup", "bob", "one"}, {"other-key", "other", "alice", "one"},
				} {
					if _, err := db.Exec(`INSERT INTO memories(id,key,value,owner_id,collection_name,is_pinned,pin_priority,updated_at) VALUES(?,?,?,?,?,?,?,?)`, row.id, row.key, "value", row.owner, row.collection, initialPin, initialPriority, oldTime); err != nil {
						t.Fatal(err)
					}
				}
				args := map[string]any{"key": "dup", "owner_id": "bob"}
				for key, value := range tc.selector {
					args[key] = value
				}
				result := call(tool, args, tc.stateless)
				if result.IsError != tc.wantError {
					t.Fatalf("result=%+v wantError=%v", result, tc.wantError)
				}
				if !tc.wantError {
					structured, _ := result.StructuredContent.(map[string]any)
					if structured["ok"] != true {
						t.Fatalf("status schema=%+v", result)
					}
					if tool == "unpin_memory" && call(tool, args, tc.stateless).IsError {
						t.Fatal("repeated unpin must succeed")
					}
				}
				rows, err := db.Query(`SELECT id,key,owner_id,collection_name,is_pinned,pin_priority,updated_at FROM memories`)
				if err != nil {
					t.Fatal(err)
				}
				defer rows.Close()
				count := 0
				for rows.Next() {
					var id, key, owner, collection, updated string
					var pinned bool
					var priority int
					if err := rows.Scan(&id, &key, &owner, &collection, &pinned, &priority, &updated); err != nil {
						t.Fatal(err)
					}
					count++
					selected := !tc.wantError && key == "dup" && owner != "bob" && (collection == tc.selected || tc.selected == "*")
					wantPin, wantPriority := initialPin, initialPriority
					if selected {
						wantPin, wantPriority = tool == "pin_memory", 0
						if wantPin {
							wantPriority = 1
						}
					}
					if pinned != wantPin || priority != wantPriority || (!selected && updated != oldTime) {
						t.Errorf("%s=(%v,%d,%s), want (%v,%d), selected=%v", id, pinned, priority, updated, wantPin, wantPriority, selected)
					}
				}
				if err := rows.Err(); err != nil || count != 6 {
					t.Fatalf("rows=%d err=%v", count, err)
				}
			})
		}
	}
}
