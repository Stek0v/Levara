package http

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
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
