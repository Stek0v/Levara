package http

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/chatimport"
)

func chatImportTestApp(t *testing.T) (*fiber.App, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "chatimport.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := chatimport.EnsureSchema(context.Background(), db, Q); err != nil {
		t.Fatal(err)
	}
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	RegisterChatImportAPI(app, APIConfig{DB: db})
	return app, db
}

func chatImportConversation() *chatimport.Conversation {
	return &chatimport.Conversation{
		Platform:  chatimport.PlatformCodex,
		SessionID: "sess-http-1",
		Title:     "REST endpoint test",
		Model:     "gpt-5.2-codex",
		CreatedAt: "2026-09-20T10:00:00Z",
		Messages: []chatimport.Message{
			{ExternalID: "m1", Ordinal: 0, Role: "user", Kind: chatimport.KindText, Content: "вопрос про WAL"},
			{ExternalID: "m2", Ordinal: 1, Role: "assistant", Kind: chatimport.KindReasoning, Content: "рассуждаю"},
			{ExternalID: "m3", Ordinal: 2, Role: "assistant", Kind: chatimport.KindText, Content: "ответ"},
		},
	}
}

func chatImportPost(t *testing.T, app *fiber.App, body string) (*http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/chats/import", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp, parsed
}

func chatImportGet(t *testing.T, app *fiber.App, path string) (*http.Response, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp, parsed
}

func marshalConversation(t *testing.T, conv *chatimport.Conversation, extra string) string {
	t.Helper()
	raw, err := json.Marshal(conv)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"run_id":"run-http-1","source_path":"rollout.jsonl","skipped":2,"finish":true,"conversation":%s%s}`, raw, extra)
}

// The HTTP-level DoD oracle: import inserts, re-import is a no-op, and the
// session reads back in order (E2-C2 + E2-C3).
func TestChatImportEndpointRoundtrip(t *testing.T) {
	app, _ := chatImportTestApp(t)
	body := marshalConversation(t, chatImportConversation(), "")

	resp, parsed := chatImportPost(t, app, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("first POST status = %d, body = %v", resp.StatusCode, parsed)
	}
	if got := int(parsed["inserted"].(float64)); got != 3 {
		t.Fatalf("inserted = %d, want 3", got)
	}
	if parsed["status"] != "ok" {
		t.Fatalf("status = %v, want ok (finish=true)", parsed["status"])
	}

	// Idempotent re-import over the same run and conversation.
	resp, parsed = chatImportPost(t, app, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-POST status = %d", resp.StatusCode)
	}
	if got := int(parsed["inserted"].(float64)); got != 0 {
		t.Fatalf("re-import inserted = %d, want 0", got)
	}

	// Session readback: chronological by ordinal, roles and kinds survive.
	resp, parsed = chatImportGet(t, app, "/chats/import/sessions/codex/sess-http-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET session status = %d", resp.StatusCode)
	}
	msgs := parsed["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" || first["kind"] != "text" || first["content"] != "вопрос про WAL" {
		t.Fatalf("first message drifted: %v", first)
	}
	second := msgs[1].(map[string]any)
	if second["kind"] != string(chatimport.KindReasoning) {
		t.Fatalf("reasoning kind lost: %v", second)
	}

	// Sessions listing.
	resp, parsed = chatImportGet(t, app, "/chats/import/sessions?platform=codex")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET sessions status = %d", resp.StatusCode)
	}
	sessions := parsed["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	if sessions[0].(map[string]any)["title"] != "REST endpoint test" {
		t.Fatalf("session title lost: %v", sessions[0])
	}

	// Run ledger.
	resp, parsed = chatImportGet(t, app, "/chats/import/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET runs status = %d", resp.StatusCode)
	}
	runs := parsed["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run := runs[0].(map[string]any)
	if run["status"] != "ok" || int(run["imported_count"].(float64)) != 3 || int(run["skipped_count"].(float64)) != 2 {
		t.Fatalf("run ledger wrong: %v", run)
	}
}

func TestChatImportEndpointValidation(t *testing.T) {
	app, _ := chatImportTestApp(t)

	resp, _ := chatImportPost(t, app, `{"conversation":null}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing conversation status = %d, want 400", resp.StatusCode)
	}

	conv := chatImportConversation()
	conv.Platform = "chatgpt-web" // reserved for the deferred web adapters
	resp, _ = chatImportPost(t, app, marshalConversation(t, conv, ""))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown platform status = %d, want 400", resp.StatusCode)
	}

	resp, _ = chatImportGet(t, app, "/chats/import/sessions/codex/does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session status = %d, want 404", resp.StatusCode)
	}
}

// Secrets stay warn-only end to end: content is stored verbatim and the
// warning surfaces in the response.
func TestChatImportEndpointSecretsWarnOnly(t *testing.T) {
	app, db := chatImportTestApp(t)
	conv := chatImportConversation()
	conv.Messages[0].Content = "проверь ghp_0123456789abcdefghijkl пожалуйста"

	resp, parsed := chatImportPost(t, app, marshalConversation(t, conv, ""))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	if int(parsed["warned_count"].(float64)) != 1 {
		t.Fatalf("warned_count = %v, want 1", parsed["warned_count"])
	}
	warnings := parsed["warnings"].([]any)
	if !strings.Contains(warnings[0].(string), "GitHub token") {
		t.Fatalf("warning = %v", warnings[0])
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM chat_import_messages WHERE external_id='m1'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "ghp_0123456789") {
		t.Fatalf("warn-only violated: %q", content)
	}
}
