package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stek0v/levara/pkg/mcp"
)

func TestMCPProjectContextScope(t *testing.T) {
	t.Setenv("LEVARA_MCP_TOOLSET", "full")
	t.Setenv("LEVARA_TENANT_ENFORCED", "1")
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.cfg.RequireAuth = true
		f.cfg.JWTSecret = "project-context-test-secret"
		h := &mcpHandler{cfg: f.cfg, sessions: mcp.NewSessionStore()}
		f.app.Post("/mcp", h.handleRPC)
		f.app.Post(latestMCPPath, h.handleLatestRPC)
		sid := h.createSession("peer")
		call := func(t *testing.T, name string, args map[string]any, stateless bool) mcp.ToolResult {
			t.Helper()
			encoded, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, encoded)
			headers := map[string]string{"Authorization": "Bearer " + createJWT("peer", "peer@test.invalid", f.cfg.JWTSecret), "Mcp-Session-Id": sid, "X-Tenant-Id": "a", "X-Test-User": "peer"}
			var envelope struct {
				Result mcp.ToolResult `json:"result"`
				Error  any            `json:"error"`
			}
			if stateless {
				body = fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s,%s}}`, name, encoded, latestMCPMetaParams())
				for key, value := range latestMCPHeaders("tools/call") {
					headers[key] = value
				}
				headers["Mcp-Name"] = name
				resp := latestMCPPost(t, f.app, body, headers)
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") != "" {
					t.Fatalf("stateless status=%d session=%q", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"))
				}
				if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
			} else {
				status, body := postRPC(t, f.app, body, headers)
				if status != http.StatusOK {
					t.Fatalf("legacy status=%d %s", status, body)
				}
				if err := json.Unmarshal([]byte(body), &envelope); err != nil {
					t.Fatal(err)
				}
			}
			if envelope.Error != nil || len(envelope.Result.Content) == 0 {
				t.Fatalf("invalid RPC: %+v", envelope)
			}
			return envelope.Result
		}
		for _, row := range []struct{ id, owner, collection, value, retired string }{
			{"m1", "peer", "main", "PEER_MAIN", ""}, {"m2", "", "main", "SHARED_MAIN", ""}, {"m3", "foreign", "main", "FOREIGN_MAIN", ""}, {"m4", "peer", "main", "OLD_MAIN", "new"},
			{"r1", "peer", "related", "PEER_RELATED", ""}, {"r2", "", "related", "SHARED_RELATED", ""}, {"r3", "foreign", "related", "FOREIGN_RELATED", ""}, {"r4", "peer", "related", "OLD_RELATED", "new"},
			{"s1", "peer", "sibling", "PEER_SIBLING", ""},
		} {
			f.exec(`INSERT INTO memories(id,key,owner_id,collection_name,value,superseded_by) VALUES($1,$2,$3,$4,$5,$6)`, row.id, row.id, row.owner, row.collection, row.value, row.retired)
		}
		f.exec(`INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('peer-alpha','alpha','peer','viewer')`)
		f.exec(`UPDATE document_resources SET mode='inherit' WHERE dataset_id='alpha' AND data_id='blob'`)
		var sourceRevision int64
		var rawHash string
		if err := f.db.QueryRow(`SELECT source_revision,raw_content_hash FROM data WHERE id='blob'`).Scan(&sourceRevision, &rawHash); err != nil {
			t.Fatal(err)
		}
		const generation = "project-context-generation"
		f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,source_revision,raw_content_hash,sources_json,lineage_verified)
			VALUES('alpha','blob','main',$1,$2,$3,$4,'[]',1)`, f.r.ContentRevision, generation, sourceRevision, rawHash)
		f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,source_revision,raw_content_hash,sources_json,lineage_verified)
			VALUES('alpha','blob','sibling',$1,$2,$3,$4,'[]',1)`, f.r.ContentRevision, generation, sourceRevision, rawHash)
		f.exec(`INSERT INTO document_index_publications(dataset_id,data_id,collection_name,content_revision,generation,source_revision,raw_content_hash,sources_json,lineage_verified)
			VALUES('beta','blob','main',0,$1,$2,$3,'[]',1)`, generation, sourceRevision, rawHash)
		source, err := json.Marshal(map[string]any{"dataset_id": "alpha", "document_id": "blob", "collection": "main", "content_revision": f.r.ContentRevision, "generation": generation, "source_revision": sourceRevision, "raw_content_hash": rawHash})
		if err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES('context-visible','visible','VISIBLE_GRAPH','alpha',$1)`, string(source))
		var foreignSource map[string]any
		if err := json.Unmarshal(source, &foreignSource); err != nil {
			t.Fatal(err)
		}
		foreignSource["dataset_id"] = "beta"
		foreignSource["content_revision"] = 0
		foreignRaw, err := json.Marshal(foreignSource)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO graph_nodes(id,name,type,dataset_id,properties) VALUES('context-denied','denied','SECRET_SCOPED_GRAPH','beta',$1)`, string(foreignRaw))
		f.exec(`INSERT INTO interactions(id,session_id,user_id,query,response) VALUES('context-visible-i','project-session','peer','AGG_QUERY','AGG_RESPONSE')`)
		f.exec(`INSERT INTO interaction_provenance(id,session_id,owner_id,tenant_id,kind,sources,created_at) VALUES('project-session-anchor','project-session','peer','a','session','[]','2026-10-08T00:00:00Z')`)
		f.exec(`INSERT INTO interaction_provenance(id,interaction_id,session_id,owner_id,tenant_id,kind,sources,created_at) VALUES('project-turn','context-visible-i','project-session','peer','a','server',$1,'2026-10-08T00:00:01Z')`, "["+string(source)+"]")
		f.exec(`INSERT INTO interactions(id,session_id,user_id,query,response) VALUES('context-mixed-i','project-session','peer','SECRET_MIXED_QUERY','SECRET_MIXED_RESPONSE')`)
		f.exec(`INSERT INTO interaction_provenance(id,interaction_id,session_id,owner_id,tenant_id,kind,sources,created_at) VALUES('project-mixed-turn','context-mixed-i','project-session','peer','a','server',$1,'2026-10-08T00:00:02Z')`, "["+string(source)+","+string(foreignRaw)+"]")
		siblingSource := map[string]any{}
		if err := json.Unmarshal(source, &siblingSource); err != nil {
			t.Fatal(err)
		}
		siblingSource["collection"] = "sibling"
		siblingRaw, err := json.Marshal(siblingSource)
		if err != nil {
			t.Fatal(err)
		}
		f.exec(`INSERT INTO interactions(id,session_id,user_id,query,response) VALUES('context-cross-project-i','project-session','peer','CROSS_PROJECT_QUERY','CROSS_PROJECT_RESPONSE')`)
		f.exec(`INSERT INTO interaction_provenance(id,interaction_id,session_id,owner_id,tenant_id,kind,sources,created_at) VALUES('project-cross-project-turn','context-cross-project-i','project-session','peer','a','server',$1,'2026-10-08T00:00:03Z')`, "["+string(source)+","+string(siblingRaw)+"]")
		f.exec(`INSERT INTO graph_nodes(id,name,type,dataset_id) VALUES('context-g','secret','SECRET_GRAPH','beta')`)
		f.exec(`INSERT INTO interactions(id,user_id,query,response) VALUES('context-i','foreign','SECRET_CHAT','SECRET_RESPONSE')`)
		for _, tc := range []struct {
			name, def string
			stateless bool
			args      map[string]any
			selected  string
			wantError bool
		}{
			{"legacy-default", "main", false, nil, "main", false},
			{"legacy-override", "main", false, map[string]any{"collection": "sibling"}, "sibling", false},
			{"stateless-explicit", "sibling", true, map[string]any{"collection": "main"}, "main", false},
			{"stateless-no-selector", "main", true, nil, "", true},
			{"unknown", "main", true, map[string]any{"collection": "missing"}, "missing", false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := call(t, "set_context", map[string]any{"collection": tc.def}, false); got.IsError {
					t.Fatalf("set_context: %+v", got)
				}
				args := map[string]any{"owner_id": "foreign", "actor_id": "foreign", "include_related": []any{"related"}}
				for k, v := range tc.args {
					args[k] = v
				}
				got := call(t, "get_project_context", args, tc.stateless)
				if got.IsError != tc.wantError {
					t.Fatalf("result=%+v wantError=%v", got, tc.wantError)
				}
				if tc.wantError {
					if got.StructuredContent != nil {
						t.Fatalf("error has partial payload: %+v", got)
					}
					return
				}
				structured, ok := got.StructuredContent.(map[string]any)
				if !ok || len(structured) != 2 || structured["collection"] != tc.selected {
					t.Fatalf("schema=%+v", got)
				}
				text, _ := structured["text"].(string)
				want := []string{"PEER_RELATED", "SHARED_RELATED", "Accessible published documents:"}
				if tc.selected == "main" {
					want = append(want, "PEER_MAIN", "SHARED_MAIN", "Accessible published documents: 1", "VISIBLE_GRAPH: 1", "AGG_QUERY", "AGG_RESPONSE")
				}
				if tc.selected == "sibling" {
					want = append(want, "PEER_SIBLING")
				}
				for _, value := range want {
					if !strings.Contains(text, value) {
						t.Errorf("missing %s in %q", value, text)
					}
				}
				absent := []string{"FOREIGN_MAIN", "FOREIGN_RELATED", "OLD_MAIN", "OLD_RELATED", "SECRET_GRAPH", "SECRET_SCOPED_GRAPH", "SECRET_CHAT", "SECRET_RESPONSE", "SECRET_MIXED_QUERY", "SECRET_MIXED_RESPONSE", "CROSS_PROJECT_QUERY", "CROSS_PROJECT_RESPONSE"}
				if tc.selected != "main" {
					absent = append(absent, "VISIBLE_GRAPH", "AGG_QUERY", "AGG_RESPONSE")
				}
				if tc.selected != "main" {
					absent = append(absent, "PEER_MAIN", "SHARED_MAIN")
				}
				if tc.selected != "sibling" {
					absent = append(absent, "PEER_SIBLING")
				}
				for _, value := range absent {
					if strings.Contains(text, value) {
						t.Errorf("exposed %s in %q", value, text)
					}
				}
			})
		}
		t.Run("sql-failure", func(t *testing.T) {
			f.exec(`DROP TABLE memories`)
			got := call(t, "get_project_context", map[string]any{"collection": "main"}, true)
			if !got.IsError || got.StructuredContent != nil {
				t.Fatalf("SQL failure masked: %+v", got)
			}
		})
	})
}
