package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stek0v/levara/pkg/chatimport"
)

func scopedChatApp(f *documentHTTPFixture, required bool) *fiber.App {
	cfg := f.cfg
	cfg.RequireAuth = required
	cfg.JWTSecret = "chat-scope-secret"
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(func(c *fiber.Ctx) error { c.Locals("auth_db", &DBRef{DB: f.db}); return c.Next() })
	app.Use(JWTMiddleware(cfg.JWTSecret, required), APIKeyPermissionMiddleware(), TenantMiddleware(AccessConfig{DB: f.db}))
	RegisterChatImportAPI(app, cfg)
	return app
}

func scopedChatRequest(t *testing.T, app *fiber.App, user, tenant, key, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+createJWT(user, user+"@test.invalid", "chat-scope-secret"))
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if tenant != "" {
		req.Header.Set("X-Tenant-Id", tenant)
	}
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	parsed := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp.StatusCode, parsed
}

func scopedChatImport(t *testing.T, app *fiber.App, user, tenant, run, source, text string) map[string]any {
	t.Helper()
	conv := chatImportConversation()
	conv.SessionID = source
	conv.Messages[0].Content = text
	raw, _ := json.Marshal(conv)
	body := fmt.Sprintf(`{"run_id":%q,"source_path":%q,"finish":true,"conversation":%s}`, run, "private-"+user, raw)
	status, p := scopedChatRequest(t, app, user, tenant, "", "POST", "/chats/import", body)
	if status != 201 {
		t.Fatalf("import %s/%s status=%d body=%v", user, tenant, status, p)
	}
	return p
}

func scopedChatRows(t *testing.T, f *documentHTTPFixture, chatID string) []string {
	t.Helper()
	rows, err := f.db.Query(Q("SELECT * FROM chat_import_messages WHERE session_id=$1 ORDER BY id"), chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	result := []string{}
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err := rows.Scan(targets...); err != nil {
			t.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%#v", values))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestChatImportScopedProjectAccess(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		f.db.SetMaxOpenConns(1)
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','b'),('root','a')")
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('peer-edit','alpha','peer','editor')")
		app := scopedChatApp(f, false)
		peer := scopedChatImport(t, app, "peer", "a", "same-run", "same-source", "peer-private")
		scopedChatImport(t, app, "peer", "a", peer["run_id"].(string), "private-only-source", "mixed-run-private")
		owner := scopedChatImport(t, app, "owner", "a", "same-run", "same-source", "owner-private")
		otherTenant := scopedChatImport(t, app, "peer", "b", "same-run", "same-source", "other-tenant")
		before := scopedChatRows(t, f, peer["chat_id"].(string))
		if peer["chat_id"] == owner["chat_id"] || peer["chat_id"] == otherTenant["chat_id"] || peer["run_id"] == owner["run_id"] {
			t.Fatal("identity collision")
		}
		path := "/chats/import/sessions/codex/same-source?chat_id=" + peer["chat_id"].(string)
		request := func(user, tenant, method, path, body string, want int) map[string]any {
			t.Helper()
			status, p := scopedChatRequest(t, app, user, tenant, "", method, path, body)
			if status != want {
				t.Fatalf("%s %s %s/%s status=%d want=%d body=%v", method, path, user, tenant, status, want, p)
			}
			return p
		}
		for _, user := range []string{"owner", "viewer", "root"} {
			request(user, "a", "GET", path, "", 403)
		}
		request("peer", "b", "GET", path, "", 403)
		privateRows := request("viewer", "a", "GET", "/chats/import/sessions", "", 200)["sessions"].([]any)
		if len(privateRows) != 0 {
			t.Fatalf("private listing disclosed: %v", privateRows)
		}
		projectPath := "/chats/import/sessions/codex/same-source/project?chat_id=" + peer["chat_id"].(string)
		request("viewer", "a", "POST", projectPath, `{"project_id":"alpha"}`, 403)
		request("root", "a", "POST", projectPath, `{"project_id":"alpha"}`, 403)
		request("peer", "a", "POST", projectPath, `{"project_id":"missing"}`, 403)
		f.exec("INSERT INTO datasets(id,name,owner_id) VALUES('public-chat','PublicChat','')")
		request("peer", "a", "POST", projectPath, `{"project_id":"public-chat"}`, 403)
		request("peer", "a", "POST", projectPath, `{"project_id":"alpha"}`, 200)
		shared := request("viewer", "a", "GET", path, "", 200)
		listed := request("viewer", "a", "GET", "/chats/import/sessions", "", 200)["sessions"].([]any)
		if len(listed) != 1 || listed[0].(map[string]any)["chat_id"] != peer["chat_id"] {
			t.Fatalf("mixed-run private chat leaked in listing %v", listed)
		}
		if shared["session_id"] != "same-source" || shared["project_id"] != "alpha" {
			t.Fatalf("selector/provenance lost %v", shared)
		}
		request("root", "a", "GET", path, "", 403)
		runs := request("viewer", "a", "GET", "/chats/import/runs", "", 200)["runs"].([]any)
		if len(runs) != 0 {
			t.Fatalf("shared chat exposed private mixed-run ledger %v", runs)
		}
		ownRuns := request("peer", "a", "GET", "/chats/import/runs", "", 200)["runs"].([]any)
		if len(ownRuns) != 1 || ownRuns[0].(map[string]any)["id"] != peer["run_id"] {
			t.Fatalf("run scope wrong %v", ownRuns)
		}
		request("viewer", "a", "DELETE", projectPath, "", 403)
		f.exec("DELETE FROM dataset_shares WHERE id='viewer-share'")
		request("viewer", "a", "GET", path, "", 403)
		f.exec("INSERT INTO dataset_shares(id,dataset_id,user_id,role) VALUES('viewer-share','alpha','viewer','viewer')")
		f.exec("UPDATE dataset_shares SET role='admin' WHERE id='viewer-share'")
		request("viewer", "a", "DELETE", projectPath, "", 200)
		request("viewer", "a", "GET", path, "", 403)
		request("peer", "a", "POST", projectPath, `{"project_id":"alpha"}`, 200)
		f.exec("UPDATE dataset_shares SET role='viewer' WHERE id='viewer-share'")
		request("owner", "a", "DELETE", projectPath, "", 200)
		request("viewer", "a", "GET", path, "", 403)
		request("peer", "a", "GET", path, "", 200)
		request("peer", "a", "POST", projectPath, `{"project_id":"alpha"}`, 200)
		// Owner's source-only lookup wins over an identically named shared source.
		own := request("owner", "a", "GET", "/chats/import/sessions/codex/same-source", "", 200)
		if own["chat_id"] != owner["chat_id"] {
			t.Fatalf("own selector preference lost %v", own)
		}
		ownerProject := "/chats/import/sessions/codex/same-source/project?chat_id=" + owner["chat_id"].(string)
		request("owner", "a", "POST", ownerProject, `{"project_id":"alpha"}`, 200)
		request("viewer", "a", "GET", "/chats/import/sessions/codex/same-source", "", 409)
		request("viewer", "a", "GET", path, "", 200)
		f.exec("DELETE FROM datasets WHERE id='alpha'")
		request("viewer", "a", "GET", path, "", 403)
		request("peer", "a", "GET", path, "", 200)
		// Local legacy import stays visible only in explicit anonymous mode.
		local := scopedChatImport(t, app, "", "", "local-run", "legacy-source", "legacy-private")
		request("peer", "a", "GET", "/chats/import/sessions/codex/legacy-source", "", 403)
		request("", "", "GET", "/chats/import/sessions/codex/legacy-source", "", 200)
		if local["chat_id"] != "legacy-source" {
			t.Fatalf("local key changed %v", local)
		}
		// Canonical retry must not rescope the run or adopt its source.
		retry := scopedChatImport(t, app, "peer", "a", peer["run_id"].(string), "same-source", "peer-private")
		if retry["run_id"] != peer["run_id"] || retry["inserted"] != float64(0) {
			t.Fatalf("canonical retry wrong %v", retry)
		}
		var ownerID, tenantID, content string
		if err := f.db.QueryRow(Q("SELECT s.owner_id,s.tenant_id,m.content FROM chat_import_sessions s JOIN chat_import_messages m ON m.session_id=s.id AND m.platform=s.platform WHERE s.id=$1 AND m.external_id='m1'"), peer["chat_id"]).Scan(&ownerID, &tenantID, &content); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual([]string{ownerID, tenantID, content}, []string{"peer", "a", "peer-private"}) {
			t.Fatalf("foreign operations altered source: %s/%s/%s", ownerID, tenantID, content)
		}
		if !reflect.DeepEqual(before, scopedChatRows(t, f, peer["chat_id"].(string))) {
			t.Fatal("message full-row controls changed")
		}
	})
}

func TestChatImportScopedCredentialAndCancellation(t *testing.T) {
	documentHTTPDialects(t, func(t *testing.T, f *documentHTTPFixture) {
		app := scopedChatApp(f, true)
		chat := scopedChatImport(t, app, "peer", "a", "run", "source", "credential-content")
		path := "/chats/import/sessions/codex/source?chat_id=" + chat["chat_id"].(string)
		for _, permissions := range []string{"read-only", "read-write"} {
			key := "chat-key-" + permissions
			f.exec("INSERT INTO api_keys(id,key_hash,user_id,permissions) VALUES($1,$2,'peer',$3)", key, apikeyHash(key), permissions)
			status, p := scopedChatRequest(t, app, "", "a", key, "GET", path, "")
			if status != 200 {
				t.Fatalf("key read status %d %v", status, p)
			}
			if permissions == "read-only" {
				status, _ = scopedChatRequest(t, app, "", "a", key, "POST", "/chats/import", `{}`)
				if status != 403 {
					t.Fatalf("read key mutation %d", status)
				}
			}
			f.exec("DELETE FROM api_keys WHERE id=$1", key)
			status, _ = scopedChatRequest(t, app, "", "a", key, "GET", path, "")
			if status != 401 {
				t.Fatalf("revoked key read %d", status)
			}
		}
		f.exec("DELETE FROM user_tenant WHERE user_id='peer' AND tenant_id='a'")
		status, _ := scopedChatRequest(t, app, "peer", "a", "", "GET", path, "")
		if status != 403 {
			t.Fatalf("removed membership %d", status)
		}
		f.exec("INSERT INTO user_tenant(user_id,tenant_id) VALUES('peer','a')")
		f.exec("UPDATE users SET is_active=FALSE WHERE id='peer'")
		status, _ = scopedChatRequest(t, app, "peer", "a", "", "GET", path, "")
		if status != 401 {
			t.Fatalf("inactive credential %d", status)
		}
		// A canceled caller cannot create a registry or ledger in explicit local mode.
		canceled := fiber.New(fiber.Config{DisableStartupMessage: true})
		canceled.Use(func(c *fiber.Ctx) error {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			c.SetUserContext(ctx)
			return c.Next()
		})
		RegisterChatImportAPI(canceled, APIConfig{DB: f.db})
		status, _ = scopedChatRequest(t, canceled, "", "", "", "POST", "/chats/import", marshalConversation(t, chatImportConversation(), ""))
		if status != 408 {
			t.Fatalf("canceled import %d", status)
		}
		var n int
		if err := f.db.QueryRow("SELECT COUNT(*) FROM chat_import_runs WHERE id='run-http-1'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("canceled write persisted %d %v", n, err)
		}
		if err := chatimport.EnsureSchema(context.Background(), f.db, Q); err != nil {
			t.Fatal(err)
		}
	})
}
