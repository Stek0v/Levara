package http

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stek0v/levara/pkg/chatimport"
)

const claudeFixture = `{"type":"summary","summary":"P2 daemon test","sessionId":"p2-sess-1"}
{"type":"user","message":{"role":"user","content":"daemon должен подхватить этот файл"},"uuid":"u1","timestamp":"2026-09-21T10:00:00.000Z","sessionId":"p2-sess-1","cwd":"/tmp","version":"2.0.14"}
{"type":"assistant","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Подхватил."}]},"uuid":"a1","timestamp":"2026-09-21T10:00:05.000Z","sessionId":"p2-sess-1"}
`

func chatSourcesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "sources.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureChatSourcesSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	// reuse chatimport tables from the resume-style fixture (schema part)
	for _, stmt := range chatimport.SchemaStatements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func newDaemon(db *sql.DB) *chatSourcesDaemon {
	return &chatSourcesDaemon{
		db:       db,
		seen:     make(map[string]fileFingerprint),
		interval: time.Minute,
	}
}

func countMessages(t *testing.T, db *sql.DB, platform, session string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_import_messages WHERE platform=? AND session_id=?`, platform, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPushRagItemSendsDatasetNameField(t *testing.T) {
	var gotDatasetName, gotLegacySnake string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/add" {
			if err := r.ParseMultipartForm(1 << 20); err == nil {
				gotDatasetName = r.FormValue("datasetName")
				gotLegacySnake = r.FormValue("dataset_name")
			}
			_, _ = w.Write([]byte(`{"dataset_name":"chat-imports"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	d := &chatSourcesDaemon{loopback: srv.URL, dataset: "chat-imports", client: srv.Client()}
	d.pushOneRagItem(context.Background(), &chatimport.Conversation{}, "chatimport-codex-s.md", "x")
	if gotDatasetName != "chat-imports" {
		t.Fatalf("datasetName field = %q, want chat-imports (API contract is camelCase)", gotDatasetName)
	}
	if gotLegacySnake != "" {
		t.Fatalf("legacy dataset_name field still sent: %q — it silently routes to the default dataset", gotLegacySnake)
	}
}

func TestSweepSupersededMonoliths(t *testing.T) {
	deleted := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/datasets" && r.Method == "GET":
			_, _ = w.Write([]byte(`[{"id":"ds1","name":"chat-imports"}]`))
		case r.URL.Path == "/api/v1/datasets/ds1/data" && r.Method == "GET":
			_, _ = w.Write([]byte(`[
				{"id":"m1","name":"chatimport-codex-s1.md"},
				{"id":"p1","name":"chatimport-codex-s1-part001.md"},
				{"id":"p2","name":"chatimport-codex-s1-part002.md"},
				{"id":"m2","name":"chatimport-codex-solo.md"}]`))
		case r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v1/datasets/ds1/data/"):
			deleted[strings.TrimPrefix(r.URL.Path, "/api/v1/datasets/ds1/data/")] = true
			_, _ = w.Write([]byte(`{"deleted":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d := &chatSourcesDaemon{loopback: srv.URL, dataset: "chat-imports", client: srv.Client()}
	d.sweepSupersededMonoliths(context.Background())
	if !deleted["m1"] {
		t.Fatalf("superseded monolith not deleted: %v", deleted)
	}
	if deleted["m2"] || deleted["p1"] || deleted["p2"] {
		t.Fatalf("over-deletion: %v", deleted)
	}
}

func TestChatSourcesDaemonScansIdempotently(t *testing.T) {
	db := chatSourcesTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rollout.jsonl"), []byte(claudeFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newDaemon(db)
	def := chatSourceDefinition{Platform: chatimport.PlatformClaudeCode, Root: root}
	ctx := context.Background()

	d.scanOnce(ctx, []chatSourceDefinition{def})
	if got := countMessages(t, db, "claude-code", "p2-sess-1"); got != 2 {
		t.Fatalf("first scan messages = %d, want 2 (user + assistant; summary is title-only)", got)
	}

	// Second scan: fingerprint unchanged → nothing re-imported, still 3.
	d.scanOnce(ctx, []chatSourceDefinition{def})
	if got := countMessages(t, db, "claude-code", "p2-sess-1"); got != 2 {
		t.Fatalf("second scan messages = %d, want 2 (idempotent)", got)
	}

	// A brand-new file lands on the next scan without any manual action.
	session2 := `{"type":"user","message":{"role":"user","content":"вторая сессия"},"uuid":"x1","timestamp":"2026-09-21T11:00:00.000Z","sessionId":"p2-sess-2"}
`
	if err := os.WriteFile(filepath.Join(root, "new.jsonl"), []byte(session2), 0o644); err != nil {
		t.Fatal(err)
	}
	d.scanOnce(ctx, []chatSourceDefinition{def})
	if got := countMessages(t, db, "claude-code", "p2-sess-2"); got != 1 {
		t.Fatalf("new file not ingested: %d messages", got)
	}

	// State persisted for observability.
	var scans, files int
	var lastErr string
	if err := db.QueryRow(`SELECT scans, files_imported, last_error FROM chat_import_sources WHERE platform='claude-code'`).Scan(&scans, &files, &lastErr); err != nil {
		t.Fatal(err)
	}
	if scans != 3 || files != 2 || lastErr != "" {
		t.Fatalf("state wrong: scans=%d files=%d err=%q", scans, files, lastErr)
	}
}

func TestChatSourcesDaemonSkipsForeignFiles(t *testing.T) {
	db := chatSourcesTestDB(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("not a transcript"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := newDaemon(db)
	d.scanOnce(context.Background(), []chatSourceDefinition{{Platform: chatimport.PlatformClaudeCode, Root: root}})
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_import_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("foreign file ingested: %d messages", n)
	}
}

func TestChatSourcesFromEnvDisabled(t *testing.T) {
	t.Setenv("LEVARA_CHAT_SOURCES", "")
	if _, ok := chatSourcesFromEnv(); ok {
		t.Fatal("empty env must disable the daemon")
	}
	t.Setenv("LEVARA_CHAT_SOURCES", "claude-code")
	defs, ok := chatSourcesFromEnv()
	if !ok || len(defs) != 1 || defs[0].Platform != chatimport.PlatformClaudeCode {
		t.Fatalf("defs = %+v ok=%v", defs, ok)
	}
	if !filepath.IsAbs(defs[0].Root) {
		t.Fatalf("root should be absolute: %s", defs[0].Root)
	}
}
