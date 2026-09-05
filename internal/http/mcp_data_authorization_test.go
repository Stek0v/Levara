package http

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/internal/store"
	"github.com/stek0v/levara/pkg/ingest"
	"github.com/stek0v/levara/pkg/mcp"
)

func TestDocumentACLMCPTransports(t *testing.T) {
	for _, latest := range []bool{false, true} {
		name := "legacy"
		if latest {
			name = "latest"
		}
		t.Run(name, func(t *testing.T) {
			_, db := documentACLHTTPFixture(t)
			ingest.SetSQLiteMode(true)
			t.Cleanup(func() { ingest.SetSQLiteMode(false) })
			cm, err := store.NewCollectionManager(2, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer cm.Close()
			h := &mcpHandler{cfg: APIConfig{DB: db, RequireAuth: true, JWTSecret: "test-doc-acl", StoragePath: t.TempDir(), Collections: cm}, sessions: mcp.NewSessionStore()}
			app := fiber.New()
			path := "/mcp"
			if latest {
				path = latestMCPPath
				app.Post(path, h.handleLatestRPC)
			} else {
				app.Post(path, h.handleRPC)
			}
			call := func(tool, args string) mcp.ToolResult {
				t.Helper()
				meta := ""
				if latest {
					meta = "," + latestMCPMetaParams()
				}
				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tool + `","arguments":` + args + meta + `}}`
				req := httptest.NewRequest("POST", path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				req.Header.Set("Authorization", "Bearer "+createJWT("alice", "alice@example.test", "test-doc-acl"))
				if latest {
					for k, v := range latestMCPHeaders("tools/call") {
						req.Header.Set(k, v)
					}
					req.Header.Set("Mcp-Name", tool)
				}
				resp, err := app.Test(req)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				var rpc struct {
					Result mcp.ToolResult `json:"result"`
				}
				if resp.StatusCode != 200 || json.Unmarshal(raw, &rpc) != nil {
					t.Fatalf("tool=%s HTTP=%d body=%s", tool, resp.StatusCode, raw)
				}
				return rpc.Result
			}
			for _, tc := range []struct{ tool, args string }{{"delete", `{"dataset_id":"b"}`}, {"prune", `{}`}, {"add", `{"dataset_name":"bob-docs","data":"forbidden"}`}} {
				r := call(tc.tool, tc.args)
				if !r.IsError {
					t.Errorf("%s did not deny forbidden action: %+v", tc.tool, r)
				}
			}
			r := call("add", `{"dataset_name":"alice-docs","data":"private transport document","room":"private"}`)
			if r.IsError {
				t.Fatalf("owner add failed: %+v", r)
			}
			var owner, ds string
			if err := db.QueryRow(`SELECT d.owner_id,dd.dataset_id FROM data d JOIN dataset_data dd ON dd.data_id=d.id`).Scan(&owner, &ds); err != nil {
				t.Fatal(err)
			}
			if owner != "alice" || ds != "a" {
				t.Errorf("owner=%s dataset=%s", owner, ds)
			}
			if _, err := db.Exec(`INSERT INTO data(id,name,extension,room,tags) VALUES('secret','hidden','.txt','private','[]')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('b','secret')`); err != nil {
				t.Fatal(err)
			}
			// Remove the existing viewer grant so a filtered listing must hide b.
			if _, err := db.Exec(`DELETE FROM dataset_shares WHERE id='share-b'`); err != nil {
				t.Fatal(err)
			}
			r = call("list_data", `{"room":"private"}`)
			if r.IsError || len(r.Content) != 1 || strings.Contains(r.Content[0].Text, "hidden") {
				t.Errorf("filtered result leaked document: %+v", r)
			}
			// Distinct ID and name namespaces must not authorize each other.
			if _, err := db.Exec(`UPDATE datasets SET name=CASE id WHEN 'a' THEN 'b' WHEN 'b' THEN 'a' ELSE name END`); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"a", "b"} {
				if err := cm.Create(name); err != nil {
					t.Fatal(err)
				}
			}
			r = call("list_data", `{}`)
			checkDocumentACLListingCollision(t, r)
		})
	}
}

func TestDocumentACLPostgres(t *testing.T) {
	app, db := documentACLHTTPFixture(t, "postgres")
	checkDocumentACLShareBindingAndUpsert(t, app, db)
	ingest.SetSQLiteMode(false)
	cm, err := store.NewCollectionManager(2, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cm.Close()
	h := &mcpHandler{cfg: APIConfig{DB: db, StoragePath: t.TempDir(), Collections: cm}, sessions: mcp.NewSessionStore()}
	ctx := context.WithValue(context.Background(), mcp.UserIDKey, "alice")
	if r := mcp.ToolDelete(ctx, h, map[string]any{"dataset_id": "b"}); !r.IsError {
		t.Fatal("foreign delete allowed")
	}
	if r := mcp.ToolPrune(ctx, h); !r.IsError {
		t.Fatal("non-admin prune allowed")
	}
	if r := mcp.ToolAdd(ctx, h, map[string]any{"data": "owner content", "dataset_name": "alice-docs", "room": "docs"}); r.IsError {
		t.Fatalf("add: %+v", r)
	}
	var owner, ds string
	if err := db.QueryRow(`SELECT d.owner_id,dd.dataset_id FROM data d JOIN dataset_data dd ON dd.data_id=d.id`).Scan(&owner, &ds); err != nil {
		t.Fatal(err)
	}
	if owner != "alice" || ds != "a" {
		t.Fatalf("owner=%s dataset=%s", owner, ds)
	}
	if _, err := db.Exec(`INSERT INTO data(id,name,extension,room,tags) VALUES('hidden','private','.txt','docs','[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO dataset_data(dataset_id,data_id) VALUES('b','hidden')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM dataset_shares`); err != nil {
		t.Fatal(err)
	}
	r := mcp.ToolListData(ctx, h, map[string]any{"room": "docs"})
	if r.IsError || len(r.Content) != 1 || strings.Contains(r.Content[0].Text, "hidden") {
		t.Fatalf("filtered leak: %+v", r)
	}
	if _, err := db.Exec(`UPDATE datasets SET name=CASE id WHEN 'a' THEN 'b' WHEN 'b' THEN 'a' ELSE name END`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := cm.Create(name); err != nil {
			t.Fatal(err)
		}
	}
	checkDocumentACLListingCollision(t, mcp.ToolListData(ctx, h, map[string]any{}))
	// Fail after the data/dataset deletes and prove transaction rollback.
	if _, err := db.Exec(`ALTER TABLE graph_edges RENAME TO graph_edges_unavailable`); err != nil {
		t.Fatal(err)
	}
	if r := mcp.ToolPrune(context.Background(), h); !r.IsError {
		t.Fatal("SQL failure reported as successful prune")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM data`).Scan(&n); err != nil || n != 2 {
		t.Errorf("prune partially committed: count=%d err=%v", n, err)
	}
	if _, err := db.Exec(`ALTER TABLE graph_edges_unavailable RENAME TO graph_edges`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET is_superuser=TRUE WHERE id='alice'`); err != nil {
		t.Fatal(err)
	}
	if r := mcp.ToolPrune(ctx, h); r.IsError {
		t.Fatalf("active admin prune failed: %+v", r)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM datasets`).Scan(&n); err != nil || n != 0 {
		t.Errorf("prune failed: count=%d err=%v", n, err)
	}
}

func checkDocumentACLListingCollision(t *testing.T, result mcp.ToolResult) {
	t.Helper()
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("list_data failed: %+v", result)
	}
	var listing struct {
		Datasets []struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Collection string `json:"collection"`
		} `json:"datasets"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].Text), &listing); err != nil {
		t.Fatal(err)
	}
	seenDataset, seenCollection := false, false
	for _, item := range listing.Datasets {
		if item.Type == "dataset" {
			if item.ID != "a" {
				t.Errorf("name alias exposed foreign dataset: %+v", item)
			}
			seenDataset = true
		} else if item.Type == "vector_collection" {
			if item.Collection != "b" {
				t.Errorf("ID alias exposed foreign collection: %+v", item)
			}
			seenCollection = true
		}
	}
	if len(listing.Datasets) != 2 || !seenDataset || !seenCollection {
		t.Errorf("owned dataset and named collection must be listed: %+v", listing)
	}
}
