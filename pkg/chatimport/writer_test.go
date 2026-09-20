package chatimport

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "chatimport.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatalf("enable FKs: %v", err)
	}
	if err := EnsureSchema(context.Background(), db, SQLiteQ); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	return db
}

func parseFixture(t *testing.T) *Conversation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex-rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	conv, _, err := ParseCodexRollout(bytes.NewReader(raw), DefaultParseOptions())
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

// Re-importing the same transcript must insert zero rows: the DoD's
// independent idempotency oracle.
func TestInsertConversationIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	conv := parseFixture(t)

	if err := StartRun(ctx, db, SQLiteQ, RunInfo{ID: "run-1", Platform: PlatformCodex, SourcePath: "rollout.jsonl", StartedAt: "2026-09-20T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var warnings []string
	inserted, err := InsertConversation(ctx, db, SQLiteQ, "run-1", conv, &warnings, now)
	if err != nil {
		t.Fatal(err)
	}
	if inserted != 7 {
		t.Fatalf("first import inserted = %d, want 7", inserted)
	}

	// Secrets warn-only: the fake GitHub token in msg_user_2 is reported but
	// stored verbatim.
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "GitHub token") && strings.Contains(w, "msg_user_2") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected GitHub token warning for msg_user_2, got %v", warnings)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM chat_import_messages WHERE external_id='msg_user_2'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "ghp_0123456789") {
		t.Fatalf("warn-only contract broken: content was modified: %q", content)
	}

	if err := FinishRun(ctx, db, SQLiteQ, "run-1", "ok", inserted, 5, len(warnings), warnings, "2026-09-20T12:00:01Z"); err != nil {
		t.Fatal(err)
	}

	// Second run over the same conversation: zero new rows.
	if err := StartRun(ctx, db, SQLiteQ, RunInfo{ID: "run-2", Platform: PlatformCodex, SourcePath: "rollout.jsonl", StartedAt: "2026-09-20T13:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var warnings2 []string
	inserted2, err := InsertConversation(ctx, db, SQLiteQ, "run-2", conv, &warnings2, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if inserted2 != 0 {
		t.Fatalf("re-import inserted = %d, want 0", inserted2)
	}
	if err := FinishRun(ctx, db, SQLiteQ, "run-2", "ok", inserted2, 0, len(warnings2), warnings2, "2026-09-20T13:00:01Z"); err != nil {
		t.Fatal(err)
	}

	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_import_messages`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 7 {
		t.Fatalf("total messages = %d, want 7 after re-import", total)
	}

	// Ledger counters round-trip.
	var status string
	var importedCount int
	if err := db.QueryRow(`SELECT status, imported_count FROM chat_import_runs WHERE id='run-1'`).Scan(&status, &importedCount); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || importedCount != 7 {
		t.Fatalf("run-1 = %s/%d, want ok/7", status, importedCount)
	}

	// Deleting a run cascades to only that run's messages (undo semantics).
	if _, err := db.Exec(`DELETE FROM chat_import_runs WHERE id='run-2'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM chat_import_messages`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 7 {
		t.Fatalf("messages after deleting empty run-2 = %d, want 7 (run-1 rows untouched)", total)
	}
}

func TestMessageIDDeterministic(t *testing.T) {
	a := messageID(PlatformCodex, "s1", "m1")
	b := messageID(PlatformCodex, "s1", "m1")
	c := messageID(PlatformClaudeCode, "s1", "m1")
	if a != b {
		t.Fatal("same inputs must give same id")
	}
	if a == c {
		t.Fatal("different platforms must give different ids")
	}
}

// A retried finish over an already-closed run must not rewrite the ledger.
func TestFinishRunDoesNotRewriteClosedRun(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := StartRun(ctx, db, SQLiteQ, RunInfo{ID: "run-c", Platform: PlatformCodex, StartedAt: "2026-09-20T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := FinishRun(ctx, db, SQLiteQ, "run-c", "ok", 7, 5, 0, nil, "2026-09-20T12:00:01Z"); err != nil {
		t.Fatal(err)
	}
	// Retry path (e.g. duplicated POST): different counters, must be ignored.
	if err := FinishRun(ctx, db, SQLiteQ, "run-c", "failed", 0, 0, 0, nil, "2026-09-20T13:00:00Z"); err != nil {
		t.Fatal(err)
	}
	var status string
	var imported int
	if err := db.QueryRow(`SELECT status, imported_count FROM chat_import_runs WHERE id='run-c'`).Scan(&status, &imported); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || imported != 7 {
		t.Fatalf("closed run rewritten: %s/%d, want ok/7", status, imported)
	}
	if err := FinishRun(ctx, db, SQLiteQ, "run-missing", "ok", 0, 0, 0, nil, "t"); err == nil {
		t.Fatal("expected error for missing run")
	}
}

// PostgreSQL TEXT rejects NUL bytes; the writer must neutralize them (found
// live: binary exec outputs in 11 of 614 codex rollouts).
func TestInsertConversationNULSanitized(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	conv := &Conversation{Platform: PlatformCodex, SessionID: "s-nul", Messages: []Message{
		{ExternalID: "m1", Ordinal: 0, Role: "tool", Kind: KindToolResult, Content: "binary\x00output\x00here"},
	}}
	if err := StartRun(ctx, db, SQLiteQ, RunInfo{ID: "run-nul", Platform: PlatformCodex, StartedAt: "2026-09-20T13:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := InsertConversation(ctx, db, SQLiteQ, "run-nul", conv, &[]string{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM chat_import_messages WHERE external_id='m1'`).Scan(&content); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(content, 0) {
		t.Fatalf("NUL survived: %q", content)
	}
	if !strings.Contains(content, "binary") || !strings.Contains(content, "output") {
		t.Fatalf("content mangled beyond NUL replacement: %q", content)
	}
}
